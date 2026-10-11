package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// readSecret reads a line from the terminal without echoing it; tests replace it.
var readSecret = func() (string, error) {
	data, err := term.ReadPassword(os.Stdin.Fd())
	return string(data), err
}

func keyCmd() *cobra.Command {
	root := &cobra.Command{Use: "key", Short: "Manage the key this node presents to the hub", Args: cobra.NoArgs}
	root.AddCommand(&cobra.Command{
		Use:   "set [KEY|-]",
		Short: "Save the hub's node key and use it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := keyArgument(cmd, args)
			if err != nil {
				return err
			}
			node, err := commandNode(cmd)
			if err != nil {
				return err
			}
			return setKey(cmd, node, key)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Stop using a hub key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			node, err := commandNode(cmd)
			if err != nil {
				return err
			}
			result, err := node.ClearKey(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !result.Cleared {
				fmt.Fprintln(out, "No hub key is configured")
				return nil
			}
			fmt.Fprintln(out, "Removed the hub key from the configuration")
			if result.Deleted {
				fmt.Fprintf(out, "Deleted %s\n", result.File)
			} else if result.File != "" {
				fmt.Fprintf(out, "Left %s in place; delete it yourself if you no longer need it\n", result.File)
			}
			reportReload(cmd, result.Reloaded)
			return nil
		},
	})
	return root
}

// keyArgument returns the key from the argument, stdin ("-") or, at a
// terminal, a prompt that does not echo. A key on the command line ends up in
// shell history, so the help recommends the other two.
func keyArgument(cmd *cobra.Command, args []string) (string, error) {
	switch {
	case len(args) == 1 && args[0] == "-":
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4096))
		if err != nil {
			return "", fmt.Errorf("read key from stdin: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	case len(args) == 1:
		return strings.TrimSpace(args[0]), nil
	case interactive(cmd):
		fmt.Fprint(cmd.ErrOrStderr(), "Node key (input is hidden): ")
		key, err := readSecret()
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}
		return strings.TrimSpace(key), nil
	}
	return "", errors.New("provide the key as an argument, or `-` to read it from stdin")
}

func setKey(cmd *cobra.Command, node *nodectl.Node, key string) error {
	result, err := node.SetKey(cmd.Context(), key)
	if err != nil {
		return err
	}
	path, _ := cmd.Flags().GetString("config")
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Saved the node key to %s (readable only by you) and set hub.key_file in %s\n", result.File, path)
	fmt.Fprintf(out, "The hub must have created this key for node %q: mcp-mesh-hub key create node %s\n", result.Node, result.Node)
	reportReload(cmd, result.Reloaded)
	return nil
}

// checkKeyShape fails early so init does not write a config and then refuse the key.
func checkKeyShape(key string) error {
	return config.ValidateKey(strings.TrimSpace(key))
}

// displayPath shortens the home directory to ~ for messages.
func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}
