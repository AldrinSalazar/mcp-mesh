package upstream

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestStandardHeaders(t *testing.T) {
	tests := []struct {
		method, params string
		name           string // Mcp-Name; "" means unset
	}{
		{"tools/list", `{}`, ""},
		{"tools/call", `{"name":"echo","arguments":{}}`, "echo"},
		{"prompts/get", `{"name":"greet"}`, "greet"},
		{"resources/read", `{"uri":"file:///a.txt"}`, "file:///a.txt"},
		{"notifications/cancelled", `{"requestId":3}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			h := http.Header{}
			setStandardHeaders(h, &jsonrpc.Request{Method: tt.method, Params: json.RawMessage(tt.params)})
			if got := h.Get("Mcp-Method"); got != tt.method {
				t.Errorf("Mcp-Method = %q", got)
			}
			if got := h.Get("Mcp-Name"); got != tt.name {
				t.Errorf("Mcp-Name = %q, want %q", got, tt.name)
			}
		})
	}
}
