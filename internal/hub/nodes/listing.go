package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
)

// Budget for one listing of an MCP: its tools, prompts, resources and
// templates together. Each page fits the wire limit, but an MCP can keep
// answering with another cursor; this bounds what the hub accumulates
// before anything is sanitized or approved. The node's tool-list observer
// uses the same values (toolfilter.MaxList*).
const (
	maxListPages = 1000
	maxListItems = 10000
	maxListBytes = 16 << 20
	maxListTime  = time.Minute
)

// errListBudget is what an MCP whose listing exceeds the budget fails with.
var errListBudget = errors.New("listing exceeds the hub's budget")

// listBudget counts one listing's pages, items and bytes.
type listBudget struct {
	pages, items, bytes int
	// limits; the max* constants unless a test sets smaller ones.
	maxPages, maxItems, maxBytes int
}

func newListBudget() *listBudget {
	return &listBudget{maxPages: maxListPages, maxItems: maxListItems, maxBytes: maxListBytes}
}

// listAll follows one kind's cursors, charging every page and item to b
// as it goes so an endless or oversized listing stops early. page fetches
// one page.
func listAll[T any](ctx context.Context, b *listBudget, kind string, page func(ctx context.Context, cursor string) ([]T, string, error)) ([]T, error) {
	var all []T
	seen := map[string]bool{}
	cursor := ""
	for {
		if b.pages++; b.pages > b.maxPages {
			return nil, fmt.Errorf("%w: more than %d pages", errListBudget, b.maxPages)
		}
		items, next, err := page(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if b.items++; b.items > b.maxItems {
				return nil, fmt.Errorf("%w: more than %d items", errListBudget, b.maxItems)
			}
			data, _ := json.Marshal(item)
			if b.bytes += len(data); b.bytes > b.maxBytes {
				return nil, fmt.Errorf("%w: more than %d bytes", errListBudget, b.maxBytes)
			}
		}
		all = append(all, items...)
		if next == "" {
			return all, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("%w: %s cursor %q repeated", errListBudget, kind, next)
		}
		seen[next] = true
		cursor = next
	}
}

func listTools(ctx context.Context, s *mcp.ClientSession, b *listBudget) ([]*mcp.Tool, error) {
	return listAll(ctx, b, "tools", func(ctx context.Context, cursor string) ([]*mcp.Tool, string, error) {
		res, err := s.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, "", err
		}
		return res.Tools, res.NextCursor, nil
	})
}

// listContent lists what an MCP offers within one budget. A server that
// doesn't advertise prompts or resources isn't asked. Failing to list them
// must not take the tools down with it (warn says why they were left out),
// but exceeding the budget fails the whole listing.
func listContent(ctx context.Context, s *mcp.ClientSession, warn func(kind string, err error)) (catalog.Content, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, maxListTime, fmt.Errorf("%w: not done in %s", errListBudget, maxListTime))
	defer cancel()
	b := newListBudget()
	var content catalog.Content
	tools, err := listTools(ctx, s, b)
	if err != nil {
		return content, fmt.Errorf("list tools: %w", budgetCause(ctx, err))
	}
	content.Tools = tools
	var caps *mcp.ServerCapabilities
	if result := s.InitializeResult(); result != nil {
		caps = result.Capabilities
	}
	// optional lists one kind, keeping going (without it) on a plain failure.
	optional := func(kind string, list func() error) error {
		err := list()
		if err == nil {
			return nil
		}
		if err = budgetCause(ctx, err); errors.Is(err, errListBudget) {
			return fmt.Errorf("list %s: %w", kind, err)
		}
		warn(kind, err)
		return nil
	}
	if caps != nil && caps.Prompts != nil {
		if err := optional("prompts", func() (err error) {
			content.Prompts, err = listAll(ctx, b, "prompts", func(ctx context.Context, cursor string) ([]*mcp.Prompt, string, error) {
				res, err := s.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
				if err != nil {
					return nil, "", err
				}
				return res.Prompts, res.NextCursor, nil
			})
			return err
		}); err != nil {
			return content, err
		}
	}
	if caps != nil && caps.Resources != nil {
		if err := optional("resources", func() (err error) {
			content.Resources, err = listAll(ctx, b, "resources", func(ctx context.Context, cursor string) ([]*mcp.Resource, string, error) {
				res, err := s.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
				if err != nil {
					return nil, "", err
				}
				return res.Resources, res.NextCursor, nil
			})
			return err
		}); err != nil {
			return content, err
		}
		if err := optional("resource templates", func() (err error) {
			content.ResourceTemplates, err = listAll(ctx, b, "resource templates", func(ctx context.Context, cursor string) ([]*mcp.ResourceTemplate, string, error) {
				res, err := s.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
				if err != nil {
					return nil, "", err
				}
				return res.ResourceTemplates, res.NextCursor, nil
			})
			return err
		}); err != nil {
			return content, err
		}
	}
	return content, nil
}

// budgetCause reports the time budget running out as such rather than as
// the bare context error a request ends with.
func budgetCause(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil && errors.Is(cause, errListBudget) {
		return cause
	}
	return err
}
