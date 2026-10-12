package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/spf13/cobra"
)

type statusJSON struct {
	Daemon  daemonJSON  `json:"daemon"`
	Service serviceJSON `json:"service"`
	Config  configJSON  `json:"config"`
}
type daemonJSON struct {
	Running bool            `json:"running"`
	Status  *control.Status `json:"status"`
	Error   string          `json:"error"`
}
type serviceJSON struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Detail    string `json:"detail"`
	UnitPath  string `json:"unitPath"`
	Error     string `json:"error"`
}
type configJSON struct {
	Path   string   `json:"path"`
	Exists bool     `json:"exists"`
	Error  string   `json:"error"`
	Hub    string   `json:"hub"`
	Node   string   `json:"node"`
	MCPs   []string `json:"mcps"`
	// HubKey says whether a hub key is configured, never its value.
	HubKey hubKeyJSON `json:"hubKey"`
}
type hubKeyJSON struct {
	Set bool `json:"set"`
	// Source is "file", "env" or "" when no key is set.
	Source string `json:"source"`
	File   string `json:"file,omitempty"`
	Env    string `json:"env,omitempty"`
}

func hubKeyState(s nodectl.Snapshot) hubKeyJSON {
	key := s.Config.Key
	switch {
	case key.File != "":
		return hubKeyJSON{Set: true, Source: "file", File: key.File}
	case key.Env != "":
		return hubKeyJSON{Set: true, Source: "env", Env: key.Env}
	}
	return hubKeyJSON{}
}

// updateLines say what to do about versions: a newer release, a node that
// runs an older binary than the installed one, or one older than its hub.
func updateLines(u nodectl.UpdateState) []string {
	var lines []string
	switch {
	case u.Latest != "":
		lines = append(lines, fmt.Sprintf("Update available: %s (%s); upgrade: %s, then restart the node (mcp-mesh-node stop && mcp-mesh-node start)", u.Latest, u.URL, u.Upgrade))
	case u.Hub != "":
		lines = append(lines, fmt.Sprintf("The hub runs %s, newer than this node; upgrade: %s, then restart the node", u.Hub, u.Upgrade))
	}
	if u.Running != "" {
		lines = append(lines, fmt.Sprintf("The running node is %s, older than this mcp-mesh-node; restart it to use the new one: mcp-mesh-node stop && mcp-mesh-node start", u.Running))
	}
	return lines
}

// pairingHint tells the user what to do with a pairing code.
func pairingHint(code string) string {
	return fmt.Sprintf("approve this machine in the hub's dashboard (Approvals); check it shows code %s", code)
}

func printStatus(cmd *cobra.Command, s nodectl.Snapshot, asJSON bool) error {
	if asJSON {
		var status *control.Status
		if s.Daemon.Running {
			status = &s.Daemon.Status
		}
		names := make([]string, 0, len(s.Config.Entries))
		for _, e := range s.Config.Entries {
			names = append(names, e.Name)
		}
		// Config entries contain credentials. Only names belong in status output.
		return json.NewEncoder(cmd.OutOrStdout()).Encode(statusJSON{
			Daemon:  daemonJSON{s.Daemon.Running, status, s.Daemon.Err},
			Service: serviceJSON{s.Service.Supported, s.Service.Installed, s.Service.Running, s.Service.Detail, s.Service.UnitPath, s.Service.Err},
			Config:  configJSON{s.ConfigPath, s.Config.Exists, s.Config.Err, s.Config.Hub, s.Config.Node, names, hubKeyState(s)},
		})
	}
	out := cmd.OutOrStdout()
	if s.Daemon.Running {
		fmt.Fprintf(out, "Node: running (pid %d, up %s)\n", s.Daemon.Status.PID, elapsed(s.Daemon.Status.StartedAt))
	} else {
		fmt.Fprintln(out, "Node: stopped")
	}
	if s.Daemon.Err != "" {
		fmt.Fprintf(out, "Node error: %s\n", s.Daemon.Err)
	}
	login := "off (turn on with mcp-mesh-node install)"
	switch {
	case !s.Service.Supported:
		login = "not supported on this OS"
	case s.Service.Installed:
		login = "on (user service)"
	}
	fmt.Fprintf(out, "Start at login: %s", login)
	// Service details are useful when an installed node fails to run.
	if s.Service.Detail != "" && s.Service.Installed && !s.Service.Running {
		fmt.Fprintf(out, " · %s", s.Service.Detail)
	}
	fmt.Fprintln(out)
	if s.Service.Err != "" {
		fmt.Fprintf(out, "Start at login error: %s\n", s.Service.Err)
	}
	hub, state, hubErr := s.Config.Hub, "node stopped", ""
	if s.Daemon.Running {
		h := s.Daemon.Status.Hub
		hub, state, hubErr = h.URL, "disconnected", h.LastError
		switch {
		case h.Connected:
			state = "connected"
		case h.PairCode != "":
			state, hubErr = "waiting for approval", pairingHint(h.PairCode)
		}
	}
	fmt.Fprintf(out, "Hub: %s (%s)", hub, state)
	if hubErr != "" {
		fmt.Fprintf(out, " %s", hubErr)
	}
	fmt.Fprintln(out)
	switch key := hubKeyState(s); key.Source {
	case "file":
		fmt.Fprintf(out, "Hub key: set (%s)\n", displayPath(key.File))
	case "env":
		fmt.Fprintf(out, "Hub key: set (from $%s)\n", key.Env)
	default:
		fmt.Fprintln(out, "Hub key: not set")
	}
	validity := "valid"
	if !s.Config.Exists {
		validity = "missing"
	}
	if s.Config.Err != "" {
		validity = "invalid: " + s.Config.Err
	}
	fmt.Fprintf(out, "Config: %s (%s)\n", s.ConfigPath, validity)
	if s.Daemon.Running && s.Daemon.Status.ConfigChanged {
		fmt.Fprintln(out, "Config changes not applied yet: run mcp-mesh-node reload (restarts the node's MCPs)")
	}
	for _, line := range updateLines(s.Update) {
		fmt.Fprintln(out, line)
	}
	if len(s.Config.Entries) == 0 && len(s.Daemon.Status.MCPs) == 0 {
		fmt.Fprintln(out, "MCPs: none yet (add one with mcp-mesh-node add)")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MCP\tKIND\tSTATE\tSTART\tPID\tUPTIME\tRESTARTS\tTOOLS\tLAST ERROR")
	if s.Daemon.Running {
		for _, m := range s.Daemon.Status.MCPs {
			pid, uptime := "-", "-"
			if m.PID != 0 {
				pid = strconv.Itoa(m.PID)
			}
			if m.Sharing == "per-client" {
				pid = fmt.Sprintf("per-client, %d running", runningClients(m))
			}
			if m.State == control.MCPRunning {
				uptime = elapsed(m.StartedAt)
			}
			state := string(m.State)
			if m.Unreachable {
				state = "waiting for app"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n", m.Name, transportKind(m.Transport), state, m.Start, pid, uptime, m.Restarts, m.Tools, m.LastError)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		for _, m := range s.Daemon.Status.MCPs {
			for _, c := range m.Clients {
				client := c.Client
				if client == "" {
					client = "(listing)"
				}
				pid := "-"
				if c.PID != 0 {
					pid = strconv.Itoa(c.PID)
				}
				fmt.Fprintf(out, "  %s [%s] %s pid %s up %s\n", m.Name, client, c.State, pid, elapsed(c.StartedAt))
			}
		}
		return nil
	} else {
		for _, e := range s.Config.Entries {
			state := "-"
			if e.Disabled {
				state = "disabled"
			}
			start := e.Start
			if start == "" {
				start = "eager"
			}
			transport := "stdio"
			if e.URL != "" {
				transport = e.Transport
				if transport == "" {
					transport = "http"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t-\t-\t-\t-\t\n", e.Name, transportKind(transport), state, start)
		}
	}
	return w.Flush()
}

func runningClients(m control.MCPStatus) int {
	n := 0
	for _, c := range m.Clients {
		if c.State == control.MCPRunning || c.State == control.MCPStarting {
			n++
		}
	}
	return n
}

func elapsed(since time.Time) string {
	if since.IsZero() {
		return "-"
	}
	return time.Since(since).Truncate(time.Second).String()
}

func transportKind(transport string) string {
	switch transport {
	case "http", "sse":
		return "connect (" + transport + ")"
	default:
		return "launch"
	}
}
