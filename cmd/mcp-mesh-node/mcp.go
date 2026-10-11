package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type mcpFlags struct {
	name, url, dir, start, idleTimeout, transport string
	sharing, callTimeout                          string
	maxClients                                    int
	flags                                         *pflag.FlagSet
	headers, envs, allowTools                     []string
}

func (f *mcpFlags) bind(cmd *cobra.Command) {
	flags := cmd.Flags()
	f.flags = flags
	flags.StringVar(&f.name, "name", "", "MCP name (default: suggested from the command or URL)")
	flags.StringVar(&f.url, "url", "", "connect to an MCP an app already runs and share it through the hub")
	flags.StringVar(&f.transport, "transport", "", "HTTP transport: http or sse (only with --url; default: detect)")
	flags.StringArrayVar(&f.headers, "header", nil, "HTTP header for --url as KEY=VALUE (repeatable)")
	flags.StringArrayVar(&f.envs, "env", nil, "extra command environment as KEY=VALUE (repeatable)")
	flags.StringVar(&f.dir, "dir", "", "working directory for the command")
	flags.StringVar(&f.start, "start", "eager", "startup policy: eager or lazy (default: lazy for --url, eager for commands)")
	flags.StringVar(&f.idleTimeout, "idle-timeout", "", "idle timeout, e.g. 10m")
	flags.StringVar(&f.sharing, "sharing", "", "shared (one process for every client) or per-client (a process per client, started on its first call)")
	flags.IntVar(&f.maxClients, "max-clients", 0, "per-client only: most clients with a process at once (default 8)")
	flags.StringVar(&f.callTimeout, "call-timeout", "", "how long one call may take, e.g. 10m (default: the hub's)")
	flags.StringSliceVar(&f.allowTools, "allow-tools", nil, "allowed tools, comma-separated (default: all)")
}
func (f *mcpFlags) entry(args []string) (config.Entry, error) {
	start := f.start
	if f.url != "" && f.flags != nil && !f.flags.Changed("start") {
		start = "lazy"
	}
	if f.sharing == "per-client" && (f.flags == nil || !f.flags.Changed("start")) {
		start = "lazy" // each client's process starts on its first call
	}
	e := config.Entry{Transport: f.transport, Name: f.name, URL: f.url, Dir: f.dir, Start: start, IdleTimeout: f.idleTimeout,
		Sharing: f.sharing, MaxClients: f.maxClients, CallTimeout: f.callTimeout, AllowTools: f.allowTools}
	switch {
	case f.url != "" && len(args) > 0:
		return e, errors.New("give either a command after -- or --url, not both")
	case f.url != "":
	case len(args) > 0:
		e.Command, e.Args = args[0], args[1:]
	default:
		return e, errors.New("give the MCP command after -- or use --url")
	}
	if f.transport != "" && f.transport != "http" && f.transport != "sse" {
		return e, errors.New("--transport must be http or sse")
	}
	if f.url == "" && (f.transport != "" || (f.flags != nil && f.flags.Changed("transport"))) {
		return e, errors.New("--transport is only allowed with --url")
	}
	var err error
	e.Headers, err = keyValues(f.headers, "--header")
	if err != nil {
		return e, err
	}
	e.Env, err = keyValues(f.envs, "--env")
	if err != nil {
		return e, err
	}
	if start != "eager" && start != "lazy" {
		return e, errors.New("--start must be eager or lazy")
	}
	if f.sharing != "" && f.sharing != "shared" && f.sharing != "per-client" {
		return e, errors.New("--sharing must be shared or per-client")
	}
	if f.sharing == "per-client" && start != "lazy" {
		return e, errors.New("per-client MCPs start for each client on its first call; leave out --start or use --start lazy")
	}
	if f.maxClients != 0 && f.sharing != "per-client" {
		return e, errors.New("--max-clients is only allowed with --sharing per-client")
	}
	if f.callTimeout != "" {
		if d, err := time.ParseDuration(f.callTimeout); err != nil || d <= 0 {
			return e, fmt.Errorf("invalid --call-timeout %q: want a positive duration", f.callTimeout)
		}
	}
	if f.idleTimeout != "" {
		if start != "lazy" {
			return e, errors.New("--idle-timeout is only allowed with --start lazy")
		}
		d, err := time.ParseDuration(f.idleTimeout)
		if err != nil || d <= 0 {
			return e, fmt.Errorf("invalid --idle-timeout %q: want a positive duration", f.idleTimeout)
		}
	}
	return e, nil
}

func detectEntry(cmd *cobra.Command, node *nodectl.Node, e *config.Entry) {
	if e.URL == "" || e.Transport != "" {
		return
	}
	transport, err := node.DetectTransport(cmd.Context(), e.URL, e.Headers)
	if err != nil {
		var reason string
		transport, reason = nodectl.TransportFallback(e.URL)
		fmt.Fprintf(cmd.ErrOrStderr(), "Couldn't reach %s (is the app running?); saved as %s. Set --transport to override.\n", e.URL, reason)
	}
	e.Transport = string(transport)
}
