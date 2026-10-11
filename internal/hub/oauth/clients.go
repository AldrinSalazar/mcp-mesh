package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const (
	maxClients     = 1000
	cimdMaxAge     = 24 * time.Hour
	fetchTimeout   = 5 * time.Second
	maxURILen      = 2048
	maxClientNames = 100
)

// client is a registered or fetched OAuth client.
type client struct {
	ID           string
	Name         string
	RedirectURIs []string
	ClientURI    string
	Fetched      time.Time
}

// bucketSet is a token bucket per key (an IP address), so the public
// registration endpoint can't be used to fill the database quickly.
type bucketSet struct {
	burst  float64
	refill time.Duration // time to earn one token
	now    func() time.Time

	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newBucketSet(burst int, refill time.Duration, now func() time.Time) *bucketSet {
	return &bucketSet{burst: float64(burst), refill: refill, now: now, m: map[string]*bucket{}}
}

func (b *bucketSet) allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if len(b.m) > 10000 {
		// Full buckets carry no information; drop the idle ones.
		for k, v := range b.m {
			if now.Sub(v.at) > time.Duration(b.burst)*b.refill {
				delete(b.m, k)
			}
		}
	}
	e := b.m[key]
	if e == nil {
		e = &bucket{tokens: b.burst, at: now}
		b.m[key] = e
	}
	e.tokens = min(b.burst, e.tokens+float64(now.Sub(e.at))/float64(b.refill))
	e.at = now
	if e.tokens < 1 {
		return false
	}
	e.tokens--
	return true
}

var loopbackHosts = []string{"127.0.0.1", "::1", "localhost"}

// checkRedirectURI accepts https, http on a loopback host, and private-use
// schemes in reverse-domain form (RFC 8252); anything else, fragments
// included, is refused.
func checkRedirectURI(raw string) error {
	if raw == "" || len(raw) > maxURILen {
		return errors.New("redirect URI is empty or too long")
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return errors.New("redirect URI contains whitespace or control characters")
	}
	if strings.Contains(raw, "#") {
		return errors.New("redirect URI must not have a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return errors.New("redirect URI is not an absolute URI")
	}
	switch u.Scheme {
	case "https":
		if u.Hostname() == "" || u.User != nil {
			return errors.New("https redirect URI needs a host and no credentials")
		}
	case "http":
		if !slices.Contains(loopbackHosts, u.Hostname()) || u.User != nil {
			return errors.New("http redirect URIs are only allowed on localhost, 127.0.0.1 and ::1")
		}
	default:
		// A dot keeps out javascript:, data:, file: and friends; apps
		// register the reverse of a domain they control.
		if !strings.Contains(u.Scheme, ".") {
			return fmt.Errorf("redirect URI scheme %q is not allowed", u.Scheme)
		}
	}
	return nil
}

// redirectMatches reports whether got is one of the client's redirect URIs:
// exactly, or for http loopback URIs with any port (RFC 8252 section 7.3,
// since native apps pick a free port at run time).
func redirectMatches(registered []string, got string) bool {
	if slices.Contains(registered, got) {
		return true
	}
	gu, err := url.Parse(got)
	if err != nil || gu.Scheme != "http" || !slices.Contains(loopbackHosts, gu.Hostname()) || checkRedirectURI(got) != nil {
		return false
	}
	for _, r := range registered {
		ru, err := url.Parse(r)
		if err == nil && ru.Scheme == "http" && ru.Hostname() == gu.Hostname() && ru.Path == gu.Path && ru.RawQuery == gu.RawQuery {
			return true
		}
	}
	return false
}

func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// httpsOrEmpty keeps a display URL only if it is a plain https URL.
func httpsOrEmpty(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(s) > maxURILen || strings.ContainsFunc(s, unicode.IsControl) {
		return ""
	}
	return s
}

// registration is the subset of RFC 7591 client metadata the hub reads.
type registration struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
	ClientURI    string   `json:"client_uri"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	GrantTypes   []string `json:"grant_types"`
	ResponseType []string `json:"response_types"`
}

// validate checks the parts both registration and metadata documents share.
func (m *registration) validate() error {
	if len(m.RedirectURIs) == 0 {
		return errors.New("redirect_uris is required")
	}
	if len(m.RedirectURIs) > 10 {
		return errors.New("at most 10 redirect_uris")
	}
	for _, u := range m.RedirectURIs {
		if err := checkRedirectURI(u); err != nil {
			return err
		}
	}
	if m.AuthMethod != "" && m.AuthMethod != "none" {
		return fmt.Errorf("token_endpoint_auth_method %q is not supported: only public clients (none)", m.AuthMethod)
	}
	for _, g := range m.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return fmt.Errorf("grant type %q is not supported", g)
		}
	}
	for _, g := range m.ResponseType {
		if g != "code" {
			return fmt.Errorf("response type %q is not supported", g)
		}
	}
	return nil
}

func registerError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registerLimit.allow(auth.ClientIP(r)) {
		w.Header().Set("Retry-After", "60")
		registerError(w, http.StatusTooManyRequests, "slow_down", "too many registrations; try again later")
		return
	}
	var m registration
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&m); err != nil {
		registerError(w, http.StatusBadRequest, "invalid_client_metadata", "request body must be a JSON object under 64 KiB")
		return
	}
	if err := m.validate(); err != nil {
		code := "invalid_client_metadata"
		if strings.Contains(err.Error(), "redirect") {
			code = "invalid_redirect_uri"
		}
		registerError(w, http.StatusBadRequest, code, err.Error())
		return
	}
	var n int
	if err := s.opts.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM oauth_clients").Scan(&n); err != nil {
		s.log.Error("counting oauth clients failed", "error", err)
		registerError(w, http.StatusInternalServerError, "server_error", "registration failed")
		return
	}
	if n >= maxClients {
		registerError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many registered clients")
		return
	}
	idPart, err := randomToken()
	if err != nil {
		registerError(w, http.StatusInternalServerError, "server_error", "registration failed")
		return
	}
	c := client{
		ID:           "oc_" + idPart[:22],
		Name:         cleanText(m.ClientName, maxClientNames),
		RedirectURIs: m.RedirectURIs,
		ClientURI:    httpsOrEmpty(m.ClientURI),
		Fetched:      s.now(),
	}
	if c.Name == "" {
		c.Name = "Unnamed client"
	}
	grants := m.GrantTypes
	if len(grants) == 0 {
		grants = []string{"authorization_code", "refresh_token"}
	}
	meta, _ := json.Marshal(map[string]any{
		"client_name": c.Name, "client_uri": c.ClientURI, "redirect_uris": c.RedirectURIs,
		"grant_types": grants, "token_endpoint_auth_method": "none",
	})
	if err := s.saveClient(r.Context(), c, meta); err != nil {
		s.log.Error("saving oauth client failed", "error", err)
		registerError(w, http.StatusInternalServerError, "server_error", "registration failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                grants,
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"client_id_issued_at":        c.Fetched.Unix(),
	})
}

// saveClient upserts: replacing the row would null keys.oauth_client.
func (s *Server) saveClient(ctx context.Context, c client, meta []byte) error {
	uris, _ := json.Marshal(c.RedirectURIs)
	_, err := s.opts.DB.ExecContext(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris, metadata, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, redirect_uris = excluded.redirect_uris,
			metadata = excluded.metadata, created_at = excluded.created_at`,
		c.ID, c.Name, string(uris), string(meta), store.Millis(c.Fetched))
	return err
}

var errUnknownClient = errors.New("unknown client")

// loadClient finds a registered client, or for a metadata document URL
// fetches (and caches) the document.
func (s *Server) loadClient(ctx context.Context, id string, ip string) (client, error) {
	if len(id) > maxURILen {
		return client{}, errUnknownClient
	}
	var (
		c       client
		uris    string
		meta    string
		created int64
	)
	err := s.opts.DB.QueryRowContext(ctx, "SELECT id, name, redirect_uris, metadata, created_at FROM oauth_clients WHERE id = ?", id).
		Scan(&c.ID, &c.Name, &uris, &meta, &created)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return client{}, fmt.Errorf("load client: %w", err)
	}
	if found {
		_ = json.Unmarshal([]byte(uris), &c.RedirectURIs)
		var m registration
		_ = json.Unmarshal([]byte(meta), &m)
		c.ClientURI = httpsOrEmpty(m.ClientURI)
		c.Fetched = store.FromMillis(created)
	}
	if !isMetadataURL(id) {
		if !found {
			return client{}, errUnknownClient
		}
		return c, nil
	}
	if found && s.now().Sub(c.Fetched) < cimdMaxAge {
		return c, nil
	}
	if !s.fetchLimit.allow(ip) {
		if found {
			return c, nil
		}
		return client{}, errors.New("too many client metadata lookups; try again later")
	}
	fresh, err := s.fetchMetadata(ctx, id)
	if err != nil {
		if found {
			s.log.Warn("refreshing client metadata failed, using the cached copy", "client", id, "error", err)
			return c, nil
		}
		return client{}, err
	}
	return fresh, nil
}

// isMetadataURL reports whether id looks like a client ID metadata document
// URL: https, a host, a path, no fragment, credentials or dot segments.
func isMetadataURL(id string) bool {
	u, err := url.Parse(id)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(id, "#") {
		return false
	}
	if u.Path == "" || u.Path == "/" {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (s *Server) fetchMetadata(ctx context.Context, id string) (client, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return client{}, err
	}
	req.Header.Set("Accept", "application/json")
	hc := *s.client
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return client{}, fmt.Errorf("fetch client metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return client{}, fmt.Errorf("fetch client metadata: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return client{}, fmt.Errorf("fetch client metadata: %w", err)
	}
	if len(body) > maxBody {
		return client{}, errors.New("client metadata document is larger than 64 KiB")
	}
	var doc struct {
		registration
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return client{}, fmt.Errorf("client metadata is not JSON: %w", err)
	}
	if doc.ClientID != id {
		return client{}, errors.New("client metadata client_id does not match its URL")
	}
	if err := doc.validate(); err != nil {
		return client{}, fmt.Errorf("client metadata: %w", err)
	}
	c := client{
		ID:           id,
		Name:         cleanText(doc.ClientName, maxClientNames),
		RedirectURIs: doc.RedirectURIs,
		ClientURI:    httpsOrEmpty(doc.ClientURI),
		Fetched:      s.now(),
	}
	if c.Name == "" {
		if u, err := url.Parse(id); err == nil {
			c.Name = u.Hostname()
		}
	}
	var n int
	if err := s.opts.DB.QueryRowContext(ctx, "SELECT count(*) FROM oauth_clients WHERE id <> ?", id).Scan(&n); err == nil && n >= maxClients {
		return client{}, errors.New("too many known clients")
	}
	if err := s.saveClient(ctx, c, body); err != nil {
		return client{}, fmt.Errorf("save client: %w", err)
	}
	return c, nil
}

// safeClient fetches with a 5 s timeout, HTTPS only through the URL check in
// fetchMetadata, and refuses to connect to private, loopback and link-local
// addresses. The check sits on the dialer, after name resolution, so a name
// that resolves into the LAN (or rebinds there) is refused too.
func safeClient() *http.Client {
	d := &net.Dialer{
		Timeout: fetchTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("refusing to connect to %q", host)
			}
			if blockedIP(ip) {
				return fmt.Errorf("refusing to connect to %s: not a public address", ip)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:               nil, // a proxy would resolve and connect for us, past the check
			DialContext:         d.DialContext,
			TLSHandshakeTimeout: fetchTimeout,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var (
	cgnat    = netip.MustParsePrefix("100.64.0.0/10")
	thisNet  = netip.MustParsePrefix("0.0.0.0/8")
	benchNet = netip.MustParsePrefix("198.18.0.0/15")
)

func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip) || thisNet.Contains(ip) || benchNet.Contains(ip)
}
