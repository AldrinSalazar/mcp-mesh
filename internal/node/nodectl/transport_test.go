package nodectl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestDetectTransport(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		want                    proto.Transport
	}{
		{"HTTP+SSE app", "text/event-stream", "event: endpoint\ndata: /messages\n\n", 200, proto.TransportSSE},
		{"comments", "text/event-stream; charset=utf-8", ": heartbeat\n\nevent: endpoint\ndata: /messages\n\n", 200, proto.TransportSSE},
		{"message", "text/event-stream", "event: message\ndata: {}\n\n", 200, proto.TransportHTTP},
		{"json", "application/json", "{}", 200, proto.TransportHTTP},
		{"requires session", "application/json", "{}", 400, proto.TransportHTTP},
		{"GET unsupported", "text/plain", "", 405, proto.TransportHTTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ended := make(chan struct{})
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(ended)
				if r.Method != "GET" || r.Header.Get("X-Test") != "present" || r.Header.Get("Accept") != "text/event-stream" {
					t.Errorf("unexpected request: %s %v", r.Method, r.Header)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
				w.(http.Flusher).Flush()
				if tc.want == proto.TransportSSE {
					<-r.Context().Done()
				}
			}))
			defer hs.Close()
			n := &Node{}
			got, err := n.DetectTransport(t.Context(), hs.URL, map[string]string{"X-Test": "present"})
			if err != nil || got != tc.want {
				t.Fatalf("detect: %q %v", got, err)
			}
			select {
			case <-ended:
			case <-time.After(3 * time.Second):
				t.Fatal("GET not closed")
			}
		})
	}
}

func TestDetectTransportBounded(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if oversized {
					io.WriteString(w, "data: "+strings.Repeat("x", 70<<10)+"\n\n")
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer hs.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			got, err := (&Node{}).DetectTransport(ctx, hs.URL, nil)
			if oversized && err == nil {
				t.Fatal("unbounded probe succeeded")
			}
			// A stream that stays silent is a Streamable HTTP GET stream.
			if !oversized && (err != nil || got != proto.TransportHTTP) {
				t.Fatalf("silent stream: %q %v", got, err)
			}
		})
	}
}

func TestDetectRefusedAndFallback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Node{}).DetectTransport(t.Context(), endpoint, nil); !errors.Is(err, upstream.ErrUnreachable) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want proto.Transport
	}{
		{"/sse", proto.TransportSSE}, {"/mcp/sse?token=abc", proto.TransportSSE}, {"/sse/", proto.TransportHTTP}, {"/mcp", proto.TransportHTTP},
	} {
		if got, _ := TransportFallback(endpoint + tc.path); got != tc.want {
			t.Fatalf("%s: %s", tc.path, got)
		}
	}
}
