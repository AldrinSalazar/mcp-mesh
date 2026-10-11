package oauth

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

//go:embed assets/authorize.html assets/authorize.css assets/fonts
var assets embed.FS

var pageTemplate = template.Must(template.ParseFS(assets, "assets/authorize.html"))

func (s *Server) handleCSS(w http.ResponseWriter, r *http.Request) {
	b, _ := assets.ReadFile("assets/authorize.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

// handleFont serves the page's fonts (assets/fonts).
func (s *Server) handleFont(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	b, err := assets.ReadFile("assets/fonts/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

// authReq is a validated authorization request.
type authReq struct {
	client      client
	redirectURI string
	state       string
	challenge   string
	resource    string
	scope       string
}

// fields are the request parameters carried through the sign-in and consent
// forms as hidden inputs; the POST validates them again.
func (a *authReq) fields() []field {
	f := []field{
		{"client_id", a.client.ID},
		{"redirect_uri", a.redirectURI},
		{"response_type", "code"},
		{"code_challenge", a.challenge},
		{"code_challenge_method", "S256"},
	}
	if a.state != "" {
		f = append(f, field{"state", a.state})
	}
	if a.resource != "" {
		f = append(f, field{"resource", a.resource})
	}
	if a.scope != "" {
		f = append(f, field{"scope", a.scope})
	}
	return f
}

type field struct{ Name, Value string }

type groupView struct {
	ID, Name, Description string
	Checked               bool
}

// keyView is a key the app already has, offered for reuse.
type keyView struct {
	ID, Name, Used string
	Checked        bool
}

type pageData struct {
	Mode  string // consent, signin, error, bounce
	Title string

	HubName       string
	CSSPath       string
	Action        string
	ContinueURL   string
	Error         string
	Warn          bool
	ClientName    string
	ClientURI     string
	RedirectLabel string
	Username      string
	CSRF          string
	KeyName       string
	Fields        []field
	Groups        []groupView
	Existing      []keyView
}

func (s *Server) render(w http.ResponseWriter, status int, d pageData, redirectURI string) {
	d.HubName, d.CSSPath, d.Action = s.opts.HubName, pathAssetsCSS, PathAuthorize
	if d.Title == "" {
		d.Title = "Authorize"
	}
	var buf bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&buf, "page", d); err != nil {
		s.log.Error("rendering authorize page failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'"+formActionSource(redirectURI))
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	// Not no-referrer: under it browsers send "Origin: null" with the form's
	// POST, which sameOrigin rejects. same-origin still keeps this URL from
	// other sites.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// formActionSource lets the form's 303 answer reach the client: Chromium
// checks form-action against redirects of a form submission too. A private
// scheme has no origin, so the scheme alone is the source.
func formActionSource(redirectURI string) string {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme == "" {
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return " " + u.Scheme + "://" + u.Host
	}
	return " " + u.Scheme + ":"
}

func (s *Server) errorPage(w http.ResponseWriter, status int, msg string) {
	s.render(w, status, pageData{Mode: "error", Title: "Error", Error: msg}, "")
}

// redirectLabel is what the page shows as the place the user ends up.
func redirectLabel(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.Host
	}
	return u.Scheme + ": (an app on this device)"
}

type oauthErr struct {
	Code, Desc string
	Status     int
}

func (e *oauthErr) Error() string { return e.Code + ": " + e.Desc }

func validVerifierLike(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}

// parseAuth validates an authorization request. A problem that makes the
// redirect untrustworthy (client, redirect URI) comes back as pageErr and
// must be shown, not redirected; anything else as an *oauthErr with ar set,
// to redirect back to the client.
func (s *Server) parseAuth(r *http.Request, v url.Values) (ar *authReq, pageErr string, oerr *oauthErr) {
	id := v.Get("client_id")
	if id == "" {
		return nil, "The request has no client_id.", nil
	}
	c, err := s.loadClient(r.Context(), id, auth.ClientIP(r))
	if err != nil {
		if errors.Is(err, errUnknownClient) {
			return nil, "The app is not registered with this hub.", nil
		}
		s.log.Info("client lookup failed", "client", id, "error", err)
		return nil, "The app could not be verified: " + err.Error() + ".", nil
	}
	redirect := v.Get("redirect_uri")
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	if !redirectMatches(c.RedirectURIs, redirect) {
		return nil, "The redirect address does not match the one the app registered.", nil
	}
	ar = &authReq{
		client: c, redirectURI: redirect, state: v.Get("state"), challenge: v.Get("code_challenge"),
		resource: v.Get("resource"), scope: v.Get("scope"),
	}
	if len(ar.state) > 1024 || len(ar.scope) > 1024 {
		return ar, "", &oauthErr{Code: "invalid_request", Desc: "state or scope is too long"}
	}
	switch {
	case v.Get("response_type") != "code":
		return ar, "", &oauthErr{Code: "unsupported_response_type", Desc: "only response_type=code is supported"}
	case ar.challenge == "" || v.Get("code_challenge_method") != "S256":
		return ar, "", &oauthErr{Code: "invalid_request", Desc: "PKCE is required: send code_challenge with code_challenge_method=S256"}
	case len(ar.challenge) != 43 || !validVerifierLike(ar.challenge):
		return ar, "", &oauthErr{Code: "invalid_request", Desc: "malformed code_challenge"}
	case ar.resource != "" && ar.resource != s.opts.Resource:
		return ar, "", &oauthErr{Code: "invalid_target", Desc: "resource must be " + s.opts.Resource}
	}
	return ar, "", nil
}

// redirectBack sends the user agent to the client's redirect URI.
func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, status int, ar *authReq, q url.Values) {
	u, err := url.Parse(ar.redirectURI)
	if err != nil {
		s.errorPage(w, http.StatusBadRequest, "The redirect address is not valid.")
		return
	}
	if ar.state != "" {
		q.Set("state", ar.state)
	}
	q.Set("iss", s.opts.Issuer)
	existing := u.Query()
	for k, vs := range q {
		existing[k] = vs
	}
	u.RawQuery = existing.Encode()
	http.Redirect(w, r, u.String(), status)
}

func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, status int, ar *authReq, e *oauthErr) {
	s.redirectBack(w, r, status, ar, url.Values{"error": {e.Code}, "error_description": {e.Desc}})
}

func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func (s *Server) handleAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sess, signedIn := s.opts.Sessions.Current(r)
	// The dashboard cookie is SameSite=Strict, so a navigation from another
	// site (claude.ai) arrives without it. One bounce through a page of our
	// own makes the next navigation same-site; _s marks that it happened.
	if !signedIn && q.Get("_s") == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		q.Set("_s", "1")
		s.render(w, http.StatusOK, pageData{Mode: "bounce", Title: "Continuing", ContinueURL: s.opts.Issuer + PathAuthorize + "?" + q.Encode()}, "")
		return
	}
	ar, pageErr, oerr := s.parseAuth(r, q)
	switch {
	case pageErr != "":
		s.errorPage(w, http.StatusBadRequest, pageErr)
	case oerr != nil:
		s.redirectError(w, r, http.StatusFound, ar, oerr)
	case signedIn:
		s.consentPage(w, r, ar, sess, "")
	default:
		s.signInPage(w, ar, "", "", false, http.StatusOK)
	}
}

func (s *Server) signInPage(w http.ResponseWriter, ar *authReq, username, msg string, warn bool, status int) {
	s.render(w, status, pageData{
		Mode: "signin", Title: "Sign in", ClientName: ar.client.Name, ClientURI: ar.client.ClientURI,
		RedirectLabel: redirectLabel(ar.redirectURI), Fields: ar.fields(), Username: username, Error: msg, Warn: warn,
	}, ar.redirectURI)
}

func (s *Server) consentPage(w http.ResponseWriter, r *http.Request, ar *authReq, sess auth.Session, keyName string) {
	groups, err := s.opts.Groups.List(r.Context())
	if err != nil {
		s.log.Error("listing groups failed", "error", err)
		s.errorPage(w, http.StatusInternalServerError, "Could not load the groups.")
		return
	}
	views := make([]groupView, len(groups))
	for i, g := range groups {
		views[i] = groupView{ID: g.ID, Name: g.Name, Description: g.Description, Checked: g.ID == acl.Everything}
	}
	if keyName == "" {
		keyName = slugify(ar.client.Name)
	}
	existing, err := s.clientKeys(ar.client.ID)
	if err != nil {
		s.log.Error("listing keys failed", "error", err)
		s.errorPage(w, http.StatusInternalServerError, "Could not load the keys.")
		return
	}
	var offered []keyView
	for i, k := range existing {
		used := "never used"
		if !k.LastUsed.IsZero() {
			used = "last used " + k.LastUsed.Format("2 Jan 2006")
		}
		// The most recently used key is the default: re-authorizing an app
		// usually means its tokens ran out, not that it is a new install.
		offered = append(offered, keyView{ID: k.ID, Name: k.Name, Used: used, Checked: i == 0})
	}
	s.render(w, http.StatusOK, pageData{
		Mode: "consent", Title: "Authorize " + ar.client.Name, ClientName: ar.client.Name, ClientURI: ar.client.ClientURI,
		RedirectLabel: redirectLabel(ar.redirectURI), Fields: ar.fields(), Username: sess.Username,
		CSRF: sess.CSRF, KeyName: keyName, Groups: views, Existing: offered,
	}, ar.redirectURI)
}

// clientKeys returns the client keys made for this OAuth client, most
// recently used (or created) first.
func (s *Server) clientKeys(clientID string) ([]auth.Key, error) {
	all, err := s.opts.Keys.List()
	if err != nil {
		return nil, err
	}
	var keys []auth.Key
	for _, k := range all {
		if k.Kind == auth.KindClient && k.OAuthClient == clientID {
			keys = append(keys, k)
		}
	}
	latest := func(k auth.Key) time.Time {
		if k.LastUsed.After(k.Created) {
			return k.LastUsed
		}
		return k.Created
	}
	slices.SortStableFunc(keys, func(a, b auth.Key) int { return latest(b).Compare(latest(a)) })
	return keys, nil
}

// sameOrigin mirrors the check in internal/hub/api/api.go (kept separate so
// this package doesn't import the API): no Origin header is allowed
// (non-browser clients), otherwise its host must be the request's host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) handleAuthorizePost(w http.ResponseWriter, r *http.Request) {
	// Sign-in has no session to carry a CSRF token, so without this a foreign
	// page could post credentials and switch the victim's browser into
	// another account (login CSRF).
	if !sameOrigin(r) {
		s.errorPage(w, http.StatusForbidden, "Cross-origin request rejected.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	ar, pageErr, oerr := s.parseAuth(r, r.PostForm)
	if pageErr != "" {
		s.errorPage(w, http.StatusBadRequest, pageErr)
		return
	}
	if oerr != nil {
		s.redirectError(w, r, http.StatusSeeOther, ar, oerr)
		return
	}
	ip := auth.ClientIP(r)
	action := r.PostForm.Get("action")

	if action == "signin" {
		username := r.PostForm.Get("username")
		sess, err := s.opts.Sessions.Login(w, r, strings.TrimSpace(username), r.PostForm.Get("password"))
		var rl *auth.RateLimitError
		switch {
		case errors.As(err, &rl):
			s.signInPage(w, ar, username, fmt.Sprintf("Too many failed sign-ins; try again in %s", rl.RetryAfter.Round(time.Second)), true, http.StatusTooManyRequests)
		case errors.Is(err, auth.ErrBadCredentials):
			s.alog.Record(r.Context(), adminlog.Entry{Action: adminlog.SignInFailed, Target: username, IP: ip})
			s.signInPage(w, ar, username, "Wrong username or password.", false, http.StatusUnauthorized)
		case err != nil:
			s.log.Error("sign-in failed", "error", err)
			s.signInPage(w, ar, username, "Sign-in failed. Try again.", false, http.StatusInternalServerError)
		default:
			s.alog.Record(r.Context(), adminlog.Entry{Actor: sess.Username, Action: adminlog.SignIn, Target: sess.Username, IP: ip})
			s.consentPage(w, r, ar, sess, "")
		}
		return
	}

	sess, ok := s.opts.Sessions.Current(r)
	if !ok {
		s.signInPage(w, ar, "", "Your session ended. Sign in again.", true, http.StatusUnauthorized)
		return
	}
	if !s.opts.Sessions.CheckCSRF(r, sess) {
		s.errorPage(w, http.StatusForbidden, "The form is out of date. Go back to the app and start again.")
		return
	}
	switch action {
	case "deny":
		s.alog.Record(r.Context(), adminlog.Entry{Actor: sess.Username, Action: adminlog.OAuthDenied, Target: ar.client.Name, IP: ip})
		s.redirectError(w, r, http.StatusSeeOther, ar, &oauthErr{Code: "access_denied", Desc: "the user denied the request"})
	case "allow":
		s.allow(w, r, ar, sess)
	default:
		s.errorPage(w, http.StatusBadRequest, "Unknown action.")
	}
}

func (s *Server) allow(w http.ResponseWriter, r *http.Request, ar *authReq, sess auth.Session) {
	ctx := r.Context()
	groups, err := s.opts.Groups.List(ctx)
	if err != nil {
		s.log.Error("listing groups failed", "error", err)
		s.errorPage(w, http.StatusInternalServerError, "Could not load the groups.")
		return
	}
	known := map[string]bool{}
	for _, g := range groups {
		known[g.ID] = true
	}
	var chosen []string
	for _, id := range r.PostForm["group"] {
		if known[id] && !slices.Contains(chosen, id) {
			chosen = append(chosen, id)
		}
	}
	// The key is made (or reused) when the code is exchanged, so a consent
	// whose code is never redeemed leaves nothing behind.
	var keyID any // NULL = make one
	if choice := r.PostForm.Get("key"); choice != "" && choice != "new" {
		owned, err := s.clientKeys(ar.client.ID)
		if err != nil {
			s.log.Error("listing keys failed", "error", err)
			s.errorPage(w, http.StatusInternalServerError, "Could not load the keys.")
			return
		}
		if !slices.ContainsFunc(owned, func(k auth.Key) bool { return k.ID == choice }) {
			s.errorPage(w, http.StatusBadRequest, "That key no longer exists or belongs to another app. Go back to the app and start again.")
			return
		}
		keyID = choice
	}
	name := slugify(r.PostForm.Get("key_name"))
	if name == "" {
		name = slugify(ar.client.Name)
	}
	if name == "" {
		name = "oauth-client"
	}
	if chosen == nil {
		chosen = []string{}
	}
	groupsJSON, err := json.Marshal(chosen)
	code, cerr := randomToken()
	if err == nil {
		err = cerr
	}
	if err == nil {
		_, err = s.opts.DB.ExecContext(ctx, `INSERT INTO oauth_codes
			(hash, client_id, redirect_uri, code_challenge, resource, expires_at, key_id, key_name, groups, approved_by, approved_ip)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			auth.Hash(code), ar.client.ID, ar.redirectURI, ar.challenge, ar.resource, store.Millis(s.now().Add(s.opts.CodeTTL)),
			keyID, name, string(groupsJSON), sess.Username, auth.ClientIP(r))
	}
	if err != nil {
		s.log.Error("issuing code failed", "error", err)
		s.errorPage(w, http.StatusInternalServerError, "Could not issue the authorization code.")
		return
	}
	s.redirectBack(w, r, http.StatusSeeOther, ar, url.Values{"code": {code}})
}

// constantEqual compares secrets without leaking where they differ.
func constantEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
