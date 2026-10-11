package upstream

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// dialTimeout bounds connecting to an app's MCP. A closed port is usually
// refused at once, but WSL's mirrored networking and some firewalls drop the
// connection attempt instead, which would otherwise hang for the OS timeout.
const dialTimeout = 5 * time.Second

// NewHTTPTransport returns a transport for reaching an HTTP or SSE MCP. Each
// upstream gets its own, so closing one session's idle sockets can't affect
// another's.
func NewHTTPTransport() *http.Transport {
	t := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		t = base.Clone()
	}
	t.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	return t
}

// Unreachable reports whether err means nothing accepted the connection:
// refused, timed out or no route. Name resolution and anything after the
// connection (TLS, HTTP status) are other problems.
func Unreachable(err error) bool {
	var op *net.OpError
	var dns *net.DNSError
	return errors.As(err, &op) && op.Op == "dial" && !errors.As(err, &dns)
}

// connectError keeps other transport errors as they are while making a
// closed app distinguishable from authentication, DNS and protocol failures.
func connectError(endpoint string, err error) error {
	if err != nil && Unreachable(err) {
		return fmt.Errorf("connect %s: %w", endpoint, ErrUnreachable)
	}
	return err
}
