package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
)

type ctxKey struct{}

// WithKey returns ctx carrying the key a request authenticated with, as
// RequireKey does; other credential checks (OAuth access tokens) use it so
// ClientIdentity and AuthorizeNode work the same downstream.
func WithKey(ctx context.Context, k Key) context.Context {
	return context.WithValue(ctx, ctxKey{}, k)
}

// ClientIP is the request's remote address without the port.
func ClientIP(r *http.Request) string { return clientIP(r) }

// KeyFrom returns the key RequireKey authenticated the request with.
func KeyFrom(ctx context.Context) (Key, bool) {
	k, ok := ctx.Value(ctxKey{}).(Key)
	return k, ok
}

// RequireKey admits only requests carrying a key of the given kind as
// "Authorization: Bearer <secret>". Everything else gets 401 with a message
// that says what is wrong without echoing the key.
func RequireKey(store *Store, kind Kind, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := bearer(r)
		if !ok {
			unauthorized(w, "missing key: send Authorization: Bearer <"+string(kind)+" key>")
			return
		}
		key, found := store.Lookup(secret)
		if !found {
			unauthorized(w, "unknown or revoked key")
			return
		}
		if key.Kind != kind {
			unauthorized(w, fmt.Sprintf("wrong kind of key: this endpoint needs a %s key, not a %s key", kind, key.Kind))
			return
		}
		store.Touch(key, clientIP(r))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, key)))
	})
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-mesh"`)
	http.Error(w, msg, http.StatusUnauthorized)
}

func bearer(r *http.Request) (string, bool) {
	const scheme = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	secret := strings.TrimSpace(h[len(scheme):])
	return secret, secret != ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIdentity names the caller after the client key in the request, or
// returns identity.Anonymous when there is none (auth off).
func ClientIdentity(r *http.Request) identity.Client {
	if key, ok := KeyFrom(r.Context()); ok {
		return identity.Client{ID: key.ID, Name: key.Name}
	}
	return identity.Anonymous
}

// AuthorizeNode checks that the request's key is a node key bound to node, so
// a leaked key can only impersonate the one node it was made for.
func AuthorizeNode(r *http.Request, node string) error {
	key, ok := KeyFrom(r.Context())
	switch {
	case !ok:
		return errors.New("no key on the request")
	case key.Kind != KindNode:
		return fmt.Errorf("key %s is a %s key, not a node key", key.ID, key.Kind)
	case key.Name != node:
		return fmt.Errorf("key %s is bound to node %q, not %q", key.ID, key.Name, node)
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10") // Tailscale and carrier-grade NAT

// LocalOrTLS refuses plain-HTTP requests from addresses that aren't on a
// private network, so keys never cross the open internet unencrypted. A
// TLS-terminating reverse proxy on the same machine connects from loopback, so
// it passes; behind TrustProxies the client's own address and scheme count.
// allowInsecure turns the check off.
func LocalOrTLS(allowInsecure bool, next http.Handler) http.Handler {
	if allowInsecure {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsHTTPS(r) && !privateRemote(r.RemoteAddr) {
			http.Error(w, "plain HTTP is only accepted from local and private networks: use TLS (--tls-cert/--tls-key or a reverse proxy) or start the hub with --allow-insecure", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func privateRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("")
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}
