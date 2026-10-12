package agent

import (
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

const pairedKey = "mm_node_paired_secret"

// fakeRequest is one pairing request the fake hub holds; the test decides
// it. (Node packages don't import the hub's: this stands in for it.)
type fakeRequest struct {
	proto.Pair
	Code   string
	decide chan *proto.Paired // nil declines
}

// pairingHub serves /node, which wants pairedKey, and a pairing endpoint
// that answers each request with a code and waits for the test, unless it
// predates pairing (404).
type pairingHub struct {
	url      string
	hellos   chan *proto.Hello
	requests chan *fakeRequest
}

func newPairingHub(t *testing.T, withPairing bool) *pairingHub {
	t.Helper()
	h := &pairingHub{hellos: make(chan *proto.Hello, 4), requests: make(chan *fakeRequest, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+proto.NodePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pairedKey {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		conn, err := wire.Accept(w, r, 0)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		e, err := conn.Recv(r.Context())
		if err != nil || e.Hello == nil {
			return
		}
		h.hellos <- e.Hello
		conn.Send(r.Context(), &proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{Version: proto.Version, NodeID: "n1", MaxMessageSize: proto.DefaultMaxMessageSize}})
		for {
			if _, err := conn.Recv(r.Context()); err != nil {
				return
			}
		}
	})
	if withPairing {
		mux.HandleFunc("GET "+proto.PairPath, func(w http.ResponseWriter, r *http.Request) {
			conn, err := wire.Accept(w, r, 0)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			e, err := conn.Recv(r.Context())
			if err != nil || e.T != proto.TPair || e.Pair == nil {
				return
			}
			req := &fakeRequest{Pair: *e.Pair, Code: "ABC-DEF", decide: make(chan *proto.Paired, 1)}
			if err := conn.Send(r.Context(), &proto.Envelope{T: proto.TPairing, Pairing: &proto.Pairing{Code: req.Code}}); err != nil {
				return
			}
			h.requests <- req
			select {
			case grant := <-req.decide:
				if grant == nil {
					conn.Close(proto.CloseDenied, "an admin declined this machine")
					return
				}
				conn.Send(r.Context(), &proto.Envelope{T: proto.TPaired, Paired: grant})
				conn.Recv(r.Context()) // until the node closes
			case <-r.Context().Done():
			}
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	h.url = "ws" + strings.TrimPrefix(server.URL, "http") + proto.NodePath
	return h
}

func (h *pairingHub) waitRequest(t *testing.T, ctx context.Context) *fakeRequest {
	t.Helper()
	select {
	case req := <-h.requests:
		return req
	case <-ctx.Done():
		t.Fatal("no pairing request:", ctx.Err())
		return nil
	}
}

type pairRecorder struct {
	mu    sync.Mutex
	codes []string
	saved [][2]string
}

func (r *pairRecorder) pairing() *Pairing {
	return &Pairing{
		Hostname: "desk.lan",
		OnCode: func(code string) {
			r.mu.Lock()
			r.codes = append(r.codes, code)
			r.mu.Unlock()
		},
		Save: func(node, key string) error {
			r.mu.Lock()
			r.saved = append(r.saved, [2]string{node, key})
			r.mu.Unlock()
			return nil
		},
	}
}

func runAgent(ctx context.Context, cfg Config) chan error {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, options{minBackoff: time.Millisecond, maxBackoff: 20 * time.Millisecond, resetAfter: time.Minute,
			jitter: func(d time.Duration) time.Duration { return d }, wait: wait})
	}()
	return done
}

// A node without a key asks to pair, shows the code, and once approved
// saves the key and connects with it under the name the admin chose.
func TestPairingApproved(t *testing.T) {
	hub := newPairingHub(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	rec := &pairRecorder{}
	connected := make(chan struct{}, 1)
	done := runAgent(ctx, Config{HubURL: hub.url, Node: "desk", Version: "1.2.0", Pairing: rec.pairing(),
		OnConnState: func(ok bool, _ error) {
			if ok {
				connected <- struct{}{}
			}
		}})
	req := hub.waitRequest(t, ctx)
	if req.Node != "desk" || req.Hostname != "desk.lan" || req.NodeVersion != "1.2.0" {
		t.Fatalf("request = %+v", req)
	}
	// The node shows the code while it waits.
	for {
		rec.mu.Lock()
		codes := append([]string(nil), rec.codes...)
		rec.mu.Unlock()
		if len(codes) > 0 {
			if len(codes) != 1 || codes[0] != req.Code {
				t.Fatalf("node showed codes %q, want the request's %q", codes, req.Code)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the node didn't show the code")
		case <-time.After(10 * time.Millisecond):
		}
	}
	req.decide <- &proto.Paired{Node: "studio", Key: pairedKey}
	select {
	case hello := <-hub.hellos:
		if hello.Node != "studio" {
			t.Fatalf("hello as %q, want the approved name studio", hello.Node)
		}
	case <-ctx.Done():
		t.Fatal("the node didn't connect with its new key")
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("no connected state")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.saved) != 1 || rec.saved[0] != [2]string{"studio", pairedKey} {
		t.Fatalf("saved %q, want studio and the key", rec.saved)
	}
	if last := rec.codes[len(rec.codes)-1]; last != "" {
		t.Fatalf("code %q still shown after pairing", last)
	}
	cancel()
	<-done
}

// A declined node reports it and doesn't ask again.
func TestPairingDenied(t *testing.T) {
	hub := newPairingHub(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	states := make(chan error, 16)
	done := runAgent(ctx, Config{HubURL: hub.url, Node: "desk", Pairing: (&pairRecorder{}).pairing(),
		OnConnState: func(_ bool, err error) {
			select {
			case states <- err:
			default:
			}
		}})
	req := hub.waitRequest(t, ctx)
	req.decide <- nil
	for i := 0; i < 3; i++ {
		select {
		case err := <-states:
			var rejected *KeyRejectedError
			if !errors.As(err, &rejected) || !errors.Is(rejected.Pairing, ErrPairingDenied) {
				t.Fatalf("state %d = %v, want a declined pairing", i, err)
			}
			if rejected.Short() != "pairing declined" || !strings.Contains(err.Error(), "restart the node to ask again") {
				t.Fatalf("error %q (short %q) doesn't say what happened", err, rejected.Short())
			}
		case <-ctx.Done():
			t.Fatal("no state:", ctx.Err())
		}
	}
	select {
	case again := <-hub.requests:
		t.Fatalf("a declined node asked again: %+v", again)
	default:
	}
	cancel()
	<-done
}

func TestPairingUnavailable(t *testing.T) {
	for _, test := range []struct {
		name        string
		withPairing bool
		refused     error
		want        string
	}{
		{"old hub", false, nil, "doesn't accept pairing requests"},
		{"refused", true, errors.New("ws:// would send the node key unencrypted"), "unencrypted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newPairingHub(t, test.withPairing)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			states := make(chan error, 16)
			p := (&pairRecorder{}).pairing()
			p.Refused = test.refused
			done := runAgent(ctx, Config{HubURL: hub.url, Node: "desk", Pairing: p,
				OnConnState: func(_ bool, err error) {
					select {
					case states <- err:
					default:
					}
				}})
			select {
			case err := <-states:
				var rejected *KeyRejectedError
				if !errors.As(err, &rejected) || rejected.Pairing == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("state = %v, want a pairing failure mentioning %q", err, test.want)
				}
				if !strings.Contains(err.Error(), "mcp-mesh-hub key create node desk") {
					t.Fatalf("error %q doesn't say how to get a key instead", err)
				}
			case <-ctx.Done():
				t.Fatal("no state:", ctx.Err())
			}
			if test.refused != nil && len(hub.requests) != 0 {
				t.Fatal("a refused node asked to pair")
			}
			cancel()
			<-done
		})
	}
}

func TestPairURL(t *testing.T) {
	for in, want := range map[string]string{
		"ws://hub.lan:7001/node":        "ws://hub.lan:7001/node/pair",
		"wss://hub.example.com/node/":   "wss://hub.example.com/node/pair",
		"wss://proxy.example/mesh/node": "wss://proxy.example/mesh/node/pair",
	} {
		if got, err := PairURL(in); err != nil || got != want {
			t.Errorf("PairURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
