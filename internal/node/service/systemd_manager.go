package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const unitName = "mcp-mesh-node.service"

type systemdManager struct {
	path   string
	runner Runner
}

func newSystemd(configRoot string, runner Runner) (*systemdManager, error) {
	if !filepath.IsAbs(configRoot) || runner == nil {
		return nil, errors.New("systemd requires an absolute config root and a runner")
	}
	return &systemdManager{filepath.Join(configRoot, "systemd", "user", unitName), runner}, nil
}

func (m *systemdManager) run(ctx context.Context, args ...string) ([]byte, error) {
	args = append([]string{"--user"}, args...)
	out, err := m.runner.Run(ctx, "systemctl", args...)
	if err == nil {
		return out, nil
	}
	wrapped := commandError("systemctl", args, out, err)
	diagnostic := strings.ToLower(string(out))
	if errors.Is(err, exec.ErrNotFound) ||
		strings.Contains(diagnostic, "failed to connect to bus") ||
		strings.Contains(diagnostic, "failed to connect to user") ||
		strings.Contains(diagnostic, "not been booted with systemd") {
		return out, fmt.Errorf("systemd user service is unavailable; run mcp-mesh-node run from the shell or enable systemd; loginctl enable-linger $USER keeps the service running without an open session: %w", wrapped)
	}
	return out, wrapped
}

func (m *systemdManager) Install(ctx context.Context, s Spec) error {
	if err := validateSpec(s); err != nil {
		return err
	}
	exists, err := installed(m.path)
	if err != nil {
		return err
	}
	if err := writeAtomic(m.path, []byte(systemdUnit(s)), 0644); err != nil {
		return err
	}
	if _, err := m.run(ctx, "daemon-reload"); err != nil {
		return err
	}
	if _, err := m.run(ctx, "enable", "--now", unitName); err != nil {
		return err
	}
	// enable --now does not restart an already running unit after an update.
	if exists {
		_, err = m.run(ctx, "restart", unitName)
	}
	return err
}

func (m *systemdManager) Uninstall(ctx context.Context) error {
	_, err := m.run(ctx, "disable", "--now", unitName)
	if err != nil {
		// A definition may already have been removed or never installed.
		if !strings.Contains(err.Error(), unitName+" does not exist") && !strings.Contains(err.Error(), unitName+" not loaded") && !strings.Contains(err.Error(), unitName+" not found") {
			return err
		}
	}
	if err := removeFile(m.path); err != nil {
		return err
	}
	_, err = m.run(ctx, "daemon-reload")
	return err
}

func (m *systemdManager) Start(ctx context.Context) error {
	exists, err := installed(m.path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("start %s: %w", m.path, ErrNotInstalled)
	}
	_, err = m.run(ctx, "start", unitName)
	return err
}

func (m *systemdManager) Stop(ctx context.Context) error {
	out, err := m.run(ctx, "stop", unitName)
	if err != nil {
		text := strings.ToLower(string(out))
		if strings.Contains(text, unitName+" not loaded") ||
			strings.Contains(text, unitName+" not found") ||
			strings.Contains(text, unitName+" does not exist") {
			return nil
		}
	}
	return err
}

func (m *systemdManager) Status(ctx context.Context) (State, error) {
	state := State{UnitPath: m.path}
	var err error
	state.Installed, err = installed(m.path)
	if err != nil {
		return state, err
	}
	out, err := m.run(ctx, "is-active", unitName)
	active := strings.TrimSpace(string(out))
	if err != nil && !((exitCode(err) == 3 || exitCode(err) == 4) && inactiveState(active)) {
		return state, err
	}
	state.Running = active == "active"
	out, err = m.run(ctx, "show", unitName, "--property=ActiveState,SubState,ActiveEnterTimestamp")
	if err != nil {
		return state, err
	}
	props := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			props[key] = value
		}
	}
	state.Detail = props["ActiveState"]
	if state.Detail == "" {
		state.Detail = active
	}
	if sub := props["SubState"]; sub != "" {
		state.Detail += " (" + sub + ")"
	}
	if since := props["ActiveEnterTimestamp"]; state.Running && since != "" {
		state.Detail += " since " + since
	}
	return state, nil
}

func inactiveState(state string) bool {
	switch state {
	case "inactive", "failed", "unknown", "activating", "deactivating", "maintenance":
		return true
	default:
		return false
	}
}

func systemdUnit(s Spec) string {
	words := make([]string, 0, len(s.Args)+1)
	for i, arg := range append([]string{s.Executable}, s.Args...) {
		words = append(words, systemdQuote(arg, i != 0))
	}
	return "[Unit]\nDescription=mcp-mesh node\n\n[Service]\nExecStart=" + strings.Join(words, " ") +
		"\nRestart=on-failure\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n"
}

func systemdQuote(s string, expandEnv bool) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\', '"':
			out.WriteByte('\\')
			out.WriteRune(c)
		case '%':
			// Quoting alone does not suppress specifier expansion.
			out.WriteRune(c)
			out.WriteRune(c)
		case '$':
			out.WriteRune(c)
			// systemd expands environment references in arguments, not the path.
			if expandEnv {
				out.WriteRune(c)
			}
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if c < 32 || c == 127 {
				fmt.Fprintf(&out, `\x%02x`, c)
			} else {
				out.WriteRune(c)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// StartsAtBoot reports whether the user lingers: systemd then starts the
// user's services at boot, without a login. The user is named by uid:
// without one, show-user describes logind itself and prints nothing.
func (m *systemdManager) StartsAtBoot(ctx context.Context) (bool, error) {
	out, err := m.runner.Run(ctx, "loginctl", "show-user", strconv.Itoa(currentUID()), "--property=Linger", "--value")
	if err != nil {
		return false, fmt.Errorf("loginctl show-user: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// currentUID is the user the service belongs to; tests replace it.
var currentUID = os.Getuid

// EnableAtBoot turns lingering on for the current user. systemd-logind lets
// users do that for themselves.
func (m *systemdManager) EnableAtBoot(ctx context.Context) error {
	if out, err := m.runner.Run(ctx, "loginctl", "enable-linger", strconv.Itoa(currentUID())); err != nil {
		return fmt.Errorf("loginctl enable-linger: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
