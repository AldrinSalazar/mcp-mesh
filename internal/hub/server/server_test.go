package server

import "testing"

func TestOAuthUnreachable(t *testing.T) {
	for _, tt := range []struct {
		url  string
		want bool // a reason is given
	}{
		{"https://mesh.example.com/mcp", false},
		{"http://localhost:7002/mcp", false},
		{"http://127.0.0.1:7002/mcp", false},
		{"http://mesh.example.com:7002/mcp", true},
		{"https://192.168.1.10:7002/mcp", true},
		{"http://192.168.1.10:7002/mcp", true},
	} {
		if got := oauthUnreachable(tt.url); (got != "") != tt.want {
			t.Errorf("oauthUnreachable(%s) = %q", tt.url, got)
		}
	}
}
