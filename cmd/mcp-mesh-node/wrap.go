// Command mcp-mesh-node wraps one MCP server and makes it available through
// an mcp-mesh hub.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/paths"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/toolfilter"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var version = "dev"

var agentRunner = agent.Run

func wrapCmd() *cobra.Command {
	var hubURL, nodeName, logLevel string
	var maxSize int64
	var add, foreground bool
	var flags mcpFlags
	cmd := &cobra.Command{
		Use:     "wrap [--name NAME] (-- COMMAND [ARGS...] | --url URL)",
		Short:   "Make one MCP server available through an mcp-mesh hub",
		Version: version, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := newLogger(logLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if add && foreground {
				return errors.New("--add and --foreground are mutually exclusive")
			}
			configPath, _ := cmd.Flags().GetString("config")
			if configPath == "" {
				configPath, err = paths.ConfigFile()
				if err != nil {
					return err
				}
			}
			_, statErr := os.Stat(configPath)
			cfg, configErr := config.Load(configPath)
			if configErr != nil && !errors.Is(statErr, os.ErrNotExist) && !cmd.Flags().Changed("hub") {
				return configErr
			}
			source := "--hub"
			if !cmd.Flags().Changed("hub") {
				source = "MCPMESH_HUB"
				if hubURL == "" && cfg != nil {
					hubURL, source = cfg.Hub.URL, configPath
				}
			}
			if hubURL == "" {
				return errors.New("--hub is required (or set MCPMESH_HUB); run `mcp-mesh-node init` to configure a hub")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Using hub %s from %s\n", hubURL, source)
			explicitNode := cmd.Flags().Changed("node") || os.Getenv("MCPMESH_NODE") != ""
			if !explicitNode && cfg != nil {
				nodeName = cfg.NodeName
			}
			// Credentials in node.yaml belong to its hub; never send them to
			// a different hub given with --hub or MCPMESH_HUB.
			var header http.Header
			if cfg != nil && hubURL == cfg.Hub.URL {
				header = cfg.Hub.Header
			}
			entry, err := flags.entry(args)
			if err != nil {
				return err
			}
			p := newPrompt(cmd)
			if entry.Name == "" && (add || foreground) {
				entry.Name = nodectl.SuggestName(args, entry.URL)
				fmt.Fprintf(cmd.OutOrStdout(), "MCP name: %s\n", entry.Name)
			} else {
				entry.Name = chooseName(cmd, p, entry.Name, args, entry.URL)
			}
			node, err := commandNode(cmd)
			if err != nil {
				return err
			}
			detectEntry(cmd, node, &entry)
			statusCtx, cancel := context.WithTimeout(cmd.Context(), time.Second)
			_, statusErr := node.Client().Status(statusCtx)
			cancel()
			running := statusErr == nil
			save := add
			if running && interactive(cmd) && !add && !foreground {
				save = p.confirm(fmt.Sprintf("The node is running. Add %q to its config? It runs once you apply it with mcp-mesh-node reload", entry.Name), true)
			}
			if save {
				if err := addEntry(cmd, node, p, &entry, !add); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Hub tool prefix: %s__%s__\n", nodeName, entry.Name)
				return nil
			}
			if running && !explicitNode {
				original := nodeName
				nodeName += "-" + entry.Name
				fmt.Fprintf(cmd.ErrOrStderr(), "The running node is named %q; using %q for this foreground MCP to avoid a second connection with the same name.\n", original, nodeName)
			}
			if !running {
				fmt.Fprintln(cmd.ErrOrStderr(), `Tip: run "mcp-mesh-node add ..." to keep it in your config`)
			}
			var up upstream.Upstream
			if entry.URL != "" {
				if entry.Transport == string(proto.TransportSSE) {
					up = upstream.NewSSE(upstream.HTTPConfig{URL: entry.URL, Headers: entry.Headers})
				} else {
					up = upstream.NewHTTP(upstream.HTTPConfig{URL: entry.URL, Headers: entry.Headers})
				}
			} else {
				up = upstream.NewStdio(upstream.StdioConfig{Command: entry.Command, Args: entry.Args, Env: flags.envs, Dir: entry.Dir, MaxMessageSize: int(maxSize), Stderr: cmd.ErrOrStderr()})
			}
			if entry.AllowTools != nil {
				up = &filteredUpstream{Upstream: up, allow: entry.AllowTools}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log.Info("mcp-mesh-node starting", "version", version, "hub", hubURL, "node", nodeName, "mcp", entry.Name)
			err = agentRunner(ctx, agent.Config{HubURL: hubURL, Node: nodeName, Version: version, Header: header,
				MCPs: []agent.MCP{{Name: entry.Name, Upstream: up, Sharing: proto.SharingShared}}, MaxMessageSize: maxSize, Logger: log})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	hostname, _ := os.Hostname()
	f := cmd.Flags()
	f.StringVar(&hubURL, "hub", os.Getenv("MCPMESH_HUB"), "hub node endpoint, e.g. ws://hub:7001/node")
	f.StringVar(&nodeName, "node", envOr("MCPMESH_NODE", hostname), "this machine's name in the mesh")
	flags.bind(cmd)
	f.BoolVar(&add, "add", false, "save this MCP in node.yaml (apply it to a running node with reload)")
	f.BoolVar(&foreground, "foreground", false, "run in the foreground without asking to add")
	f.Int64Var(&maxSize, "max-message-size", proto.DefaultMaxMessageSize, "largest MCP message in bytes")
	f.StringVar(&logLevel, "log-level", envOr("MCPMESH_LOG_LEVEL", "info"), "debug, info, warn or error")
	return cmd
}

type filteredUpstream struct {
	upstream.Upstream
	allow []string
}

func (u *filteredUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	conn, err := u.Upstream.Open(ctx)
	if err != nil {
		return nil, err
	}
	return toolfilter.Wrap(conn, toolfilter.Options{Allow: u.allow}), nil
}

func keyValues(pairs []string, flag string) (map[string]string, error) {
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%s %q: want KEY=VALUE", flag, p)
		}
		m[k] = v
	}
	return m, nil
}

func newLogger(level string, output io.Writer) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: lv})), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
