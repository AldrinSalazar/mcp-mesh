package upstream

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, true},
		// WSL's mirrored networking drops connections to closed loopback ports.
		{"dial timeout", fmt.Errorf("HTTP POST: %w", &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}), true},
		{"dns", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "app.invalid"}}, false},
		{"read", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, false},
		{"other", errors.New("HTTP POST: 401 Unauthorized"), false},
	} {
		if got := Unreachable(tc.err); got != tc.want {
			t.Errorf("%s: Unreachable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
