//go:build e2e

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/e2e/mesh"
	"github.com/AldrinSalazar/mcp-mesh/e2e/oauthapp"
)

// Two machines join a hub on the LAN over plain HTTP (private addresses
// may), and one client sees and calls both machines' tools.
func TestTwoNodesMergeOverLAN(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a", "node-b")
	h := e.startHub(mesh.HubOptions{})
	e.joinNode("node-a")
	e.joinNode("node-b")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)
	e.waitMCPReady("node-b", "fake", 2*time.Minute)

	key, err := h.CreateKey(e.ctx, "client", "e2e-client")
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.Client(e.ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	toolNames := func() ([]string, error) {
		res, err := s.ListTools(e.ctx, nil)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}
		return names, nil
	}
	hasBoth := func() error {
		names, err := toolNames()
		if err != nil {
			return err
		}
		for _, want := range []string{"node-a__fake__render", "node-b__fake__render"} {
			if !slices.Contains(names, want) {
				return fmt.Errorf("tools = %v, want %s", names, want)
			}
		}
		return nil
	}
	// The key was made by the CLI a moment ago; its first list must already
	// have everything (the hub reloads groups for a new client).
	if err := hasBoth(); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"node-a", "node-b"} {
		res, err := s.CallTool(e.ctx, &mcp.CallToolParams{Name: node + "__fake__render", Arguments: map[string]any{"scene": "from " + node}})
		if err != nil {
			t.Fatalf("call on %s: %v", node, err)
		}
		if got := text(res); res.IsError || !strings.Contains(got, "from "+node) {
			t.Fatalf("call on %s = %q (isError=%v)", node, got, res.IsError)
		}
	}
}

// A node joins over wss:// trusting a private CA, and an app that only
// knows the hub's URL gets in with OAuth: discovery, dynamic registration,
// PKCE, the admin's consent, the code exchange. The hub makes it a client
// key; denying consent gives the app nothing.
func TestOAuthAppOverTLS(t *testing.T) {
	e := newEnv(t, 15*time.Minute, "hub", "node-a")
	h := e.startHub(mesh.HubOptions{TLS: true})
	e.joinNode("node-a")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)

	robot := &oauthapp.Robot{Username: "admin", Password: h.AdminPassword, KeyName: "e2e-app", HTTP: h.HTTP}
	app, err := oauthapp.Connect(e.ctx, h.ClientURL(), robot, h.HTTP)
	if err != nil {
		t.Fatalf("OAuth connect: %v (pages seen: %d)", err, len(robot.Pages))
	}
	defer app.Close()
	res, err := app.Session.CallTool(e.ctx, &mcp.CallToolParams{Name: "node-a__fake__render", Arguments: map[string]any{"scene": "oauth"}})
	if err != nil {
		t.Fatalf("call with the OAuth token: %v", err)
	}
	if got := text(res); !strings.Contains(got, "oauth") {
		t.Fatalf("call = %q", got)
	}
	keys, err := h.CLI(e.ctx, "key", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(keys, "e2e-app") {
		t.Fatalf("key list doesn't have the app's key:\n%s", keys)
	}

	denier := &oauthapp.Robot{Username: "admin", Password: h.AdminPassword, Deny: true, HTTP: h.HTTP}
	if denied, err := oauthapp.Connect(e.ctx, h.ClientURL(), denier, h.HTTP); err == nil {
		denied.Close()
		t.Fatal("connect succeeded after the admin denied consent")
	} else if !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied connect error = %v, want access_denied", err)
	}
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
