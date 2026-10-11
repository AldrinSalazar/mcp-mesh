package catalog

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type checkKey struct{}

// Listed looks a tool up, by its upstream name, in what the session a call
// is about to be sent on listed. A nil Listed means that session's listing
// is the catalog's own (a shared MCP's).
type Listed func(upstreamName string) (*mcp.Tool, bool)

// ErrSessionDiffers means the session a call was routed to lists another
// definition of the tool than the catalog holds, so whatever the catalog's
// definition was approved or granted for says nothing about what would run.
var ErrSessionDiffers = errors.New("the session's tool definition differs from the catalog's")

// Check reports ErrSessionDiffers unless the session listed t's definition
// as it is. Admission checks call it with the catalog's current entry.
func (l Listed) Check(t *Tool) error {
	if l == nil {
		return nil
	}
	if def, ok := l(t.Upstream.Name); !ok || !SameTool(def, t.Upstream) {
		return ErrSessionDiffers
	}
	return nil
}

// SameTool reports whether two definitions are equal as JSON.
func SameTool(a, b *mcp.Tool) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// WithCheck attaches an admission check to a call's context. The Caller runs
// it (RunCheck) once the MCP is ready and the call holds its slot, right
// before sending: a lazy start or a wait in the queue can change the tool's
// definition or the client's access after the gateway first checked them.
// The check gets what the selected session listed, so it can compare that
// with the catalog in the same read (Listed.Check).
func WithCheck(ctx context.Context, check func(Listed) error) context.Context {
	return context.WithValue(ctx, checkKey{}, check)
}

// RunCheck runs the context's admission check; nil when there is none.
func RunCheck(ctx context.Context, listed Listed) error {
	if check, ok := ctx.Value(checkKey{}).(func(Listed) error); ok && check != nil {
		return check(listed)
	}
	return nil
}
