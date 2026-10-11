// Package catalog holds what every connected MCP offers: tools, prompts,
// resources and resource templates, under the names clients see.
//
// The node registry writes to it (Set, RemoveMCP, RemoveNode) as MCPs come
// and go; the downstream gateway reads from it and watches Changes to keep
// each client's MCP server in sync. Access control and approvals are filters
// between the catalog and each client's view (gateway.Options.Visible).
//
// Exposed names are recomputed from scratch on every change, so they depend
// only on what is in the catalog and on the overrides, never on the order
// MCPs arrived in.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxNameLen is the longest exposed tool name; many clients reject longer.
const MaxNameLen = 64

// Caller calls a tool on one upstream MCP. *mcp.ClientSession implements it.
type Caller interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

// PromptGetter and ResourceReader are the Caller's optional other halves:
// an MCP's prompts and resources need them (*mcp.ClientSession has both).
type PromptGetter interface {
	GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error)
}

type ResourceReader interface {
	ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error)
}

// Content is everything one MCP offers.
type Content struct {
	Tools             []*mcp.Tool
	Prompts           []*mcp.Prompt
	Resources         []*mcp.Resource
	ResourceTemplates []*mcp.ResourceTemplate
	// Timeout bounds one call, prompt or read on this MCP; zero means the
	// hub default.
	Timeout time.Duration
}

// ToolKey identifies a tool by where it comes from.
type ToolKey struct{ Node, MCP, Tool string }

// Override changes how one tool is shown to clients: an alias replacing the
// generated name, and/or a description replacing the MCP's. Empty fields
// keep the original.
type Override struct {
	Name        string
	Description string
}

// Tool is one tool in the merged catalog.
type Tool struct {
	// Name is the exposed name, unique among tools.
	Name string
	Node string
	MCP  string
	// Upstream is the tool as the MCP reported it, with its original name.
	Upstream *mcp.Tool
	// Caller reaches the MCP that owns the tool.
	Caller Caller
	// Timeout is the MCP's (zero: the hub default).
	Timeout time.Duration
	// Aliased says Name came from an override; Description, when not empty,
	// replaces the upstream description.
	Aliased     bool
	Description string
}

// Exposed returns the tool as clients see it: exposed name and a description
// prefixed with the machine and MCP it acts on.
func (t *Tool) Exposed() *mcp.Tool {
	out := *t.Upstream
	out.Name = t.Name
	desc := out.Description
	if t.Description != "" {
		desc = t.Description
	}
	out.Description = withPrefix(t.Node, t.MCP, desc)
	return &out
}

// Call forwards a call to the upstream MCP using the original tool name.
func (t *Tool) Call(ctx context.Context, arguments any) (*mcp.CallToolResult, error) {
	return t.CallWith(ctx, &mcp.CallToolParams{Arguments: arguments})
}

// CallWith forwards a call, keeping params' other fields (such as _meta for
// progress) but replacing the name with the original one.
func (t *Tool) CallWith(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	p := *params
	p.Name = t.Upstream.Name
	return t.Caller.CallTool(ctx, &p)
}

// Prompt is one prompt in the merged catalog.
type Prompt struct {
	Name     string // exposed name, unique among prompts
	Node     string
	MCP      string
	Upstream *mcp.Prompt
	Caller   Caller
	Timeout  time.Duration
}

// Exposed returns the prompt as clients see it.
func (p *Prompt) Exposed() *mcp.Prompt {
	out := *p.Upstream
	out.Name = p.Name
	out.Description = withPrefix(p.Node, p.MCP, out.Description)
	return &out
}

// Get forwards prompts/get under the original name.
func (p *Prompt) Get(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	g, ok := p.Caller.(PromptGetter)
	if !ok {
		return nil, fmt.Errorf("%s/%s can't serve prompts", p.Node, p.MCP)
	}
	q := *params
	q.Name = p.Upstream.Name
	return g.GetPrompt(ctx, &q)
}

// Resource is one resource in the merged catalog. Its exposed URI wraps the
// upstream one (see ResourceURI), so URIs from different MCPs can't collide.
type Resource struct {
	URI      string
	Node     string
	MCP      string
	Upstream *mcp.Resource
}

// Exposed returns the resource as clients see it.
func (r *Resource) Exposed() *mcp.Resource {
	out := *r.Upstream
	out.URI = r.URI
	out.Description = withPrefix(r.Node, r.MCP, out.Description)
	return &out
}

// ResourceTemplate is one resource template; its exposed template is the
// upstream one behind the same prefix as ResourceURI.
type ResourceTemplate struct {
	URITemplate string
	Node        string
	MCP         string
	Upstream    *mcp.ResourceTemplate
}

// Exposed returns the template as clients see it.
func (r *ResourceTemplate) Exposed() *mcp.ResourceTemplate {
	out := *r.Upstream
	out.URITemplate = r.URITemplate
	out.Description = withPrefix(r.Node, r.MCP, out.Description)
	return &out
}

func withPrefix(node, mcpName, desc string) string {
	prefix := fmt.Sprintf("[%s/%s]", node, mcpName)
	if desc == "" {
		return prefix
	}
	return prefix + " " + desc
}

// URIScheme starts every exposed resource URI.
const URIScheme = "mcp-mesh"

// ResourceURI wraps an upstream resource URI (or template) for clients:
// "mcp-mesh://<node>/<mcp>/<upstream>", node and MCP path-escaped and the
// upstream text kept as is, so a template's variables still expand.
func ResourceURI(node, mcpName, upstream string) string {
	return URIScheme + "://" + url.PathEscape(node) + "/" + url.PathEscape(mcpName) + "/" + upstream
}

// ParseResourceURI undoes ResourceURI.
func ParseResourceURI(uri string) (node, mcpName, upstream string, ok bool) {
	rest, ok := strings.CutPrefix(uri, URIScheme+"://")
	if !ok {
		return "", "", "", false
	}
	n, rest, ok1 := strings.Cut(rest, "/")
	m, up, ok2 := strings.Cut(rest, "/")
	if !ok1 || !ok2 || n == "" || m == "" || up == "" {
		return "", "", "", false
	}
	var err1, err2 error
	node, err1 = url.PathUnescape(n)
	mcpName, err2 = url.PathUnescape(m)
	if err1 != nil || err2 != nil {
		return "", "", "", false
	}
	return node, mcpName, up, true
}

type mcpKey struct{ node, mcp string }

// entry is what Set stored for one MCP, before naming.
type entry struct {
	content Content
	caller  Caller
}

// Catalog is safe for concurrent use.
type Catalog struct {
	mu        sync.RWMutex
	mcps      map[mcpKey]entry
	overrides map[ToolKey]Override
	// Derived by rebuildLocked.
	tools      map[string]*Tool
	prompts    map[string]*Prompt
	resources  []*Resource
	templates  []*ResourceTemplate
	toolsByKey map[ToolKey]*Tool
	subs       map[chan struct{}]struct{}
}

// New returns an empty catalog.
func New() *Catalog {
	c := &Catalog{
		mcps:      make(map[mcpKey]entry),
		overrides: make(map[ToolKey]Override),
		subs:      make(map[chan struct{}]struct{}),
	}
	c.rebuildLocked()
	return c
}

// Set replaces what one MCP on one node offers. Entries Sanitize rejects are
// left out; Set returns why, for the MCP's status.
func (c *Catalog) Set(node, mcpName string, content Content, caller Caller) []string {
	content, problems := Sanitize(node, mcpName, content)
	c.mu.Lock()
	c.mcps[mcpKey{node, mcpName}] = entry{content, caller}
	c.rebuildLocked()
	c.mu.Unlock()
	c.notify()
	return problems
}

// SetMCP replaces one MCP's offer with just tools (no prompts or resources,
// the default timeout).
func (c *Catalog) SetMCP(node, mcpName string, tools []*mcp.Tool, caller Caller) []string {
	return c.Set(node, mcpName, Content{Tools: tools}, caller)
}

// RemoveMCP drops one MCP.
func (c *Catalog) RemoveMCP(node, mcpName string) {
	c.mu.Lock()
	_, ok := c.mcps[mcpKey{node, mcpName}]
	if ok {
		delete(c.mcps, mcpKey{node, mcpName})
		c.rebuildLocked()
	}
	c.mu.Unlock()
	if ok {
		c.notify()
	}
}

// RemoveNode drops every MCP on a node.
func (c *Catalog) RemoveNode(node string) {
	c.mu.Lock()
	removed := false
	for k := range c.mcps {
		if k.node == node {
			delete(c.mcps, k)
			removed = true
		}
	}
	if removed {
		c.rebuildLocked()
	}
	c.mu.Unlock()
	if removed {
		c.notify()
	}
}

// SetOverrides replaces every tool override (aliases and descriptions). An
// alias that would collide with another exposed name, or that isn't a valid
// name, is ignored for the tool with the higher ToolKey; see ValidAlias.
func (c *Catalog) SetOverrides(o map[ToolKey]Override) {
	c.mu.Lock()
	c.overrides = make(map[ToolKey]Override, len(o))
	for k, v := range o {
		c.overrides[k] = v
	}
	c.rebuildLocked()
	c.mu.Unlock()
	c.notify()
}

// ValidAlias reports whether name can be an exposed tool name: 1 to
// MaxNameLen characters from [A-Za-z0-9_-].
func ValidAlias(name string) bool {
	return name != "" && len(name) <= MaxNameLen && sanitize(name) == name
}

// rebuildLocked recomputes every exposed name from the stored offers and
// overrides. Aliases are placed first (in ToolKey order), then generated
// names; a generated name that is taken gets a hash suffix.
func (c *Catalog) rebuildLocked() {
	keys := make([]mcpKey, 0, len(c.mcps))
	for k := range c.mcps {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b mcpKey) int {
		if n := strings.Compare(a.node, b.node); n != 0 {
			return n
		}
		return strings.Compare(a.mcp, b.mcp)
	})

	type pending struct {
		key ToolKey
		t   *Tool
	}
	var all []pending
	for _, k := range keys {
		e := c.mcps[k]
		for _, ut := range e.content.Tools {
			key := ToolKey{k.node, k.mcp, ut.Name}
			t := &Tool{Node: k.node, MCP: k.mcp, Upstream: ut, Caller: e.caller, Timeout: e.content.Timeout}
			if o, ok := c.overrides[key]; ok {
				t.Description = o.Description
			}
			all = append(all, pending{key, t})
		}
	}
	slices.SortFunc(all, func(a, b pending) int { return compareKeys(a.key, b.key) })

	tools := make(map[string]*Tool, len(all))
	byKey := make(map[ToolKey]*Tool, len(all))
	for _, p := range all {
		if o := c.overrides[p.key]; ValidAlias(o.Name) {
			if _, taken := tools[o.Name]; !taken {
				p.t.Name, p.t.Aliased = o.Name, true
				tools[o.Name] = p.t
			}
		}
	}
	// Triples that sanitize to the same name all get hashed names. Giving the
	// plain name to one of them (the first in some order) would let a new
	// colliding MCP take it over from the tool clients knew by it; this way
	// the plain name stops working instead of silently pointing elsewhere.
	generated := make(map[string]int, len(all))
	for _, p := range all {
		if !p.t.Aliased {
			generated[ExposedName(p.key.Node, p.key.MCP, p.key.Tool)]++
		}
	}
	for _, p := range all {
		if p.t.Aliased {
			byKey[p.key] = p.t
			continue
		}
		name := ExposedName(p.key.Node, p.key.MCP, p.key.Tool)
		if _, taken := tools[name]; taken || generated[name] > 1 {
			// Colliding triples, or an alias took the name.
			name = withHash(name, p.key.Node+"\x00"+p.key.MCP+"\x00"+p.key.Tool, true)
		}
		p.t.Name = name
		tools[name] = p.t
		byKey[p.key] = p.t
	}

	prompts := map[string]*Prompt{}
	var resources []*Resource
	var templates []*ResourceTemplate
	promptNames := map[string]int{}
	for _, k := range keys {
		for _, up := range c.mcps[k].content.Prompts {
			promptNames[ExposedName(k.node, k.mcp, up.Name)]++
		}
	}
	for _, k := range keys {
		e := c.mcps[k]
		for _, up := range e.content.Prompts {
			name := ExposedName(k.node, k.mcp, up.Name)
			if promptNames[name] > 1 { // as for tools
				name = withHash(name, k.node+"\x00"+k.mcp+"\x00"+up.Name, true)
			}
			prompts[name] = &Prompt{Name: name, Node: k.node, MCP: k.mcp, Upstream: up, Caller: e.caller, Timeout: e.content.Timeout}
		}
		for _, ur := range e.content.Resources {
			resources = append(resources, &Resource{URI: ResourceURI(k.node, k.mcp, ur.URI), Node: k.node, MCP: k.mcp, Upstream: ur})
		}
		for _, ut := range e.content.ResourceTemplates {
			templates = append(templates, &ResourceTemplate{URITemplate: ResourceURI(k.node, k.mcp, ut.URITemplate), Node: k.node, MCP: k.mcp, Upstream: ut})
		}
	}
	c.tools, c.toolsByKey, c.prompts, c.resources, c.templates = tools, byKey, prompts, resources, templates
}

func compareKeys(a, b ToolKey) int {
	if n := strings.Compare(a.Node, b.Node); n != 0 {
		return n
	}
	if n := strings.Compare(a.MCP, b.MCP); n != 0 {
		return n
	}
	return strings.Compare(a.Tool, b.Tool)
}

// List returns every tool, sorted by exposed name.
func (c *Catalog) List() []*Tool {
	c.mu.RLock()
	out := make([]*Tool, 0, len(c.tools))
	for _, t := range c.tools {
		out = append(out, t)
	}
	c.mu.RUnlock()
	slices.SortFunc(out, func(a, b *Tool) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Lookup finds a tool by exposed name.
func (c *Catalog) Lookup(name string) (*Tool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[name]
	return t, ok
}

// ToolByKey finds a tool by where it comes from.
func (c *Catalog) ToolByKey(k ToolKey) (*Tool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.toolsByKey[k]
	return t, ok
}

// Prompts returns every prompt, sorted by exposed name.
func (c *Catalog) Prompts() []*Prompt {
	c.mu.RLock()
	out := make([]*Prompt, 0, len(c.prompts))
	for _, p := range c.prompts {
		out = append(out, p)
	}
	c.mu.RUnlock()
	slices.SortFunc(out, func(a, b *Prompt) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// LookupPrompt finds a prompt by exposed name.
func (c *Catalog) LookupPrompt(name string) (*Prompt, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.prompts[name]
	return p, ok
}

// Resources returns every listed resource, sorted by exposed URI.
func (c *Catalog) Resources() []*Resource {
	c.mu.RLock()
	out := slices.Clone(c.resources)
	c.mu.RUnlock()
	slices.SortFunc(out, func(a, b *Resource) int { return strings.Compare(a.URI, b.URI) })
	return out
}

// ResourceTemplates returns every resource template, sorted.
func (c *Catalog) ResourceTemplates() []*ResourceTemplate {
	c.mu.RLock()
	out := slices.Clone(c.templates)
	c.mu.RUnlock()
	slices.SortFunc(out, func(a, b *ResourceTemplate) int { return strings.Compare(a.URITemplate, b.URITemplate) })
	return out
}

// Route is where a resource read goes.
type Route struct {
	Node, MCP string
	// URI is the upstream URI to read.
	URI     string
	Reader  ResourceReader
	Timeout time.Duration
}

// ResolveResource maps an exposed resource URI, listed or produced from a
// template, to the MCP that serves it. It fails for URIs not in the
// mcp-mesh scheme, for MCPs not in the catalog, and for MCPs that offer
// neither resources nor templates.
func (c *Catalog) ResolveResource(uri string) (Route, bool) {
	node, mcpName, upstream, ok := ParseResourceURI(uri)
	if !ok {
		return Route{}, false
	}
	c.mu.RLock()
	e, ok := c.mcps[mcpKey{node, mcpName}]
	c.mu.RUnlock()
	if !ok || (len(e.content.Resources) == 0 && len(e.content.ResourceTemplates) == 0) {
		return Route{}, false
	}
	r, ok := e.caller.(ResourceReader)
	if !ok {
		return Route{}, false
	}
	return Route{Node: node, MCP: mcpName, URI: upstream, Reader: r, Timeout: e.content.Timeout}, true
}

// Changes returns a channel that receives a value after the catalog changes.
// Bursts of changes coalesce into one signal. Call cancel to stop watching.
func (c *Catalog) Changes() (ch <-chan struct{}, cancel func()) {
	sub := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[sub] = struct{}{}
	c.mu.Unlock()
	return sub, func() {
		c.mu.Lock()
		delete(c.subs, sub)
		c.mu.Unlock()
	}
}

func (c *Catalog) notify() {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for sub := range c.subs {
		select {
		case sub <- struct{}{}:
		default:
		}
	}
}

// ExposedName builds "<node>__<mcp>__<tool>". Characters outside
// [A-Za-z0-9_-] become "_", and names over MaxNameLen are truncated with a
// short hash suffix so they stay unique and stable.
func ExposedName(node, mcpName, tool string) string {
	name := sanitize(node) + "__" + sanitize(mcpName) + "__" + sanitize(tool)
	if len(name) <= MaxNameLen {
		return name
	}
	return withHash(name, node+"\x00"+mcpName+"\x00"+tool, false)
}

// withHash appends "_" and 6 hex chars of a hash of key, truncating name so
// the result fits MaxNameLen. When always is false and name already fits, it
// is returned unchanged.
func withHash(name, key string, always bool) string {
	if !always && len(name) <= MaxNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(key))
	suffix := "_" + hex.EncodeToString(sum[:])[:6]
	if len(name) > MaxNameLen-len(suffix) {
		name = name[:MaxNameLen-len(suffix)]
	}
	return name + suffix
}

func sanitize(s string) string {
	b := []byte(s)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-'
		if !ok {
			b[i] = '_'
		}
	}
	return string(b)
}
