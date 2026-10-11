package catalog

import (
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestListedCheck(t *testing.T) {
	approved := &mcp.Tool{Name: "t", Description: "v1", InputSchema: map[string]any{"type": "object"}}
	entry := &Tool{Name: "n__m__t", Upstream: approved}
	same := &mcp.Tool{Name: "t", Description: "v1", InputSchema: map[string]any{"type": "object"}}
	changed := &mcp.Tool{Name: "t", Description: "v2", InputSchema: map[string]any{"type": "object"}}
	listing := func(tools ...*mcp.Tool) Listed {
		return func(name string) (*mcp.Tool, bool) {
			for _, t := range tools {
				if t.Name == name {
					return t, true
				}
			}
			return nil, false
		}
	}
	tests := []struct {
		name   string
		listed Listed
		want   error
	}{
		{"shared session", nil, nil},
		{"equal definition", listing(same), nil},
		{"changed definition", listing(changed), ErrSessionDiffers},
		{"not listed", listing(), ErrSessionDiffers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.listed.Check(entry); !errors.Is(err, tt.want) {
				t.Errorf("Check = %v, want %v", err, tt.want)
			}
		})
	}
}
