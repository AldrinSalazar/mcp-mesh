package discovery

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

func TestTXT(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields []string
		want   Hub
	}{
		{"absent", nil, Hub{Name: "instance", NodePath: "/node"}},
		{"all fields", []string{"path=/custom", "v=2", "admin=7000", "client=7002", "name=My hub"}, Hub{Name: "My hub", NodePath: "/custom", Version: 2, AdminPort: 7000, ClientPort: 7002}},
		{"malformed", []string{"v=no", "v=-1", "admin=65536", "client=-3", "admin=0", "path=relative", "name=", "v", "unknown=value"}, Hub{Name: "instance", NodePath: "/node"}},
		{"invalid duplicates ignored", []string{"admin=80", "admin=oops", "v=3", "v=1.2", "path=/a=b", "path="}, Hub{Name: "instance", NodePath: "/a=b", Version: 3, AdminPort: 80}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Hub{Name: "instance"}
			parseTXT(&got, tt.fields)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name string
		a    Announcement
		want []string
	}{
		{"omit ports", Announcement{Name: "hub", NodePath: "/node", Version: 1}, []string{"path=/node", "v=1", "name=hub"}},
		{"include ports", Announcement{Name: "hub", NodePath: "/mesh", Version: 2, AdminPort: 80, ClientPort: 81}, []string{"path=/mesh", "v=2", "admin=80", "client=81", "name=hub"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := announcementTXT(tt.a); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func subnet(cidr string) *net.IPNet {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	network.IP = ip
	return network
}

func TestBestAddress(t *testing.T) {
	for _, tt := range []struct {
		name string
		ips  []string
		want string
	}{
		{"same subnet", []string{"2001:db8::1", "10.1.0.3", "192.168.1.9"}, "192.168.1.9"},
		{"first IPv4", []string{"2001:db8::1", "10.1.0.3", "10.2.0.4"}, "10.1.0.3"},
		{"IPv6 only", []string{"2001:db8::1", "2001:db8::2"}, "2001:db8::1"},
		{"empty", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ips []net.IP
			for _, s := range tt.ips {
				ips = append(ips, net.ParseIP(s))
			}
			if got := bestAddress(ips, []*net.IPNet{subnet("192.168.1.5/24")}); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInterfaces(t *testing.T) {
	goodFlags := net.FlagUp | net.FlagMulticast
	for _, tt := range []struct {
		name     string
		flags    net.Flags
		included bool
	}{
		{"eth0", goodFlags, true}, {"en0", goodFlags, true},
		{"down", net.FlagMulticast, false}, {"no-multicast", net.FlagUp, false}, {"lo", goodFlags | net.FlagLoopback, false},
		{"docker0", goodFlags, false}, {"br-abcd", goodFlags, false}, {"veth123", goodFlags, false},
		{"virbr0", goodFlags, false}, {"cni0", goodFlags, false}, {"flannel.1", goodFlags, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := interfaceInfo{Interface: net.Interface{Name: tt.name, Flags: tt.flags}, addrs: []*net.IPNet{subnet("192.168.1.2/24"), subnet("2001:db8::1/64"), subnet("fe80::1/64"), subnet("127.0.0.1/8"), subnet("0.0.0.0/32"), subnet("ff02::1/128")}}
			got := interfaceIPs([]interfaceInfo{info, info})
			want := []net.IP(nil)
			if tt.included {
				want = []net.IP{net.ParseIP("192.168.1.2"), net.ParseIP("2001:db8::1")}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func entry(instance, host, ip string, txt ...string) *mdns.ServiceEntry {
	e := &mdns.ServiceEntry{Name: instance + "." + ServiceType + ".local.", Host: host, Port: 7001, InfoFields: txt}
	if addr := net.ParseIP(ip); addr.To4() != nil {
		e.AddrV4 = addr
	} else {
		e.AddrV6 = addr
	}
	return e
}

func TestHubsFromEntries(t *testing.T) {
	for _, tt := range []struct {
		name        string
		entries     []*mdns.ServiceEntry
		names, urls []string
		ipCounts    []int
	}{
		{"empty", nil, nil, nil, nil},
		{"filter other service", []*mdns.ServiceEntry{nil, {Name: "printer._ipp._tcp.local.", Port: 631}, {Name: "x_mcp-mesh._tcp.local.", Port: 7001}, {Name: "bad." + ServiceType + ".local.", Port: 0}}, nil, nil, nil},
		{"merge by instance and host", []*mdns.ServiceEntry{
			entry("escaped", "host.local.", "10.0.0.2", "name=Hub"),
			entry("escaped", "host.local.", "10.0.0.2", "name=Hub"),
			entry("escaped", "host.local.", "192.168.1.8", "name=Hub"),
			entry("escaped", "host.local.", "2001:db8::1", "name=Hub"),
			entry("escaped", "other.local.", "2001:db8::2", "name=Hub", "path=/mesh"),
		}, []string{"Hub", "Hub"}, []string{"ws://192.168.1.8:7001/node", "ws://[2001:db8::2]:7001/mesh"}, []int{3, 1}},
		{"sort name then URL", []*mdns.ServiceEntry{entry("z", "z.local.", "10.0.0.3"), entry("a", "b.local.", "10.0.0.2"), entry("a", "a.local.", "10.0.0.1")}, []string{"a", "a", "z"}, []string{"ws://10.0.0.1:7001/node", "ws://10.0.0.2:7001/node", "ws://10.0.0.3:7001/node"}, []int{1, 1, 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := hubsFromEntries(tt.entries, []*net.IPNet{subnet("192.168.1.5/24")})
			if got == nil || len(got) != len(tt.names) {
				t.Fatalf("got %v, want %d hubs", got, len(tt.names))
			}
			for i, h := range got {
				if h.Name != tt.names[i] || h.URL != tt.urls[i] || len(h.IPs) != tt.ipCounts[i] {
					t.Errorf("hub %d: %+v", i, h)
				}
			}
		})
	}
}

func TestAnnounceInvalid(t *testing.T) {
	for _, tt := range []struct {
		name string
		a    Announcement
	}{
		{"empty addresses", Announcement{NodePort: 7001, IPs: []net.IP{}}},
		{"bad address", Announcement{NodePort: 7001, IPs: []net.IP{{1}}}},
		{"zero port", Announcement{IPs: []net.IP{net.IPv4(192, 0, 2, 1)}}},
		{"out of range port", Announcement{NodePort: 65536, IPs: []net.IP{net.IPv4(192, 0, 2, 1)}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Announce(t.Context(), tt.a); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBrowseCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := Browse(ctx, time.Hour, nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if err := Announce(ctx, Announcement{}); err != context.Canceled {
		t.Fatalf("announce: %v", err)
	}
}

func TestAnnounceBrowse(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	name := fmt.Sprintf("mesh-test-%d-%d", port, time.Now().UnixNano())
	ctx, cancel := context.WithCancel(t.Context())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done, announceErr := announce(ctx, Announcement{Name: name, NodePort: port, NodePath: "/test", Version: 3, AdminPort: 7000, ClientPort: 7002, Logger: logger})
	defer func() {
		cancel()
		if done != nil {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("responder did not stop")
			}
		}
	}()
	hubs, err := Browse(ctx, 3*time.Second, logger)
	if err != nil {
		t.Logf("browse: %v", err)
	}
	if announceErr != nil {
		t.Logf("announce: %v", announceErr)
	}
	for _, h := range hubs {
		if h.Name == name {
			if h.NodePort != port || h.NodePath != "/test" || h.Version != 3 || h.AdminPort != 7000 || h.ClientPort != 7002 || !strings.HasSuffix(h.URL, fmt.Sprintf(":%d/test", port)) {
				t.Fatalf("incorrect announcement: %+v", h)
			}
			return
		}
	}
	if len(hubs) == 0 {
		t.Skip("multicast seems unavailable here (sandboxes): no hubs answered within 3s")
	}
	t.Fatalf("other hubs answered but ours (%s) did not: %+v", name, hubs)
}
