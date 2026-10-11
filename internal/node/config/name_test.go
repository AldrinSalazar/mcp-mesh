package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetNodeName(t *testing.T) {
	tests := []struct {
		name   string
		before func(path string) error
	}{
		{"starter config", func(path string) error { return Create(path, "ws://hub.lan:7001/node", false) }},
		{"existing node mapping", func(path string) error {
			return os.WriteFile(path, []byte("hub:\n  url: ws://hub.lan:7001/node\nnode:\n  name: old # keep me\n"), 0o600)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node.yaml")
			if err := tt.before(path); err != nil {
				t.Fatal(err)
			}
			if err := SetNodeName(path, "studio-pc"); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.NodeName != "studio-pc" {
				t.Fatalf("NodeName = %q", cfg.NodeName)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "#   name: work-pc") {
				t.Errorf("the commented-out example is still there:\n%s", data)
			}
			if strings.Count(string(data), "node:") != 1 {
				t.Errorf("want one node entry:\n%s", data)
			}
		})
	}
	if err := SetNodeName(filepath.Join(t.TempDir(), "x.yaml"), " "); err == nil {
		t.Error("an empty name must be refused")
	}
}
