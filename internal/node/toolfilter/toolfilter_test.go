package toolfilter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
)

type connTransport struct{ conn mcp.Connection }

func (t connTransport) Connect(context.Context) (mcp.Connection, error) { return t.conn, nil }

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func connectClient(t *testing.T, ctx context.Context, opts Options) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "filter-server", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"a", "b", "c"} {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: req.Params.Name}}}, nil
		})
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	conn, err := ct.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := Wrap(conn, opts)
	t.Cleanup(func() { _ = wrapped.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "filter-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, connTransport{wrapped}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var tools []struct{ Name string }
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestClientPagination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow []string
		want  []string
	}{
		{"restricted", []string{"a", "c"}, []string{"a", "c"}},
		{"nil", nil, []string{"a", "b", "c"}},
		{"empty", []string{}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testContext(t)
			observed := make(chan json.RawMessage, 4)
			cs := connectClient(t, ctx, Options{Allow: tc.allow, OnToolList: func(raw json.RawMessage) { observed <- raw }})
			for range 2 {
				names := []string{}
				cursor := ""
				pages := 0
				for {
					page, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
					if err != nil {
						t.Fatal(err)
					}
					pages++
					for _, tool := range page.Tools {
						names = append(names, tool.Name)
					}
					cursor = page.NextCursor
					if cursor == "" {
						break
					}
					select {
					case raw := <-observed:
						t.Fatalf("observed partial listing: %s", raw)
					default:
					}
				}
				if pages != 3 || !reflect.DeepEqual(names, tc.want) {
					t.Fatalf("%d pages, names %v; want 3 pages, %v", pages, names, tc.want)
				}
				select {
				case raw := <-observed:
					if got := toolNames(t, raw); !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("observed %s; want %v", raw, tc.want)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case raw := <-observed:
					t.Fatalf("duplicate observation: %s", raw)
				default:
				}
			}
			if len(tc.want) > 0 {
				result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.want[0]})
				if err != nil {
					t.Fatal(err)
				}
				if result.IsError || result.Content[0].(*mcp.TextContent).Text != tc.want[0] {
					t.Fatalf("allowed call = %+v", result)
				}
			}
		})
	}
}

func TestCallErrorShape(t *testing.T) {
	ctx := testContext(t)
	cs := connectClient(t, ctx, Options{Allow: []string{"a", "missing"}})
	var wireErrors []*jsonrpc.Error
	for _, name := range []string{"b", "hidden-missing", "missing"} {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name})
		var wireErr *jsonrpc.Error
		if !errors.As(err, &wireErr) {
			t.Fatalf("%s error = %v", name, err)
		}
		if wireErr.Code != -32602 || wireErr.Data != nil {
			t.Fatalf("%s error = %+v", name, wireErr)
		}
		if name != "missing" && wireErr.Message != "Unknown tool: "+name {
			t.Fatalf("message = %q", wireErr.Message)
		}
		wireErrors = append(wireErrors, wireErr)
	}
	// The SDK spells the missing-tool message differently. All errors must
	// still have the same wire fields, with no data that reveals tool existence.
	for _, wireErr := range wireErrors {
		raw, err := json.Marshal(wireErr)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if len(fields) != 2 || fields["code"] == nil || fields["message"] == nil {
			t.Fatalf("error shape = %s", raw)
		}
	}
}

type stubConn struct {
	incoming chan event
	entered  chan struct{}
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	writes   []jsonrpc.Message
	writeErr error
	closeErr error
}

func newStub(t *testing.T) *stubConn {
	t.Helper()
	s := &stubConn{incoming: make(chan event, 16), entered: make(chan struct{}, 16), closed: make(chan struct{})}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func (s *stubConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	s.entered <- struct{}{}
	select {
	case e := <-s.incoming:
		return e.msg, e.err
	case <-s.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stubConn) Write(_ context.Context, msg jsonrpc.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, msg)
	return s.writeErr
}

func (s *stubConn) Close() error      { s.once.Do(func() { close(s.closed) }); return s.closeErr }
func (s *stubConn) SessionID() string { return "stub-session" }

func wrapStub(t *testing.T, s *stubConn, opts Options) mcp.Connection {
	t.Helper()
	c := Wrap(s, opts)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func decode(t *testing.T, raw string) jsonrpc.Message {
	t.Helper()
	msg, err := jsonrpc.DecodeMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func readMessage(t *testing.T, ctx context.Context, c mcp.Connection) jsonrpc.Message {
	t.Helper()
	msg, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func writeMessage(t *testing.T, ctx context.Context, c mcp.Connection, raw string) {
	t.Helper()
	if err := c.Write(ctx, decode(t, raw)); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticResponseWhileUpstreamSilent(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{Allow: []string{}})
	readDone := make(chan jsonrpc.Message, 1)
	go func() {
		msg, err := c.Read(ctx)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		readDone <- msg
	}()
	select {
	case <-s.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	writeMessage(t, ctx, c, `{"jsonrpc":"2.0","id":"hub-id","method":"tools/call","params":{"name":"b"}}`)
	select {
	case msg := <-readDone:
		raw, err := jsonrpc.EncodeMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"jsonrpc":"2.0","id":"hub-id","error":{"code":-32602,"message":"Unknown tool: b"}}`
		if string(raw) != want {
			t.Fatalf("synthetic response = %s", raw)
		}
	case <-ctx.Done():
		t.Fatal("synthetic response blocked behind upstream")
	}
	if len(s.writes) != 0 {
		t.Fatalf("forwarded disallowed call: %v", s.writes)
	}
}

func TestResultPreservationAndPassThrough(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{Allow: []string{"a"}})
	writeMessage(t, ctx, c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	const tools = `[ { "name": "a", "unknown": 1.00 }, {"name":"b"} ]`
	result := `{ "_meta" : { "float": 1.00 }, "tools" : ` + tools + `, "nextCursor": "more", "future": [ 3, 4 ] }`
	resp := decode(t, `{"jsonrpc":"2.0","id":1,"result":`+result+`}`).(*jsonrpc.Response)
	resp.Extra = "metadata"
	s.incoming <- event{msg: resp}
	got := readMessage(t, ctx, c).(*jsonrpc.Response)
	want := strings.Replace(result, tools, `[{ "name": "a", "unknown": 1.00 }]`, 1)
	if string(got.Result) != want || got.Extra != resp.Extra || string(resp.Result) != result {
		t.Fatalf("result changed beyond tools: %s", got.Result)
	}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"unknown":1}}`,
		`{"jsonrpc":"2.0","id":"1","result":{"tools":[{"name":"b"}]}}`,
		`{"jsonrpc":"2.0","id":9,"error":{"code":-32000,"message":"error"}}`,
		`{"jsonrpc":"2.0","id":10,"method":"server/request","params":{"unknown":true}}`,
	} {
		msg := decode(t, raw)
		s.incoming <- event{msg: msg}
		if readMessage(t, ctx, c) != msg {
			t.Fatalf("pass-through message changed: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"a","unknown":true}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"b"}}`,
	} {
		msg := decode(t, raw)
		if err := c.Write(ctx, msg); err != nil {
			t.Fatal(err)
		}
		if s.writes[len(s.writes)-1] != msg {
			t.Fatalf("outgoing message changed: %s", raw)
		}
	}
}

func TestReadErrorIdentityAndDelegation(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{OnToolList: func(json.RawMessage) {}})
	want := fmt.Errorf("lifecycle: %w", upstream.ErrIdle)
	s.incoming <- event{err: want}
	for range 2 {
		if _, err := c.Read(ctx); err != want || !errors.Is(err, upstream.ErrIdle) {
			t.Fatalf("read error = %v", err)
		}
	}
	s.writeErr = errors.New("write failed")
	if err := c.Write(ctx, decode(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); err != s.writeErr {
		t.Fatalf("write error = %v", err)
	}
	if c.SessionID() != s.SessionID() {
		t.Fatal("session ID changed")
	}
	s.closeErr = errors.New("close failed")
	if err := c.Close(); err != s.closeErr {
		t.Fatalf("close error = %v", err)
	}
	if err := c.Close(); err != s.closeErr {
		t.Fatalf("repeated close error = %v", err)
	}
}

func TestCloseUnblocksRead(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{Allow: []string{}})
	readDone := make(chan error, 1)
	go func() { _, err := c.Read(ctx); readDone <- err }()
	select {
	case <-s.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- c.Close() }()
	}
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("Close blocked")
		}
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("Read succeeded after Close")
		}
	case <-ctx.Done():
		t.Fatal("Read blocked after Close")
	}
}

func TestWrapNoOptions(t *testing.T) {
	s := newStub(t)
	if Wrap(s, Options{}) != s {
		t.Fatal("no-options wrapper changed connection")
	}
}

func TestCollectionRestartAndErrors(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	observed := make(chan json.RawMessage, 8)
	c := wrapStub(t, s, Options{Allow: []string{"a", "c"}, OnToolList: func(raw json.RawMessage) { observed <- raw }})
	request := func(id int, cursor string) {
		t.Helper()
		writeMessage(t, ctx, c, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{"cursor":%q}}`, id, cursor))
	}
	response := func(id int, payload string) jsonrpc.Message {
		t.Helper()
		msg := decode(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,%s}`, id, payload))
		s.incoming <- event{msg: msg}
		return readMessage(t, ctx, c)
	}
	noObservation := func() {
		t.Helper()
		select {
		case raw := <-observed:
			t.Fatalf("unexpected observation: %s", raw)
		default:
		}
	}
	request(1, "")
	response(1, `"result":{"tools":[{"name":"a"}],"nextCursor":"old"}`)
	request(2, "old")
	request(3, "")
	response(3, `"result":{"tools":[{"name":"b"},{"name":"c"}],"nextCursor":"new"}`)
	response(2, `"result":{"tools":[{"name":"a"}]}`)
	noObservation()
	request(4, "new")
	response(4, `"result":{"tools":[{"name":"a"}]}`)
	select {
	case raw := <-observed:
		if got := toolNames(t, raw); !reflect.DeepEqual(got, []string{"c", "a"}) {
			t.Fatalf("restarted collection = %s", raw)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	noObservation()
	request(5, "")
	errResponse := response(5, `"error":{"code":-32000,"message":"failed"}`)
	if errResponse.(*jsonrpc.Response).Error == nil {
		t.Fatal("error response changed")
	}
	request(6, "new")
	response(6, `"result":{"tools":[{"name":"a"}]}`)
	noObservation()
	request(7, "")
	response(7, `"result":{"tools":{}}`)
	noObservation()
	request(8, "")
	response(8, `"result":{"tools":[]}`)
	select {
	case raw := <-observed:
		if string(raw) != "[]" {
			t.Fatalf("empty collection = %s", raw)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestNilAllowPreservesListAndCalls(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	observed := make(chan json.RawMessage, 1)
	c := wrapStub(t, s, Options{OnToolList: func(raw json.RawMessage) { observed <- raw }})
	writeMessage(t, ctx, c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	msg := decode(t, `{"jsonrpc":"2.0","id":1,"result":{ "tools" : [ { "name" : "b" } ], "unknown" : 1.00 }}`)
	s.incoming <- event{msg: msg}
	if got := readMessage(t, ctx, c); got != msg {
		t.Fatal("nil allow changed list response")
	}
	select {
	case raw := <-observed:
		if string(raw) != `[{ "name" : "b" }]` {
			t.Fatalf("observation = %s", raw)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	call := decode(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"anything"}}`)
	if err := c.Write(ctx, call); err != nil {
		t.Fatal(err)
	}
	if s.writes[len(s.writes)-1] != call {
		t.Fatal("nil allow changed call")
	}
}

func TestConcurrentRejectedCalls(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{Allow: []string{}})
	const calls = 32
	written := make(chan error, calls)
	for i := range calls {
		msg := decode(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"hidden"}}`, i))
		go func() { written <- c.Write(ctx, msg) }()
	}
	seen := make(map[jsonrpc.ID]bool)
	for range calls {
		resp := readMessage(t, ctx, c).(*jsonrpc.Response)
		if resp.Error == nil || seen[resp.ID] {
			t.Fatalf("duplicate or successful response: %+v", resp)
		}
		seen[resp.ID] = true
	}
	for range calls {
		select {
		case err := <-written:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if len(s.writes) != 0 {
		t.Fatal("rejected calls reached upstream")
	}
}

func TestCanceledReadDoesNotLoseMessages(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{Allow: []string{}})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Read(canceled); err != context.Canceled {
		t.Fatalf("canceled Read = %v", err)
	}
	msg := decode(t, `{"jsonrpc":"2.0","method":"notifications/unchanged"}`)
	s.incoming <- event{msg: msg}
	if readMessage(t, ctx, c) != msg {
		t.Fatal("message lost after canceled Read")
	}
}

type blockedWriter struct {
	*stubConn
	started chan struct{}
	release chan struct{}
}

func (s *blockedWriter) Write(ctx context.Context, msg jsonrpc.Message) error {
	close(s.started)
	select {
	case <-s.release:
		return s.stubConn.Write(ctx, msg)
	case <-s.closed:
		return mcp.ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRejectedCallWhileUpstreamWriteBlocked(t *testing.T) {
	ctx := testContext(t)
	s := &blockedWriter{stubConn: newStub(t), started: make(chan struct{}), release: make(chan struct{})}
	c := Wrap(s, Options{Allow: []string{"a"}})
	t.Cleanup(func() { _ = c.Close() })
	allowed := decode(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a"}}`)
	written := make(chan error, 1)
	go func() { written <- c.Write(ctx, allowed) }()
	select {
	case <-s.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	disallowed := decode(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"b"}}`)
	rejected := make(chan error, 1)
	go func() { rejected <- c.Write(ctx, disallowed) }()
	select {
	case err := <-rejected:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("rejected Write blocked behind upstream Write")
	}
	resp := readMessage(t, ctx, c).(*jsonrpc.Response)
	if resp.ID != disallowed.(*jsonrpc.Request).ID || resp.Error == nil {
		t.Fatalf("rejected call response = %+v", resp)
	}
	close(s.release)
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestFailsClosedWithAllowlist(t *testing.T) {
	ctx := testContext(t)
	s := newStub(t)
	c := wrapStub(t, s, Options{Allow: []string{"a"}})

	// A call whose tool name can't be read is rejected, not forwarded.
	for i, params := range []string{`{"invalid":true}`, `{"name":null}`, `{"name":7}`} {
		writes := len(s.writes)
		writeMessage(t, ctx, c, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":%s}`, 100+i, params))
		if len(s.writes) != writes {
			t.Fatalf("call with params %s was forwarded", params)
		}
		resp, ok := readMessage(t, ctx, c).(*jsonrpc.Response)
		var rpcErr *jsonrpc.Error
		if !ok || !errors.As(resp.Error, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("params %s: got %+v, want invalid-params error", params, resp)
		}
	}

	// A list result that can't be filtered unambiguously becomes an error.
	for i, result := range []string{
		`{"tools":[{"name":"a"}],"tools":[{"name":"secret"}]}`,
		`{"tools":{"name":"secret"}}`,
		`{"nextCursor":"x"}`,
	} {
		id := 200 + i
		writeMessage(t, ctx, c, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list"}`, id))
		s.incoming <- event{msg: decode(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, result))}
		resp, ok := readMessage(t, ctx, c).(*jsonrpc.Response)
		if !ok || resp.Error == nil || resp.Result != nil {
			t.Fatalf("result %s: got %+v, want an error", result, resp)
		}
	}
}
