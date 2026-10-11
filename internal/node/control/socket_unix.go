//go:build !windows

package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Listen creates a private Unix socket, replacing a stale socket if needed.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	created := errors.Is(statErr, os.ErrNotExist)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect socket directory %s: %w", dir, err)
	}
	// Never change permissions on a directory we didn't create (it could be
	// the user's home). Only refuse one that others can write to, such as /tmp:
	// they could replace the socket. Callers put it in a dedicated directory.
	if dirInfo.Mode()&os.ModeSticky != 0 || dirInfo.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("socket directory %s is writable by other users; use a private directory", dir)
	}
	if created {
		// MkdirAll applies the umask; make sure the new directory is private.
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, fmt.Errorf("set socket directory permissions %s: %w", dir, err)
		}
	}
	info, err := os.Lstat(path)
	if err == nil {
		// Never remove an unrelated file or follow a symlink at the socket path.
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("listen on %s: existing file is not a socket", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("listen on %s: %w", path, ErrDaemonRunning)
		}
		// Permission errors and timeouts do not establish that the owner is gone.
		if !socketAbsent(dialErr) {
			return nil, fmt.Errorf("check socket %s: %w", path, dialErr)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect socket %s: %w", path, err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("set socket permissions %s: %w", path, err)
	}
	return l, nil
}

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if socketAbsent(err) {
		return nil, fmt.Errorf("dial socket %s: %w", path, ErrNotRunning)
	}
	if err != nil {
		return nil, fmt.Errorf("dial socket %s: %w", path, err)
	}
	return conn, nil
}

func socketAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

func socketCleanup(l net.Listener) func() error {
	if l.Addr().Network() != "unix" {
		return func() error { return nil }
	}
	path := l.Addr().String()
	info, err := os.Lstat(path)
	return func() error {
		if err != nil {
			return nil
		}
		current, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect socket %s during shutdown: %w", path, err)
		}
		// A replacement listener may already own this name after ours closes.
		if !os.SameFile(info, current) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove socket %s: %w", path, err)
		}
		return nil
	}
}
