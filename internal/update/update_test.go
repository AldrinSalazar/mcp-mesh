package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"v1.2.0", Version{Major: 1, Minor: 2}, true},
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"v1.3.0-rc.1", Version{Major: 1, Minor: 3, Pre: "rc.1"}, true},
		{"v1.2.0-3-gabc1234", Version{Major: 1, Minor: 2, Commits: 3}, true},
		{"v1.2.0-3-gabc1234-dirty", Version{Major: 1, Minor: 2, Commits: 3}, true},
		{"v1.3.0-rc.1-2-g0123abcd", Version{Major: 1, Minor: 3, Pre: "rc.1", Commits: 2}, true},
		{"v1.2.0+build.5", Version{Major: 1, Minor: 2}, true},
		{"1.0.0-beta-gamma", Version{Major: 1, Pre: "beta-gamma"}, true},
		{"dev", Version{}, false},
		{"abc1234", Version{}, false}, // git describe --always without a tag
		{"", Version{}, false},
		{"1.2", Version{}, false},
		{"v01.2.0", Version{}, false},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			got, ok := Parse(test.in)
			if ok != test.ok || got != test.want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v, %v", test.in, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestBehind(t *testing.T) {
	tests := []struct {
		version, reference string
		want               bool
	}{
		{"v1.2.0", "v1.3.0", true},
		{"1.2.0", "v1.2.0", false},
		{"v1.3.0", "v1.2.0", false},
		{"v1.2.9", "v1.10.0", true},
		{"v1.3.0-rc.1", "v1.3.0", true},
		{"v1.3.0-rc.2", "v1.3.0-rc.10", true},
		{"v1.3.0-alpha", "v1.3.0-beta", true},
		{"v1.3.0-rc.1", "v1.3.0-rc.1.1", true},
		{"v1.3.0-rc", "v1.3.0-1", false}, // numeric identifiers rank lower
		{"v1.2.0-3-gabc1234", "v1.2.0", false},
		{"v1.2.0", "v1.2.0-3-gabc1234", true},
		{"v1.2.0-3-gabc1234", "v1.3.0", true},
		{"v1.3.0-rc.1-4-gabc1234", "v1.3.0", true},
		{"dev", "v1.3.0", false},
		{"v1.2.0", "dev", false},
	}
	for _, test := range tests {
		if got := Behind(test.version, test.reference); got != test.want {
			t.Errorf("Behind(%q, %q) = %v, want %v", test.version, test.reference, got, test.want)
		}
	}
}

// releases serves a GitHub-like releases API and counts its requests.
func releases(t *testing.T, latest string, list []githubRelease) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/releases/latest":
			if latest == "" {
				http.Error(w, "rate limited", http.StatusForbidden)
				return
			}
			json.NewEncoder(w).Encode(githubRelease{TagName: latest, HTMLURL: "https://example/releases/" + latest})
		case "/releases":
			json.NewEncoder(w).Encode(list)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCheck(t *testing.T) {
	list := []githubRelease{
		{TagName: "v1.4.0-rc.1", HTMLURL: "u/rc", Prerelease: true},
		{TagName: "v1.5.0", Draft: true},
		{TagName: "v1.3.0", HTMLURL: "u/1.3"},
	}
	tests := []struct {
		name, current string
		want          Status
	}{
		{"outdated release", "v1.2.0", Status{Current: "v1.2.0", Latest: "v1.3.0", Available: true, URL: "https://example/releases/v1.3.0"}},
		{"up to date", "v1.3.0", Status{Current: "v1.3.0", Latest: "v1.3.0", URL: "https://example/releases/v1.3.0"}},
		{"ahead of the tag", "v1.3.0-2-gabcdef0", Status{Current: "v1.3.0-2-gabcdef0", Latest: "v1.3.0", URL: "https://example/releases/v1.3.0"}},
		// A pre-release build counts pre-releases, but not drafts.
		{"pre-release", "v1.4.0-beta.1", Status{Current: "v1.4.0-beta.1", Latest: "v1.4.0-rc.1", Available: true, URL: "u/rc"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, _ := releases(t, "v1.3.0", list)
			c := New(Options{Current: test.current, URL: srv.URL + "/releases"})
			if err := c.Check(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := c.Status()
			got.CheckedAt = time.Time{}
			if got != test.want {
				t.Fatalf("status = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestDevBuildsAndOptOutNeverAsk(t *testing.T) {
	for _, opts := range []Options{{Current: "dev"}, {Current: "abc1234"}, {Current: "v1.2.0", Disabled: true}} {
		srv, hits := releases(t, "v1.3.0", nil)
		opts.URL = srv.URL + "/releases"
		c := New(opts)
		if c.Enabled() {
			t.Fatalf("%+v: enabled", opts)
		}
		c.Run(t.Context()) // returns at once
		if hits.Load() != 0 || c.Status().Available {
			t.Fatalf("%+v: asked %d times, status %+v", opts, hits.Load(), c.Status())
		}
	}
}

func TestFailureIsQuiet(t *testing.T) {
	srv, _ := releases(t, "", nil) // rate limited
	cache := FileCache{Path: filepath.Join(t.TempDir(), "update.json")}
	old := State{Latest: Release{Version: "v1.3.0", URL: "u"}, CheckedAt: time.Now().Add(-48 * time.Hour)}
	if err := cache.Save(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	c := New(Options{Current: "v1.2.0", URL: srv.URL + "/releases", Cache: cache})
	if err := c.Check(t.Context()); err == nil {
		t.Fatal("Check succeeded against a rate-limited API")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	// The failed check keeps the cached answer.
	deadline := time.Now().Add(5 * time.Second)
	for !c.Status().Available {
		if time.Now().After(deadline) {
			t.Fatal("cached release not loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if s, _ := cache.Load(t.Context()); !s.CheckedAt.Equal(old.CheckedAt) {
		t.Fatalf("cache overwritten after a failure: %+v", s)
	}
}

func TestCacheSkipsTheCheck(t *testing.T) {
	srv, hits := releases(t, "v1.4.0", nil)
	cache := FileCache{Path: filepath.Join(t.TempDir(), "sub", "update.json")}
	changes := make(chan Status, 4)
	c := New(Options{Current: "v1.2.0", URL: srv.URL + "/releases", Cache: cache, OnChange: func(s Status) { changes <- s }})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	if s := <-changes; s.Latest != "v1.4.0" || !s.Available {
		t.Fatalf("first run: %+v", s)
	}
	cancel()
	<-done
	if hits.Load() != 1 {
		t.Fatalf("first run asked %d times", hits.Load())
	}
	// A restart within the interval uses the cache.
	c = New(Options{Current: "v1.2.0", URL: srv.URL + "/releases", Cache: cache, OnChange: func(s Status) { changes <- s }})
	ctx, cancel = context.WithCancel(t.Context())
	done = make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	if s := <-changes; s.Latest != "v1.4.0" {
		t.Fatalf("restart: %+v", s)
	}
	cancel()
	<-done
	if hits.Load() != 1 {
		t.Fatalf("restart asked again (%d requests)", hits.Load())
	}
	// A pre-release build ignores a cache made by a release build.
	if st := StatusOf("v1.5.0-rc.1", State{Latest: Release{Version: "v1.4.0"}}); st.Latest != "" {
		t.Fatalf("pre-release used a release cache: %+v", st)
	}
}

func TestNodeUpgrade(t *testing.T) {
	for _, exe := range []string{"/opt/homebrew/Cellar/mcp-mesh-node/1.2.0/bin/mcp-mesh-node", "/opt/homebrew/opt/mcp-mesh-node/bin/mcp-mesh-node", "/home/linuxbrew/.linuxbrew/Cellar/mcp-mesh-node/1.2.0/bin/mcp-mesh-node"} {
		if got := NodeUpgrade(exe); got != "brew upgrade mcp-mesh-node" {
			t.Errorf("NodeUpgrade(%q) = %q", exe, got)
		}
	}
	if got := NodeUpgrade("/home/me/.local/bin/mcp-mesh-node"); got == "brew upgrade mcp-mesh-node" {
		t.Errorf("plain binary: %q", got)
	}
}
