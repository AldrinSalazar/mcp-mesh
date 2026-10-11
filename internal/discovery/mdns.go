package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

type interfaceInfo struct {
	net.Interface
	addrs []*net.IPNet
}

func localInterfaces() ([]interfaceInfo, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	var out []interfaceInfo
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("addresses of %s: %w", iface.Name, err)
		}
		info := interfaceInfo{Interface: iface}
		for _, addr := range addrs {
			if subnet, ok := addr.(*net.IPNet); ok {
				info.addrs = append(info.addrs, subnet)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func eligibleInterfaces(interfaces []interfaceInfo) []interfaceInfo {
	var out []interfaceInfo
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		virtual := false
		for _, prefix := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel"} {
			if strings.HasPrefix(iface.Name, prefix) {
				virtual = true
				break
			}
		}
		if !virtual {
			out = append(out, iface)
		}
	}
	return out
}

func interfaceIPs(interfaces []interfaceInfo) []net.IP {
	var ips []net.IP
	for _, iface := range eligibleInterfaces(interfaces) {
		for _, addr := range iface.addrs {
			ip := addr.IP
			if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.To16() == nil {
				continue
			}
			if ip.To4() == nil && ip.IsLinkLocalUnicast() {
				continue
			}
			ips = appendIP(ips, ip)
		}
	}
	return ips
}

func announce(ctx context.Context, a Announcement) (<-chan struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("get hostname: %w", err)
	}
	if a.Name == "" {
		a.Name = host
	}
	if a.NodePort < 1 || a.NodePort > 65535 {
		return nil, fmt.Errorf("invalid node port %d", a.NodePort)
	}
	if a.NodePath == "" {
		a.NodePath = "/node"
	}
	if a.IPs == nil {
		interfaces, err := localInterfaces()
		if err != nil {
			return nil, err
		}
		a.IPs = interfaceIPs(interfaces)
	}
	// NewMDNSService resolves the hostname for an empty list, even a non-nil one.
	if len(a.IPs) == 0 {
		return nil, fmt.Errorf("no addresses available for mDNS announcement")
	}
	service, err := mdns.NewMDNSService(a.Name, ServiceType, "local.", strings.TrimSuffix(host, ".")+".local.", a.NodePort, a.IPs, announcementTXT(a))
	if err != nil {
		return nil, fmt.Errorf("create mDNS service: %w", err)
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: service, Logger: slog.NewLogLogger(loggerOrDefault(a.Logger).Handler(), slog.LevelDebug)})
	if err != nil {
		return nil, fmt.Errorf("start mDNS responder: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		if err := server.Shutdown(); err != nil {
			loggerOrDefault(a.Logger).Debug("stop mDNS responder", "error", err)
		}
	}()
	return done, nil
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

func announcementTXT(a Announcement) []string {
	txt := []string{"path=" + a.NodePath, "v=" + strconv.Itoa(a.Version)}
	if a.AdminPort != 0 {
		txt = append(txt, "admin="+strconv.Itoa(a.AdminPort))
	}
	if a.ClientPort != 0 {
		txt = append(txt, "client="+strconv.Itoa(a.ClientPort))
	}
	return append(txt, "name="+a.Name)
}

func parseTXT(h *Hub, fields []string) {
	h.NodePath = "/node"
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "name":
			if value != "" {
				h.Name = value
			}
		case "path":
			if strings.HasPrefix(value, "/") {
				h.NodePath = value
			}
		case "v", "admin", "client":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (key != "v" && (n == 0 || n > 65535)) {
				continue
			}
			switch key {
			case "v":
				h.Version = n
			case "admin":
				h.AdminPort = n
			case "client":
				h.ClientPort = n
			}
		}
	}
}

func appendIP(ips []net.IP, ip net.IP) []net.IP {
	if ip.To16() == nil {
		return ips
	}
	for _, existing := range ips {
		if existing.Equal(ip) {
			return ips
		}
	}
	return append(ips, append(net.IP(nil), ip...))
}

func bestAddress(ips []net.IP, subnets []*net.IPNet) string {
	for _, ip := range ips {
		if ip.To4() != nil {
			for _, subnet := range subnets {
				if subnet.Contains(ip) {
					return ip.String()
				}
			}
		}
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	for _, ip := range ips {
		if ip.To16() != nil {
			return ip.String()
		}
	}
	return ""
}

func hubsFromEntries(entries []*mdns.ServiceEntry, subnets []*net.IPNet) []Hub {
	hubs := make([]Hub, 0)
	indices := make(map[string]int)
	suffix := "." + ServiceType + ".local."
	for _, entry := range entries {
		if entry == nil || !strings.HasSuffix(strings.ToLower(entry.Name), suffix) || entry.Port < 1 || entry.Port > 65535 {
			continue
		}
		instance := entry.Name[:len(entry.Name)-len(suffix)]
		if instance == "" {
			continue
		}
		key := strings.ToLower(entry.Name + "\x00" + entry.Host)
		index, exists := indices[key]
		if !exists {
			h := Hub{Name: instance, Host: entry.Host, NodePort: entry.Port}
			parseTXT(&h, entry.InfoFields)
			index = len(hubs)
			indices[key] = index
			hubs = append(hubs, h)
		}
		h := &hubs[index]
		for _, ip := range []net.IP{entry.AddrV4, entry.AddrV6, entry.Addr} {
			h.IPs = appendIP(h.IPs, ip)
		}
		if entry.AddrV6IPAddr != nil {
			h.IPs = appendIP(h.IPs, entry.AddrV6IPAddr.IP)
		}
	}
	for i := range hubs {
		h := &hubs[i]
		h.URL = "ws://" + net.JoinHostPort(bestAddress(h.IPs, subnets), strconv.Itoa(h.NodePort)) + h.NodePath
	}
	sort.Slice(hubs, func(i, j int) bool {
		if hubs[i].Name == hubs[j].Name {
			return hubs[i].URL < hubs[j].URL
		}
		return hubs[i].Name < hubs[j].Name
	})
	return hubs
}

// The library sends pointers which it continues to mutate. Collect pointers
// concurrently, but inspect them only after QueryContext returns.
func queryEntries(ctx context.Context, params *mdns.QueryParam) ([]*mdns.ServiceEntry, error) {
	entries := make(chan *mdns.ServiceEntry, 128)
	params.Entries = entries
	done := make(chan error, 1)
	go func() {
		done <- mdns.QueryContext(ctx, params)
		close(entries)
	}()
	var out []*mdns.ServiceEntry
	for entry := range entries {
		out = append(out, entry)
	}
	return out, <-done
}

func browse(ctx context.Context, timeout time.Duration, logger *slog.Logger) ([]Hub, error) {
	if ctx.Err() != nil || timeout <= 0 {
		return []Hub{}, nil
	}
	interfaces, err := localInterfaces()
	if err != nil {
		return nil, err
	}
	var subnets []*net.IPNet
	for _, iface := range interfaces {
		subnets = append(subnets, iface.addrs...)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	type result struct {
		entries []*mdns.ServiceEntry
		err     error
		ran     bool
	}
	var wg sync.WaitGroup
	results := make(chan result)
	count := 0
	for _, iface := range eligibleInterfaces(interfaces) {
		for _, ipv6 := range []bool{false, true} {
			hasFamily := false
			for _, addr := range iface.addrs {
				if (addr.IP.To4() == nil) == ipv6 {
					hasFamily = true
				}
			}
			if !hasFamily {
				continue
			}
			count++
			wg.Go(func() {
				r := result{}
				for ctx.Err() == nil {
					// Re-query once a second (multicast is lossy) rather than
					// flooding the link. QueryContext only returns at the end of
					// its window, so cancellation takes up to a second.
					window := min(time.Second, time.Until(deadline))
					if window <= 0 {
						break
					}
					entries, err := queryEntries(ctx, &mdns.QueryParam{
						Service: ServiceType, Domain: "local", Timeout: window,
						Interface: &iface.Interface, DisableIPv4: ipv6, DisableIPv6: !ipv6,
						Logger: slog.NewLogLogger(loggerOrDefault(logger).Handler(), slog.LevelDebug),
					})
					r.entries = append(r.entries, entries...)
					if err != nil {
						r.err = err
						break
					}
					r.ran = true
				}
				results <- r
			})
		}
	}
	go func() { wg.Wait(); close(results) }()
	var entries []*mdns.ServiceEntry
	ran := false
	for r := range results {
		entries = append(entries, r.entries...)
		ran = ran || r.ran
		if r.err != nil {
			err = r.err
		}
	}
	if !ran && ctx.Err() == nil {
		if count == 0 {
			err = fmt.Errorf("no multicast interfaces available")
		}
		return nil, fmt.Errorf("query mDNS: %w", err)
	}
	return hubsFromEntries(entries, subnets), nil
}
