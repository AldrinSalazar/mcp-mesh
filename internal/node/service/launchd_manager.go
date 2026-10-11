package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

const launchdLabel = "dev.mcp-mesh.node"

type launchdManager struct {
	path   string
	domain string
	runner Runner
}

func newLaunchd(home string, uid int, runner Runner) (*launchdManager, error) {
	if !filepath.IsAbs(home) || uid < 0 || runner == nil {
		return nil, errors.New("launchd requires an absolute home root, nonnegative UID and a runner")
	}
	return &launchdManager{
		path:   filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"),
		domain: "gui/" + strconv.Itoa(uid), runner: runner,
	}, nil
}

func (m *launchdManager) target() string { return m.domain + "/" + launchdLabel }

func (m *launchdManager) run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := m.runner.Run(ctx, "launchctl", args...)
	if err != nil {
		return out, commandError("launchctl", args, out, err)
	}
	return out, nil
}

func launchdMissing(out []byte, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(string(out))
	return exitCode(err) == 113 || strings.Contains(text, "could not find service") ||
		strings.Contains(text, "could not find specified service") || strings.Contains(text, "no such process") ||
		strings.Contains(text, "not loaded")
}

func (m *launchdManager) Install(ctx context.Context, s Spec) error {
	if err := validateSpec(s); err != nil {
		return err
	}
	if !filepath.IsAbs(s.LogFile) {
		return errors.New("launchd log file must be an absolute path")
	}
	data, err := launchdPlist(s)
	if err != nil {
		return err
	}
	out, err := m.run(ctx, "print", m.target())
	if err != nil && !launchdMissing(out, err) {
		return err
	}
	if err == nil {
		if _, err := m.run(ctx, "bootout", m.target()); err != nil {
			return err
		}
	}
	if err := writeAtomic(m.path, data, 0644); err != nil {
		return err
	}
	// launchd cannot open its log file if the parent directory does not exist.
	if err := makeLogDir(s.LogFile); err != nil {
		return err
	}
	_, err = m.run(ctx, "bootstrap", m.domain, m.path)
	return err
}

func (m *launchdManager) Uninstall(ctx context.Context) error {
	out, err := m.run(ctx, "bootout", m.target())
	if err != nil && !launchdMissing(out, err) {
		return err
	}
	return removeFile(m.path)
}

func (m *launchdManager) Start(ctx context.Context) error {
	exists, err := installed(m.path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("start %s: %w", m.path, ErrNotInstalled)
	}
	out, err := m.run(ctx, "print", m.target())
	if launchdMissing(out, err) {
		_, err = m.run(ctx, "bootstrap", m.domain, m.path)
		return err
	}
	if err != nil {
		return err
	}
	_, err = m.run(ctx, "kickstart", m.target())
	return err
}

func (m *launchdManager) Stop(ctx context.Context) error {
	out, err := m.run(ctx, "bootout", m.target())
	if launchdMissing(out, err) {
		return nil
	}
	return err
}

func (m *launchdManager) Status(ctx context.Context) (State, error) {
	state := State{UnitPath: m.path}
	var err error
	state.Installed, err = installed(m.path)
	if err != nil {
		return state, err
	}
	out, err := m.run(ctx, "print", m.target())
	if launchdMissing(out, err) {
		state.Detail = "not loaded"
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Detail = "loaded"
	pid := ""
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "state":
			state.Detail = value
			state.Running = value == "running"
		case "pid":
			pid = value
		}
	}
	if state.Running && pid != "" {
		state.Detail += " (pid " + pid + ")"
	}
	return state, nil
}

var plistTemplate = template.Must(template.New("plist").Funcs(template.FuncMap{
	"xml": func(s string) (string, error) {
		var buf bytes.Buffer
		// XML 1.0 cannot represent most control characters, even as entities.
		for _, c := range s {
			if !((c >= 0x20 && c <= 0xd7ff) || (c >= 0xe000 && c <= 0xfffd) ||
				(c >= 0x10000 && c <= 0x10ffff) || c == '\n' || c == '\r' || c == '\t') {
				return "", fmt.Errorf("invalid XML character U+%04X", c)
			}
		}
		if err := xml.EscapeText(&buf, []byte(s)); err != nil {
			return "", err
		}
		return buf.String(), nil
	},
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.mcp-mesh.node</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{xml .Executable}}</string>{{range .Args}}
    <string>{{xml .}}</string>{{end}}
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>{{xml .LogFile}}</string>
  <key>StandardErrorPath</key>
  <string>{{xml .LogFile}}</string>
</dict>
</plist>
`))

func launchdPlist(s Spec) ([]byte, error) {
	var buf bytes.Buffer
	if err := plistTemplate.Execute(&buf, s); err != nil {
		return nil, fmt.Errorf("render launchd plist: %w", err)
	}
	return buf.Bytes(), nil
}
