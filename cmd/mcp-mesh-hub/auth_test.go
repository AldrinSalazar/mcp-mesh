package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/server"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

func runKey(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--data-dir", dataDir, "--public-host", "hub.example", "key"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func secretIn(t *testing.T, out, kind string) string {
	t.Helper()
	m := regexp.MustCompile(`mm_` + kind + `_[0-9A-Za-z]{43}`).FindString(out)
	if m == "" {
		t.Fatalf("no %s secret in output:\n%s", kind, out)
	}
	return m
}

// Without URL flags of its own, key create prints the URLs the running hub
// recorded when it started (the CLI doesn't see the hub's flags).
func TestKeyCreatePrintsRecordedURLs(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.SetSettings(t.Context(), map[string]string{
		store.SettingNodeURL: "wss://mesh.example.com/node", store.SettingClientURL: "https://mesh.example.com/mcp",
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := rootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--data-dir", dir, "key", "create"}, args...))
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := run("node", "studio"); !strings.Contains(out, "init --hub wss://mesh.example.com/node --name studio") {
		t.Errorf("node key output:\n%s", out)
	}
	if out := run("client", "phone"); !strings.Contains(out, "https://mesh.example.com/mcp") {
		t.Errorf("client key output:\n%s", out)
	}
}

func TestKeyCommands(t *testing.T) {
	dir := t.TempDir()
	out, err := runKey(t, dir, "create", "node", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	nodeSecret := secretIn(t, out, "node")
	for _, want := range []string{
		"mcp-mesh-node init --hub ws://hub.example:7001/node --name laptop --key " + nodeSecret,
		"mcp-mesh-node key set " + nodeSecret, "within a second",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("node output lacks %q:\n%s", want, out)
		}
	}
	out, err = runKey(t, dir, "create", "client", "claude")
	if err != nil {
		t.Fatal(err)
	}
	clientSecret := secretIn(t, out, "client")
	for _, want := range []string{
		`claude mcp add --transport http mcp-mesh http://hub.example:7002/mcp --header "Authorization: Bearer ` + clientSecret + `"`,
		`"headers"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("client output lacks %q:\n%s", want, out)
		}
	}
	out, err = runKey(t, dir, "create", "admin", "me")
	if err == nil || !strings.Contains(err.Error(), "sign in with a password") || !strings.Contains(err.Error(), "mcp-mesh-hub admin create") {
		t.Errorf("key create admin: %v\n%s", err, out)
	}
	if _, err := runKey(t, dir, "create", "node", "bad name"); err == nil {
		t.Error("invalid name accepted")
	}
	if _, err := runKey(t, dir, "create", "bogus", "x"); err == nil {
		t.Error("invalid kind accepted")
	}

	out, err = runKey(t, dir, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "KIND", "NAME", "CREATED", "laptop", "claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "mm_") || strings.Contains(out, "hash") {
		t.Errorf("list leaks secrets:\n%s", out)
	}
	out, err = runKey(t, dir, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []keyJSON
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("json list: %v\n%s", err, out)
	}
	if strings.Contains(out, "mm_") || strings.Contains(out, "hash") {
		t.Errorf("json leaks secrets:\n%s", out)
	}

	if _, err := runKey(t, dir, "revoke", "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := runKey(t, dir, "revoke", "laptop"); err == nil {
		t.Error("second revoke succeeded")
	}
	out, _ = runKey(t, dir, "list")
	if strings.Contains(out, "laptop") {
		t.Errorf("revoked key listed:\n%s", out)
	}
	if info, err := os.Stat(filepath.Join(dir, "hub.db")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("hub.db: %v %v", info, err)
	}
}

type addrs struct{ admin, node, client string }

// startHub serves with auth on and returns the bound addresses.
func startHub(t *testing.T, dir string) addrs {
	t.Helper()
	ready := make(chan addrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(t.Context(), config{Config: server.Config{
			AdminListen: "127.0.0.1:0", NodeListen: "127.0.0.1:0", ClientListen: "127.0.0.1:0",
			PublicHost: "localhost", CallTimeout: time.Minute, DataDir: dir, Auth: true,
			OnListen: func(a, n, c string) { ready <- addrs{a, n, c} },
		}, logLevel: "error", logFormat: "text"})
	}()
	t.Cleanup(func() {
		// t.Context() ends just before cleanups run.
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	select {
	case a := <-ready:
		return a
	case err := <-done:
		t.Fatalf("serve returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hub did not start")
	}
	return addrs{}
}

type bearerTransport struct{ key string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

func TestServeAuthClient(t *testing.T) {
	dir := t.TempDir()
	out, _ := runKey(t, dir, "create", "client", "c1")
	clientSecret := secretIn(t, out, "client")
	out, _ = runKey(t, dir, "create", "node", "n1")
	nodeSecret := secretIn(t, out, "node")
	a := startHub(t, dir)
	url := "http://" + a.client + "/mcp"

	resp, err := http.Post(url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("no key: %d", resp.StatusCode)
	}

	connect := func(key string) error {
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(t.Context(),
			&mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearerTransport{key}}}, nil)
		if err == nil {
			cs.Close()
		}
		return err
	}
	if err := connect(clientSecret); err != nil {
		t.Errorf("initialize with client key: %v", err)
	}
	if err := connect(nodeSecret); err == nil {
		t.Error("node key accepted on the client endpoint")
	}

	// /healthz is exempt from sign-in.
	resp, err = http.Get("http://" + a.admin + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz: %v %v", resp, err)
	}
	resp.Body.Close()
	// So is /readyz, which checks the database, watchers and audit log.
	resp, err = http.Get("http://" + a.admin + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ready\n" {
		t.Errorf("readyz: %d %q", resp.StatusCode, body)
	}
	// The metrics need a session.
	resp, err = http.Get("http://" + a.admin + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("health without a session: %d", resp.StatusCode)
	}
}

func TestServeAuthNode(t *testing.T) {
	dir := t.TempDir()
	out, _ := runKey(t, dir, "create", "node", "n1")
	right := secretIn(t, out, "node")
	out, _ = runKey(t, dir, "create", "node", "other")
	other := secretIn(t, out, "node")
	out, _ = runKey(t, dir, "create", "client", "c")
	clientSecret := secretIn(t, out, "client")
	a := startHub(t, dir)
	url := "ws://" + a.node + proto.NodePath

	dial := func(key string) (*wire.Conn, error) {
		h := http.Header{}
		if key != "" {
			h.Set("Authorization", "Bearer "+key)
		}
		return wire.Dial(t.Context(), url, h, 1<<20)
	}
	hello := func(c *wire.Conn) (*proto.Envelope, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := c.Send(ctx, &proto.Envelope{T: proto.THello, Hello: &proto.Hello{Version: proto.Version, Node: "n1"}}); err != nil {
			return nil, err
		}
		return c.Recv(ctx)
	}

	if _, err := dial(""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("no key: err = %v", err)
	}
	if _, err := dial(clientSecret); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("client key on node endpoint: err = %v", err)
	}

	c, err := dial(other)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hello(c)
	if got := wire.CloseStatus(err); got != websocket.StatusCode(proto.CloseInvalidKey) {
		t.Errorf("key bound to another node: close %d, err %v", got, err)
	}
	c.CloseNow()

	c, err = dial(right)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	e, err := hello(c)
	if err != nil || e.T != proto.TWelcome {
		t.Errorf("right key: %+v, %v", e, err)
	}
}

func TestKeyCreateClientJoinsEverythingAndIsLogged(t *testing.T) {
	dir := t.TempDir()
	if _, err := runKey(t, dir, "create", "client", "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := runKey(t, dir, "revoke", "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := runKey(t, dir, "create", "client", "tablet"); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), filepath.Join(dir, store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var group string
	if err := db.QueryRow(`SELECT kg.group_id FROM key_groups kg JOIN keys k ON k.id = kg.key_id WHERE k.name = 'tablet'`).Scan(&group); err != nil || group != acl.Everything {
		t.Errorf("tablet's group = %q, %v", group, err)
	}
	entries, err := adminlog.New(db, nil, 0).Query(t.Context(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Actor+":"+e.Action)
	}
	if want := "cli:key.create cli:key.revoke cli:key.create"; strings.Join(got, " ") != want {
		t.Errorf("admin log = %v, want %s", got, want)
	}
}
