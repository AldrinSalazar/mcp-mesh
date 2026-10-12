// Package update finds out whether a newer mcp-mesh release is out, so the
// hub and the node can tell their user. It only tells: nothing is downloaded
// or replaced.
//
// A [Checker] asks GitHub's releases API with a plain GET, at start and then
// once a day, and caches the answer so restarts don't ask again. Every
// failure is quiet: offline or rate limited, the last known answer stays.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ReleasesURL is the GitHub API for mcp-mesh's releases.
	ReleasesURL = "https://api.github.com/repos/AldrinSalazar/mcp-mesh/releases"
	// DefaultInterval is how often a Checker asks.
	DefaultInterval = 24 * time.Hour
	// EnvDisable turns the check off for both binaries when set to a true
	// value ("1", "true").
	EnvDisable = "MCPMESH_NO_UPDATE_CHECK"
	// retryAfter is how soon a failed check is retried.
	retryAfter = time.Hour
	timeout    = 10 * time.Second
)

// DisabledByEnv reports whether EnvDisable turns the check off.
func DisabledByEnv() bool {
	b, err := strconv.ParseBool(os.Getenv(EnvDisable))
	return err == nil && b
}

// Release is a published release.
type Release struct {
	Version string `json:"version"` // the tag, e.g. "v1.3.0"
	URL     string `json:"url"`     // its page, with the release notes
}

// State is what a Checker caches.
type State struct {
	Latest    Release   `json:"latest"`
	CheckedAt time.Time `json:"checkedAt"`
	// Prerelease: pre-releases were counted, because the build that checked
	// is one. A release build ignores such a state, and the other way round.
	Prerelease bool `json:"prerelease,omitempty"`
}

// Cache keeps the State between runs.
type Cache interface {
	// Load returns the saved State, or a zero State if there is none.
	Load(ctx context.Context) (State, error)
	Save(ctx context.Context, s State) error
}

// Status is what a UI shows.
type Status struct {
	// Current is the running build's version.
	Current string `json:"current"`
	// Latest is the newest release known; empty until a check succeeds.
	Latest string `json:"latest,omitempty"`
	// Available: Latest is newer than Current.
	Available bool      `json:"available"`
	URL       string    `json:"url,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
}

// StatusOf compares a cached State with the running version.
func StatusOf(current string, s State) Status {
	st := Status{Current: current}
	cur, ok := Parse(current)
	if !ok || s.Latest.Version == "" || s.Prerelease != cur.Prerelease() {
		return st
	}
	st.Latest, st.URL, st.CheckedAt = s.Latest.Version, s.Latest.URL, s.CheckedAt
	st.Available = Behind(current, s.Latest.Version)
	return st
}

// Options configure a Checker.
type Options struct {
	// Current is the running build's version. A build without one ("dev")
	// never checks.
	Current string
	// Disabled turns the check off (an opt-out flag or setting).
	Disabled bool
	Cache    Cache  // nil keeps the State in memory only
	URL      string // the releases API; empty means ReleasesURL
	Client   *http.Client
	// Interval between checks; zero means DefaultInterval.
	Interval time.Duration
	Logger   *slog.Logger // nil means slog.Default()
	// OnChange, if set, is called when the Status changes.
	OnChange func(Status)
	// Upgrade says how to upgrade, for the log line about a new release.
	Upgrade string
}

// Checker asks for the latest release now and then.
type Checker struct {
	opts    Options
	log     *slog.Logger
	current Version
	enabled bool

	mu    sync.Mutex
	state State
}

// New returns a Checker; Run drives it.
func New(opts Options) *Checker {
	if opts.URL == "" {
		opts.URL = ReleasesURL
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: timeout}
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	cur, ok := Parse(opts.Current)
	return &Checker{opts: opts, log: opts.Logger, current: cur, enabled: ok && !opts.Disabled}
}

// Enabled reports whether the Checker checks at all.
func (c *Checker) Enabled() bool { return c.enabled }

// Status returns what is known now.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return StatusOf(c.opts.Current, c.state)
}

// Run loads the cache, then checks whenever the last check is older than
// the interval, until ctx ends. The caller owns the goroutine.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		return
	}
	if c.opts.Cache != nil {
		s, err := c.opts.Cache.Load(ctx)
		if err != nil {
			c.log.Debug("update check cache not read", "error", err)
		} else if s.Prerelease == c.current.Prerelease() {
			c.set(s)
		}
	}
	for {
		c.mu.Lock()
		due := c.state.CheckedAt.Add(c.opts.Interval)
		c.mu.Unlock()
		wait := time.Until(due)
		if wait <= 0 {
			wait = c.opts.Interval
			if err := c.Check(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				c.log.Debug("update check failed; trying again later", "error", err, "retry_in", min(retryAfter, c.opts.Interval))
				wait = min(retryAfter, c.opts.Interval)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Check asks for the latest release now and saves the answer.
func (c *Checker) Check(ctx context.Context) error {
	latest, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	s := State{Latest: latest, CheckedAt: time.Now(), Prerelease: c.current.Prerelease()}
	c.set(s)
	if c.opts.Cache != nil {
		if err := c.opts.Cache.Save(ctx, s); err != nil {
			c.log.Debug("update check cache not saved", "error", err)
		}
	}
	return nil
}

func (c *Checker) set(s State) {
	c.mu.Lock()
	before := StatusOf(c.opts.Current, c.state)
	c.state = s
	after := StatusOf(c.opts.Current, c.state)
	c.mu.Unlock()
	if after.Latest != before.Latest || after.Available != before.Available {
		if after.Available {
			args := []any{"running", after.Current, "latest", after.Latest, "release", after.URL}
			if c.opts.Upgrade != "" {
				args = append(args, "upgrade", c.opts.Upgrade)
			}
			c.log.Info("a newer mcp-mesh release is available", args...)
		}
		if c.opts.OnChange != nil {
			c.opts.OnChange(after)
		}
	}
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// fetch returns the newest release. A release build asks for GitHub's
// latest release, which is never a pre-release; a pre-release build lists
// the recent ones and takes the newest, pre-releases included.
func (c *Checker) fetch(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := c.opts.URL + "/latest"
	if c.current.Prerelease() {
		url = c.opts.URL + "?per_page=20"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	// GitHub requires a User-Agent; it doesn't carry the version.
	req.Header.Set("User-Agent", "mcp-mesh-update-check")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.opts.Client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("ask for the latest release: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, fmt.Errorf("read the latest release: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("ask for the latest release: %s", resp.Status)
	}
	var list []githubRelease
	if c.current.Prerelease() {
		err = json.Unmarshal(body, &list)
	} else {
		var one githubRelease
		err = json.Unmarshal(body, &one)
		list = []githubRelease{one}
	}
	if err != nil {
		return Release{}, fmt.Errorf("decode the latest release: %w", err)
	}
	var best Release
	var bestV Version
	for _, r := range list {
		v, ok := Parse(r.TagName)
		if r.Draft || !ok || (v.Prerelease() && !c.current.Prerelease()) {
			continue
		}
		if best.Version == "" || v.Compare(bestV) > 0 {
			best, bestV = Release{Version: r.TagName, URL: r.HTMLURL}, v
		}
	}
	if best.Version == "" {
		return Release{}, errors.New("no release found")
	}
	return best, nil
}

// FileCache keeps the State in a JSON file. Several processes may share it:
// a save replaces the file whole.
type FileCache struct{ Path string }

func (f FileCache) Load(context.Context) (State, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("decode %s: %w", f.Path, err)
	}
	return s, nil
}

func (f FileCache) Save(_ context.Context, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".update-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

// Executable is this binary's path with symlinks resolved, for
// NodeUpgrade; empty if unknown.
func Executable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}

// NodeUpgrade says how to upgrade the node binary at executable: with
// Homebrew when it came from there, otherwise by downloading this
// platform's archive.
func NodeUpgrade(executable string) string {
	if strings.Contains(executable, "/Cellar/") || strings.HasSuffix(filepath.Dir(executable), "/opt/mcp-mesh-node/bin") {
		return "brew upgrade mcp-mesh-node"
	}
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv7"
	}
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return "download https://github.com/AldrinSalazar/mcp-mesh/releases/latest/download/mcp-mesh-node_" + runtime.GOOS + "_" + arch + ext
}
