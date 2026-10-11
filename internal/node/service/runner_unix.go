//go:build linux || darwin

package service

import (
	"os/exec"
	"syscall"
)

func configureCommand(cmd *exec.Cmd) {
	// A login profile may launch children that keep pipes open after the shell
	// exits. On cancellation, terminate the whole command's process group.
	// A new session also detaches the interactive login shell from our
	// terminal, so its profile cannot draw over the TUI or stop on SIGTTOU.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
