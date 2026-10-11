//go:build linux || darwin

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MCPMESH_TEST_COMMAND", "~/bin/mcp")
	key := filepath.Join(home, "node.key")
	if err := os.WriteFile(key, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := mustParse(t, `hub: {url: ws://localhost/node, key_file: ~/node.key}
mcps:
  test:
    command: '${env:MCPMESH_TEST_COMMAND}'
    args: [~/code, '~', '~another/code', 'prefix/~/code']
    dir: ~/work
    env: { LITERAL: ~/unchanged }
`)
	mcp := cfg.MCPs[0]
	if mcp.Command != filepath.Join(home, "bin", "mcp") || mcp.Dir != filepath.Join(home, "work") || !reflect.DeepEqual(mcp.Args, []string{filepath.Join(home, "code"), "~", "~another/code", "prefix/~/code"}) || !reflect.DeepEqual(mcp.Env, []string{"LITERAL=~/unchanged"}) || cfg.Hub.Header.Get("Authorization") != "Bearer key" {
		t.Fatalf("home expansion: %#v, %#v", cfg, mcp)
	}
	t.Setenv("HOME", "")
	_, err := Parse([]byte("hub: {url: ws://localhost/node}\nmcps: {test: {command: ~/bin/mcp}}"), "node.yaml")
	if err == nil || !strings.Contains(err.Error(), "mcps.test.command") || !strings.Contains(err.Error(), "home") {
		t.Fatalf("want located home error, got %v", err)
	}
}

func TestKeyFilePermissions(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		bad  bool
	}{
		{"private", 0600, false},
		{"read only", 0400, false},
		{"group readable", 0640, true},
		{"world readable", 0604, true},
		{"both readable", 0644, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "node.key")
			if err := os.WriteFile(file, []byte("key"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := Parse([]byte("hub: {url: ws://localhost/node, key_file: '"+file+"'}"), "node.yaml")
			if test.bad {
				if err == nil || !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), "hub.key_file") {
					t.Fatalf("want chmod 600 error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
