// Command mcp-mesh-node manages a node daemon or wraps one foreground MCP.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/daemon"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/paths"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

const notRunning = "the node is not running (start it with `mcp-mesh-node start`, or `mcp-mesh-node install` to start it now and at every login)"

func rootCmd() *cobra.Command {
	var configPath, socketPath string
	var noInput bool
	defaultConfig, configErr := paths.ConfigFile()
	defaultSocket, socketErr := paths.SocketPath()
	root := &cobra.Command{Use: "mcp-mesh-node", Short: "Manage MCP servers connected to an mcp-mesh hub", Version: version, SilenceUsage: true, Args: cobra.NoArgs,
		Long:    "Manage this machine’s MCP servers through one hub connection.\n\nGetting started:\n  mcp-mesh-node                      open the dashboard (does everything below)\n  mcp-mesh-node init --hub ws://HUB:7001/node\n  mcp-mesh-node add -- npx -y @modelcontextprotocol/server-filesystem ~/code\n  mcp-mesh-node add --name blender --url http://127.0.0.1:9876/mcp\n                                     connect to an app’s MCP and share it through the hub\n  mcp-mesh-node start                start the node now, until logout\n  mcp-mesh-node install              start it now and at every login",
		Example: "  mcp-mesh-node",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interactive(cmd) {
				return openDashboard(cmd)
			}
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Name() == "wrap" || cmd.Name() == "mcp-mesh-node" {
				return nil
			}
			if configPath == "" && configErr != nil {
				return configErr
			}
			if socketPath == "" && socketErr != nil {
				return socketErr
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "node configuration path (MCPMESH_CONFIG)")
	root.PersistentFlags().StringVar(&socketPath, "socket", defaultSocket, "node control socket (MCPMESH_SOCKET)")
	root.PersistentFlags().BoolVar(&noInput, "no-input", os.Getenv("MCPMESH_NO_INPUT") == "1", "disable interactive prompts (MCPMESH_NO_INPUT=1)")
	root.AddGroup(&cobra.Group{ID: "setup", Title: "Setup:"}, &cobra.Group{ID: "node", Title: "Node:"}, &cobra.Group{ID: "mcps", Title: "MCPs:"})
	root.AddCommand(wrapCmd())
	root.AddCommand(discoverCmd())
	add := func(use, short string, args cobra.PositionalArgs, fn func(*cobra.Command, []string) error) *cobra.Command {
		cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			err := fn(cmd, args)
			if errors.Is(err, control.ErrNotRunning) {
				return errors.New(notRunning)
			}
			return err
		}}
		root.AddCommand(cmd)
		return cmd
	}
	add("run", "Run the node daemon in the foreground", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error {
		env, err := paths.EnvFile()
		if err != nil {
			return err
		}
		cache, err := paths.ToolCacheDir()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return daemon.Run(ctx, daemon.Options{ConfigPath: configPath, SocketPath: socketPath, EnvFile: env, ToolCacheDir: cache, Version: version})
	})
	var hub, initKey, initName string
	var force bool
	initCmd := add("init", "Write a starter node configuration", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("key") {
			// Resolve and check the key before anything is written.
			initKey, err = keyArgument(cmd, []string{initKey})
			if err == nil {
				err = checkKeyShape(initKey)
			}
			if err != nil {
				return err
			}
		}
		if !cmd.Flags().Changed("hub") && interactive(cmd) {
			hub, err = promptHub(cmd, node)
			if err != nil {
				return err
			}
		}
		if err := node.InitConfig(hub, force); err != nil {
			if errors.Is(err, config.ErrConfigExists) {
				return fmt.Errorf("config %s already exists (use --force to overwrite): %w", configPath, err)
			}
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", configPath)
		if initName != "" {
			if _, err := node.SetNodeName(cmd.Context(), initName); err != nil {
				return fmt.Errorf("%s was written, but setting the node name failed: %w", configPath, err)
			}
		}
		if initKey != "" {
			if err := setKey(cmd, node, initKey); err != nil {
				return fmt.Errorf("%s was written, but saving the key failed: %w", configPath, err)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Run `mcp-mesh-node` to open the dashboard, or `mcp-mesh-node add` then `mcp-mesh-node start`. Validate with `mcp-mesh-node config check`.")
		return nil
	})
	initCmd.Flags().StringVar(&initKey, "key", "", "node key created on the hub (`-` reads it from stdin)")
	initCmd.Flags().StringVar(&hub, "hub", "", "hub node endpoint")
	initCmd.Flags().StringVar(&initName, "name", "", "this machine's name in the mesh (default: the hostname); must match the name the node key was created for")
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	configCmd := &cobra.Command{Use: "config", Short: "Inspect node configuration", Args: cobra.NoArgs}
	configCmd.AddCommand(&cobra.Command{Use: "check", Short: "Validate the configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Hub: %s\nNode: %s\n", cfg.Hub.URL, cfg.NodeName)
		for _, m := range cfg.MCPs {
			allow := "all tools"
			if m.AllowTools != nil {
				allow = "[" + strings.Join(m.AllowTools, ", ") + "]"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: transport=%s start=%s allow_tools=%s\n", m.Name, m.Transport, m.Start, allow)
		}
		return nil
	}})
	root.AddCommand(configCmd)
	root.AddCommand(keyCmd())
	var asJSON bool
	statusCmd := add("status", "Show node and MCP status", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		return printStatus(cmd, node.Inspect(cmd.Context()), asJSON)
	})
	statusCmd.Aliases = []string{"list"}
	statusCmd.Flags().BoolVar(&asJSON, "json", false, "print the status as JSON")
	var maxBytes int
	var follow bool
	logsCmd := add("logs MCP", "Show an MCP's stderr", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		if maxBytes < 0 {
			return errors.New("-n must be non-negative")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		client := control.NewClient(socketPath)
		tail, err := client.Logs(ctx, args[0], maxBytes)
		if err != nil {
			return err
		}
		if _, err := cmd.OutOrStdout().Write(tail); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		events, err := client.Events(ctx)
		if err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case e, ok := <-events:
				if !ok {
					return nil
				}
				if e.Kind == control.EventLog && e.MCP == args[0] {
					if _, err := fmt.Fprint(cmd.OutOrStdout(), e.Data); err != nil {
						return err
					}
				}
			}
		}
	})
	logsCmd.Flags().IntVarP(&maxBytes, "bytes", "n", 64*1024, "maximum tail bytes")
	logsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow new stderr events")
	add("restart MCP", "Restart an MCP", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return control.NewClient(socketPath).Restart(cmd.Context(), args[0])
	})
	add("reload", "Apply the saved configuration (restarts the node's MCPs once calls in progress finish)", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error {
		if err := control.NewClient(socketPath).Reload(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "node reloaded with the saved configuration")
		return nil
	})
	add("tui", "Open the interactive node dashboard", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error { return openDashboard(cmd) })
	var atBoot bool
	installCmd := add("install", "Start the node at login (installs a user service)", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		state, err := node.Install(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if atBoot {
			if err := node.EnableAtBoot(cmd.Context()); err != nil {
				return fmt.Errorf("the service is installed, but starting it at boot failed: %w", err)
			}
		}
		on, supported, err := node.AtBoot(cmd.Context())
		switch {
		case err != nil:
			fmt.Fprintf(out, "The node will start at login (user service: %s)\n", state.UnitPath)
			fmt.Fprintf(cmd.ErrOrStderr(), "note: couldn't tell whether it also starts at boot: %v\n", err)
		case on:
			fmt.Fprintf(out, "The node will start when the machine boots (user service: %s)\n", state.UnitPath)
		default:
			fmt.Fprintf(out, "The node will start at login (user service: %s)\n", state.UnitPath)
			if supported {
				fmt.Fprintln(out, "After a reboot it waits until you log in. On a machine nobody logs in to (a server), run mcp-mesh-node install --at-boot.")
			}
		}
		fmt.Fprintf(out, "Logs: %s\n", node.LogsHint())
		return nil
	})
	installCmd.Flags().BoolVar(&atBoot, "at-boot", false, "start when the machine boots, without a login (Linux: loginctl enable-linger)")
	add("uninstall", "Stop starting the node at login (removes the user service; keeps config)", cobra.NoArgs, func(cmd *cobra.Command, _ []string) error {
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		if err := node.Uninstall(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "The node no longer starts at login · stopped")
		return nil
	})
	envCmd := &cobra.Command{Use: "env", Short: "Manage the captured login environment", Args: cobra.NoArgs}
	envCmd.AddCommand(&cobra.Command{Use: "refresh", Short: "Re-capture the login environment", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := refreshEnv(cmd.Context()); err != nil {
			return err
		}
		file, err := paths.EnvFile()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %s\n", file)
		if runtime.GOOS == "darwin" {
			fmt.Fprintln(cmd.OutOrStdout(), "Apply it with: launchctl kickstart -k gui/$(id -u)/dev.mcp-mesh.node")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "Apply it with: systemctl --user restart mcp-mesh-node")
		}
		return nil
	}})
	root.AddCommand(envCmd)
	addManagementCommands(root)
	commandHelp(root)
	return root
}
func refreshEnv(ctx context.Context) error {
	env, err := service.CaptureLoginEnv(ctx)
	if err != nil {
		return err
	}
	file, err := paths.EnvFile()
	if err != nil {
		return err
	}
	return service.WriteEnvFile(file, env)
}
