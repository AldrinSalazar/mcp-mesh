//go:build !windows

package nodectl

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func (n *Node) startBackground(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(n.opts.StateDir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(n.opts.StateDir, 0700); err != nil {
		return fmt.Errorf("set state directory permissions: %w", err)
	}
	log, err := os.OpenFile(n.logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer log.Close()
	if err := log.Chmod(0600); err != nil {
		return fmt.Errorf("set daemon log permissions: %w", err)
	}
	if _, err := fmt.Fprintf(log, "--- mcp-mesh-node started %s\n", time.Now().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("write daemon log marker: %w", err)
	}
	info, err := log.Stat()
	if err != nil {
		return fmt.Errorf("stat daemon log: %w", err)
	}
	offset := info.Size()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open daemon stdin: %w", err)
	}
	defer stdin.Close()
	// CommandContext would tie the detached daemon to the caller's lifetime.
	cmd := exec.Command(n.opts.Executable, n.runArgs()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, log, log
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		exited <- cmd.Wait()
	}()
	if err := n.waitDaemon(ctx, true, exited); err != nil {
		// A failed start must not become a surprise daemon later. Wait remains
		// owned by the reaper, including when the child has already exited.
		_ = cmd.Process.Kill()
		<-reaped
		return fmt.Errorf("start daemon: %w\n%s", err, logTail(n.logFile(), offset))
	}
	return nil
}

func logTail(path string, offset int64) string {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("read daemon log: %v", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Sprintf("stat daemon log: %v", err)
	}
	// Bound diagnostic memory even when a failing process floods stderr.
	start := offset
	if info.Size()-start > 64*1024 {
		start = info.Size() - 64*1024
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return fmt.Sprintf("seek daemon log: %v", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return fmt.Sprintf("read daemon log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}
