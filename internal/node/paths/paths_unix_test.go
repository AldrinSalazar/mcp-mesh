//go:build linux || darwin

package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocations(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  string
		state   string
		runtime string
	}{
		{name: "defaults"},
		{name: "XDG", config: "/tmp/mesh-config", state: "/tmp/mesh-state", runtime: "/tmp/mesh-runtime"},
		{name: "runtime only", runtime: "/tmp/mesh-runtime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// macOS's default temporary root can itself exhaust the socket limit.
			home, err := os.MkdirTemp("/tmp", "mesh-paths-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(home); err != nil {
					t.Error(err)
				}
			})
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", test.config)
			t.Setenv("XDG_STATE_HOME", test.state)
			t.Setenv("XDG_RUNTIME_DIR", test.runtime)
			t.Setenv("MCPMESH_CONFIG", "")
			t.Setenv("MCPMESH_SOCKET", "")
			config := filepath.Join(home, ".config", "mcp-mesh")
			if test.config != "" {
				config = filepath.Join(test.config, "mcp-mesh")
			}
			state := filepath.Join(home, ".local", "state", "mcp-mesh")
			if test.state != "" {
				state = filepath.Join(test.state, "mcp-mesh")
			}
			socket := filepath.Join(state, "node.sock")
			if runtime.GOOS == "linux" && test.runtime != "" {
				socket = filepath.Join(test.runtime, "mcp-mesh", "node.sock")
			}
			for _, check := range []struct {
				name string
				fn   func() (string, error)
				want string
			}{
				{"ConfigDir", ConfigDir, config},
				{"ConfigFile", ConfigFile, filepath.Join(config, "node.yaml")},
				{"StateDir", StateDir, state},
				{"SocketPath", SocketPath, socket},
				{"EnvFile", EnvFile, filepath.Join(state, "login.env")},
				{"ToolCacheDir", ToolCacheDir, filepath.Join(state, "tools")},
			} {
				got, err := check.fn()
				if err != nil || got != check.want {
					t.Errorf("%s() = %q, %v; want %q", check.name, got, err, check.want)
				}
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("path lookup created files: %v, %v", entries, err)
			}
		})
	}
}

func TestOverrides(t *testing.T) {
	t.Setenv("MCPMESH_CONFIG", "/tmp/other-daemon/custom.yaml")
	t.Setenv("MCPMESH_SOCKET", "/tmp/other-daemon/control.sock")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/ignored")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/ignored")
	for _, test := range []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"ConfigFile", ConfigFile, "/tmp/other-daemon/custom.yaml"},
		{"ConfigDir", ConfigDir, "/tmp/other-daemon"},
		{"SocketPath", SocketPath, "/tmp/other-daemon/control.sock"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.fn()
			if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestSocketPathLimits(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		limit int
		bad   bool
	}{
		{"macOS boundary", strings.Repeat("a", 103), 103, false},
		{"macOS NUL space", strings.Repeat("a", 104), 103, true},
		{"Linux boundary", strings.Repeat("a", 107), 107, false},
		{"Linux NUL space", strings.Repeat("a", 108), 107, true},
		{"byte length", strings.Repeat("é", 52), 103, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := checkSocketPath(test.path, test.limit)
			if test.bad {
				if err == nil || !strings.Contains(err.Error(), "limit") {
					t.Fatalf("want length-limit error, got %q, %v", got, err)
				}
			} else if err != nil || got != test.path {
				t.Fatalf("got %q, %v; want %q", got, err, test.path)
			}
		})
	}
	t.Setenv("MCPMESH_SOCKET", strings.Repeat("x", 108))
	if _, err := SocketPath(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("override should enforce socket limit: %v", err)
	}
	if _, err := checkSocketPath("/tmp/node\x00.sock", 103); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("want NUL-byte error, got %v", err)
	}
}

func TestMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("MCPMESH_CONFIG", "")
	t.Setenv("MCPMESH_SOCKET", "")
	for _, fn := range []func() (string, error){ConfigDir, ConfigFile, StateDir, SocketPath, EnvFile, ToolCacheDir} {
		if _, err := fn(); err == nil {
			t.Error("want missing home error")
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/config")
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	if _, err := ConfigFile(); err != nil {
		t.Fatalf("XDG config should not require HOME: %v", err)
	}
	if _, err := StateDir(); err != nil {
		t.Fatalf("XDG state should not require HOME: %v", err)
	}
}
