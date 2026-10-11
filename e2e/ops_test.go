//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
	"github.com/AldrinSalazar/mcp-mesh/e2e/lab"
	"github.com/AldrinSalazar/mcp-mesh/e2e/mesh"
)

// A node machine finds the hub on the LAN by itself (mDNS), without being
// told its address.
func TestMDNSDiscovery(t *testing.T) {
	e := newEnv(t, 10*time.Minute, "hub", "node-b")
	h := e.startHub(mesh.HubOptions{})
	m := e.machine("node-b")
	if err := mesh.Install(e.ctx, m); err != nil {
		t.Fatal(err)
	}
	out, err := m.Run(e.ctx, "MCPMESH_NO_INPUT=1 /usr/local/bin/mcp-mesh-node discover")
	t.Logf("discover:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "e2e-hub") || !strings.Contains(out, h.M.IP) {
		t.Fatalf("discover didn't find the hub e2e-hub at %s", h.M.IP)
	}
}

// The hub machine dies. Its data directory, backed up while the hub was
// stopped, is restored on another machine: the same node and client keys
// work against the new hub.
func TestBackupRestoreOnNewMachine(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a", "node-b")
	h := e.startHub(mesh.HubOptions{})
	nodeKey, err := h.CreateKey(e.ctx, "node", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := h.CreateKey(e.ctx, "client", "survivor")
	if err != nil {
		t.Fatal(err)
	}
	// Back up while stopped (the documented way doesn't exist yet: copy the
	// whole data directory, WAL included).
	if out, err := h.M.Run(e.ctx, "sudo systemctl stop mcp-mesh-hub && sudo tar czf /tmp/hub-backup.tgz -C /var/lib mcp-mesh-hub && base64 -w0 /tmp/hub-backup.tgz"); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	} else {
		backup, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
		if err != nil {
			t.Fatal(err)
		}
		if err := h.M.PowerOff(e.ctx); err != nil {
			t.Fatal(err)
		}
		restore := func(ctx context.Context, m *lab.Machine) error {
			if err := m.Write(ctx, strings.NewReader(string(backup)), "/tmp/hub-backup.tgz", 0o600); err != nil {
				return err
			}
			if out, err := m.Run(ctx, "sudo tar xzf /tmp/hub-backup.tgz -C /var/lib"); err != nil {
				return fmt.Errorf("restore: %w\n%s", err, out)
			}
			return nil
		}
		nh, err := mesh.StartHub(e.ctx, theLab, e.machine("node-b"), mesh.HubOptions{BeforeStart: restore, AdminPassword: h.AdminPassword})
		if err != nil {
			t.Fatal(err)
		}
		e.hub = nh
		n, err := mesh.SetupNode(e.ctx, nh, e.machine("node-a"), nodeKey)
		if err != nil {
			t.Fatal(err)
		}
		e.nodes = append(e.nodes, n)
		if _, err := n.Run(e.ctx, "add", "--name", "fake", "--", "/usr/local/bin/fakeapp", "stdio"); err != nil {
			t.Fatal(err)
		}
		if _, err := n.Run(e.ctx, "start"); err != nil {
			t.Fatal(err)
		}
		e.waitMCPReady("node-a", "fake", 2*time.Minute)
		s, err := nh.Client(e.ctx, clientKey)
		if err != nil {
			t.Fatalf("the restored client key: %v", err)
		}
		defer s.Close()
		e.call(s, "node-a__fake__render", map[string]any{"scene": "restored"})
	}
}

// Revoking a node's key disconnects that node at once, and the node says
// why instead of retrying quietly.
func TestRevokedNodeKey(t *testing.T) {
	e := newEnv(t, 10*time.Minute, "hub", "node-a")
	h := e.startHub(mesh.HubOptions{})
	n := e.joinNode("node-a")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	list, err := h.CLI(e.ctx, "key", "list")
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`(k_[0-9a-f]+)\s+node\s+node-a`).FindStringSubmatch(list)
	if id == nil {
		t.Fatalf("no node-a key in:\n%s", list)
	}
	if _, err := h.CLI(e.ctx, "key", "revoke", id[1]); err != nil {
		t.Fatal(err)
	}
	err = mesh.Eventually(e.ctx, 30*time.Second, func() error {
		ns, err := h.Nodes(e.ctx)
		if err != nil {
			return err
		}
		for _, x := range ns {
			if x.Name == "node-a" && x.Online {
				return errString("node-a still online")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = mesh.Eventually(e.ctx, 30*time.Second, func() error {
		out, _ := n.Run(e.ctx, "status")
		if !strings.Contains(strings.ToLower(out), "rejected") {
			return fmt.Errorf("status doesn't say the key was rejected:\n%s", out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Adding an MCP to a running node only saves it, like an MCP client that
// reads its config at start: a call in progress isn't touched and the hub
// doesn't see the new MCP. reload applies it, waiting for the call first.
func TestConfigChangeDuringCall(t *testing.T) {
	e := newEnv(t, 10*time.Minute, "hub", "node-a")
	e.startHub(mesh.HubOptions{})
	n := e.joinNode("node-a")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	s := e.client("reload")
	call := e.startCall(s, "node-a__fake__slow", map[string]any{"seconds": 8})
	e.waitInFlight("node-a", "fake", 1)

	start := time.Now()
	out, err := n.Run(e.ctx, "add", "--name", "second", "--", "/usr/local/bin/fakeapp", "stdio")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mcp-mesh-node reload") || time.Since(start) > 5*time.Second {
		t.Fatalf("add took %s and said:\n%s", time.Since(start).Round(100*time.Millisecond), out)
	}
	if st, _ := n.Run(e.ctx, "status"); !strings.Contains(st, "not applied yet") {
		t.Fatalf("status doesn't say the change is pending:\n%s", st)
	}
	if ns, err := e.hub.Nodes(e.ctx); err == nil {
		for _, x := range ns {
			for _, m := range x.MCPs {
				if m.Name == "second" {
					t.Fatal("the hub lists the new MCP before reload")
				}
			}
		}
	}

	start = time.Now()
	if _, err := n.Run(e.ctx, "reload"); err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	t.Logf("reload returned after %s (it waits for the call)", waited.Round(100*time.Millisecond))
	r := <-call
	if r.err != nil || r.isError {
		t.Fatalf("the call was cut off: err=%v %q", r.err, r.text)
	}
	// The call had 8 seconds; the reload's limit is a minute.
	if waited > 30*time.Second {
		t.Fatalf("reload waited %s for an 8s call", waited.Round(time.Second))
	}
	e.waitMCPReady("node-a", "second", 2*time.Minute)
}

// A brand-new machine follows the dashboard: the admin creates a node key,
// and the setup command the dashboard shows is run on the machine word for
// word. Then the user adds an MCP and starts the node.
func TestFirstRunFromDashboardGuide(t *testing.T) {
	e := newEnv(t, 10*time.Minute, "hub", "node-b")
	h := e.startHub(mesh.HubOptions{})
	m := e.machine("node-b")
	if err := mesh.Install(e.ctx, m); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Setup map[string]string `json:"setup"`
	}
	// The name differs from the machine's hostname on purpose: the command
	// must set it.
	if err := h.API(e.ctx, http.MethodPost, "/keys", map[string]string{"kind": "node", "name": "studio"}, &created); err != nil {
		t.Fatal(err)
	}
	cmd := created.Setup["init_command"]
	if !strings.HasPrefix(cmd, "mcp-mesh-node init ") {
		t.Fatalf("setup = %v", created.Setup)
	}
	if out, err := m.Run(e.ctx, "MCPMESH_NO_INPUT=1 PATH=/usr/local/bin:$PATH "+cmd); err != nil {
		t.Fatalf("the dashboard's setup command: %v\n%s", err, out)
	}
	n := &mesh.Node{M: m, Hub: h}
	e.nodes = append(e.nodes, n)
	if _, err := n.Run(e.ctx, "add", "--name", "fake", "--", "/usr/local/bin/fakeapp", "stdio"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Run(e.ctx, "start"); err != nil {
		t.Fatal(err)
	}
	e.waitMCPReady("studio", "fake", 2*time.Minute)
}

// The Windows host joins as a node: mcp-mesh-node.exe wrap, in the
// foreground (the Windows daemon isn't built yet), sharing a launched MCP.
func TestWindowsHostNode(t *testing.T) {
	if _, err := exec.LookPath(hyperv.PowerShell); err != nil {
		t.Skip("no Windows host")
	}
	e := newEnv(t, 10*time.Minute, "hub")
	h := e.startHub(mesh.HubOptions{})
	key, err := h.CreateKey(e.ctx, "node", "winhost")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(theLab.Dir, "winhost")
	_ = os.RemoveAll(dir)
	for _, d := range []string{"appdata", "local"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.CommandContext(e.ctx, "go", "build", "-o", dir+"/", "../cmd/mcp-mesh-node", "../tools/fakeapp")
	build.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	win := func(p string) string {
		// Not e.ctx: the cleanup below runs after the test's context ends.
		w, err := hyperv.WindowsPath(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.key"), []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("node:\n  name: winhost\nhub:\n  url: %s\n  key_file: %s\n", h.NodeURL(), win(filepath.Join(dir, "hub.key")))
	if err := os.WriteFile(filepath.Join(dir, "appdata", "node.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(dir, "mcp-mesh-node.exe"), "wrap", "--foreground", "--config", win(filepath.Join(dir, "appdata", "node.yaml")),
		"--name", "fake", "--", win(filepath.Join(dir, "fakeapp.exe")), "stdio")
	cmd.Env = append(os.Environ(), "AppData="+win(filepath.Join(dir, "appdata")), "LocalAppData="+win(filepath.Join(dir, "local")), "WSLENV=AppData:LocalAppData")
	logf, err := os.Create(filepath.Join(dir, "node.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Killing the interop process doesn't stop the Windows one.
		_, _ = hyperv.Run(context.Background(), fmt.Sprintf(`Get-Process mcp-mesh-node,fakeapp -ErrorAction SilentlyContinue | Where-Object { $_.Path -like %s } | Stop-Process -Force`, hyperv.Quote(win(dir)+`\*`)))
		_ = cmd.Wait()
		logf.Close()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(dir, "node.log"))
			t.Logf("windows node log:\n%s", b)
		}
	})
	e.waitMCPReady("winhost", "fake", 2*time.Minute)
	s := e.client("windows")
	r := e.call(s, "winhost__fake__render", map[string]any{"scene": "from windows"})
	if !strings.Contains(r.text, "from windows") {
		t.Fatalf("call = %q", r.text)
	}
	ns, err := h.Nodes(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ns {
		if x.Name == "winhost" && x.OS != "windows" {
			t.Fatalf("winhost reports OS %q", x.OS)
		}
	}
}
