// Package service installs the node as a user service. The install CLI should
// capture the login environment with CaptureLoginEnv and save it with WriteEnvFile;
// the daemon reads that file and calls ApplyEnv before starting MCP processes.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Spec struct {
	Executable string
	Args       []string
	LogFile    string
}

// ErrNotInstalled is returned by Start when the service definition is absent.
var ErrNotInstalled = errors.New("service is not installed")

type Manager interface {
	Install(context.Context, Spec) error
	Uninstall(context.Context) error
	// Start starts the installed service now. It does not change whether it
	// starts at login. Error if not installed.
	Start(context.Context) error
	// Stop stops it now; it still starts at the next login. Stopping a service
	// that is not running is not an error.
	Stop(context.Context) error
	Status(context.Context) (State, error)
}

type State struct {
	Installed bool
	Running   bool
	UnitPath  string
	Detail    string
}

// BootStarter is implemented by managers whose user service can start when
// the machine boots instead of when the user logs in. With systemd that is
// lingering: without it, a machine nobody logs in to (a server, a headless
// box) never starts the node after a reboot.
type BootStarter interface {
	StartsAtBoot(context.Context) (bool, error)
	EnableAtBoot(context.Context) error
}

// Runner must honor cancellation. Output includes diagnostics on failure.
// The default runner gives commands EOF on stdin, never an interactive terminal.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Options makes service operations independent of the current user's paths and
// commands. NewWithOptions requires an explicit runner and absolute roots.
type Options struct {
	Runner    Runner
	HomeDir   string // launchd agent root
	ConfigDir string // systemd root, normally XDG_CONFIG_HOME or ~/.config
	UID       int    // launchd GUI domain
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Bound waiting for pipes inherited by shell-profile background processes.
	cmd.WaitDelay = 250 * time.Millisecond
	configureCommand(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil && stderr.Len() != 0 {
		out = append(append(out, '\n'), stderr.Bytes()...)
	}
	return out, err
}

func defaultOptions() (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, fmt.Errorf("find home directory: %w", err)
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	return Options{Runner: commandRunner{}, HomeDir: home, ConfigDir: config, UID: os.Getuid()}, nil
}

func validateSpec(s Spec) error {
	if !filepath.IsAbs(s.Executable) {
		return errors.New("service executable must be an absolute path")
	}
	for _, arg := range append([]string{s.Executable, s.LogFile}, s.Args...) {
		if strings.ContainsRune(arg, 0) {
			return errors.New("service paths and arguments cannot contain NUL")
		}
	}
	return nil
}

func installed(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("service definition %s is not a regular file", path)
	}
	return true, nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func exitCode(err error) int {
	var status interface{ ExitCode() int }
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	return -1
}

func commandError(name string, args []string, out []byte, err error) error {
	return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".mcp-mesh-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temporary file for %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
