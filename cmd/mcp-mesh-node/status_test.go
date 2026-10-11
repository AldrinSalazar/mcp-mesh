package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/spf13/cobra"
)

func TestStatusNodeAndStartAtLogin(t *testing.T) {
	for _, tc := range []struct {
		name, login string
		running     bool
		service     nodectl.ServiceState
		nodeErr     string
	}{
		{name: "running by hand", running: true, service: nodectl.ServiceState{Supported: true}, login: "off (turn on with mcp-mesh-node install)"},
		{name: "running at login", running: true, service: nodectl.ServiceState{Supported: true, Installed: true, Running: true, Detail: "active (running)"}, login: "on (user service)"},
		{name: "stopped at login", service: nodectl.ServiceState{Supported: true, Installed: true}, login: "on (user service)"},
		{name: "failed service", service: nodectl.ServiceState{Supported: true, Installed: true, Detail: "failed (exit-code)"}, login: "on (user service) · failed (exit-code)"},
		{name: "stopped by hand", service: nodectl.ServiceState{Supported: true}, login: "off (turn on with mcp-mesh-node install)"},
		{name: "unsupported", login: "not supported on this OS"},
		{name: "errors", service: nodectl.ServiceState{Supported: true, Err: "service inspection failed"}, nodeErr: "socket permission denied", login: "off (turn on with mcp-mesh-node install)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := nodectl.Snapshot{Service: tc.service, Daemon: nodectl.DaemonState{Running: tc.running, Err: tc.nodeErr, Status: control.Status{PID: 123, StartedAt: time.Now().Add(-3 * time.Minute)}}}
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := printStatus(cmd, s, false); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			node := "Node: stopped\n"
			if tc.running {
				node = "Node: running (pid 123, up 3m"
			}
			for _, want := range []string{node, "Start at login: " + tc.login + "\n"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q: %s", want, text)
				}
			}
			for _, old := range []string{"Daemon:", "Service:", "active (running)"} {
				if strings.Contains(text, old) {
					t.Fatalf("old status %q: %s", old, text)
				}
			}
			if tc.nodeErr != "" && !strings.Contains(text, "Node error: "+tc.nodeErr+"\n") {
				t.Fatal("missing node error line")
			}
			if tc.service.Err != "" && !strings.Contains(text, "Start at login error: "+tc.service.Err+"\n") {
				t.Fatal("missing start at login error line")
			}
			if !tc.running && !strings.Contains(text, "(node stopped)") {
				t.Fatal("missing stopped hub state")
			}
		})
	}
}

func TestStatusJSONVocabularyUnchanged(t *testing.T) {
	s := nodectl.Snapshot{
		Daemon:  nodectl.DaemonState{Running: true, Err: "node error", Status: control.Status{PID: 123}},
		Service: nodectl.ServiceState{Supported: true, Installed: true, Detail: "failed", UnitPath: "/unit", Err: "service error"},
	}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printStatus(cmd, s, true); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["daemon"] == nil || fields["service"] == nil || fields["config"] == nil {
		t.Fatalf("JSON fields changed: %s", out.String())
	}
	var got statusJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Daemon.Running || got.Daemon.Status.PID != 123 || got.Daemon.Error != s.Daemon.Err || !got.Service.Supported || !got.Service.Installed || got.Service.Running || got.Service.Detail != s.Service.Detail || got.Service.UnitPath != s.Service.UnitPath || got.Service.Error != s.Service.Err {
		t.Fatalf("JSON state changed: %#v", got)
	}
}

func TestNodeCommandHelp(t *testing.T) {
	setupCLI(t, false)
	root := rootCmd()
	for name, want := range map[string]string{
		"start":     "Start the node in the background (until logout)",
		"stop":      "Stop the node",
		"install":   "Start the node at login (installs a user service)",
		"uninstall": "Stop starting the node at login (removes the user service; keeps config)",
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _, err := root.Find([]string{name})
			if err != nil || cmd.Short != want {
				t.Fatalf("help: %q, error: %v", cmd.Short, err)
			}
			if strings.Contains(cmd.Long, "daemon") {
				t.Fatalf("old wording: %s", cmd.Long)
			}
		})
	}
	out, err := execute(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Node:", "start the node now, until logout", "start it now and at every login"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Daemon:") {
		t.Fatal("old help group")
	}
}
