//go:build e2e

// Package e2e runs mcp-mesh across Hyper-V machines: a hub VM and node VMs,
// set up from scratch for each scenario. See e2e/README.md.
//
//	go test -tags e2e -timeout 60m ./e2e/
package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/e2e/lab"
	"github.com/AldrinSalazar/mcp-mesh/e2e/mesh"
)

var (
	theLab  *lab.Lab
	results string // e2e-results/<run> for failed scenarios' logs
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	dir, err := lab.Dir()
	if err != nil {
		// The suite needs Windows with Hyper-V and WSL; elsewhere it is skipped.
		fmt.Fprintln(os.Stderr, "e2e: skipped:", err)
		return 0
	}
	theLab = lab.New(dir, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := theLab.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: lab relay:", err)
		return 1
	}
	defer theLab.Close()
	for _, s := range lab.Machines {
		if err := theLab.Ensure(ctx, s); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: prepare", s.Name+":", err)
			return 1
		}
	}
	if _, err := mesh.Build(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	wd, _ := os.Getwd()
	results = filepath.Join(wd, "..", "e2e-results", time.Now().Format("20060102-150405"))
	code := m.Run()
	if os.Getenv("KEEP") == "" {
		names := make([]string, len(lab.Machines))
		for i, s := range lab.Machines {
			names[i] = s.Name
		}
		_ = theLab.Down(context.Background(), names...)
	}
	return code
}

// env is one scenario's machines and what to collect if it fails.
type env struct {
	t     *testing.T
	ctx   context.Context
	hub   *mesh.Hub
	nodes []*mesh.Node
	m     map[string]*lab.Machine
}

// newEnv restores the named machines to their clean checkpoint (in
// parallel) and boots them.
func newEnv(t *testing.T, timeout time.Duration, names ...string) *env {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)
	e := &env{t: t, ctx: ctx, m: map[string]*lab.Machine{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := theLab.Reset(ctx, n)
			if err != nil {
				errs[i] = err
				return
			}
			mu.Lock()
			e.m[n] = m
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("reset machines: %v", err)
		}
	}
	t.Cleanup(e.collect)
	return e
}

// collect saves logs and state when the scenario failed.
func (e *env) collect() {
	if !e.t.Failed() {
		return
	}
	dir := filepath.Join(results, e.t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	save := func(name, content string) { _ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644) }
	if e.hub != nil {
		logs, err := e.hub.Logs(ctx)
		if err != nil {
			logs += "\n" + err.Error()
		}
		save("hub.log", logs)
	}
	for _, n := range e.nodes {
		save(n.M.Name+".log", n.Logs(ctx))
		st, _ := n.Run(ctx, "status", "--json")
		save(n.M.Name+".status.json", st)
	}
	e.t.Logf("logs saved in %s", dir)
}

func (e *env) machine(name string) *lab.Machine {
	m, ok := e.m[name]
	if !ok {
		e.t.Fatalf("machine %s wasn't reset for this scenario", name)
	}
	return m
}

// startHub starts the hub on the "hub" machine.
func (e *env) startHub(opts mesh.HubOptions) *mesh.Hub {
	e.t.Helper()
	h, err := mesh.StartHub(e.ctx, theLab, e.machine("hub"), opts)
	if err != nil {
		e.t.Fatal(err)
	}
	e.hub = h
	return h
}

// joinNode creates a node key for the machine, configures the node, adds
// the fake MCP and starts the node in the background.
func (e *env) joinNode(name string) *mesh.Node {
	e.t.Helper()
	return e.joinNodeWith(name, "start")
}

// joinNodeWith is joinNode with the command that starts the node: "start"
// (a background process) or "install" (the user service), with its flags.
func (e *env) joinNodeWith(name string, start ...string) *mesh.Node {
	e.t.Helper()
	key, err := e.hub.CreateKey(e.ctx, "node", name)
	if err != nil {
		e.t.Fatal(err)
	}
	n, err := mesh.SetupNode(e.ctx, e.hub, e.machine(name), key)
	if err != nil {
		e.t.Fatal(err)
	}
	e.nodes = append(e.nodes, n)
	if _, err := n.Run(e.ctx, "add", "--name", "fake", "--", "/usr/local/bin/fakeapp", "stdio"); err != nil {
		e.t.Fatal(err)
	}
	if _, err := n.Run(e.ctx, start...); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// client creates a client key and connects with it, waiting until the key
// sees tools.
func (e *env) client(name string) *mcp.ClientSession {
	e.t.Helper()
	key, err := e.hub.CreateKey(e.ctx, "client", name)
	if err != nil {
		e.t.Fatal(err)
	}
	s, err := e.hub.Client(e.ctx, key)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { s.Close() })
	err = mesh.Eventually(e.ctx, 30*time.Second, func() error {
		res, err := s.ListTools(e.ctx, nil)
		if err != nil {
			return err
		}
		if len(res.Tools) == 0 {
			return fmt.Errorf("no tools yet")
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// callResult is a finished tool call.
type callResult struct {
	text    string
	isError bool
	err     error
	took    time.Duration
}

// startCall runs a tool call in the background.
func (e *env) startCall(s *mcp.ClientSession, tool string, args map[string]any) <-chan callResult {
	ch := make(chan callResult, 1)
	go func() {
		start := time.Now()
		res, err := s.CallTool(e.ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		r := callResult{err: err, took: time.Since(start)}
		if res != nil {
			r.text, r.isError = text(res), res.IsError
		}
		ch <- r
	}()
	return ch
}

// call runs a tool call and fails the scenario unless it succeeds.
func (e *env) call(s *mcp.ClientSession, tool string, args map[string]any) callResult {
	e.t.Helper()
	r := <-e.startCall(s, tool, args)
	if r.err != nil || r.isError {
		e.t.Fatalf("%s: err=%v isError=%v %q", tool, r.err, r.isError, r.text)
	}
	return r
}

// waitInFlight waits until the hub counts n calls running on node/mcp.
func (e *env) waitInFlight(node, mcpName string, n int) {
	e.t.Helper()
	err := mesh.Eventually(e.ctx, 30*time.Second, func() error {
		got, err := e.hub.InFlight(e.ctx, node, mcpName)
		if err != nil {
			return err
		}
		if got < n {
			return fmt.Errorf("%d calls in flight, want %d", got, n)
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// waitMCPReady waits until the hub shows node's MCP as ready.
func (e *env) waitMCPReady(node, mcpName string, timeout time.Duration) {
	e.t.Helper()
	err := mesh.Eventually(e.ctx, timeout, func() error {
		ns, err := e.hub.Nodes(e.ctx)
		if err != nil {
			return err
		}
		for _, n := range ns {
			if n.Name != node {
				continue
			}
			if !n.Online {
				return fmt.Errorf("%s is offline", node)
			}
			for _, m := range n.MCPs {
				if m.Name == mcpName {
					if m.State == "ready" {
						return nil
					}
					return fmt.Errorf("%s/%s is %s %s", node, mcpName, m.State, m.Error)
				}
			}
			return fmt.Errorf("%s has no MCP %s", node, mcpName)
		}
		names := make([]string, len(ns))
		for i, n := range ns {
			names[i] = n.Name
		}
		return fmt.Errorf("hub doesn't list %s (has %s)", node, strings.Join(names, ", "))
	})
	if err != nil {
		e.t.Fatalf("waiting for %s/%s: %v", node, mcpName, err)
	}
}

// knownIssue runs check for a bug that is known and not fixed yet. While
// it fails, the scenario logs it and goes on; once it passes, the scenario
// fails so the marker gets removed and the check becomes a plain assertion.
func knownIssue(t *testing.T, id, what string, check func() error) {
	t.Helper()
	if err := check(); err != nil {
		t.Logf("KNOWN ISSUE %s: %s: %v", id, what, err)
		return
	}
	t.Errorf("known issue %s seems fixed (%s): remove the knownIssue marker", id, what)
}
