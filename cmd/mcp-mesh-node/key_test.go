package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
)

const cliKey = "mm_node_0123456789abcdefghijklmnop"

// executeWithInput runs a command with stdin content.
func executeWithInput(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func assertKeyInstalled(t *testing.T, path, key string) string {
	t.Helper()
	keyFile := filepath.Join(filepath.Dir(path), "node.key")
	data, err := os.ReadFile(keyFile)
	if err != nil || strings.TrimSpace(string(data)) != key {
		t.Fatalf("key file: %q, %v", data, err)
	}
	if info, err := os.Stat(keyFile); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("key file mode: %v, %v", info, err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.Header.Get("Authorization") != "Bearer "+key {
		t.Fatal("config does not use the key")
	}
	return keyFile
}

func TestKeySet(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		args  []string
	}{
		{name: "argument", args: []string{cliKey}},
		{name: "stdin", input: cliKey + "\n", args: []string{"-"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			if _, err := execute(t, "init", "--hub", "wss://mesh.example.com/node"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(config.Example(), []byte("node:\n  name: desk\n")...), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := executeWithInput(t, test.input, append([]string{"key", "set"}, test.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			keyFile := assertKeyInstalled(t, path, cliKey)
			for _, want := range []string{keyFile, `node "desk"`, "mcp-mesh-hub key create node desk", "start the node"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, cliKey) {
				t.Errorf("output repeats the key:\n%s", out)
			}

			status, err := execute(t, "status")
			if err != nil || !strings.Contains(status, "Hub key: set ("+displayPath(keyFile)+")") {
				t.Fatalf("status: %v\n%s", err, status)
			}
			raw, err := execute(t, "status", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Config struct {
					HubKey hubKeyJSON `json:"hubKey"`
				} `json:"config"`
			}
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				t.Fatal(err)
			}
			if got := decoded.Config.HubKey; !got.Set || got.Source != "file" || got.File != keyFile {
				t.Fatalf("hubKey = %+v", got)
			}
			if strings.Contains(raw, cliKey) {
				t.Fatal("status --json contains the key")
			}

			out, err = execute(t, "key", "clear")
			if err != nil || !strings.Contains(out, "Deleted "+keyFile) {
				t.Fatalf("clear: %v\n%s", err, out)
			}
			if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
				t.Fatalf("key file remains: %v", err)
			}
			status, _ = execute(t, "status")
			if !strings.Contains(status, "Hub key: not set") {
				t.Fatalf("status after clear:\n%s", status)
			}
			if out, err = execute(t, "key", "clear"); err != nil || !strings.Contains(out, "No hub key") {
				t.Fatalf("second clear: %v\n%s", err, out)
			}
		})
	}
}

func TestKeySetPrompt(t *testing.T) {
	path, _, _ := setupCLI(t, true)
	if _, err := execute(t, "init", "--hub", "wss://mesh.example.com/node"); err != nil {
		t.Fatal(err)
	}
	old := readSecret
	t.Cleanup(func() { readSecret = old })
	readSecret = func() (string, error) { return cliKey + "\n", nil }
	out, err := execute(t, "key", "set")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Node key (input is hidden)") || strings.Contains(out, cliKey) {
		t.Fatalf("output:\n%s", out)
	}
	assertKeyInstalled(t, path, cliKey)
}

func TestKeySetErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		args  []string
		want  string
	}{
		{name: "no key and no terminal", args: nil, want: "provide the key as an argument"},
		{name: "wrong prefix", args: []string{"mm_client_0123456789abcdefghijklmnop"}, want: "starts with mm_node_"},
		{name: "empty stdin", args: []string{"-"}, want: "starts with mm_node_"},
		{name: "too short", args: []string{"mm_node_abc"}, want: "too short"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			if _, err := execute(t, "init", "--hub", "wss://mesh.example.com/node"); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			_, err := executeWithInput(t, test.input, append([]string{"key", "set"}, test.args...)...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("config changed")
			}
		})
	}
}

func TestInitWithKey(t *testing.T) {
	t.Run("argument", func(t *testing.T) {
		path, _, _ := setupCLI(t, false)
		out, err := execute(t, "init", "--hub", "wss://mesh.example.com/node", "--key", cliKey)
		if err != nil {
			t.Fatal(err)
		}
		assertKeyInstalled(t, path, cliKey)
		if !strings.Contains(out, "Wrote "+path) || !strings.Contains(out, "mcp-mesh-hub key create node") {
			t.Fatalf("output:\n%s", out)
		}
	})
	t.Run("stdin", func(t *testing.T) {
		path, _, _ := setupCLI(t, false)
		if _, err := executeWithInput(t, cliKey+"\n", "init", "--hub", "wss://mesh.example.com/node", "--key", "-"); err != nil {
			t.Fatal(err)
		}
		assertKeyInstalled(t, path, cliKey)
	})
	t.Run("name", func(t *testing.T) {
		path, _, _ := setupCLI(t, false)
		out, err := execute(t, "init", "--hub", "wss://mesh.example.com/node", "--name", "studio-pc", "--key", cliKey)
		if err != nil {
			t.Fatal(err)
		}
		assertKeyInstalled(t, path, cliKey)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NodeName != "studio-pc" {
			t.Fatalf("node name = %q", cfg.NodeName)
		}
		if !strings.Contains(out, `node "studio-pc"`) {
			t.Fatalf("output doesn't name the node the key is for:\n%s", out)
		}
	})
	t.Run("bad key writes nothing", func(t *testing.T) {
		path, _, _ := setupCLI(t, false)
		if _, err := execute(t, "init", "--hub", "wss://mesh.example.com/node", "--key", "nope"); err == nil {
			t.Fatal("expected an error")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("config written: %v", err)
		}
	})
	t.Run("insecure hub explains", func(t *testing.T) {
		path, _, _ := setupCLI(t, false)
		_, err := execute(t, "init", "--hub", "ws://mesh.example.com/node", "--key", cliKey)
		if err == nil || !strings.Contains(err.Error(), "unencrypted") || !strings.Contains(err.Error(), "was written") {
			t.Fatalf("error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), "node.key")); !os.IsNotExist(err) {
			t.Fatalf("key file left behind: %v", err)
		}
	})
}
