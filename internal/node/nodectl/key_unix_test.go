//go:build !windows

package nodectl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
)

const testNodeKey = "mm_node_0123456789abcdefghijklmnop"

func TestSetAndClearKey(t *testing.T) {
	n := newTestNode(t, nil)
	if _, err := n.SetKey(t.Context(), testNodeKey); err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("SetKey without a config: %v", err)
	}
	if err := n.InitConfig("wss://mesh.example.com/node", false); err != nil {
		t.Fatal(err)
	}
	// Give the node a name so the result can be checked.
	if err := os.WriteFile(n.opts.ConfigPath, append(config.Example(), []byte("node:\n  name: desk\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	backend, _ := serveFake(t, n.opts.SocketPath)

	result, err := n.SetKey(t.Context(), "  "+testNodeKey+"\n")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(filepath.Dir(n.opts.ConfigPath), "node.key")
	if result.File != keyFile || result.Node != "desk" || !result.Reloaded {
		t.Fatalf("result = %+v", result)
	}
	info, err := os.Stat(keyFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(keyFile); string(data) != testNodeKey+"\n" {
		t.Fatalf("key file content %q", data)
	}
	data, _ := os.ReadFile(n.opts.ConfigPath)
	for _, want := range []string{"# Connect this node to your hub's node endpoint.", "# Choose one credential source", "key_file: " + keyFile} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config lost %q:\n%s", want, data)
		}
	}
	backend.mu.Lock()
	reloads := backend.reloads
	backend.mu.Unlock()
	if reloads != 1 {
		t.Fatalf("reloads = %d", reloads)
	}
	snapshot := n.Inspect(t.Context())
	if snapshot.Config.Err != "" || snapshot.Config.Key.File != keyFile || !snapshot.Config.Key.Set() {
		t.Fatalf("config state = %+v", snapshot.Config)
	}

	// Setting a new key replaces the file and keeps pointing at it.
	other := "mm_node_zyxwvutsrqponmlkjihgfedcba"
	if _, err := n.SetKey(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(keyFile); string(data) != other+"\n" {
		t.Fatalf("key file content %q", data)
	}

	cleared, err := n.ClearKey(t.Context())
	if err != nil || !cleared.Cleared || !cleared.Deleted || cleared.File != keyFile || !cleared.Reloaded {
		t.Fatalf("ClearKey = %+v, %v", cleared, err)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatalf("key file still exists: %v", err)
	}
	if data, _ := os.ReadFile(n.opts.ConfigPath); strings.Contains(string(data), "\n  key_file:") || !strings.Contains(string(data), "# Choose one credential source") {
		t.Fatalf("config after clear:\n%s", data)
	}
	if again, err := n.ClearKey(t.Context()); err != nil || again.Cleared {
		t.Fatalf("second ClearKey = %+v, %v", again, err)
	}
}

func TestClearKeyKeepsForeignFile(t *testing.T) {
	n := newTestNode(t, nil)
	foreign := filepath.Join(t.TempDir(), "mine.key")
	if err := config.WriteKeyFile(foreign, testNodeKey); err != nil {
		t.Fatal(err)
	}
	data := "hub:\n  url: wss://mesh.example.com/node\n  key_file: " + foreign + "\n"
	if err := os.WriteFile(n.opts.ConfigPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := n.ClearKey(t.Context())
	if err != nil || !result.Cleared || result.Deleted {
		t.Fatalf("ClearKey = %+v, %v", result, err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign key file was touched: %v", err)
	}
}

func TestSetKeyErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		key    string
		want   string
	}{
		{"no prefix", "hub: {url: wss://mesh.example.com/node}\n", "secret", "starts with mm_node_"},
		{"too short", "hub: {url: wss://mesh.example.com/node}\n", "mm_node_x", "too short"},
		{"with spaces", "hub: {url: wss://mesh.example.com/node}\n", "mm_node_ 0123456789abcdefghijklmnop", "no spaces"},
		{"insecure hub", "hub: {url: ws://mesh.example.com/node}\n", testNodeKey, "unencrypted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := newTestNode(t, nil)
			if err := os.WriteFile(n.opts.ConfigPath, []byte(test.config), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := n.SetKey(t.Context(), test.key)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if got, _ := os.ReadFile(n.opts.ConfigPath); string(got) != test.config {
				t.Fatalf("config changed:\n%s", got)
			}
			if _, err := os.Stat(n.DefaultKeyFile()); !os.IsNotExist(err) {
				t.Fatalf("key file left behind: %v", err)
			}
		})
	}
}

func TestSetKeyRestoresPreviousFileOnFailure(t *testing.T) {
	n := newTestNode(t, nil)
	if err := os.WriteFile(n.opts.ConfigPath, []byte("hub: {url: ws://mesh.example.com/node, allow_insecure: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.SetKey(t.Context(), testNodeKey); err != nil {
		t.Fatal(err)
	}
	// Now break the config so the next edit is refused, and check the old key survives.
	if err := os.WriteFile(n.opts.ConfigPath, []byte("hub: {url: ws://mesh.example.com/node}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.SetKey(t.Context(), "mm_node_zyxwvutsrqponmlkjihgfedcba"); err == nil {
		t.Fatal("expected the insecure transport error")
	}
	if data, _ := os.ReadFile(n.DefaultKeyFile()); string(data) != testNodeKey+"\n" {
		t.Fatalf("key file = %q", data)
	}
}
