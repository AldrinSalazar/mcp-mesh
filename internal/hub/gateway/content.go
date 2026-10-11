package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompts, resources and templates follow the same sync as tools. They are
// granted per MCP (Options.VisibleMCP), not per item.

func (g *Gateway) syncPrompts(m *serverMirror, prompts []*catalog.Prompt) {
	current := make(map[string]string, len(prompts))
	for _, p := range prompts {
		if !g.opts.VisibleMCP(m.client, p.Node, p.MCP) {
			continue
		}
		exposed := p.Exposed()
		definition, ok := g.definition("prompt", p.Name, exposed)
		if !ok {
			continue
		}
		current[p.Name] = definition
		if old, ok := m.prompts[p.Name]; !ok || old != definition {
			m.server.AddPrompt(exposed, g.getPrompt(m))
		}
	}
	var removed []string
	for name := range m.prompts {
		if _, ok := current[name]; !ok {
			removed = append(removed, name)
		}
	}
	if len(removed) != 0 {
		m.server.RemovePrompts(removed...)
	}
	m.prompts = current
}

func (g *Gateway) syncResources(m *serverMirror, resources []*catalog.Resource, templates []*catalog.ResourceTemplate) {
	currentRes := make(map[string]string, len(resources))
	for _, r := range resources {
		if !g.opts.VisibleMCP(m.client, r.Node, r.MCP) {
			continue
		}
		exposed := r.Exposed()
		definition, ok := g.definition("resource", r.URI, exposed)
		if !ok {
			continue
		}
		if old, ok := m.resources[r.URI]; !ok || old != definition {
			if !g.addResource(m, exposed) {
				continue
			}
		}
		currentRes[r.URI] = definition
	}
	var removed []string
	for uri := range m.resources {
		if _, ok := currentRes[uri]; !ok {
			removed = append(removed, uri)
		}
	}
	if len(removed) != 0 {
		m.server.RemoveResources(removed...)
	}
	m.resources = currentRes

	currentTpl := make(map[string]string, len(templates))
	for _, t := range templates {
		if !g.opts.VisibleMCP(m.client, t.Node, t.MCP) {
			continue
		}
		exposed := t.Exposed()
		definition, ok := g.definition("resource template", t.URITemplate, exposed)
		if !ok {
			continue
		}
		if old, ok := m.templates[t.URITemplate]; !ok || old != definition {
			if !g.addTemplate(m, exposed) {
				continue
			}
		}
		currentTpl[t.URITemplate] = definition
	}
	removed = removed[:0]
	for tpl := range m.templates {
		if _, ok := currentTpl[tpl]; !ok {
			removed = append(removed, tpl)
		}
	}
	if len(removed) != 0 {
		m.server.RemoveResourceTemplates(removed...)
	}
	m.templates = currentTpl
}

// The SDK panics on a resource URI or template it can't parse, and one MCP's
// odd URI must not take the gateway down.

func (g *Gateway) addResource(m *serverMirror, r *mcp.Resource) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			g.opts.Logger.Warn("resource skipped", "uri", r.URI, "error", p)
			ok = false
		}
	}()
	m.server.AddResource(r, g.readResource(m))
	return true
}

func (g *Gateway) addTemplate(m *serverMirror, t *mcp.ResourceTemplate) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			g.opts.Logger.Warn("resource template skipped", "template", t.URITemplate, "error", p)
			ok = false
		}
	}()
	m.server.AddResourceTemplate(t, g.readResource(m))
	return true
}

// definition serializes an exposed item to compare it with what the server
// already has without retaining pointers to mutable values.
func (g *Gateway) definition(kind, key string, v any) (string, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		g.opts.Logger.Error("serialize exposed "+kind, "name", key, "error", err)
		return "", false
	}
	return string(data), true
}

func (g *Gateway) getPrompt(m *serverMirror) mcp.PromptHandler {
	return func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		m.inflight.Add(1)
		defer m.inflight.Add(-1)
		prompt, ok := g.opts.Catalog.LookupPrompt(req.Params.Name)
		if !ok || !g.opts.VisibleMCP(m.client, prompt.Node, prompt.MCP) {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown prompt %q", req.Params.Name)}
		}
		ctx = g.checkMCP(ctx, m, prompt.Node, prompt.MCP, func() error {
			return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown prompt %q", req.Params.Name)}
		})
		ctx, cancel := g.callContext(ctx, m, prompt.Timeout)
		defer cancel()
		result, err := prompt.Get(ctx, &mcp.GetPromptParams{Arguments: req.Params.Arguments})
		if err := g.upstreamError(m, prompt.Node, prompt.MCP, err); err != nil {
			return nil, err
		}
		return result, nil
	}
}

func (g *Gateway) readResource(m *serverMirror) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		m.inflight.Add(1)
		defer m.inflight.Add(-1)
		uri := req.Params.URI
		route, ok := g.opts.Catalog.ResolveResource(uri)
		// A hidden MCP's resources look like ones that don't exist.
		if !ok || !g.opts.VisibleMCP(m.client, route.Node, route.MCP) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		ctx = g.checkMCP(ctx, m, route.Node, route.MCP, func() error { return mcp.ResourceNotFoundError(uri) })
		ctx, cancel := g.callContext(ctx, m, route.Timeout)
		defer cancel()
		result, err := route.Reader.ReadResource(ctx, &mcp.ReadResourceParams{URI: route.URI})
		if err := g.upstreamError(m, route.Node, route.MCP, err); err != nil {
			return nil, err
		}
		// Contents carry the URI they came from; clients correlate them with
		// what they asked for, so they must name the exposed URI.
		out := *result
		out.Contents = make([]*mcp.ResourceContents, len(result.Contents))
		for i, c := range result.Contents {
			// The SDK keeps a JSON null entry as a nil pointer.
			if c == nil {
				return nil, fmt.Errorf("mcp-mesh: %s/%s sent an invalid resource: content entry %d is null", route.Node, route.MCP, i)
			}
			wrapped := *c
			if wrapped.URI == "" {
				wrapped.URI = route.URI
			}
			wrapped.URI = catalog.ResourceURI(route.Node, route.MCP, wrapped.URI)
			out.Contents[i] = &wrapped
		}
		return &out, nil
	}
}

// checkMCP re-checks the client's access to the MCP once the request is
// about to be sent (see forward); hidden answers what a denied request says.
func (g *Gateway) checkMCP(ctx context.Context, m *serverMirror, node, mcpName string, hidden func() error) context.Context {
	return catalog.WithCheck(ctx, func(catalog.Listed) error {
		if !g.opts.VisibleMCP(m.client, node, mcpName) {
			return hidden()
		}
		return nil
	})
}

// callContext bounds a prompt or read by the MCP's timeout (else the hub's)
// and by the client's disconnection. The caller must call cancel.
func (g *Gateway) callContext(ctx context.Context, m *serverMirror, perMCP time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout(perMCP))
	stop := context.AfterFunc(m.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

// upstreamError turns an upstream failure into what the client is told:
// protocol errors pass through with their code and data, a disconnected
// client gets the revocation message, anything else says which MCP is
// unavailable.
func (g *Gateway) upstreamError(m *serverMirror, node, mcpName string, err error) error {
	if err == nil {
		return nil
	}
	if m.ctx.Err() != nil {
		return context.Cause(m.ctx)
	}
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	return fmt.Errorf("mcp-mesh: %s/%s is unavailable: %w", node, mcpName, err)
}
