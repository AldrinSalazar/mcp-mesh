//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/e2e/mesh"
)

// The dashboard in a real browser against the hub on its VM: the embedded
// build and the real API, through the lab relay as a SOCKS5 proxy. The
// browser part is web/e2e/hub (Playwright).
func TestDashboardAgainstHub(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("no npx")
	}
	e := newEnv(t, 15*time.Minute, "hub", "node-a")
	h := e.startHub(mesh.HubOptions{})
	e.joinNode("node-a")
	e.waitMCPReady("node-a", "fake", 2*time.Minute)

	cmd := exec.CommandContext(e.ctx, "npx", "playwright", "test")
	cmd.Dir = "../web"
	cmd.Env = append(os.Environ(),
		"MCPMESH_E2E_HUB_URL="+strings.TrimSuffix(h.AdminURL(), "/"),
		"MCPMESH_E2E_NODE_URL="+h.NodeURL(),
		"MCPMESH_E2E_ADMIN_PASSWORD="+h.AdminPassword,
		"MCPMESH_E2E_PROXY=socks5://"+theLab.RelayAddr(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("playwright: %v\n%s\n(a missing libasound.so.2 means the browser's system libraries: sudo npx playwright install-deps chromium)", err, out)
	}
	t.Logf("playwright:\n%s", out)
	keys, err := h.CLI(e.ctx, "key", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(keys, "browser-made") {
		t.Fatalf("the key made in the browser isn't in the hub:\n%s", keys)
	}
}
