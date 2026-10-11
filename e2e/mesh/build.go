// Package mesh installs and drives mcp-mesh on lab machines the way a user
// would: the real binaries, the documented commands, the hub's CLI and
// API. Scenarios in e2e build on it.
package mesh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/AldrinSalazar/mcp-mesh/e2e/lab"
)

// Binaries are built once per test process, for linux/amd64.
var Binaries = []string{"mcp-mesh-hub", "mcp-mesh-node", "fakeapp", "mcpcall"}

var build struct {
	once sync.Once
	dir  string
	err  error
}

// Build compiles the binaries the lab machines run into a temporary
// directory. The dashboard is built first, since the hub embeds web/dist
// (MCPMESH_E2E_SKIP_WEB=1 keeps the dist that is there).
func Build(ctx context.Context) (string, error) {
	build.once.Do(func() {
		if os.Getenv("MCPMESH_E2E_SKIP_WEB") == "" {
			cmd := exec.CommandContext(ctx, "npm", "--prefix", "web", "run", "build")
			cmd.Dir = moduleRoot()
			if out, err := cmd.CombinedOutput(); err != nil {
				build.err = fmt.Errorf("build the dashboard: %w\n%s", err, out)
				return
			}
		}
		dir, err := os.MkdirTemp("", "mcp-mesh-e2e-bin-")
		if err != nil {
			build.err = err
			return
		}
		pkgs := map[string]string{
			"mcp-mesh-hub":  "./cmd/mcp-mesh-hub",
			"mcp-mesh-node": "./cmd/mcp-mesh-node",
			"fakeapp":       "./tools/fakeapp",
			"mcpcall":       "./tools/mcpcall",
		}
		for _, name := range Binaries {
			cmd := exec.CommandContext(ctx, goTool(), "build", "-trimpath", "-o", filepath.Join(dir, name), pkgs[name])
			cmd.Dir = moduleRoot()
			cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				build.err = fmt.Errorf("build %s: %w\n%s", name, err, out)
				return
			}
		}
		build.dir = dir
	})
	return build.dir, build.err
}

// Install copies the binaries to /usr/local/bin on the machine, which is
// what unpacking a release would do.
func Install(ctx context.Context, m *lab.Machine) error {
	dir, err := Build(ctx)
	if err != nil {
		return err
	}
	for _, name := range Binaries {
		if err := m.Upload(ctx, filepath.Join(dir, name), "/tmp/"+name, 0o755); err != nil {
			return err
		}
	}
	if out, err := m.Run(ctx, "sudo install -m 0755 /tmp/mcp-mesh-hub /tmp/mcp-mesh-node /tmp/fakeapp /tmp/mcpcall /usr/local/bin/"); err != nil {
		return fmt.Errorf("install on %s: %w\n%s", m.Name, err, out)
	}
	return nil
}

func goTool() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

func moduleRoot() string {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}
