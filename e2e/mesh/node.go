package mesh

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/e2e/lab"
)

// Node is mcp-mesh-node on a lab machine, run as the lab user.
type Node struct {
	M   *lab.Machine
	Hub *Hub
}

// SetupNode installs the binaries and configures the node the documented
// way: trust the hub's CA (when it has one), init with the hub URL and key.
// MCPs are added and the node started by the scenario.
func SetupNode(ctx context.Context, h *Hub, m *lab.Machine, key string) (*Node, error) {
	if err := Install(ctx, m); err != nil {
		return nil, err
	}
	if h.Host != h.M.IP {
		line := fmt.Sprintf("%s %s", h.M.IP, h.Host)
		if out, err := m.Run(ctx, "echo "+lab.Shq(line)+" | sudo tee -a /etc/hosts >/dev/null"); err != nil {
			return nil, fmt.Errorf("hosts entry: %w\n%s", err, out)
		}
	}
	if h.CA != nil {
		if err := m.Write(ctx, bytes.NewReader(h.CA.CertPEM), "/tmp/mcp-mesh-e2e-ca.crt", 0o644); err != nil {
			return nil, err
		}
		if out, err := m.Run(ctx, "sudo install -m 0644 /tmp/mcp-mesh-e2e-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates >/dev/null"); err != nil {
			return nil, fmt.Errorf("trust CA: %w\n%s", err, out)
		}
	}
	n := &Node{M: m, Hub: h}
	if _, err := n.RunInput(ctx, strings.NewReader(key+"\n"), "init", "--hub", h.NodeURL(), "--key", "-"); err != nil {
		return nil, err
	}
	return n, nil
}

// Run runs an mcp-mesh-node command without prompts.
func (n *Node) Run(ctx context.Context, args ...string) (string, error) {
	return n.RunInput(ctx, nil, args...)
}

// RunInput is Run with stdin.
func (n *Node) RunInput(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	cmd := "MCPMESH_NO_INPUT=1 /usr/local/bin/mcp-mesh-node " + shJoin(args)
	var out string
	var err error
	if stdin != nil {
		out, err = n.M.RunInput(ctx, cmd, stdin)
	} else {
		out, err = n.M.Run(ctx, cmd)
	}
	if err != nil {
		return out, fmt.Errorf("mcp-mesh-node %s on %s: %w\n%s", strings.Join(args, " "), n.M.Name, err, out)
	}
	return out, nil
}

// Logs returns the node daemon's log (background start or user service).
func (n *Node) Logs(ctx context.Context) string {
	out, _ := n.M.Run(ctx, "tail -n 200 ~/.local/state/mcp-mesh/node.log 2>/dev/null; journalctl --user -u mcp-mesh-node --no-pager -o cat 2>/dev/null | tail -n 200")
	return out
}

// HubNode is one entry of the hub's GET /nodes.
type HubNode struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Online bool   `json:"online"`
	MCPs   []struct {
		Name     string `json:"name"`
		State    string `json:"state"`
		Error    string `json:"error"`
		Tools    int    `json:"tools"`
		InFlight int    `json:"in_flight"`
	} `json:"mcps"`
}

// Nodes is the hub's view of its nodes.
func (h *Hub) Nodes(ctx context.Context) ([]HubNode, error) {
	var ns []HubNode
	err := h.API(ctx, http.MethodGet, "/nodes", nil, &ns)
	return ns, err
}

// InFlight is how many calls the hub has running on node/mcp.
func (h *Hub) InFlight(ctx context.Context, node, mcpName string) (int, error) {
	ns, err := h.Nodes(ctx)
	if err != nil {
		return 0, err
	}
	for _, n := range ns {
		for _, m := range n.MCPs {
			if n.Name == node && m.Name == mcpName {
				return m.InFlight, nil
			}
		}
	}
	return 0, fmt.Errorf("hub doesn't list %s/%s", node, mcpName)
}

// Client connects an MCP client with a client key, through the relay.
func (h *Hub) Client(ctx context.Context, key string) (*mcp.ClientSession, error) {
	hc := &http.Client{Transport: &bearer{key: key, base: h.HTTP.Transport}}
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "1"}, nil)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.ClientURL(), HTTPClient: hc}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect client: %w", err)
	}
	return s, nil
}

type bearer struct {
	key  string
	base http.RoundTripper
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.base.RoundTrip(r)
}

// Eventually polls check until it returns nil or the timeout passes, and
// returns check's last error.
func Eventually(ctx context.Context, timeout time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	var err error
	for {
		if err = check(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("after %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
