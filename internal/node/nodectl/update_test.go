package nodectl

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/paths"
	"github.com/AldrinSalazar/mcp-mesh/internal/update"
)

func TestUpdateState(t *testing.T) {
	brew := "/opt/homebrew/Cellar/mcp-mesh-node/1.2.0/bin/mcp-mesh-node"
	running := func(version, hub string) DaemonState {
		return DaemonState{Running: true, Status: control.Status{Version: version, Hub: control.HubStatus{Version: hub}}}
	}
	tests := []struct {
		name   string
		cached string // latest release in the cache
		s      Snapshot
		env    bool
		want   UpdateState
	}{
		{name: "nothing known", want: UpdateState{}},
		{name: "up to date", cached: "v1.2.0", s: Snapshot{Daemon: running("v1.2.0", "v1.2.0")}},
		{name: "newer release", cached: "v1.3.0",
			want: UpdateState{Latest: "v1.3.0", URL: "https://example/v1.3.0", Upgrade: "brew upgrade mcp-mesh-node"}},
		{name: "turned off in node.yaml", cached: "v1.3.0", s: Snapshot{Config: ConfigState{NoUpdateCheck: true}}},
		{name: "turned off by the environment", cached: "v1.3.0", env: true},
		{name: "running an older binary", s: Snapshot{Daemon: running("v1.1.0", "")}, want: UpdateState{Running: "v1.1.0"}},
		{name: "hub is newer", s: Snapshot{Daemon: running("v1.2.0", "v1.4.0")}, want: UpdateState{Hub: "v1.4.0", Upgrade: "brew upgrade mcp-mesh-node"}},
		{name: "dev hub", s: Snapshot{Daemon: running("v1.2.0", "dev")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env {
				t.Setenv(update.EnvDisable, "1")
			}
			dir := t.TempDir()
			if test.cached != "" {
				cache := update.FileCache{Path: filepath.Join(dir, paths.UpdateFileName)}
				if err := cache.Save(t.Context(), update.State{Latest: update.Release{Version: test.cached, URL: "https://example/" + test.cached}, CheckedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			n := &Node{opts: Options{Version: "v1.2.0", StateDir: dir, Executable: brew}}
			if got := n.updateState(t.Context(), test.s); got != test.want {
				t.Fatalf("got %+v, want %+v", got, test.want)
			}
		})
	}
}
