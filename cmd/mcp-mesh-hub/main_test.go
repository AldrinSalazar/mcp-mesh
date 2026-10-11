package main

import (
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/server"

	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestDiscoveryFlags(t *testing.T) {
	for _, tt := range []struct {
		name, value string
		args        []string
		want        bool
	}{
		{"default", "", nil, true}, {"true", "true", nil, true}, {"false", "false", nil, false},
		{"one", "1", nil, true}, {"zero", "0", nil, false},
		{"flag overrides env", "false", []string{"--mdns=true", "--name=Flag hub"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MCPMESH_MDNS", tt.value)
			t.Setenv("MCPMESH_NAME", "Env hub")
			cmd := rootCmd()
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatal(err)
			}
			got, _ := cmd.Flags().GetBool("mdns")
			if got != tt.want {
				t.Errorf("mdns = %v, want %v", got, tt.want)
			}
			name, _ := cmd.Flags().GetString("name")
			wantName := "Env hub"
			if len(tt.args) > 0 {
				wantName = "Flag hub"
			}
			if name != wantName {
				t.Errorf("name = %q, want %q", name, wantName)
			}
		})
	}
}

func TestHostnameDefault(t *testing.T) {
	old, exists := os.LookupEnv("MCPMESH_NAME")
	os.Unsetenv("MCPMESH_NAME")
	t.Cleanup(func() {
		if exists {
			os.Setenv("MCPMESH_NAME", old)
		} else {
			os.Unsetenv("MCPMESH_NAME")
		}
	})
	want, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := rootCmd().Flags().GetString("name")
	if got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

func TestServeBindFailureClosesListeners(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := first.Addr().String()
	first.Close()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	err = serve(t.Context(), config{Config: server.Config{DataDir: t.TempDir(), AdminListen: addr, NodeListen: busy.Addr().String(), ClientListen: "127.0.0.1:0"}, logLevel: "error", logFormat: "text"})
	if err == nil {
		t.Fatal("expected bind error")
	}
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("admin listener left open: %v", err)
	}
	probe.Close()
}

func TestServeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, config{Config: server.Config{DataDir: t.TempDir(), AdminListen: "127.0.0.1:0", NodeListen: "127.0.0.1:0", ClientListen: "127.0.0.1:0"}, logLevel: "error", logFormat: "text"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hub did not stop")
	}
}
