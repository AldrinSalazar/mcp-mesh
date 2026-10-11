package service

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type result struct {
	out string
	err error
}

type fakeRunner struct {
	t       *testing.T
	results []result
	calls   [][]string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.t.Helper()
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(r.results) == 0 {
		r.t.Fatalf("unexpected command: %s %v", name, args)
	}
	res := r.results[0]
	r.results = r.results[1:]
	return []byte(res.out), res.err
}

func (r *fakeRunner) check(want [][]string) {
	r.t.Helper()
	if !reflect.DeepEqual(r.calls, want) {
		r.t.Fatalf("commands = %v, want %v", r.calls, want)
	}
	if len(r.results) != 0 {
		r.t.Fatalf("%d scripted results were unused", len(r.results))
	}
}

type statusError int

func (e statusError) Error() string { return "command exited unsuccessfully" }
func (e statusError) ExitCode() int { return int(e) }

func testSpec(t *testing.T, root string) Spec {
	t.Helper()
	return Spec{Executable: filepath.Join(root, "bin", "mcp-mesh-node"),
		Args:    []string{"run", "--config", filepath.Join(root, "node.yaml")},
		LogFile: filepath.Join(root, "logs", "node.log")}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSystemdUnitGolden(t *testing.T) {
	spec := Spec{Executable: `/opt/My $node%/mcp-mesh-node`,
		Args: []string{"run", "--config", `/home/me/a b/node.yaml`, "", `a"b\c`,
			`$HOME ${TOKEN} %h`, "line\nnext\r\t\x01", "'quote'", ";", "日本語"}}
	want := `[Unit]
Description=mcp-mesh node

[Service]
ExecStart="/opt/My $node%%/mcp-mesh-node" "run" "--config" "/home/me/a b/node.yaml" "" "a\"b\\c" "$$HOME $${TOKEN} %%h" "line\nnext\r\t\x01" "'quote'" ";" "日本語"
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
	if got := systemdUnit(spec); got != want {
		t.Fatalf("unit:\n%s\nwant:\n%s", got, want)
	}
}

func TestLaunchdPlistGolden(t *testing.T) {
	spec := Spec{Executable: `/opt/My &node/mcp-mesh-node`, Args: []string{"run", "", `a<"b">'c'`, "line\nnext"}, LogFile: `/tmp/log &out`}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.mcp-mesh.node</string>
  <key>ProgramArguments</key>
  <array>
    <string>/opt/My &amp;node/mcp-mesh-node</string>
    <string>run</string>
    <string></string>
    <string>a&lt;&#34;b&#34;&gt;&#39;c&#39;</string>
    <string>line&#xA;next</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>/tmp/log &amp;out</string>
  <key>StandardErrorPath</key>
  <string>/tmp/log &amp;out</string>
</dict>
</plist>
`
	data, err := launchdPlist(spec)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("plist:\n%s\nwant:\n%s", data, want)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var values []string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
	}
	wantValues := append([]string{launchdLabel, spec.Executable}, spec.Args...)
	wantValues = append(wantValues, spec.LogFile, spec.LogFile)
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatalf("decoded strings = %q, want %q", values, wantValues)
	}
}

func TestSystemdLifecycle(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{t: t, results: make([]result, 7)}
	m, err := newSystemd(root, runner)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "systemd", "user", unitName)
	if m.path != wantPath {
		t.Fatalf("path = %s, want %s", m.path, wantPath)
	}
	spec := testSpec(t, root)
	if err := m.Install(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, m.path); got != systemdUnit(spec) {
		t.Fatal("installed unit differs from generated content")
	}
	spec.Args = append(spec.Args, "--updated")
	if err := m.Install(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, m.path); got != systemdUnit(spec) {
		t.Fatal("reinstall did not update unit")
	}
	if err := m.Uninstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists: %v", err)
	}
	runner.check([][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", unitName},
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", unitName},
		{"systemctl", "--user", "restart", unitName},
		{"systemctl", "--user", "disable", "--now", unitName},
		{"systemctl", "--user", "daemon-reload"},
	})
}

func TestLaunchdLifecycle(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{t: t, results: []result{
		{out: "Could not find service", err: statusError(113)}, {},
		{out: "state = running"}, {}, {}, {},
	}}
	m, err := newLaunchd(root, 501, runner)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "Library", "LaunchAgents", launchdLabel+".plist")
	if m.path != wantPath {
		t.Fatalf("path = %s, want %s", m.path, wantPath)
	}
	spec := testSpec(t, root)
	if err := m.Install(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	spec.Args = append(spec.Args, "--updated")
	if err := m.Install(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	want, err := launchdPlist(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, m.path); got != string(want) {
		t.Fatal("reinstall did not update plist")
	}
	if _, err := os.Stat(filepath.Dir(spec.LogFile)); err != nil {
		t.Fatalf("log directory was not created: %v", err)
	}
	if err := m.Uninstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist still exists: %v", err)
	}
	target := "gui/501/" + launchdLabel
	runner.check([][]string{
		{"launchctl", "print", target},
		{"launchctl", "bootstrap", "gui/501", wantPath},
		{"launchctl", "print", target},
		{"launchctl", "bootout", target},
		{"launchctl", "bootstrap", "gui/501", wantPath},
		{"launchctl", "bootout", target},
	})
}

func TestSystemdStartStop(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		installed       bool
		results         []result
		wantErr         error
	}{
		{name: "start installed", operation: "start", installed: true, results: []result{{}}},
		{name: "start not installed", operation: "start", wantErr: ErrNotInstalled},
		{name: "stop running", operation: "stop", installed: true, results: []result{{}}},
		{name: "stop already stopped", operation: "stop", installed: true, results: []result{{}}},
		{name: "stop not loaded", operation: "stop", installed: true, results: []result{{out: "Failed to stop mcp-mesh-node.service: Unit mcp-mesh-node.service not loaded.", err: statusError(5)}}},
		{name: "stop not installed", operation: "stop", results: []result{{out: "Unit mcp-mesh-node.service not found.", err: statusError(5)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{t: t, results: tc.results}
			m, err := newSystemd(t.TempDir(), runner)
			if err != nil {
				t.Fatal(err)
			}
			if tc.installed {
				if err := writeAtomic(m.path, []byte("unit"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.operation == "start" {
				err = m.Start(t.Context())
			} else {
				err = m.Stop(t.Context())
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			var want [][]string
			if tc.wantErr != ErrNotInstalled {
				want = [][]string{{"systemctl", "--user", tc.operation, unitName}}
			}
			runner.check(want)
			if tc.installed {
				if got := readFile(t, m.path); got != "unit" {
					t.Fatalf("service definition changed: %q", got)
				}
			} else if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("service definition created: %v", err)
			}
		})
	}
}

func TestLaunchdStartStop(t *testing.T) {
	cause := errors.New("permission denied")
	target := "gui/501/" + launchdLabel
	for _, tc := range []struct {
		name, operation string
		installed       bool
		results         []result
		commands        []string
		wantErr         error
	}{
		{name: "start not installed", operation: "start", wantErr: ErrNotInstalled},
		{name: "start not loaded", operation: "start", installed: true, results: []result{{out: "Could not find service", err: statusError(113)}, {}}, commands: []string{"print", "bootstrap"}},
		{name: "start loaded", operation: "start", installed: true, results: []result{{out: "state = waiting"}, {}}, commands: []string{"print", "kickstart"}},
		{name: "start running", operation: "start", installed: true, results: []result{{out: "state = running"}, {}}, commands: []string{"print", "kickstart"}},
		{name: "start print failure", operation: "start", installed: true, results: []result{{err: cause}}, commands: []string{"print"}, wantErr: cause},
		{name: "start bootstrap failure", operation: "start", installed: true, results: []result{{out: "No such process", err: statusError(3)}, {err: cause}}, commands: []string{"print", "bootstrap"}, wantErr: cause},
		{name: "start kickstart failure", operation: "start", installed: true, results: []result{{out: "state = waiting"}, {err: cause}}, commands: []string{"print", "kickstart"}, wantErr: cause},
		{name: "stop running", operation: "stop", installed: true, results: []result{{}}, commands: []string{"bootout"}},
		{name: "stop already stopped", operation: "stop", installed: true, results: []result{{out: "Could not find specified service", err: statusError(113)}}, commands: []string{"bootout"}},
		{name: "stop not loaded", operation: "stop", installed: true, results: []result{{out: "Service not loaded", err: statusError(1)}}, commands: []string{"bootout"}},
		{name: "stop not installed", operation: "stop", results: []result{{out: "No such process", err: statusError(3)}}, commands: []string{"bootout"}},
		{name: "stop failure", operation: "stop", installed: true, results: []result{{err: cause}}, commands: []string{"bootout"}, wantErr: cause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{t: t, results: tc.results}
			m, err := newLaunchd(t.TempDir(), 501, runner)
			if err != nil {
				t.Fatal(err)
			}
			if tc.installed {
				if err := writeAtomic(m.path, []byte("plist"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.operation == "start" {
				err = m.Start(t.Context())
			} else {
				err = m.Stop(t.Context())
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			var want [][]string
			for _, command := range tc.commands {
				if command == "bootstrap" {
					want = append(want, []string{"launchctl", command, "gui/501", m.path})
				} else {
					want = append(want, []string{"launchctl", command, target})
				}
			}
			runner.check(want)
			if tc.installed {
				if got := readFile(t, m.path); got != "plist" {
					t.Fatalf("service definition changed: %q", got)
				}
			} else if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("service definition created: %v", err)
			}
		})
	}
}

func TestSystemdUnavailable(t *testing.T) {
	for _, res := range []result{
		{err: exec.ErrNotFound},
		{out: "Failed to connect to bus: No medium found", err: statusError(1)},
		{out: "Failed to connect to user scope bus: No such file or directory", err: statusError(1)},
		{out: "System has not been booted with systemd as init system", err: statusError(1)},
	} {
		t.Run(res.out+res.err.Error(), func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeRunner{t: t, results: []result{res}}
			m, err := newSystemd(root, runner)
			if err != nil {
				t.Fatal(err)
			}
			err = m.Install(t.Context(), testSpec(t, root))
			for _, hint := range []string{"unavailable", "mcp-mesh-node run", "enable systemd", "loginctl enable-linger $USER"} {
				if err == nil || !strings.Contains(err.Error(), hint) {
					t.Fatalf("error = %v, missing %q", err, hint)
				}
			}
			if !errors.Is(err, res.err) {
				t.Fatalf("error did not preserve cause: %v", err)
			}
			runner.check([][]string{{"systemctl", "--user", "daemon-reload"}})
		})
	}
}

func TestSystemdStatus(t *testing.T) {
	for _, tc := range []struct {
		name, active, show, detail string
		code                       statusError
		installed, running         bool
	}{
		{name: "running", active: "active", show: "ActiveState=active\nSubState=running\nActiveEnterTimestamp=Fri 2026-10-09 12:00:00 UTC\n", detail: "active (running) since Fri 2026-10-09 12:00:00 UTC", installed: true, running: true},
		{name: "stopped", active: "inactive", show: "ActiveState=inactive\nSubState=dead\n", detail: "inactive (dead)", installed: true, code: 3},
		{name: "failed", active: "failed", show: "ActiveState=failed\nSubState=failed\n", detail: "failed (failed)", installed: true, code: 3},
		{name: "starting", active: "activating", show: "ActiveState=activating\nSubState=start\n", detail: "activating (start)", installed: true, code: 3},
		{name: "missing", active: "unknown", detail: "unknown", code: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			res := result{out: tc.active + "\n"}
			if tc.code != 0 {
				res.err = tc.code
			}
			runner := &fakeRunner{t: t, results: []result{res, {out: tc.show}}}
			m, err := newSystemd(root, runner)
			if err != nil {
				t.Fatal(err)
			}
			if tc.installed {
				if err := writeAtomic(m.path, []byte("unit"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			state, err := m.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := State{Installed: tc.installed, Running: tc.running, UnitPath: m.path, Detail: tc.detail}
			if state != want {
				t.Fatalf("state = %+v, want %+v", state, want)
			}
			runner.check([][]string{{"systemctl", "--user", "is-active", unitName}, {"systemctl", "--user", "show", unitName, "--property=ActiveState,SubState,ActiveEnterTimestamp"}})
		})
	}
}

func TestLaunchdStatus(t *testing.T) {
	for _, tc := range []struct {
		name, output, detail string
		err                  error
		installed, running   bool
	}{
		{name: "running", output: "gui/501/dev.mcp-mesh.node = {\n\tstate = running\n\tpid = 1234\n}\n", detail: "running (pid 1234)", installed: true, running: true},
		{name: "waiting", output: "\tstate = waiting\n", detail: "waiting", installed: true},
		{name: "unloaded", output: "Could not find service", err: statusError(113), detail: "not loaded", installed: true},
		{name: "missing", output: "Could not find service", err: statusError(113), detail: "not loaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{t: t, results: []result{{out: tc.output, err: tc.err}}}
			m, err := newLaunchd(t.TempDir(), 501, runner)
			if err != nil {
				t.Fatal(err)
			}
			if tc.installed {
				if err := writeAtomic(m.path, []byte("plist"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			state, err := m.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := State{Installed: tc.installed, Running: tc.running, UnitPath: m.path, Detail: tc.detail}
			if state != want {
				t.Fatalf("state = %+v, want %+v", state, want)
			}
			runner.check([][]string{{"launchctl", "print", "gui/501/" + launchdLabel}})
		})
	}
}

func TestManagersRejectInvalidSpecs(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{t: t}
	systemd, err := newSystemd(root, runner)
	if err != nil {
		t.Fatal(err)
	}
	launchd, err := newLaunchd(root, 501, runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, manager := range []Manager{systemd, launchd} {
		for _, spec := range []Spec{{Executable: "relative"}, {Executable: filepath.Join(root, "node"), Args: []string{"bad\x00arg"}}} {
			if err := manager.Install(t.Context(), spec); err == nil {
				t.Fatal("invalid spec accepted")
			}
		}
	}
	spec := testSpec(t, root)
	spec.LogFile = "relative"
	if err := launchd.Install(t.Context(), spec); err == nil {
		t.Fatal("relative log path accepted")
	}
	spec.LogFile = filepath.Join(root, "log")
	spec.Args = []string{"invalid\x01xml"}
	if err := launchd.Install(t.Context(), spec); err == nil {
		t.Fatal("invalid XML accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatal("invalid spec executed a command")
	}
}

func TestManagerCommandErrorsPreserveFiles(t *testing.T) {
	for _, kind := range []string{"systemd", "launchd"} {
		for _, operation := range []string{"install", "uninstall", "start", "stop", "status"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				root := t.TempDir()
				cause := errors.New("permission denied")
				runner := &fakeRunner{t: t, results: []result{{out: "permission denied", err: cause}}}
				var manager Manager
				var path string
				if kind == "systemd" {
					m, err := newSystemd(root, runner)
					if err != nil {
						t.Fatal(err)
					}
					manager, path = m, m.path
				} else {
					m, err := newLaunchd(root, 501, runner)
					if err != nil {
						t.Fatal(err)
					}
					manager, path = m, m.path
				}
				if err := writeAtomic(path, []byte("existing"), 0644); err != nil {
					t.Fatal(err)
				}
				var err error
				switch operation {
				case "install":
					err = manager.Install(t.Context(), testSpec(t, root))
				case "uninstall":
					err = manager.Uninstall(t.Context())
				case "start":
					err = manager.Start(t.Context())
				case "stop":
					err = manager.Stop(t.Context())
				case "status":
					_, err = manager.Status(t.Context())
				}
				if !errors.Is(err, cause) {
					t.Fatalf("error = %v, want cause %v", err, cause)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("command failure removed definition: %v", err)
				}
			})
		}
	}
}

func TestSystemdAtBoot(t *testing.T) {
	old := currentUID
	currentUID = func() int { return 1000 }
	defer func() { currentUID = old }()
	runner := &fakeRunner{t: t, results: []result{{out: "no\n"}, {}, {out: "yes\n"}}}
	m, err := newSystemd(t.TempDir(), runner)
	if err != nil {
		t.Fatal(err)
	}
	var _ BootStarter = m
	if on, err := m.StartsAtBoot(t.Context()); err != nil || on {
		t.Fatalf("StartsAtBoot = %v, %v; want false", on, err)
	}
	if err := m.EnableAtBoot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if on, err := m.StartsAtBoot(t.Context()); err != nil || !on {
		t.Fatalf("StartsAtBoot = %v, %v; want true", on, err)
	}
	runner.check([][]string{
		{"loginctl", "show-user", "1000", "--property=Linger", "--value"},
		{"loginctl", "enable-linger", "1000"},
		{"loginctl", "show-user", "1000", "--property=Linger", "--value"},
	})
}
