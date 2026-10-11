//go:build !windows

package upstream

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func processGroupAlive(cmd *exec.Cmd) bool {
	return syscall.Kill(-cmd.Process.Pid, 0) != syscall.ESRCH
}
func signalProcess(cmd *exec.Cmd, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal process group %d: %w", cmd.Process.Pid, err)
	}
	return nil
}
