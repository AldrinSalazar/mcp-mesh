package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// A stateless server sees each POST as its own session, so a call is only
// stopped by ending its HTTP request.
func TestHTTPCancelledCallEndsItsRequest(t *testing.T) {
	ctx := testContext(t)
	aStarted, bStarted := make(chan struct{}), make(chan struct{})
	aEnded, releaseB := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "initialize":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2026-07-28"}}`, req.ID)
		case req.Method == "tools/call" && string(req.ID) == "1":
			close(aStarted)
			<-r.Context().Done()
			close(aEnded)
		case req.Method == "tools/call":
			close(bStarted)
			select {
			case <-releaseB:
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
			case <-r.Context().Done():
			}
		case req.Method == "ping":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()
	conn, err := NewHTTP(HTTPConfig{URL: server.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	write := func(raw string) {
		t.Helper()
		msg, err := jsonrpc.DecodeMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(ch chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	readResponse := func(id any) {
		t.Helper()
		msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		want, _ := jsonrpc.MakeID(id)
		if resp, ok := msg.(*jsonrpc.Response); !ok || resp.ID != want {
			t.Fatalf("unexpected message %#v, want response %v", msg, id)
		}
	}
	write(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)
	readResponse(float64(0))
	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	write(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`)
	write(`{"jsonrpc":"2.0","id":"b","method":"tools/call","params":{}}`)
	wait(aStarted)
	wait(bStarted)
	write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	wait(aEnded)
	// Cancelling an unknown or finished call changes nothing.
	write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`)
	close(releaseB)
	readResponse("b")
	write(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	readResponse(float64(3))
}
