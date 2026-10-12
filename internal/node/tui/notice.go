package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m *model) noticeLine() string {
	switch m.confirm {
	case "stop":
		return "Stop the node? y/n"
	case "install":
		return "Start the node at login? y/n"
	case "uninstall":
		return "Stop starting at login? y/n"
	case "remove":
		return "Remove " + singleLine(m.confirmName) + "? y/n"
	}
	if m.busy {
		return m.accent(m.indicator.View()) + " " + singleLine(m.notice)
	}
	if m.notice == "" {
		return ""
	}
	if m.noticeError {
		return m.red("✗ " + singleLine(m.notice))
	}
	return m.green("✓ " + singleLine(m.notice))
}

// Snapshot errors stay visible in full even when a status card must truncate them.
func (m *model) noticeLines() []string {
	text := m.noticeLine()
	s := m.snapshot
	var errors []string
	for _, item := range [][2]string{{"Node", s.Daemon.Err}, {"Start at login", s.Service.Err}, {"Config", s.Config.Err}} {
		if item[1] != "" {
			errors = append(errors, item[0]+": "+singleLine(item[1]))
		}
	}
	hub := s.Daemon.Status.Hub
	switch {
	case !s.Daemon.Running || hub.Connected:
	case hub.PairCode != "":
		// A request waiting for an admin is not an error, and says what to do.
		if text != "" {
			text += " · "
		}
		text += m.accent("Waiting for approval: approve this machine in the hub's dashboard (Approvals); check it shows code " + m.bold(singleLine(hub.PairCode)))
	case hub.LastError != "":
		errors = append(errors, "Hub disconnected: "+singleLine(hub.LastError))
	}
	if len(errors) > 0 {
		if text != "" {
			text += " · "
		}
		text += m.red("✗ " + strings.Join(errors, " · "))
	}
	return strings.Split(ansi.Wrap(text, max(1, m.width), ""), "\n")
}

func (m *model) confirmLines() []string {
	switch m.confirm {
	case "stop":
		lines := []string{m.red(m.bold("Stop the node? y/n")), m.dim("Its MCPs stop and the hub loses them until you start it again (s).")}
		if m.snapshot.Service.Installed {
			lines = append(lines, m.dim("It starts again at next login."))
		}
		return lines
	case "install":
		return []string{m.bold("Start the node at login? y/n"), m.dim("Installs a user service (systemd/launchd) that starts the node at login and restarts it if it crashes. It captures your login shell's PATH so MCP commands are found."), m.dim("If the node is running now, it moves under the service.")}
	case "uninstall":
		return []string{m.red(m.bold("Stop starting at login? y/n")), m.dim("Removes the user service and stops the node. Your config is kept; s still starts it by hand.")}
	default:
		return []string{m.red(m.bold("Remove " + singleLine(m.confirmName) + "? y/n")), m.dim("Remove this MCP from the config (press R afterwards to apply).")}
	}
}
