package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

func (m *model) validConfig() bool { return m.snapshot.Config.Exists && m.snapshot.Config.Err == "" }

func (m *model) requireConfig() bool {
	if m.validConfig() {
		return true
	}
	if m.snapshot.Config.Err != "" {
		m.setNotice("Fix the config with e before starting the node: "+m.snapshot.Config.Err, true)
	} else {
		m.setNotice("Create the config with n first", true)
	}
	return false
}

func (m *model) handleKey(key string) tea.Cmd {
	node := m.node
	r, selected := m.selectedMCP()
	switch key {
	case "s":
		if !m.snapshot.Daemon.Running && m.requireConfig() {
			return m.begin("Starting the node…", func(ctx context.Context) actionMsg {
				return actionMsg{label: "node started", failure: "start node", err: node.Start(ctx)}
			})
		}
	case "S":
		if m.snapshot.Daemon.Running {
			m.confirm = "stop"
		}
	case "i":
		if m.snapshot.Service.Supported && !m.snapshot.Service.Installed && m.requireConfig() {
			m.confirm = "install"
		}
	case "U":
		if m.snapshot.Service.Installed {
			m.confirm = "uninstall"
		}
	case "n":
		if m.inspected && !m.snapshot.Config.Exists {
			return m.openForm(false)
		}
	case "a":
		if m.validConfig() {
			return m.openForm(true)
		}
	case "p":
		return m.startPairing()
	case "K":
		if m.validConfig() {
			return m.openKeyForm()
		}
	case "e":
		if m.snapshot.Config.Exists {
			m.busy = true
			m.actionID++
			m.setNotice("Opening editor…", false)
			id := m.actionID
			return tea.Batch(func() tea.Msg { c, err := node.EditorCommand(); return editorReadyMsg{id: id, command: c, err: err} }, m.spinner())
		}
	case "d":
		if selected {
			name, disabled := r.entry.Name, !r.entry.Disabled
			label := "enabled " + name
			if disabled {
				label = "disabled " + name
			}
			return m.begin("Updating "+name+"…", func(ctx context.Context) actionMsg {
				pending, err := node.SetDisabled(ctx, name, disabled)
				return actionMsg{label: editResult(label, pending), failure: "update " + name, err: err}
			})
		}
	case "x":
		if selected {
			m.confirm, m.confirmName = "remove", r.entry.Name
		}
	case "r":
		if selected && m.snapshot.Daemon.Running && !r.entry.Disabled {
			name := r.entry.Name
			return m.begin("Restarting "+name+"…", func(ctx context.Context) actionMsg {
				return actionMsg{label: "restarted " + name, failure: "restart " + name, err: node.Restart(ctx, name)}
			})
		}
	case "R":
		if m.snapshot.Daemon.Running {
			return m.begin("Reloading config…", func(ctx context.Context) actionMsg {
				return actionMsg{label: "applied the saved config", failure: "reload config", err: node.Reload(ctx)}
			})
		}
	case "tab":
		if selected {
			m.tab = (m.tab + 1) % 3
			return m.loadDetail()
		}
	case "up", "k", "down", "j":
		old := m.selected
		if key == "up" || key == "k" {
			m.selected = max(0, old-1)
		} else {
			m.selected = min(max(0, len(m.rows)-1), old+1)
		}
		if m.selected != old {
			return m.loadDetail()
		}
	case "f":
		if selected && m.tab == 0 && m.snapshot.Daemon.Running {
			m.follow = !m.follow
			if m.follow {
				m.viewport.GotoBottom()
			}
		}
	case "pgup":
		if selected {
			if m.tab == 0 {
				m.follow = false
			}
			m.viewport.PageUp()
		}
	case "pgdown":
		if selected {
			if m.tab == 0 {
				m.follow = false
			}
			m.viewport.PageDown()
		}
	}
	return nil
}

// editResult follows an MCP change: saved, and applied to a running node
// only when the user presses R (it restarts the node's MCPs).
func editResult(label string, pending bool) string {
	if pending {
		return label + " · press R to apply"
	}
	return label + " · press s to start the node"
}

func (m *model) confirmed(action, name string) tea.Cmd {
	node := m.node
	switch action {
	case "stop":
		return m.begin("Stopping the node…", func(ctx context.Context) actionMsg {
			return actionMsg{label: "node stopped", failure: "stop node", err: node.Stop(ctx)}
		})
	case "install":
		return m.begin("Setting the node to start at login…", func(ctx context.Context) actionMsg {
			_, err := node.Install(ctx)
			return actionMsg{label: "node will start at login", failure: "start at login", err: err}
		})
	case "uninstall":
		return m.begin("Removing start at login…", func(ctx context.Context) actionMsg {
			return actionMsg{label: "node no longer starts at login · stopped", failure: "stop starting at login", err: node.Uninstall(ctx)}
		})
	case "pair":
		return m.openPairForm()
	case "remove":
		return m.begin("Removing "+name+"…", func(ctx context.Context) actionMsg {
			pending, err := node.RemoveMCP(ctx, name)
			return actionMsg{label: editResult("removed "+name, pending), failure: "remove " + name, err: err}
		})
	}
	return nil
}
