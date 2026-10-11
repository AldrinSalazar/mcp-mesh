package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func fakeHubs() []discovery.Hub {
	return []discovery.Hub{
		{Name: "office", Host: "office.local.", IPs: []net.IP{net.ParseIP("192.168.1.10"), net.ParseIP("2001:db8::10")}, URL: "ws://192.168.1.10:7001/node", Version: proto.MinVersion, AdminPort: 7000, ClientPort: 7002},
		{Name: "laptop", Host: "laptop.local.", IPs: []net.IP{net.ParseIP("2001:db8::20")}, URL: "ws://[2001:db8::20]:8001/node", Version: proto.Version, AdminPort: 8000},
		{Name: "old", URL: "ws://192.168.1.30:7001/node", Version: proto.MinVersion - 1},
		{Name: "new", URL: "ws://192.168.1.40:7001/node", Version: proto.Version + 1},
	}
}

func TestDiscover(t *testing.T) {
	for _, tt := range []struct {
		name    string
		json    bool
		empty   bool
		timeout string
	}{
		{name: "text"},
		{name: "json", json: true, timeout: "750ms"},
		{name: "empty text", empty: true},
		{name: "empty json", empty: true, json: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupCLI(t, false)
			hubs := fakeHubs()
			if tt.empty {
				hubs = nil
			}
			wantTimeout := 3 * time.Second
			args := []string{"discover"}
			if tt.timeout != "" {
				args = append(args, "--timeout", tt.timeout)
				var err error
				wantTimeout, err = time.ParseDuration(tt.timeout)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tt.json {
				args = append(args, "--json")
			}
			calls := 0
			discoverHubs = func(ctx context.Context, node *nodectl.Node, timeout time.Duration) ([]discovery.Hub, error) {
				calls++
				if ctx != t.Context() || node == nil || timeout != wantTimeout {
					t.Fatalf("discovery: context=%v node=%v timeout=%v", ctx, node, timeout)
				}
				return hubs, nil
			}
			out, notices, err := runCLI(t, "", args...)
			if err != nil || calls != 1 || !strings.HasPrefix(notices, "Searching for hubs on the local network…\n") || strings.Contains(out, "Searching") {
				t.Fatalf("stdout=%s stderr=%s calls=%d err=%v", out, notices, calls, err)
			}
			if tt.empty {
				message := out
				if tt.json {
					if strings.TrimSpace(out) != "[]" {
						t.Fatalf("empty JSON: %s", out)
					}
					message = notices
				}
				for _, hint := range []string{"No hubs found", "mDNS", "default", "same network", "Docker bridge", "VPNs", "Tailscale", "hub's URL"} {
					if !strings.Contains(message, hint) {
						t.Errorf("missing %q: %s", hint, message)
					}
				}
				return
			}
			if tt.json {
				var got []map[string]any
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				want := []map[string]any{
					{"name": "office", "url": "ws://192.168.1.10:7001/node", "host": "office.local.", "ips": []any{"192.168.1.10", "2001:db8::10"}, "version": float64(proto.MinVersion), "adminPort": float64(7000), "clientPort": float64(7002), "compatible": true},
					{"name": "laptop", "url": "ws://[2001:db8::20]:8001/node", "host": "laptop.local.", "ips": []any{"2001:db8::20"}, "version": float64(proto.Version), "adminPort": float64(8000), "clientPort": float64(0), "compatible": true},
					{"name": "old", "url": "ws://192.168.1.30:7001/node", "host": "", "ips": []any{}, "version": float64(proto.MinVersion - 1), "adminPort": float64(0), "clientPort": float64(0), "compatible": false},
					{"name": "new", "url": "ws://192.168.1.40:7001/node", "host": "", "ips": []any{}, "version": float64(proto.Version + 1), "adminPort": float64(0), "clientPort": float64(0), "compatible": false},
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("JSON=%#v want=%#v", got, want)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			want := [][]string{
				{"NAME", "URL", "VERSION", "ADMIN"},
				{"office", hubs[0].URL, strconv.Itoa(proto.MinVersion), "http://192.168.1.10:7000"},
				{"laptop", hubs[1].URL, strconv.Itoa(proto.Version), "http://[2001:db8::20]:8000"},
				{"old", hubs[2].URL, strconv.Itoa(proto.MinVersion - 1), "(incompatible)", "-"},
				{"new", hubs[3].URL, strconv.Itoa(proto.Version + 1), "(incompatible)", "-"},
			}
			if len(lines) != len(want) {
				t.Fatalf("table: %s", out)
			}
			for i, line := range lines {
				if !reflect.DeepEqual(strings.Fields(line), want[i]) {
					t.Errorf("row %d: %s want=%v", i, line, want[i])
				}
			}
		})
	}
}

func TestInitDiscover(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
		empty             bool
		prompts           int
		notice            string
	}{
		{name: "number", input: "2\n", want: "ws://[2001:db8::20]:8001/node", prompts: 1},
		{name: "enter", input: "\n", want: "ws://192.168.1.10:7001/node", prompts: 1},
		{name: "ws URL", input: "ws://manual:7001/node\n", want: "ws://manual:7001/node", prompts: 1},
		{name: "wss URL", input: "wss://mesh.example.com/node\n", want: "wss://mesh.example.com/node", prompts: 1},
		{name: "listed URL", input: "ws://[2001:db8::20]:8001/node\n", want: "ws://[2001:db8::20]:8001/node", prompts: 1},
		{name: "old hub", input: "3\n2\n", want: "ws://[2001:db8::20]:8001/node", prompts: 2, notice: `Hub "old" is incompatible`},
		{name: "new hub", input: "4\n\n", want: "ws://192.168.1.10:7001/node", prompts: 2, notice: `Hub "new" is incompatible`},
		{name: "incompatible URL", input: "ws://192.168.1.40:7001/node\n1\n", want: "ws://192.168.1.10:7001/node", prompts: 2, notice: `Hub "new" is incompatible`},
		{name: "invalid choices", input: "0\n5\nabc\nhttps://hub/node\nws:///node\n1\n", want: "ws://192.168.1.10:7001/node", prompts: 6, notice: "Enter a hub number or a full ws:// or wss:// URL."},
		{name: "none found", input: "ws://manual:7001/node\n", want: "ws://manual:7001/node", empty: true, notice: "No hubs found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, true)
			hubs := fakeHubs()
			if tt.empty {
				hubs = nil
			}
			calls := 0
			discoverHubs = func(_ context.Context, node *nodectl.Node, timeout time.Duration) ([]discovery.Hub, error) {
				calls++
				if node == nil || timeout != 3*time.Second {
					t.Fatalf("node=%v timeout=%v", node, timeout)
				}
				return hubs, nil
			}
			out, notices, err := runCLI(t, tt.input, "init")
			if err != nil || calls != 1 {
				t.Fatalf("stderr=%s calls=%d err=%v", notices, calls, err)
			}
			cfg, err := config.Load(path)
			if err != nil || cfg.Hub.URL != tt.want {
				t.Fatalf("config=%v err=%v", cfg, err)
			}
			if strings.Count(notices, "Hub [1]:") != tt.prompts || !strings.HasPrefix(notices, "Searching for hubs on the local network…\n") || !strings.Contains(notices, tt.notice) {
				t.Fatalf("stderr: %s", notices)
			}
			if !tt.empty {
				for _, hub := range hubs {
					if !strings.Contains(notices, hub.Name+" — "+hub.URL) {
						t.Errorf("missing hub: %s", notices)
					}
				}
				if strings.Count(notices, "(incompatible)") != 2 || strings.Contains(notices, "Hub URL") {
					t.Fatalf("list: %s", notices)
				}
			} else if !strings.Contains(notices, "Hub URL") {
				t.Fatalf("missing URL prompt: %s", notices)
			}
			if !strings.Contains(out, "Wrote "+path) || strings.Contains(out, "Hub [1]") || strings.Contains(out, "Searching") || strings.Contains(out, "office") {
				t.Fatalf("stdout: %s", out)
			}
		})
	}
}

func TestInitSkipsDiscovery(t *testing.T) {
	for _, tt := range []struct {
		name, want  string
		interactive bool
		args        []string
		noInputEnv  bool
	}{
		{name: "noninteractive", want: "wss://mesh.example.com/node"},
		{name: "hub flag", interactive: true, args: []string{"--hub", "ws://flag/node"}, want: "ws://flag/node"},
		{name: "no-input flag", interactive: true, args: []string{"--no-input"}, want: "wss://mesh.example.com/node"},
		{name: "no-input env", interactive: true, noInputEnv: true, want: "wss://mesh.example.com/node"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, tt.interactive)
			if tt.noInputEnv {
				t.Setenv("MCPMESH_NO_INPUT", "1")
			}
			discoverHubs = func(context.Context, *nodectl.Node, time.Duration) ([]discovery.Hub, error) {
				t.Fatal("unexpected discovery")
				return nil, nil
			}
			_, notices, err := runCLI(t, "", append([]string{"init"}, tt.args...)...)
			if err != nil || notices != "" {
				t.Fatalf("stderr=%s err=%v", notices, err)
			}
			cfg, err := config.Load(path)
			if err != nil || cfg.Hub.URL != tt.want {
				t.Fatalf("config=%v err=%v", cfg, err)
			}
		})
	}
}

func TestDiscoverErrors(t *testing.T) {
	for _, args := range [][]string{{"discover"}, {"discover", "--json"}, {"discover", "--timeout", "0s"}, {"discover", "--timeout", "-1s"}, {"discover", "--timeout", "bad"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			setupCLI(t, false)
			failure := errors.New("no multicast interface")
			calls := 0
			discoverHubs = func(context.Context, *nodectl.Node, time.Duration) ([]discovery.Hub, error) {
				calls++
				return nil, failure
			}
			out, _, err := runCLI(t, "", args...)
			if err == nil || out != "" {
				t.Fatalf("stdout=%s err=%v", out, err)
			}
			if len(args) <= 2 {
				if !errors.Is(err, failure) || calls != 1 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if calls != 0 {
				t.Fatal("discovery called with invalid timeout")
			}
		})
	}
}

func TestInitDiscoveryFailure(t *testing.T) {
	path, _, _ := setupCLI(t, true)
	discoverHubs = func(context.Context, *nodectl.Node, time.Duration) ([]discovery.Hub, error) {
		return nil, errors.New("no multicast interface")
	}
	_, notices, err := runCLI(t, "ws://manual/node\n", "init")
	if err != nil || !strings.Contains(notices, "Could not discover hubs: no multicast interface") || !strings.Contains(notices, "Hub URL") {
		t.Fatalf("stderr=%s err=%v", notices, err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Hub.URL != "ws://manual/node" {
		t.Fatalf("config=%v err=%v", cfg, err)
	}
}

func TestInitHubSelectionEOF(t *testing.T) {
	for _, input := range []string{"", "3\n", "invalid\n"} {
		t.Run(input, func(t *testing.T) {
			path, _, _ := setupCLI(t, true)
			discoverHubs = func(context.Context, *nodectl.Node, time.Duration) ([]discovery.Hub, error) { return fakeHubs(), nil }
			_, _, err := runCLI(t, input, "init")
			if !errors.Is(err, io.EOF) {
				t.Fatalf("err=%v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("config exists: %v", err)
			}
		})
	}
}

func TestHubAdminURL(t *testing.T) {
	for _, tt := range []struct {
		name string
		hub  discovery.Hub
		want string
	}{
		{"no port", discovery.Hub{URL: "ws://192.168.1.10/node"}, "-"},
		{"no address", discovery.Hub{AdminPort: 7000}, "-"},
		{"unknown IP", discovery.Hub{URL: "ws://hub.local/node", AdminPort: 7000}, "-"},
		{"selected address", discovery.Hub{URL: "ws://192.168.1.20/node", IPs: []net.IP{net.ParseIP("192.168.1.10")}, AdminPort: 7000}, "http://192.168.1.20:7000"},
		{"fallback IP", discovery.Hub{URL: "ws://hub.local/node", IPs: []net.IP{nil, net.ParseIP("192.168.1.10")}, AdminPort: 7000}, "http://192.168.1.10:7000"},
		{"IPv6", discovery.Hub{URL: "ws://[2001:db8::1]/node", AdminPort: 7000}, "http://[2001:db8::1]:7000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := hubAdminURL(tt.hub); got != tt.want {
				t.Fatalf("admin=%s want=%s", got, tt.want)
			}
		})
	}
}

func TestDiscoverHelp(t *testing.T) {
	setupCLI(t, false)
	cmd, _, err := rootCmd().Find([]string{"discover"})
	if err != nil || cmd.GroupID != "setup" {
		t.Fatalf("discover group: %v %v", cmd, err)
	}
	out, _, err := runCLI(t, "", "discover", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"never connects", "--timeout", "3s", "--json", "mDNS", "Docker", "Tailscale"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}
