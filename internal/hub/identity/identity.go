// Package identity carries who is calling through a request's context.
//
// The gateway attaches a Client to every MCP request; routing reads it to
// pick a channel (per-client MCPs) and the audit log records it. With auth
// off every request is Anonymous; with auth on it is the client key (or the
// key behind an OAuth token).
package identity

import "context"

// Client is the caller of a tool.
type Client struct {
	// ID is stable for one client key. Per-client MCP channels are keyed by it.
	ID string
	// Name is for display (admin UI, logs).
	Name string
}

// Anonymous is the client used when auth is off.
var Anonymous = Client{ID: "anonymous", Name: "anonymous"}

type key struct{}

// With returns ctx carrying c.
func With(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, key{}, c)
}

// From returns the client in ctx, or Anonymous.
func From(ctx context.Context) Client {
	if c, ok := ctx.Value(key{}).(Client); ok {
		return c
	}
	return Anonymous
}
