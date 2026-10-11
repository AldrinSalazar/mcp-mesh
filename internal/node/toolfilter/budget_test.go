package toolfilter

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func budgetPage(n int, next string, pad int) string {
	tools := make([]string, n)
	for i := range tools {
		tools[i] = fmt.Sprintf(`{"name":"t","description":%q}`, strings.Repeat("x", pad))
	}
	res := `"result":{"tools":[` + strings.Join(tools, ",") + `]`
	if next != "" {
		res += fmt.Sprintf(`,"nextCursor":%q`, next)
	}
	return res + `}`
}

func listTools(t *testing.T, result json.RawMessage) json.RawMessage {
	t.Helper()
	var r struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatal(err)
	}
	return r.Tools
}

func TestListBudget(t *testing.T) {
	uniqueCursor := func(i int) string { return fmt.Sprintf("c%d", i) }
	tests := []struct {
		name      string
		opts      Options
		pages     int  // pages sent
		endless   bool // the last page still carries a nextCursor
		perPage   int
		pad       int
		cursor    func(i int) string
		wantCalls int
	}{
		{name: "normal", pages: 3, perPage: 2, cursor: uniqueCursor, wantCalls: 1},
		{name: "page budget", opts: Options{MaxPages: 5}, pages: 12, endless: true, perPage: 1, cursor: uniqueCursor},
		{name: "item budget", opts: Options{MaxItems: 5}, pages: 3, perPage: 3, cursor: uniqueCursor},
		{name: "byte budget", opts: Options{MaxBytes: 500}, pages: 3, perPage: 2, pad: 200, cursor: uniqueCursor},
		{name: "repeated cursor", pages: 4, endless: true, perPage: 1, cursor: func(i int) string { return fmt.Sprintf("c%d", i%2) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testContext(t)
			s := newStub(t)
			var calls int
			var last json.RawMessage
			var logs strings.Builder
			tt.opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			tt.opts.Allow = []string{"t"}
			tt.opts.OnToolList = func(raw json.RawMessage) { calls++; last = raw }
			c := wrapStub(t, s, tt.opts)
			cursor := ""
			for i := 0; i < tt.pages; i++ {
				writeMessage(t, ctx, c, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{"cursor":%q}}`, i+1, cursor))
				next := ""
				if tt.endless || i < tt.pages-1 {
					next = tt.cursor(i)
				}
				s.incoming <- event{msg: decode(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,%s}`, i+1, budgetPage(tt.perPage, next, tt.pad)))}
				resp := readMessage(t, ctx, c).(*jsonrpc.Response)
				if resp.Error != nil || len(toolNames(t, listTools(t, resp.Result))) != tt.perPage {
					t.Fatalf("page %d not relayed intact: %s %v", i, resp.Result, resp.Error)
				}
				cursor = next
			}
			if calls != tt.wantCalls {
				t.Fatalf("onToolList calls = %d, want %d", calls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && len(toolNames(t, last)) != tt.pages*tt.perPage {
				t.Fatalf("incomplete list: %s", last)
			}
			wantWarn := 1
			if tt.wantCalls == 1 {
				wantWarn = 0
			}
			if got := strings.Count(logs.String(), "abandoned"); got != wantWarn {
				t.Fatalf("warnings = %d, want %d: %s", got, wantWarn, logs.String())
			}
		})
	}
}
