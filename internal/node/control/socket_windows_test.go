//go:build windows

package control

import (
	"errors"
	"testing"
)

func TestWindowsUnsupported(t *testing.T) {
	if l, err := Listen("node.pipe"); l != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Listen = %v, %v; want ErrUnsupported", l, err)
	}
	c := NewClient("node.pipe")
	if _, err := c.Status(t.Context()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Status error = %v; want ErrUnsupported", err)
	}
}
