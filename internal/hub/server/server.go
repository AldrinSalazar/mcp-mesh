// Package server assembles the hub: catalog, node registry, gateway, admin
// UI and auth, behind its three listeners (or one, with Config.Listen). cmd/mcp-mesh-hub only turns flags
// into a Config; new hub features are wired here.
package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/api"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/dashboard"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/gateway"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/oauth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/overrides"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/progress"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/trust"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/web"
)

// Config is everything the hub needs to run.
type Config struct {
	AdminListen  string
	NodeListen   string
	ClientListen string
	// Listen, if set, serves the dashboard, /node and /mcp on this one
	// address instead of the three above (for a reverse proxy).
	Listen string
	// PublicHost, NodeURL and ClientURL are what nodes and clients use to
	// reach the hub, for the dashboard's and CLI's snippets.
	PublicHost  string
	NodeURL     string
	ClientURL   string
	CallTimeout time.Duration
	MDNS        bool
	Name        string // announced over mDNS; empty means the host name

	DataDir       string // hub.db lives here
	Auth          bool
	AllowInsecure bool
	// TrustedProxies are reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto are believed (client address and HTTPS).
	TrustedProxies []netip.Prefix
	TLSCert        string
	TLSKey         string

	// AdminUser and AdminPassword create the first admin user when the
	// database has none (auth on). AdminUser defaults to "admin"; an empty
	// AdminPassword makes the hub generate one and log it once.
	AdminUser     string
	AdminPassword string

	Version string
	Logger  *slog.Logger // nil means slog.Default()
	// OnListen, if set, receives the bound admin, node and client addresses
	// once the hub is listening (tests use port 0).
	OnListen func(admin, node, client string)
}

func (c Config) TLS() bool { return c.TLSCert != "" }

// Ports returns the configured admin, node and client ports.
func (c Config) Ports() (admin, node, client string) {
	if c.Listen != "" {
		p := Port(c.Listen)
		return p, p, p
	}
	return Port(c.AdminListen), Port(c.NodeListen), Port(c.ClientListen)
}

// URLs returns the admin, node and client URLs that people and nodes should
// use, unless the flags override them.
func (c Config) URLs(adminPort, nodePort, clientPort string) (adminURL, nodeURL, clientURL string) {
	web, ws := "http", "ws"
	if c.TLS() {
		web, ws = "https", "wss"
	}
	adminURL = fmt.Sprintf("%s://%s:%s/", web, c.PublicHost, adminPort)
	nodeURL, clientURL = c.NodeURL, c.ClientURL
	if nodeURL == "" {
		nodeURL = fmt.Sprintf("%s://%s:%s%s", ws, c.PublicHost, nodePort, proto.NodePath)
	}
	if clientURL == "" {
		clientURL = fmt.Sprintf("%s://%s:%s/mcp", web, c.PublicHost, clientPort)
	}
	return adminURL, nodeURL, clientURL
}

// oauthUnreachable says why OAuth clients would refuse clientURL as the
// authorization server's address, or "". The MCP SDKs require HTTPS (except
// on loopback) and refuse a private IP address to guard against SSRF, so an
// app can't sign in to a hub addressed as https://192.168.1.10.
func oauthUnreachable(clientURL string) string {
	u, err := url.Parse(clientURL)
	if err != nil {
		return "it doesn't parse"
	}
	host := u.Hostname()
	ip, err := netip.ParseAddr(host)
	loopback := host == "localhost" || (err == nil && ip.IsLoopback())
	switch {
	case loopback:
		return ""
	case u.Scheme != "https":
		return "it isn't HTTPS (serve TLS or put an HTTPS proxy in front, and set --client-url)"
	case err == nil:
		return "it is an IP address; OAuth clients only accept a host name (set --public-host or --client-url to a name)"
	}
	return ""
}

func (c Config) DBPath() string { return filepath.Join(c.DataDir, store.FileName) }

// Port returns the port of a listen address such as ":7000".
func Port(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return strings.TrimPrefix(addr, ":")
}

// Run serves the hub until ctx ends: the admin, node and client listeners,
// with every component wired together. It returns when all have stopped.
func Run(ctx context.Context, cfg Config) error {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return errors.New("--tls-cert and --tls-key must be given together")
	}
	if cfg.TLS() {
		if _, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey); err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
	}
	if cfg.DataDir == "" {
		return errors.New("no data directory (--data-dir)")
	}
	// Opening is quick and local; a cancelled ctx still means a clean stop
	// below rather than an open error.
	db, err := store.Open(context.WithoutCancel(ctx), cfg.DBPath(), log.With("component", "store"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	var keys *auth.Store
	if cfg.Auth {
		keys, err = auth.Open(ctx, db, log.With("component", "auth"))
		if err != nil {
			return err
		}
	} else {
		log.Warn("authentication is off (--auth=false): anyone who can reach the hub's ports can use every MCP on every node")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listeners := make([]net.Listener, 0, 3)
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	addrs := []string{cfg.AdminListen, cfg.NodeListen, cfg.ClientListen}
	if cfg.Listen != "" {
		addrs = []string{cfg.Listen}
	}
	for _, addr := range addrs {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", addr, err)
		}
		listeners = append(listeners, listener)
	}
	// bound is the listener serving role 0 (admin), 1 (node) or 2 (client).
	bound := func(role int) net.Listener { return listeners[min(role, len(listeners)-1)] }
	port := func(role int) int { return bound(role).Addr().(*net.TCPAddr).Port }
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	discoveryName := ""
	if cfg.MDNS {
		if err := discovery.Announce(ctx, discovery.Announcement{
			Name: cfg.Name, NodePort: port(1),
			NodePath: proto.NodePath, Version: proto.Version,
			AdminPort:  port(0),
			ClientPort: port(2),
			Logger:     log.With("component", "discovery"),
		}); err != nil {
			log.Warn("local network announcement failed", "error", err)
		} else {
			discoveryName = cfg.Name
			log.Info("announced on the local network", "name", cfg.Name)
		}
	}
	adminURL, nodeURL, clientURL := cfg.URLs(strconv.Itoa(port(0)), strconv.Itoa(port(1)), strconv.Itoa(port(2)))
	cfg.NodeURL, cfg.ClientURL = nodeURL, clientURL
	if err := db.SetSettings(context.WithoutCancel(ctx), map[string]string{
		store.SettingAdminURL: adminURL, store.SettingNodeURL: nodeURL, store.SettingClientURL: clientURL,
	}); err != nil {
		log.Warn("advertised URLs not recorded; the hub's CLI prints its own defaults", "error", err)
	}
	if cfg.Auth {
		if why := oauthUnreachable(clientURL); why != "" {
			log.Info("apps that sign in with OAuth can't use this hub's client URL; keys still work", "client_url", clientURL, "reason", why)
		}
	}
	if cfg.OnListen != nil {
		cfg.OnListen(bound(0).Addr().String(), bound(1).Addr().String(), bound(2).Addr().String())
	}

	cat := catalog.New()
	// gw is set below; groups and trust changes resync client tool lists
	// through it once the goroutines that call these start.
	var gw *gateway.Gateway
	refresh := func() {
		if gw != nil {
			gw.Refresh()
		}
	}
	tr, err := trust.Open(context.WithoutCancel(ctx), db, trust.Options{Catalog: cat, Logger: log.With("component", "trust"), OnChange: refresh})
	if err != nil {
		return err
	}
	var groups *acl.Store
	var oauthSrv *oauth.Server // nil with auth off
	if cfg.Auth {
		if groups, err = acl.Open(context.WithoutCancel(ctx), db, acl.Options{Logger: log.With("component", "acl"), OnChange: refresh}); err != nil {
			return err
		}
	}
	toolOverrides, err := overrides.Open(context.WithoutCancel(ctx), db, overrides.Options{Catalog: cat, Logger: log.With("component", "overrides")})
	if err != nil {
		return err
	}
	calls := activity.New(200)
	audit := activity.NewAudit(db, activity.AuditOptions{Logger: log.With("component", "audit")})
	// One router: the gateway registers a token per client call, the node
	// registry's MCP clients dispatch the notifications that carry it.
	progressRouter := progress.New()
	registryOpts := nodes.Options{Catalog: cat, Logger: log.With("component", "nodes"), Progress: progressRouter, Version: cfg.Version}
	gatewayOpts := gateway.Options{
		Catalog:     cat,
		Activity:    activity.Multi(calls, audit),
		Logger:      log.With("component", "gateway"),
		CallTimeout: cfg.CallTimeout,
		Version:     cfg.Version,
		// A key the CLI made a moment ago is in the Everything group in the
		// database before the group watch has seen it.
		NewClient: func(ctx context.Context, _ identity.Client) {
			if groups == nil {
				return
			}
			if err := groups.Sync(ctx); err != nil {
				log.Warn("groups not reloaded for a new client; the next database watch retries", "error", err)
			}
		},
		// A client sees a tool when it is approved and, with auth on, its
		// groups grant it. Calls re-check, so a hidden tool is never callable.
		Visible: func(c identity.Client, t *catalog.Tool) bool {
			if !tr.Visible(t) {
				return false
			}
			return groups == nil || (t.Upstream != nil && groups.Allows(c.ID, t.Node, t.MCP, t.Upstream.Name))
		},
		Progress: progressRouter,
		// Prompts and resources are granted per MCP: the node must be
		// approved and, with auth on, a group grants the MCP's tool "*".
		// A grant of single tools doesn't expose an MCP's prompts or
		// resources, which the tool list can't show the model first.
		VisibleMCP: func(c identity.Client, node, mcp string) bool {
			return tr.NodeVisible(node) && (groups == nil || groups.Allows(c.ID, node, mcp, "*"))
		},
	}
	apiOpts := api.Options{
		Catalog:       cat,
		Recent:        calls,
		Audit:         audit,
		Version:       cfg.Version,
		NodeURL:       cfg.NodeURL,
		ClientURL:     cfg.ClientURL,
		DiscoveryName: discoveryName,
		Logger:        log.With("component", "api"),
		Trust:         tr,
		Overrides:     toolOverrides,
	}
	if cfg.Auth {
		registryOpts.Authorize = auth.AuthorizeNode
		registryOpts.KeyID = func(r *http.Request) string {
			k, _ := auth.KeyFrom(r.Context())
			return k.ID
		}
		gatewayOpts.Identify = auth.ClientIdentity
		users, err := auth.OpenUsers(ctx, db, log.With("component", "auth"))
		if err != nil {
			return err
		}
		if err := bootstrapAdmin(ctx, users, cfg, adminURL, log); err != nil {
			return err
		}
		sessions := auth.NewSessions(db, users, auth.SessionOptions{Logger: log.With("component", "auth")})
		// Assigned only here so the interfaces stay nil with auth off.
		apiOpts.Sessions, apiOpts.Admins, apiOpts.Keys = sessions, users, keys
		apiOpts.Groups = groups
		alog := adminlog.New(db, log.With("component", "adminlog"), 0)
		apiOpts.AdminLog = alog
		// The hub is the OAuth authorization server for its own MCP
		// endpoint; its issuer is the client URL's origin.
		u, err := url.Parse(clientURL)
		if err != nil {
			return fmt.Errorf("client URL %q: %w", clientURL, err)
		}
		oauthSrv = oauth.New(oauth.Options{
			DB: db, Keys: keys, Sessions: sessions, Groups: groups, Log: alog,
			Logger: log.With("component", "oauth"),
			Issuer: u.Scheme + "://" + u.Host, Resource: clientURL, HubName: cfg.Name,
		})
		apiOpts.OAuthIssuer = u.Scheme + "://" + u.Host
		apiOpts.OAuthName = func(id string) string {
			var name string
			if err := db.QueryRow("SELECT name FROM oauth_clients WHERE id = ?", id).Scan(&name); err != nil {
				return ""
			}
			return name
		}
		if err := logKeyHint(keys, cfg, log); err != nil {
			return err
		}
	}
	registry := nodes.NewRegistry(registryOpts)
	gw = gateway.New(gatewayOpts)
	apiOpts.Nodes = registry
	apiOpts.Health = func(ctx context.Context) api.Health {
		return health(ctx, db, keys, audit, registry, gw)
	}
	apiServer := api.New(apiOpts)

	// Every listener refuses plain HTTP from public addresses; /healthz is
	// exempt because the Docker healthcheck uses it (over loopback, which
	// passes anyway).
	var nodeHandler http.Handler = registry
	clientHandler := gw.Handler()
	if cfg.Auth {
		nodeHandler = auth.RequireKey(keys, auth.KindNode, nodeHandler)
		// A client key or an OAuth access token.
		clientHandler = oauthSrv.RequireClient(clientHandler)
	}
	nodeMux := http.NewServeMux()
	nodeMux.Handle("GET "+proto.NodePath, auth.LocalOrTLS(cfg.AllowInsecure, nodeHandler))
	clientMux := http.NewServeMux()
	clientMux.Handle("/mcp", auth.LocalOrTLS(cfg.AllowInsecure, clientHandler))
	if oauthSrv != nil {
		// Discovery documents and the OAuth endpoints live next to /mcp,
		// where an app that only knows the MCP URL looks for them.
		oauthHandler := auth.LocalOrTLS(cfg.AllowInsecure, oauthSrv.Handler())
		clientMux.Handle("/.well-known/", oauthHandler)
		clientMux.Handle("/oauth/", oauthHandler)
	}
	adminMux := http.NewServeMux()
	adminMux.Handle("/", auth.LocalOrTLS(cfg.AllowInsecure, dashboard.Handler(web.Dist())))
	adminMux.Handle("/api/v1/", auth.LocalOrTLS(cfg.AllowInsecure, apiServer.Handler()))
	adminMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	// Readiness names what is wrong (paths, errors), so unlike /healthz it
	// keeps the plain-HTTP rule.
	adminMux.Handle("GET /readyz", auth.LocalOrTLS(cfg.AllowInsecure, http.HandlerFunc(apiServer.Ready)))

	g, ctx := errgroup.WithContext(ctx)
	// Shutdown order: the gateway closes its sessions and the listeners
	// finish their requests, and only then does the audit writer drain and
	// stop, so calls cancelled by the shutdown still record their outcome.
	var stopping sync.WaitGroup // the gateway and each listener's shutdown
	auditCtx, stopAudit := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAudit()
	stopping.Add(1)
	g.Go(func() error { defer stopping.Done(); gw.Run(ctx); return nil })
	g.Go(func() error { audit.Run(auditCtx); return nil })
	g.Go(func() error { apiServer.Run(ctx); return nil })
	g.Go(func() error { tr.Run(ctx); return nil })
	g.Go(func() error { toolOverrides.Watch(ctx, time.Second); return nil })
	if groups != nil {
		g.Go(func() error { groups.Watch(ctx, time.Second); return nil })
	}
	if oauthSrv != nil {
		g.Go(func() error { oauthSrv.Run(ctx); return nil })
	}
	if cfg.Auth {
		g.Go(func() error {
			// Revocation is per key: only what authenticated with it closes.
			keys.Watch(ctx, time.Second, func(k auth.Key) {
				switch k.Kind {
				case auth.KindNode:
					log.Info("node key revoked", "key", k.ID, "node", k.Name, "disconnected", registry.DisconnectKey(k.ID))
				case auth.KindClient:
					log.Info("client key revoked", "key", k.ID, "name", k.Name, "disconnected", gw.DisconnectClient(k.ID),
						"per_client_sessions_closed", registry.CloseClient(k.ID))
				default:
					log.Info("key revoked", "key", k.ID, "kind", k.Kind, "name", k.Name)
				}
			})
			return nil
		})
	}
	type service struct {
		name string
		h    http.Handler
	}
	services := []service{{"admin", adminMux}, {"node", nodeMux}, {"client", clientMux}}
	if cfg.Listen != "" {
		// Path routing; the three muxes don't overlap.
		hubMux := http.NewServeMux()
		hubMux.Handle("/", adminMux)
		hubMux.Handle("GET "+proto.NodePath, nodeMux)
		hubMux.Handle("/mcp", clientMux)
		hubMux.Handle("/.well-known/", clientMux)
		hubMux.Handle("/oauth/", clientMux)
		services = []service{{"hub", hubMux}}
	}
	stopping.Add(len(services))
	for i, s := range services {
		listener := listeners[i]
		srv := &http.Server{
			// Outermost, so every check below sees the client's address.
			Handler:           auth.TrustProxies(cfg.TrustedProxies, s.h),
			ReadHeaderTimeout: 10 * time.Second,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
		g.Go(func() error {
			log.Info("listening", "server", s.name, "addr", listener.Addr().String(), "tls", cfg.TLS())
			var err error
			if cfg.TLS() {
				err = srv.ServeTLS(listener, cfg.TLSCert, cfg.TLSKey)
			} else {
				err = srv.Serve(listener)
			}
			if !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("%s server: %w", s.name, err)
			}
			return nil
		})
		g.Go(func() error {
			defer stopping.Done()
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(sctx); err != nil {
				// A client that keeps a request (or a fresh, still silent
				// connection) open past the grace period shouldn't turn a
				// clean stop into a failure.
				log.Warn("connections still open after 5s; closing them", "server", s.name, "error", err)
				srv.Close()
			}
			return nil
		})
	}
	g.Go(func() error {
		<-ctx.Done()
		stopping.Wait()
		stopAudit()
		return nil
	})
	log.Info("mcp-mesh-hub started", "version", cfg.Version, "node_url", cfg.NodeURL, "client_url", cfg.ClientURL, "auth", cfg.Auth)
	return g.Wait()
}

// auditLossWindow is how recently the audit log may have lost calls before
// the hub counts as not ready.
const auditLossWindow = time.Minute

// health collects readiness problems and metrics from the hub's parts.
func health(ctx context.Context, db *store.DB, keys *auth.Store, audit *activity.Audit, registry *nodes.Registry, gw *gateway.Gateway) api.Health {
	h := api.Health{Metrics: map[string]any{}}
	if err := db.CheckWritable(ctx); err != nil {
		h.Problems = append(h.Problems, err.Error())
	}
	failing, err := db.FailingWatchers()
	if failing > 0 {
		h.Problems = append(h.Problems, fmt.Sprintf("%d database watchers failing (changes from other processes may be missed): %v", failing, err))
	}
	if keys != nil {
		// The key watcher's database watch only nudges; its reloads are
		// where a failure shows.
		if since, err := keys.ReloadError(); err != nil {
			failing++
			h.Problems = append(h.Problems, fmt.Sprintf("keys not reloaded for %s (revocations by other processes don't reach live connections): %v", time.Since(since).Round(time.Second), err))
		}
	}
	h.Metrics["watchers_failing"] = failing
	lost, at := audit.Lost()
	h.Metrics["audit_calls_lost"] = lost
	if !at.IsZero() && time.Since(at) < auditLossWindow {
		h.Problems = append(h.Problems, fmt.Sprintf("audit log lost calls %s ago (%d since start)", time.Since(at).Round(time.Second), lost))
	}
	for k, v := range metricsOf(registry.Stats()) {
		h.Metrics[k] = v
	}
	for k, v := range metricsOf(gw.Stats()) {
		h.Metrics["client_"+k] = v
	}
	return h
}

// metricsOf flattens a stats struct into its JSON names.
func metricsOf(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// bootstrapAdmin creates the first admin user when there is none, so a fresh
// hub can be signed in to. Without a configured password it makes one up and
// logs it, once: it is stored only as a hash.
func bootstrapAdmin(ctx context.Context, users *auth.Users, cfg Config, adminURL string, log *slog.Logger) error {
	n, err := users.Count(ctx)
	if err != nil || n > 0 {
		return err
	}
	name, password := cfg.AdminUser, cfg.AdminPassword
	if name == "" {
		name = "admin"
	}
	generated := password == ""
	if generated {
		if password, err = randomPassword(24); err != nil {
			return err
		}
	}
	if _, err := users.Create(ctx, name, password); err != nil {
		return fmt.Errorf("create first admin user %q: %w", name, err)
	}
	if generated {
		log.Warn(fmt.Sprintf("first admin user %q created; sign in at %s with password: %s; it is not shown again; change it with mcp-mesh-hub admin passwd %s", name, adminURL, password, name))
	} else {
		log.Info("first admin user created", "user", name, "url", adminURL)
	}
	return nil
}

const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// randomPassword returns n random characters, without look-alikes (0/O, 1/l/I).
func randomPassword(n int) (string, error) {
	out := make([]byte, n)
	limit := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("random password: %w", err)
		}
		out[i] = passwordAlphabet[v.Int64()]
	}
	return string(out), nil
}

// logKeyHint points at the key commands when nodes and clients have nothing
// to connect with yet.
func logKeyHint(keys *auth.Store, cfg Config, log *slog.Logger) error {
	has := map[auth.Kind]bool{}
	list, err := keys.List()
	if err != nil {
		return err
	}
	for _, k := range list {
		has[k.Kind] = true
	}
	if !has[auth.KindNode] || !has[auth.KindClient] {
		log.Info("no node or client keys yet; create them with: mcp-mesh-hub key create node <node-name>, mcp-mesh-hub key create client <label>", "data_dir", cfg.DataDir)
	}
	return nil
}
