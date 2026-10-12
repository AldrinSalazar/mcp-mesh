// Command mcp-mesh-hub is the central server: it accepts node connections,
// merges their MCPs and serves them to clients as one MCP endpoint.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/server"
)

var version = "dev"

type config struct {
	server.Config
	logLevel       string
	logFormat      string
	trustedProxies string
}

func defaultDataDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "mcp-mesh-hub")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mcp-mesh-hub")
	}
	return filepath.Join(home, ".local", "state", "mcp-mesh-hub")
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var cfg config
	root := &cobra.Command{
		Use:           "mcp-mesh-hub",
		Short:         "Merge MCP servers from many machines into one MCP endpoint",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serve(ctx, cfg)
		},
	}
	hostname, _ := os.Hostname()
	// Persistent: the key subcommands need the data dir and the URLs they
	// print in their snippets.
	p := root.PersistentFlags()
	p.StringVar(&cfg.DataDir, "data-dir", env("MCPMESH_DATA_DIR", defaultDataDir()), "directory for the hub's state (hub.db)")
	p.StringVar(&cfg.AdminListen, "admin-listen", env("MCPMESH_ADMIN_LISTEN", ":7000"), "admin UI listen address")
	p.StringVar(&cfg.NodeListen, "node-listen", env("MCPMESH_NODE_LISTEN", ":7001"), "node WebSocket listen address")
	p.StringVar(&cfg.ClientListen, "client-listen", env("MCPMESH_CLIENT_LISTEN", ":7002"), "MCP client listen address")
	p.StringVar(&cfg.Listen, "listen", env("MCPMESH_LISTEN", ""), "serve the dashboard, /node and /mcp on this one address instead of the three ports (for a reverse proxy)")
	p.StringVar(&cfg.PublicHost, "public-host", env("MCPMESH_PUBLIC_HOST", "localhost"), "host name nodes and clients use to reach the hub (for UI snippets)")
	p.StringVar(&cfg.NodeURL, "node-url", env("MCPMESH_NODE_URL", ""), "public node URL for UI snippets (default ws://<public-host>:<node port>/node, wss with TLS)")
	p.StringVar(&cfg.ClientURL, "client-url", env("MCPMESH_CLIENT_URL", ""), "public client URL for UI snippets (default http://<public-host>:<client port>/mcp, https with TLS)")
	p.StringVar(&cfg.TLSCert, "tls-cert", env("MCPMESH_TLS_CERT", ""), "TLS certificate file; serves all listeners over TLS (needs --tls-key)")
	p.StringVar(&cfg.TLSKey, "tls-key", env("MCPMESH_TLS_KEY", ""), "TLS private key file (needs --tls-cert)")

	f := root.Flags()
	f.BoolVar(&cfg.Auth, "auth", envBool("MCPMESH_AUTH", true), "require keys from nodes, clients and the dashboard; false lets anyone who can reach the hub use it")
	f.StringVar(&cfg.AdminUser, "admin-user", env("MCPMESH_ADMIN_USER", ""), "name of the first admin user, created when the hub has none (default admin)")
	f.StringVar(&cfg.AdminPassword, "admin-password", env("MCPMESH_ADMIN_PASSWORD", ""), "password of the first admin user (default: generated and logged once); prefer MCPMESH_ADMIN_PASSWORD or MCPMESH_ADMIN_PASSWORD_FILE, as flags show in the process list")
	f.StringVar(&cfg.trustedProxies, "trusted-proxies", env("MCPMESH_TRUSTED_PROXIES", ""), "comma-separated addresses or CIDR prefixes of reverse proxies whose X-Forwarded-For and X-Forwarded-Proto are trusted")
	f.BoolVar(&cfg.AllowInsecure, "allow-insecure", envBool("MCPMESH_ALLOW_INSECURE", false), "accept plain HTTP from public addresses (by default only loopback and private networks may use it)")
	f.BoolVar(&cfg.Pairing, "pairing", envBool("MCPMESH_PAIRING", true), "let a node without a key ask to join; an admin approves it in the dashboard")
	f.BoolVar(&cfg.MDNS, "mdns", envBool("MCPMESH_MDNS", true), "announce the hub on the local network")
	f.StringVar(&cfg.Name, "name", env("MCPMESH_NAME", hostname), "hub name shown to nodes")
	f.DurationVar(&cfg.CallTimeout, "call-timeout", envDuration("MCPMESH_CALL_TIMEOUT", 120*time.Second), "default tool call timeout")
	f.StringVar(&cfg.logLevel, "log-level", env("MCPMESH_LOG_LEVEL", "info"), "debug, info, warn or error")
	f.StringVar(&cfg.logFormat, "log-format", env("MCPMESH_LOG_FORMAT", "text"), "text or json")

	root.AddCommand(healthcheckCmd(&cfg), keyCmd(&cfg), adminCmd(&cfg))
	return root
}

func serve(ctx context.Context, cfg config) error {
	log, err := newLogger(cfg.logLevel, cfg.logFormat)
	if err != nil {
		return err
	}
	if err := resolveAdminPassword(&cfg); err != nil {
		return err
	}
	if cfg.TrustedProxies, err = auth.ParseProxies(cfg.trustedProxies); err != nil {
		return err
	}
	cfg.Logger, cfg.Version = log, version
	return server.Run(ctx, cfg.Config)
}

func adminPort(c server.Config) string {
	p, _, _ := c.Ports()
	return p
}

// adminPasswordFileEnv names a file holding the first admin user's password,
// for secret mounts (Docker, Kubernetes) where an environment variable would
// leak into inspect output.
const adminPasswordFileEnv = "MCPMESH_ADMIN_PASSWORD_FILE"

// resolveAdminPassword reads the password file, if one is configured. Giving
// the password both ways is an error rather than a silent precedence rule.
func resolveAdminPassword(cfg *config) error {
	path := os.Getenv(adminPasswordFileEnv)
	if path == "" {
		return nil
	}
	if cfg.AdminPassword != "" {
		return fmt.Errorf("both %s and --admin-password/MCPMESH_ADMIN_PASSWORD are set: use one", adminPasswordFileEnv)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", adminPasswordFileEnv, err)
	}
	cfg.AdminPassword = strings.TrimRight(string(data), "\r\n")
	return nil
}

// healthcheckCmd lets Docker check the hub without curl in the image.
func healthcheckCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit 0 if the hub's admin server answers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Over TLS the certificate is for the public name, not 127.0.0.1;
			// this only asks whether the process answers.
			c := http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			scheme := "http"
			if cfg.TLS() {
				scheme = "https"
			}
			resp, err := c.Get(scheme + "://127.0.0.1:" + adminPort(cfg.Config) + "/healthz")
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthz: %s", resp.Status)
			}
			return nil
		},
	}
}

func port(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return strings.TrimPrefix(addr, ":")
}

func newLogger(level, format string) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: lv}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, opts)), nil
	}
	return nil, fmt.Errorf("log format %q: want text or json", format)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
