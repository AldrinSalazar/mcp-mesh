package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestKeyRejection(t *testing.T) {
	const maxBackoff = 30 * time.Second
	for _, test := range []struct {
		name    string
		hub     func(w http.ResponseWriter, r *http.Request)
		key     bool
		want    []string
		wantNot []string
	}{
		{
			name: "401 plain text",
			key:  true,
			hub: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "key revoked", http.StatusUnauthorized)
			},
			want: []string{"hub rejected the node key: key revoked;", `for node "desk"`, "mcp-mesh-node key set"},
		},
		{
			name: "401 JSON",
			key:  true,
			hub: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"key is bound to node \"laptop\""}`))
			},
			want: []string{`hub rejected the node key: key is bound to node "laptop";`},
		},
		{
			name: "401 without a key",
			hub: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
			},
			want:    []string{"hub requires a node key;", "mcp-mesh-hub key create node desk", "mcp-mesh-node key set <key>"},
			wantNot: []string{"rejected"},
		},
		{
			name: "close 4003",
			key:  true,
			hub: func(w http.ResponseWriter, r *http.Request) {
				conn, err := wire.Accept(w, r, 0)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_, _ = conn.Recv(r.Context())
				_ = conn.Close(proto.CloseInvalidKey, "unknown key")
			},
			want: []string{"hub rejected the node key: unknown key;", "mcp-mesh-node key set"},
		},
		{
			name: "close 4003 without a key",
			hub: func(w http.ResponseWriter, r *http.Request) {
				conn, err := wire.Accept(w, r, 0)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_, _ = conn.Recv(r.Context())
				_ = conn.Close(proto.CloseInvalidKey, "key required")
			},
			want: []string{"hub requires a node key;"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(test.hub))
			t.Cleanup(server.Close)
			type state struct {
				connected bool
				err       error
			}
			states := make(chan state, 8)
			delays := make(chan time.Duration, 8)
			logs := &lockedBuffer{}
			cfg := Config{
				HubURL: "ws" + strings.TrimPrefix(server.URL, "http") + proto.NodePath,
				Node:   "desk",
				Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				OnConnState: func(connected bool, err error) {
					states <- state{connected, err}
				},
			}
			if test.key {
				cfg.Header = http.Header{"Authorization": {"Bearer mm_node_secret"}}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- run(ctx, cfg, options{
					minBackoff: time.Second, maxBackoff: maxBackoff, resetAfter: time.Minute,
					jitter: func(d time.Duration) time.Duration { return d },
					wait: func(ctx context.Context, d time.Duration) error {
						select {
						case delays <- d:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					},
				})
			}()
			// Three attempts: the backoff must stay at its maximum, not grow from the minimum.
			for attempt := 0; attempt < 3; attempt++ {
				select {
				case got := <-states:
					var rejected *KeyRejectedError
					if got.connected || !errors.As(got.err, &rejected) {
						t.Fatalf("state = %+v, want a key rejection", got)
					}
					for _, want := range test.want {
						if !strings.Contains(got.err.Error(), want) {
							t.Errorf("error %q lacks %q", got.err, want)
						}
					}
					for _, bad := range test.wantNot {
						if strings.Contains(got.err.Error(), bad) {
							t.Errorf("error %q contains %q", got.err, bad)
						}
					}
				case <-ctx.Done():
					t.Fatal("waiting for state:", ctx.Err())
				}
				select {
				case d := <-delays:
					if d != maxBackoff {
						t.Fatalf("attempt %d delay = %v, want %v", attempt, d, maxBackoff)
					}
				case <-ctx.Done():
					t.Fatal("waiting for delay:", ctx.Err())
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run = %v", err)
			}
			var warns, debugs int
			for _, line := range strings.Split(logs.String(), "\n") {
				if !strings.Contains(line, "hub rejected the node key; retrying") && !strings.Contains(line, "hub requires") {
					continue
				}
				switch {
				case strings.Contains(line, "level=WARN"):
					warns++
				case strings.Contains(line, "level=DEBUG"):
					debugs++
				}
			}
			if warns != 1 || debugs < 2 {
				t.Errorf("rejection logged %d times at WARN and %d at DEBUG, want 1 and 2 or more:\n%s", warns, debugs, logs.String())
			}
			if strings.Contains(logs.String(), "mm_node_secret") {
				t.Error("log contains the key")
			}
		})
	}
}

// The hub answers 403 when it refuses plain HTTP from a non-local network;
// that's a transport problem, and its message must reach the user as is.
func TestForbiddenIsNotKeyRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "plain HTTP is only accepted from local networks; use TLS", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	states := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Config{
			HubURL: "ws" + strings.TrimPrefix(server.URL, "http") + proto.NodePath, Node: "desk",
			Header: http.Header{"Authorization": {"Bearer mm_node_secret"}},
			Logger: slog.New(slog.DiscardHandler),
			OnConnState: func(_ bool, err error) {
				select {
				case states <- err:
				default:
				}
			},
		}, options{minBackoff: time.Second, maxBackoff: time.Second, resetAfter: time.Minute,
			jitter: func(d time.Duration) time.Duration { return d },
			wait:   func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }})
	}()
	select {
	case err := <-states:
		var rejected *KeyRejectedError
		if errors.As(err, &rejected) {
			t.Fatalf("403 reported as a key rejection: %v", err)
		}
		if !strings.Contains(err.Error(), "hub answered 403: plain HTTP is only accepted from local networks") {
			t.Fatalf("error lost the hub's message: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no connection state")
	}
	cancel()
	<-done
}
