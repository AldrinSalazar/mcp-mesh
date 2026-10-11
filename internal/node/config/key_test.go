package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testKey = "mm_node_0123456789abcdefghijklmnop"

func TestInsecureTransportGuard(t *testing.T) {
	t.Setenv("MCPMESH_TEST_KEY", testKey)
	for _, test := range []struct {
		name     string
		url      string
		extra    string
		noKey    bool
		wantHost string // non-empty: expect the guard error naming this host
	}{
		{name: "loopback v4", url: "ws://127.0.0.1:7101/node"},
		{name: "loopback v6", url: "ws://[::1]:7101/node"},
		{name: "localhost", url: "ws://localhost/node"},
		{name: "10/8", url: "ws://10.1.2.3/node"},
		{name: "172.16/12", url: "ws://172.31.0.1/node"},
		{name: "192.168/16", url: "ws://192.168.1.10/node"},
		{name: "ULA", url: "ws://[fd12:3456::1]/node"},
		{name: "link-local v4", url: "ws://169.254.1.1/node"},
		{name: "link-local v6", url: "ws://[fe80::1]/node"},
		{name: "CGNAT low", url: "ws://100.64.0.1/node"},
		{name: "CGNAT high", url: "ws://100.127.255.254/node"},
		{name: "single label", url: "ws://myhub:7101/node"},
		{name: ".local", url: "ws://hub.local/node"},
		{name: ".lan", url: "ws://hub.lan/node"},
		{name: ".home.arpa", url: "ws://hub.home.arpa/node"},
		{name: ".ts.net", url: "ws://hub.tail1234.ts.net/node"},
		{name: "public name", url: "ws://mesh.example.com/node", wantHost: "mesh.example.com"},
		{name: "public ip", url: "ws://8.8.8.8/node", wantHost: "8.8.8.8"},
		{name: "outside 172.16/12", url: "ws://172.32.0.1/node", wantHost: "172.32.0.1"},
		{name: "outside CGNAT", url: "ws://100.128.0.1/node", wantHost: "100.128.0.1"},
		{name: "public v6", url: "ws://[2001:db8::1]/node", wantHost: "2001:db8::1"},
		{name: "name that only looks local", url: "ws://hub.local.example.com/node", wantHost: "hub.local.example.com"},
		{name: "wss is fine", url: "wss://mesh.example.com/node"},
		{name: "allow_insecure", url: "ws://mesh.example.com/node", extra: "  allow_insecure: true\n"},
		{name: "no key", url: "ws://mesh.example.com/node", noKey: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := "hub:\n  url: " + test.url + "\n" + test.extra
			if !test.noKey {
				data += "  key_env: MCPMESH_TEST_KEY\n"
			}
			cfg, err := Parse([]byte(data), "node.yaml")
			if test.wantHost == "" {
				if err != nil {
					t.Fatal(err)
				}
				if got := cfg.Hub.Header.Get("Authorization") != ""; got == test.noKey {
					t.Fatalf("authorization header present = %v", got)
				}
				return
			}
			if err == nil {
				t.Fatal("expected the insecure transport error")
			}
			for _, want := range []string{"node.yaml:2:", "hub.url: ws:// would send the node key unencrypted to " + test.wantHost, "wss://", "hub.allow_insecure: true"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestValidateKey(t *testing.T) {
	for _, test := range []struct {
		key  string
		want string // empty: valid
	}{
		{key: testKey},
		{key: "mm_node_" + strings.Repeat("A1_-", 8)},
		{key: "", want: "starts with mm_node_"},
		{key: "mm_client_0123456789abcdefghijklmnop", want: "starts with mm_node_"},
		{key: "mm_node_short", want: "too short"},
		{key: testKey + strings.Repeat("x", 600), want: "too long"},
		{key: testKey + " extra", want: "no spaces"},
		{key: testKey + "\n", want: "no spaces"},
		{key: "mm_node_0123456789abcdefé", want: "no spaces"},
	} {
		err := ValidateKey(test.key)
		if test.want == "" {
			if err != nil {
				t.Errorf("%q: %v", test.key, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%q: error %v, want %q", test.key, err, test.want)
		}
	}
}

func TestSetAndClearKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	if err := Create(path, "wss://mesh.example.com/node", false); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, DefaultKeyFile)
	if err := WriteKeyFile(keyPath, testKey); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode %v", info.Mode().Perm())
	}
	if err := SetKeyFile(path, keyPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Connect this node to your hub's node endpoint.", "# Choose one credential source", "# key_env: MCPMESH_NODE_KEY", "# Omit the name", "key_file: " + keyPath, "# blender:"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config lost %q:\n%s", want, data)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.Header.Get("Authorization") != "Bearer "+testKey {
		t.Fatal("key not loaded")
	}
	source, err := HubKey(path)
	if err != nil || source.File != keyPath || source.Env != "" || !source.Set() {
		t.Fatalf("HubKey = %+v, %v", source, err)
	}

	// Replacing an env key keeps its place and removes the variable.
	t.Setenv("MCPMESH_TEST_KEY", testKey)
	env := "# top comment\nhub:\n  url: wss://mesh.example.com/node\n  key_env: MCPMESH_TEST_KEY # from the environment\nnode: {name: desktop}\n"
	if err := os.WriteFile(path, []byte(env), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyFile(path, keyPath); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "key_env") || !strings.Contains(string(data), "# top comment") || !strings.Contains(string(data), "key_file: "+keyPath) {
		t.Fatalf("env replacement:\n%s", data)
	}

	if err := ClearKey(path); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "key_") || !strings.Contains(string(data), "# top comment") {
		t.Fatalf("after clear:\n%s", data)
	}
	if source, err := HubKey(path); err != nil || source.Set() {
		t.Fatalf("HubKey after clear = %+v, %v", source, err)
	}
	// Clearing again is harmless.
	if err := ClearKey(path); err != nil {
		t.Fatal(err)
	}
}

func TestSetKeyFileRejectsInsecureHub(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	before := "hub:\n  url: ws://mesh.example.com/node\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, DefaultKeyFile)
	if err := WriteKeyFile(keyPath, testKey); err != nil {
		t.Fatal(err)
	}
	err := SetKeyFile(path, keyPath)
	if err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Fatalf("error = %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != before {
		t.Fatalf("config changed:\n%s", after)
	}
}
