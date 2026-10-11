package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseProxies reads a comma-separated list of addresses and CIDR prefixes
// (MCPMESH_TRUSTED_PROXIES / --trusted-proxies).
func ParseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not an address or CIDR prefix", f)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

type httpsKey struct{}

// IsHTTPS reports whether the client's connection was encrypted: TLS to the
// hub itself, or HTTPS to a trusted proxy (see TrustProxies).
func IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	https, _ := r.Context().Value(httpsKey{}).(bool)
	return https
}

// TrustProxies makes requests that arrive through a trusted reverse proxy
// look like they came from the client: the remote address becomes the
// rightmost X-Forwarded-For entry that isn't itself a trusted proxy, and
// X-Forwarded-Proto "https" counts as TLS for IsHTTPS (so LocalOrTLS and the
// session cookie's Secure flag see the client's side). Requests from other
// peers are untouched and their forwarding headers ignored, since anyone can
// send them. With no trusted proxies it returns next.
func TrustProxies(trusted []netip.Prefix, next http.Handler) http.Handler {
	if len(trusted) == 0 {
		return next
	}
	isTrusted := func(a netip.Addr) bool {
		a = a.Unmap().WithZone("")
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := addrOf(r.RemoteAddr)
		if !ok || !isTrusted(peer) {
			next.ServeHTTP(w, r)
			return
		}
		client := peer
		// Walk from the nearest hop outwards; each trusted hop vouches for
		// the one before it, the first untrusted one is the client.
		hops := forwardedFor(r.Header.Values("X-Forwarded-For"))
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(hops[i])
			if err != nil {
				break // garbage: stop at the last address we could trust
			}
			client = a.Unmap()
			if !isTrusted(client) {
				break
			}
		}
		r2 := r.Clone(r.Context())
		r2.RemoteAddr = net.JoinHostPort(client.String(), "0")
		// The proxy's hop to the client is what matters: take the last value.
		if protos := forwardedFor(r.Header.Values("X-Forwarded-Proto")); len(protos) > 0 &&
			strings.EqualFold(protos[len(protos)-1], "https") {
			r2 = r2.WithContext(context.WithValue(r2.Context(), httpsKey{}, true))
		}
		next.ServeHTTP(w, r2)
	})
}

func addrOf(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	return a.Unmap(), err == nil
}

// forwardedFor splits comma-separated header values into trimmed entries.
func forwardedFor(values []string) []string {
	var out []string
	for _, v := range values {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				out = append(out, f)
			}
		}
	}
	return out
}
