package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDisconnectClient(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			startedA, startedB := make(chan struct{}), make(chan struct{})
			ctxErrA := make(chan error, 1)
			releaseB := make(chan struct{})
			c.SetMCP("n", "a", []*mcp.Tool{tool("block", "")}, callerFunc(func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				close(startedA)
				<-ctx.Done()
				ctxErrA <- ctx.Err()
				return nil, ctx.Err()
			}))
			c.SetMCP("n", "b", []*mcp.Tool{tool("block", "")}, callerFunc(func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				close(startedB)
				select {
				case <-releaseB:
					return textResult("b done"), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}))
			calls := activity.New(10)
			g := New(Options{Catalog: c, Activity: calls, Identify: byHeader, Visible: ownTools, CallTimeout: 30 * time.Second})
			a := connectAs(t, g, version, "a", nil)
			b := connectAs(t, g, version, "b", nil)

			type outcome struct {
				res *mcp.CallToolResult
				err error
			}
			callA, callB := make(chan outcome, 1), make(chan outcome, 1)
			go func() {
				r, err := a.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__a__block"})
				callA <- outcome{r, err}
			}()
			go func() {
				r, err := b.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__b__block"})
				callB <- outcome{r, err}
			}()
			for _, ch := range []chan struct{}{startedA, startedB} {
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("call did not start")
				}
			}

			if g.DisconnectClient("id-nobody") {
				t.Error("DisconnectClient(unknown) = true")
			}
			if !g.DisconnectClient("id-a") {
				t.Fatal("DisconnectClient(a) = false")
			}
			if g.DisconnectClient("id-a") {
				t.Error("second DisconnectClient(a) = true")
			}
			select {
			case err := <-ctxErrA:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("upstream ctx error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disconnect did not cancel the upstream call")
			}
			select {
			case o := <-callA:
				// The client hears why, not just that its session closed.
				if o.err == nil || !strings.Contains(o.err.Error(), "key revoked") {
					t.Errorf("disconnected call = %+v, %v; want a key revoked error", o.res, o.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disconnected call did not return")
			}

			// B is untouched until released.
			select {
			case o := <-callB:
				t.Fatalf("other client's call ended early: %+v", o)
			default:
			}
			close(releaseB)
			select {
			case o := <-callB:
				if o.err != nil {
					t.Fatalf("b call: %v", o.err)
				}
				assertResult(t, o.res, textResult("b done"))
			case <-time.After(5 * time.Second):
				t.Fatal("b call did not finish")
			}

			var found bool
			for _, call := range calls.Recent() {
				if call.ClientID == "id-a" {
					found = true
					if call.Outcome != "error" || !strings.Contains(call.Error, "cancelled") {
						t.Errorf("activity for a = %+v", call)
					}
				}
			}
			if !found {
				t.Error("no activity recorded for the disconnected call")
			}

			// A request authenticated before the revocation but arriving
			// after it gets nothing: no tools, no new mirror.
			a2 := connectAs(t, g, version, "a", nil)
			if got := listNames(t, a2); len(got) != 0 {
				t.Errorf("after revocation sees %v", got)
			}
			if _, err := a2.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__a__block"}); err == nil {
				t.Error("call after revocation succeeded")
			}
			if g.DisconnectClient("id-a") {
				t.Error("revoked client got a mirror again")
			}
			// Others still connect as usual.
			if got := listNames(t, connectAs(t, g, version, "c", nil)); len(got) != 0 {
				t.Errorf("c sees %v (it owns no tools)", got)
			}
			if !g.DisconnectClient("id-c") {
				t.Error("c's mirror not registered")
			}
		})
	}
}

func TestDisconnectClosesSessionBasedSession(t *testing.T) {
	c := catalog.New()
	c.SetMCP("n", "a", []*mcp.Tool{tool("t", "")}, okCaller("ok"))
	g := New(Options{Catalog: c, Identify: byHeader})
	a := connectAs(t, g, "2025-11-25", "a", nil)
	b := connectAs(t, g, "2025-11-25", "b", nil)
	if got := listNames(t, a); len(got) != 1 {
		t.Fatalf("a sees %v", got)
	}
	if !g.DisconnectClient("id-a") {
		t.Fatal("DisconnectClient = false")
	}
	waited := make(chan struct{})
	go func() {
		a.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("client session stayed open after disconnect")
	}
	if got := listNames(t, b); len(got) != 1 {
		t.Errorf("b sees %v after a was disconnected", got)
	}
}

// Ending Run (hub shutdown) closes every client's sessions, so an HTTP
// server's graceful shutdown isn't held open by notification streams, and
// cancels calls with a message that says why.
func TestRunEndClosesSessions(t *testing.T) {
	c := catalog.New()
	started := make(chan struct{})
	c.SetMCP("n", "a", []*mcp.Tool{tool("block", "")}, callerFunc(func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	g := New(Options{Catalog: c, Identify: byHeader, CallTimeout: time.Minute})
	ctx, cancel := context.WithCancel(t.Context())
	ran := make(chan struct{})
	go func() { g.Run(ctx); close(ran) }()
	a := connectAs(t, g, "2025-11-25", "a", nil)
	result := make(chan error, 1)
	go func() {
		_, err := a.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__a__block"})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "shutting down") && !strings.Contains(err.Error(), "closed") {
			t.Errorf("call at shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call still running after Run ended")
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	waited := make(chan struct{})
	go func() { a.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("session stayed open after Run ended")
	}
	if g.sessionCount(identity.Client{ID: "id-a"}) != 0 {
		t.Error("a mirror kept sessions")
	}
}
