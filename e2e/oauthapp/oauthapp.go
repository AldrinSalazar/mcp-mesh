// Package oauthapp is a fake MCP app that connects to the hub with OAuth,
// the way a hosted app or a phone does: it is the Go SDK's own OAuth client
// (protected resource discovery, dynamic client registration, PKCE, code
// exchange, refresh). The only fake part is the person: a robot that signs
// in on the hub's consent page and clicks Allow, without a browser.
package oauthapp

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// RedirectURL is where the hub sends the robot after consent. Nothing
// listens there: the robot reads the code from the redirect itself.
const RedirectURL = "http://127.0.0.1:9/callback"

// Robot plays the admin at the consent page.
type Robot struct {
	Username string
	Password string
	// KeyName is the name of the client key the hub makes for the app.
	KeyName string
	// Groups to tick on the consent page, by ID. Empty keeps the page's
	// defaults.
	Groups []string
	// Deny clicks Deny instead of Allow.
	Deny bool
	// HTTP is the browser's client (TLS roots for a test CA). Nil means
	// http.DefaultClient's transport.
	HTTP *http.Client

	// Pages records what the robot saw, for failure messages.
	Pages []string
}

// Fetch is an auth.AuthorizationCodeFetcher: it opens the authorization URL,
// signs in, consents and returns the code from the redirect.
func (r *Robot) Fetch(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	b := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if r.HTTP != nil {
		b.Transport = r.HTTP.Transport
	}
	start, err := url.Parse(args.URL)
	if err != nil {
		return nil, fmt.Errorf("authorization URL: %w", err)
	}
	page, resp, err := r.get(ctx, b, args.URL)
	if err != nil {
		return nil, err
	}
	for step := 0; step < 6; step++ {
		if loc := resp.Header.Get("Location"); loc != "" && isRedirect(resp.StatusCode) {
			if strings.HasPrefix(loc, RedirectURL) {
				return result(loc)
			}
			next, err := start.Parse(loc)
			if err != nil {
				return nil, fmt.Errorf("redirect %q: %w", loc, err)
			}
			if page, resp, err = r.get(ctx, b, next.String()); err != nil {
				return nil, err
			}
			continue
		}
		form, err := parseForm(page)
		if err != nil {
			return nil, fmt.Errorf("authorization page (HTTP %d): %w", resp.StatusCode, err)
		}
		action := start.ResolveReference(&url.URL{Path: start.Path})
		if form.action != "" {
			if a, err := start.Parse(form.action); err == nil {
				action = a
			}
		}
		values := form.values
		switch {
		case strings.Contains(page, `name="password"`):
			values.Set("action", "signin")
			values.Set("username", r.Username)
			values.Set("password", r.Password)
		case strings.Contains(page, `value="allow"`):
			if r.Deny {
				values.Set("action", "deny")
			} else {
				values.Set("action", "allow")
			}
			if r.KeyName != "" {
				values.Set("key_name", r.KeyName)
			}
			if len(r.Groups) > 0 {
				values["group"] = r.Groups
			}
		default:
			return nil, fmt.Errorf("authorization page has neither sign-in nor consent (HTTP %d): %.300s", resp.StatusCode, page)
		}
		page, resp, err = r.post(ctx, b, action.String(), start, values)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("authorization did not redirect back to the app")
}

func (r *Robot) get(ctx context.Context, c *http.Client, u string) (string, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", nil, err
	}
	return r.do(c, req)
}

// post submits a form the way a browser does, Origin included: the hub
// refuses cross-origin form posts.
func (r *Robot) post(ctx context.Context, c *http.Client, u string, origin *url.URL, v url.Values) (string, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(v.Encode()))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
	return r.do(c, req)
}

func (r *Robot) do(c *http.Client, req *http.Request) (string, *http.Response, error) {
	resp, err := c.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", req.URL.Path, err)
	}
	r.Pages = append(r.Pages, string(b))
	return string(b), resp, nil
}

func isRedirect(code int) bool { return code >= 300 && code < 400 }

// result reads the code, state and issuer from the redirect to the app.
func result(loc string) (*auth.AuthorizationResult, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return nil, fmt.Errorf("redirect %q: %w", loc, err)
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		return nil, fmt.Errorf("authorization refused: %s: %s", e, q.Get("error_description"))
	}
	if q.Get("code") == "" {
		return nil, fmt.Errorf("redirect without a code: %s", loc)
	}
	return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
}

type form struct {
	action string
	values url.Values
}

var (
	formRe  = regexp.MustCompile(`<form[^>]*action="([^"]*)"`)
	inputRe = regexp.MustCompile(`<input ([^>]*)>`)
	attrRe  = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

// parseForm collects what a browser would submit from the page's form:
// hidden inputs, checked boxes and radios, and text fields' values.
func parseForm(page string) (form, error) {
	m := formRe.FindStringSubmatch(page)
	if m == nil {
		return form{}, errors.New("no form")
	}
	f := form{action: html.UnescapeString(m[1]), values: url.Values{}}
	for _, in := range inputRe.FindAllStringSubmatch(page, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(in[1], -1) {
			attrs[a[1]] = html.UnescapeString(a[2])
		}
		name := attrs["name"]
		if name == "" {
			continue
		}
		switch attrs["type"] {
		case "checkbox", "radio":
			if strings.Contains(in[1], " checked") {
				f.values.Add(name, attrs["value"])
			}
		case "password":
		default:
			f.values.Add(name, attrs["value"])
		}
	}
	return f, nil
}

// App is a connected fake app.
type App struct {
	Session *mcp.ClientSession
	Handler *auth.AuthorizationCodeHandler
}

// Connect runs the whole flow against an MCP endpoint (the hub's /mcp): the
// first request gets a 401, the SDK discovers the hub's authorization
// server, registers, sends the robot through consent, and retries with the
// token.
func Connect(ctx context.Context, endpoint string, robot *Robot, httpClient *http.Client) (*App, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	h, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:              "e2e fake app",
				RedirectURIs:            []string{RedirectURL},
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				TokenEndpointAuthMethod: "none",
			},
		},
		RedirectURL:              RedirectURL,
		AuthorizationCodeFetcher: robot.Fetch,
		RequestRefreshToken:      true,
		Client:                   httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("oauth handler: %w", err)
	}
	t := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, OAuthHandler: h}
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-fake-app", Version: "1"}, nil)
	s, err := c.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("connect with OAuth: %w", err)
	}
	return &App{Session: s, Handler: h}, nil
}

// Close ends the app's session.
func (a *App) Close() error { return a.Session.Close() }
