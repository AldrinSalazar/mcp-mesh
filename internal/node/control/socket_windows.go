//go:build windows

package control

import (
	"context"
	"errors"
	"net"
)

// Listen will use a named pipe when Windows daemon support is added.
func Listen(path string) (net.Listener, error) {
	return nil, errors.ErrUnsupported
}

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	return nil, errors.ErrUnsupported
}

func socketCleanup(l net.Listener) func() error {
	return func() error { return nil }
}
