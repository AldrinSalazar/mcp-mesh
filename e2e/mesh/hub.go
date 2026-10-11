package mesh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/e2e/lab"
)

// Hub is mcp-mesh-hub running on a lab machine as a systemd service.
type Hub struct {
	M             *lab.Machine
	Lab           *lab.Lab
	AdminPassword string
	// CA is set when the hub serves TLS.
	CA *CA
	// Host is the name or address nodes and clients use for the hub: its
	// IP on plain HTTP, TLSHostName with TLS.
	Host string

	HTTP *http.Client // through the lab relay, trusting CA
	api  *http.Client // HTTP plus the dashboard session's cookies
	csrf string
}

// HubOptions configures StartHub.
type HubOptions struct {
	TLS   bool
	Flags []string // extra hub flags
	// AdminPassword is the first admin's password; empty makes one up. A
	// hub started on a restored database keeps the old one.
	AdminPassword string
	// BeforeStart runs after the binaries are installed, before the hub
	// starts (to restore a backup into DataDir, for instance).
	BeforeStart func(context.Context, *lab.Machine) error
}

// TLSHostName is the TLS hub's name. OAuth clients refuse an authorization
// server at a private IP address (SSRF protection), so a hub that serves
// apps is addressed by name; nodes get it in /etc/hosts, the tests' HTTP
// client through Lab.AddHost.
const TLSHostName = "hub.e2e.test"

// DataDir is where the hub keeps hub.db on the machine.
const DataDir = "/var/lib/mcp-mesh-hub"

// StartHub installs the binaries and runs the hub as a transient systemd
// service (journal logs, restartable), addressed by the machine's IP.
func StartHub(ctx context.Context, l *lab.Lab, m *lab.Machine, opts HubOptions) (*Hub, error) {
	if err := Install(ctx, m); err != nil {
		return nil, err
	}
	h := &Hub{M: m, Lab: l, AdminPassword: opts.AdminPassword, Host: m.IP}
	if h.AdminPassword == "" {
		h.AdminPassword = "e2e-" + randomHex(8)
	}
	if opts.TLS {
		h.Host = TLSHostName
		if err := l.AddHost(TLSHostName, m.IP); err != nil {
			return nil, err
		}
	}
	flags := []string{"--public-host", h.Host, "--data-dir", DataDir, "--name", "e2e-hub"}
	if opts.TLS {
		ca, err := NewCA()
		if err != nil {
			return nil, err
		}
		cert, key, err := ca.Issue([]string{m.IP}, []string{TLSHostName, m.Name})
		if err != nil {
			return nil, err
		}
		if err := m.Write(ctx, bytes.NewReader(cert), "/tmp/hub.crt", 0o644); err != nil {
			return nil, err
		}
		if err := m.Write(ctx, bytes.NewReader(key), "/tmp/hub.key", 0o600); err != nil {
			return nil, err
		}
		if out, err := m.Run(ctx, "sudo install -d -m 0700 /etc/mcp-mesh-hub && sudo install -m 0644 /tmp/hub.crt /etc/mcp-mesh-hub/hub.crt && sudo install -m 0600 /tmp/hub.key /etc/mcp-mesh-hub/hub.key"); err != nil {
			return nil, fmt.Errorf("install TLS files: %w\n%s", err, out)
		}
		flags = append(flags, "--tls-cert", "/etc/mcp-mesh-hub/hub.crt", "--tls-key", "/etc/mcp-mesh-hub/hub.key")
		h.CA = ca
	}
	flags = append(flags, opts.Flags...)
	if opts.BeforeStart != nil {
		if err := opts.BeforeStart(ctx, m); err != nil {
			return nil, err
		}
	}
	cmd := fmt.Sprintf("sudo systemd-run --unit mcp-mesh-hub --property=Restart=on-failure --setenv=MCPMESH_ADMIN_PASSWORD=%s /usr/local/bin/mcp-mesh-hub %s",
		lab.Shq(h.AdminPassword), shJoin(flags))
	if out, err := m.Run(ctx, cmd); err != nil {
		return nil, fmt.Errorf("start hub: %w\n%s", err, out)
	}
	if h.CA != nil {
		h.HTTP = l.HTTPClient(h.CA.TLSConfig())
	} else {
		h.HTTP = l.HTTPClient(nil)
	}
	if err := h.WaitReady(ctx, time.Minute); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Hub) scheme() string {
	if h.CA != nil {
		return "https"
	}
	return "http"
}

// AdminURL, NodeURL and ClientURL are the hub's three endpoints.
func (h *Hub) AdminURL() string { return fmt.Sprintf("%s://%s:7000", h.scheme(), h.Host) }
func (h *Hub) NodeURL() string {
	ws := "ws"
	if h.CA != nil {
		ws = "wss"
	}
	return fmt.Sprintf("%s://%s:7001/node", ws, h.Host)
}
func (h *Hub) ClientURL() string { return fmt.Sprintf("%s://%s:7002/mcp", h.scheme(), h.Host) }

// WaitReady polls /readyz until the hub says it is ready.
func (h *Hub) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.AdminURL()+"/readyz", nil)
		resp, err := h.HTTP.Do(req)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("%s: %s", resp.Status, b)
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	logs, _ := h.Logs(ctx)
	return fmt.Errorf("hub not ready after %s (last: %s)\nhub log:\n%s", timeout, last, logs)
}

// Logs returns the hub service's journal.
func (h *Hub) Logs(ctx context.Context) (string, error) {
	return h.M.Run(ctx, "sudo journalctl -u mcp-mesh-hub --no-pager -o cat | tail -n 300")
}

// Restart restarts the hub service.
func (h *Hub) Restart(ctx context.Context) error {
	if out, err := h.M.Run(ctx, "sudo systemctl restart mcp-mesh-hub"); err != nil {
		return fmt.Errorf("restart hub: %w\n%s", err, out)
	}
	return h.WaitReady(ctx, time.Minute)
}

// CLI runs an mcp-mesh-hub subcommand on the hub machine against its
// database, as the operator would.
func (h *Hub) CLI(ctx context.Context, args ...string) (string, error) {
	out, err := h.M.Run(ctx, "sudo /usr/local/bin/mcp-mesh-hub --data-dir "+DataDir+" "+shJoin(args))
	if err != nil {
		return out, fmt.Errorf("mcp-mesh-hub %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

var secretRe = regexp.MustCompile(`mm_(node|client)_[0-9A-Za-z]{43}`)

// CreateKey makes a node or client key with the CLI and returns its secret.
func (h *Hub) CreateKey(ctx context.Context, kind, name string) (string, error) {
	out, err := h.CLI(ctx, "key", "create", kind, name)
	if err != nil {
		return "", err
	}
	s := secretRe.FindString(out)
	if s == "" {
		return "", fmt.Errorf("key create printed no secret:\n%s", out)
	}
	return s, nil
}

// SignIn signs the API session in as the first admin.
func (h *Hub) SignIn(ctx context.Context) error {
	jar, _ := cookiejar.New(nil)
	h.api = &http.Client{Transport: h.HTTP.Transport, Jar: jar, Timeout: time.Minute}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": h.AdminPassword})
	var resp struct {
		CSRF string `json:"csrf"`
	}
	if err := h.call(ctx, http.MethodPost, "/api/v1/session", body, &resp); err != nil {
		h.api = nil
		return fmt.Errorf("sign in: %w", err)
	}
	h.csrf = resp.CSRF
	return nil
}

// API calls the JSON API with the signed-in session and decodes the answer
// into out (if not nil).
func (h *Hub) API(ctx context.Context, method, path string, body any, out any) error {
	if h.api == nil {
		if err := h.SignIn(ctx); err != nil {
			return err
		}
	}
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return h.call(ctx, method, "/api/v1"+path, b, out)
}

func (h *Hub) call(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, h.AdminURL()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.AdminURL())
	if h.csrf != "" {
		req.Header.Set("X-CSRF-Token", h.csrf)
	}
	resp, err := h.api.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, rb)
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("%s %s: decode: %w: %.300s", method, path, err, rb)
		}
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = lab.Shq(a)
	}
	return strings.Join(q, " ")
}
