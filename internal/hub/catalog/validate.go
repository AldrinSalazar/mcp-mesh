package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxDefinitionBytes bounds one tool, prompt, resource or template as JSON.
// Every client's server holds a copy of each, so one huge definition costs
// memory per client.
const MaxDefinitionBytes = 256 << 10

// MaxUpstreamNameLen bounds an upstream tool or prompt name (the MCP spec's
// limit for tool names); resource URIs get the same bound times 16.
const MaxUpstreamNameLen = 128

// Sanitize drops the entries of one MCP's content that clients can't be
// served: nil entries, missing, overlong or duplicate names, oversized
// definitions, and anything the SDK refuses to register (it panics on, for
// example, a tool without an object input schema). It returns what is left
// and one problem per dropped entry. Content comes from a node, so nothing in
// it is trusted; c itself is not modified.
func Sanitize(node, mcpName string, c Content) (Content, []string) {
	s := sanitizer{
		scratch: mcp.NewServer(&mcp.Implementation{Name: "sanitize"}, &mcp.ServerOptions{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}),
		seen: map[string]bool{},
	}
	out := Content{Timeout: c.Timeout}
	for i, t := range c.Tools {
		var name string
		if t != nil {
			name = t.Name
		}
		if s.ok("tool", i, t == nil, name, MaxUpstreamNameLen, t, func() {
			cp := *t
			cp.Name = "t" // clients see a generated name; the upstream one only needs to be sane
			s.scratch.AddTool(&cp, nil)
		}) {
			out.Tools = append(out.Tools, t)
		}
	}
	for i, p := range c.Prompts {
		var name string
		if p != nil {
			name = p.Name
		}
		if s.ok("prompt", i, p == nil, name, MaxUpstreamNameLen, p, nil) {
			out.Prompts = append(out.Prompts, p)
		}
	}
	for i, r := range c.Resources {
		var uri string
		if r != nil {
			uri = r.URI
		}
		if s.ok("resource", i, r == nil, uri, 16*MaxUpstreamNameLen, r, func() {
			cp := *r
			cp.URI = ResourceURI(node, mcpName, r.URI)
			s.scratch.AddResource(&cp, nil)
		}) {
			out.Resources = append(out.Resources, r)
		}
	}
	for i, t := range c.ResourceTemplates {
		var tmpl string
		if t != nil {
			tmpl = t.URITemplate
		}
		if s.ok("resource template", i, t == nil, tmpl, 16*MaxUpstreamNameLen, t, func() {
			cp := *t
			cp.URITemplate = ResourceURI(node, mcpName, t.URITemplate)
			s.scratch.AddResourceTemplate(&cp, nil)
		}) {
			out.ResourceTemplates = append(out.ResourceTemplates, t)
		}
	}
	return out, s.problems
}

type sanitizer struct {
	scratch  *mcp.Server
	seen     map[string]bool // kind + "\x00" + name
	problems []string
}

// ok checks one entry and records why it is dropped. register, when not
// nil, adds a copy to the scratch server; a panic there drops the entry.
func (s *sanitizer) ok(kind string, i int, missing bool, name string, maxName int, v any, register func()) bool {
	err := func() error {
		switch {
		case missing:
			return errors.New("null entry")
		case name == "":
			return errors.New("no name")
		case len(name) > maxName:
			return fmt.Errorf("name longer than %d bytes", maxName)
		case s.seen[kind+"\x00"+name]:
			return errors.New("listed twice")
		}
		s.seen[kind+"\x00"+name] = true
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("can't encode: %w", err)
		}
		if len(data) > MaxDefinitionBytes {
			return fmt.Errorf("definition is %d bytes (max %d)", len(data), MaxDefinitionBytes)
		}
		if register != nil {
			return tryRegister(register)
		}
		return nil
	}()
	if err == nil {
		return true
	}
	label := fmt.Sprintf("#%d", i+1)
	if name != "" {
		if len(name) > 80 {
			name = name[:80] + "…"
		}
		label = fmt.Sprintf("%q", name)
	}
	s.problems = append(s.problems, fmt.Sprintf("%s %s dropped: %v", kind, label, err))
	return false
}

// tryRegister runs one SDK registration, turning its panic into an error.
func tryRegister(register func()) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	register()
	return nil
}
