//go:build windows

package upstream

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func configureProcess(*exec.Cmd)       {}
func processGroupAlive(*exec.Cmd) bool { return false }
func signalProcess(cmd *exec.Cmd, _ bool) error {
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill process: %w", err)
	}
	return nil
}
