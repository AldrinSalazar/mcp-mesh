package config

import "errors"

var (
	// ErrConfigExists is returned by Create when the file exists and force is false.
	ErrConfigExists = errors.New("config already exists")
	// ErrMCPExists is returned by AddMCP for a name already in the file.
	ErrMCPExists = errors.New("MCP already configured")
	// ErrNoMCP is returned by RemoveMCP and SetDisabled for a name not in the file.
	ErrNoMCP = errors.New("MCP not configured")
)

// Entry is one MCP as written in node.yaml, before ${env:} expansion and
// including disabled entries. The CLI and TUI display and edit Entries; the
// daemon only sees the validated MCPs from Load.
type Entry struct {
	Name    string
	Command string // stdio; exclusive with URL
	Args    []string
	Env     map[string]string
	Dir     string
	URL     string // an MCP something else already runs
	// Transport is "", "http" or "sse" for URL entries ("" means http);
	// always "" for commands.
	Transport   string
	Headers     map[string]string
	Start       string // "", "eager" or "lazy"; "" means eager
	IdleTimeout string // e.g. "10m"; "" means the default
	Sharing     string // "", "shared" or "per-client"; "" means shared
	MaxClients  int    // per-client only; 0 means the default
	CallTimeout string // e.g. "10m"; "" means the hub's default
	AllowTools  []string
	Disabled    bool
}
