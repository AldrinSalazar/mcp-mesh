package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/tui"
	"github.com/spf13/cobra"
)

var (
	terminalInput  = func() bool { return charDevice(os.Stdin) && charDevice(os.Stderr) }
	tuiRunner      = tui.Run
	editorRunner   = func(cmd *exec.Cmd) error { return cmd.Run() }
	nodectlOptions nodectl.Options
)

func charDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func interactive(cmd *cobra.Command) bool {
	noInput, _ := cmd.Flags().GetBool("no-input")
	return !noInput && os.Getenv("MCPMESH_NO_INPUT") != "1" && terminalInput()
}

func commandNode(cmd *cobra.Command) (*nodectl.Node, error) {
	opts := nodectlOptions
	opts.ConfigPath, _ = cmd.Flags().GetString("config")
	opts.SocketPath, _ = cmd.Flags().GetString("socket")
	opts.Version = version
	return nodectl.New(opts)
}

func openDashboard(cmd *cobra.Command) error {
	node, err := commandNode(cmd)
	if err != nil {
		return err
	}
	return tuiRunner(cmd.Context(), node)
}

type prompt struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompt(cmd *cobra.Command) *prompt {
	return &prompt{bufio.NewReader(cmd.InOrStdin()), cmd.ErrOrStderr()}
}
func (p *prompt) ask(question, def string) string {
	answer, _ := p.askWithError(question, def)
	return answer
}
func (p *prompt) askWithError(question, def string) (string, error) {
	if def == "" {
		fmt.Fprintf(p.out, "%s: ", question)
	} else {
		fmt.Fprintf(p.out, "%s [%s]: ", question, def)
	}
	answer, err := p.in.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return def, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return def, err
	}
	return answer, err
}
func (p *prompt) confirm(question string, defaultYes bool) bool {
	def, choices := "n", "y/N"
	if defaultYes {
		def, choices = "y", "Y/n"
	}
	answer := p.ask(question+" ["+choices+"]", "")
	if answer == "" {
		answer = def
	}
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
}

func chooseName(cmd *cobra.Command, p *prompt, name string, args []string, url string) string {
	if name != "" {
		return name
	}
	name = nodectl.SuggestName(args, url)
	if interactive(cmd) {
		return p.ask("MCP name", name)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "MCP name: %s\n", name)
	return name
}

func addEntry(cmd *cobra.Command, node *nodectl.Node, p *prompt, entry *config.Entry, rename bool) error {
	for {
		pending, err := node.AddMCP(cmd.Context(), *entry)
		if rename && errors.Is(err, config.ErrMCPExists) && interactive(cmd) {
			name := p.ask(fmt.Sprintf("MCP %q already exists. Choose another name", entry.Name), "")
			if name == "" {
				return err
			}
			entry.Name = name
			continue
		}
		if err != nil {
			return err
		}
		path, _ := cmd.Flags().GetString("config")
		fmt.Fprintf(cmd.OutOrStdout(), "Added %s to %s\n", entry.Name, path)
		reportSaved(cmd, pending)
		return nil
	}
}

// reportReload follows a change the node applies at once (its key).
func reportReload(cmd *cobra.Command, reloaded bool) {
	if reloaded {
		fmt.Fprintln(cmd.OutOrStdout(), "node reloaded")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "start the node with `mcp-mesh-node start`")
	}
}

// applyHint is how to apply saved MCP changes to a running node.
const applyHint = "The running node still uses its previous MCPs. Apply the change with `mcp-mesh-node reload`; it restarts all of the node's MCPs once calls in progress finish."

// reportSaved follows an MCP change, which is saved and not applied: like
// an MCP client that reads its config at start, the node changes its MCPs
// only when the user says so.
func reportSaved(cmd *cobra.Command, pending bool) {
	if pending {
		fmt.Fprintln(cmd.OutOrStdout(), applyHint)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "start the node with `mcp-mesh-node start`")
	}
}

// pairingWait bounds how long start and install wait for a node without a
// key to get its pairing code from the hub.
var pairingWait = 5 * time.Second

// reportPairing shows the pairing code of a node that was started without
// a key, so the user can approve it in the hub's dashboard right away.
func reportPairing(cmd *cobra.Command, node *nodectl.Node, before nodectl.Snapshot) {
	if before.Config.Key.Set() || before.Config.Hub == "" {
		return
	}
	out := cmd.OutOrStdout()
	ctx, cancel := context.WithTimeout(cmd.Context(), pairingWait)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if st, err := node.Client().Status(ctx); err == nil {
			switch h := st.Hub; {
			case h.Connected:
				return
			case h.PairCode != "":
				fmt.Fprintf(out, "No hub key, so the node asked the hub to pair: %s\n", pairingHint(h.PairCode))
				return
			case h.KeyRejected && h.LastError != "":
				fmt.Fprintf(out, "No hub key, and pairing failed: %s\n", h.LastError)
				return
			}
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "No hub key: the node asks the hub to pair once it reaches it; `mcp-mesh-node status` shows the code to approve in the hub's dashboard")
			return
		case <-tick.C:
		}
	}
}

func addManagementCommands(root *cobra.Command) {
	root.AddCommand(&cobra.Command{Use: "start", Short: "Start the node in the background (until logout)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		before := node.Inspect(cmd.Context())
		if err := node.Start(cmd.Context()); err != nil {
			return err
		}
		switch {
		case before.Daemon.Running:
			fmt.Fprintln(cmd.OutOrStdout(), "Node already running")
		case before.Service.Installed:
			fmt.Fprintln(cmd.OutOrStdout(), "Started the node via the user service")
		default:
			fmt.Fprintf(cmd.OutOrStdout(), "Started the node in the background until logout (log: %s)\n", before.LogFile)
		}
		reportPairing(cmd, node, before)
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "stop", Short: "Stop the node", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		before := node.Inspect(cmd.Context())
		if err := node.Stop(cmd.Context()); err != nil {
			return err
		}
		if before.Daemon.Running {
			fmt.Fprintln(cmd.OutOrStdout(), "Stopped the node")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Node is not running")
		}
		return nil
	}})
	var flags mcpFlags
	add := &cobra.Command{Use: "add [--name NAME] (-- COMMAND [ARGS...] | --url URL)", Short: "Save an MCP in the node configuration", RunE: func(cmd *cobra.Command, args []string) error {
		entry, err := flags.entry(args)
		if err != nil {
			return err
		}
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		detectEntry(cmd, node, &entry)
		p := newPrompt(cmd)
		entry.Name = chooseName(cmd, p, entry.Name, args, entry.URL)
		return addEntry(cmd, node, p, &entry, true)
	}}
	flags.bind(add)
	root.AddCommand(add)
	var yes bool
	remove := &cobra.Command{Use: "remove NAME", Short: "Remove an MCP from the configuration", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if interactive(cmd) && !yes && !newPrompt(cmd).confirm(fmt.Sprintf("Remove %q from the configuration?", args[0]), false) {
			fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled")
			return nil
		}
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		pending, err := node.RemoveMCP(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", args[0])
		reportSaved(cmd, pending)
		return nil
	}}
	remove.Flags().BoolVar(&yes, "yes", false, "skip confirmation")
	root.AddCommand(remove)
	for _, action := range []string{"enable", "disable"} {
		root.AddCommand(&cobra.Command{Use: action + " NAME", Short: strings.ToUpper(action[:1]) + action[1:] + " a configured MCP", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			node, err := commandNode(cmd)
			if err != nil {
				return err
			}
			pending, err := node.SetDisabled(cmd.Context(), args[0], action == "disable")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sd %s\n", strings.ToUpper(action[:1])+action[1:], args[0])
			reportSaved(cmd, pending)
			return nil
		}})
	}
	root.AddCommand(&cobra.Command{Use: "edit", Short: "Edit and validate the node configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		editor, err := node.EditorCommand()
		if err != nil {
			return err
		}
		editor.Stdin, editor.Stdout, editor.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := editorRunner(editor); err != nil {
			return fmt.Errorf("run editor: %w", err)
		}
		path, _ := cmd.Flags().GetString("config")
		if _, err := config.Load(path); err != nil {
			return fmt.Errorf("%w; the node keeps the previous config", err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), time.Second)
		defer cancel()
		if st, err := node.Client().Status(ctx); err == nil {
			if st.ConfigChanged {
				fmt.Fprintln(cmd.OutOrStdout(), applyHint)
			}
		} else if !errors.Is(err, control.ErrNotRunning) {
			return err
		}
		return nil
	}})
}
