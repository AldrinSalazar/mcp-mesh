// Package discovery lets nodes find hubs on the local network with mDNS /
// DNS-SD. The hub announces ServiceType; nodes browse for it and present what
// they find for the user to choose. A discovered hub is only a suggestion:
// connecting to a hub lets it call this machine's MCP tools, so callers never
// connect to one without the user picking it.
//
// mDNS stays on one link: it does not cross Docker bridge networks, VPNs such
// as Tailscale, or routers. Those setups keep using explicit URLs.
package discovery

import (
	"context"
	"log/slog"
	"net"
	"time"
)

// ServiceType is the DNS-SD service hubs announce, in the "local." domain.
const ServiceType = "_mcp-mesh._tcp"

// Announcement is what a hub publishes. The port is the node endpoint's.
type Announcement struct {
	Name       string // instance name shown to users; defaults to the hostname
	NodePort   int
	NodePath   string // e.g. "/node"
	Version    int    // proto.Version
	AdminPort  int    // 0 if unknown
	ClientPort int    // 0 if unknown
	// IPs to announce; nil means every up, non-loopback interface address
	// (resolving the hostname is wrong on many systems, e.g. 127.0.1.1).
	IPs    []net.IP
	Logger *slog.Logger
}

// Announce publishes a until ctx ends. It returns once the responder is
// running; the responder stops when ctx ends.
func Announce(ctx context.Context, a Announcement) error {
	_, err := announce(ctx, a)
	return err
}

// Hub is one hub found on the network.
type Hub struct {
	Name       string
	Host       string   // mDNS host name, e.g. "home-lab.local."
	IPs        []net.IP // as announced
	NodePort   int
	NodePath   string
	Version    int
	AdminPort  int
	ClientPort int
	// URL is the node endpoint to use, built from the best address: an IPv4
	// address on one of this machine's subnets, else the first IPv4, else IPv6.
	URL string
}

// Browse queries the network for timeout (or until ctx ends) and returns
// the hubs that answered, de-duplicated and sorted by name.
func Browse(ctx context.Context, timeout time.Duration, logger *slog.Logger) ([]Hub, error) {
	return browse(ctx, timeout, logger)
}
