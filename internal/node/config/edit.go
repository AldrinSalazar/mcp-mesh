package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Entries returns all MCPs, sorted by name, without validation or expansion.
func Entries(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := readDocument(data, path)
	if err != nil {
		return nil, err
	}
	// Decode only MCPs so unrelated settings cannot prevent listing entries.
	var raw struct {
		MCPs map[string]rawMCP `yaml:"mcps"`
	}
	if err := doc.Decode(&raw); err != nil {
		return nil, decodeError(path, err)
	}
	entries := make([]Entry, 0, len(raw.MCPs))
	for _, name := range sortedKeys(raw.MCPs) {
		mcp := raw.MCPs[name]
		e := Entry{
			Name: name, Command: mcp.Command, Args: mcp.Args, Env: mcp.Env,
			Dir: mcp.Dir, URL: mcp.URL, Headers: mcp.Headers, Start: mcp.Start,
			AllowTools: mcp.AllowTools, Disabled: mcp.Disabled,
		}
		if mcp.Transport != nil {
			e.Transport = *mcp.Transport
		}
		if mcp.IdleTimeout != nil {
			e.IdleTimeout = *mcp.IdleTimeout
		}
		e.Sharing = mcp.Sharing
		if mcp.MaxClients != nil {
			e.MaxClients = *mcp.MaxClients
		}
		if mcp.CallTimeout != nil {
			e.CallTimeout = *mcp.CallTimeout
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// Create writes a validated starter config, replacing an existing file only
// when force is true. Existing permission bits are preserved.
func Create(path, hubURL string, force bool) error {
	mode := os.FileMode(0600)
	info, err := os.Lstat(path)
	if err == nil {
		if !force {
			return fmt.Errorf("create %s: %w", path, ErrConfigExists)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if path, err = filepath.EvalSymlinks(path); err != nil {
				return fmt.Errorf("resolve %s: %w", path, err)
			}
			if info, err = os.Stat(path); err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}
		}
		mode = permissionBits(info.Mode())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	doc, err := readDocument(Example(), path)
	if err != nil {
		return err
	}
	if hubURL != "" {
		hub := doc.Content[0].Content[mappingIndex(doc.Content[0], "hub")+1]
		url := hub.Content[mappingIndex(hub, "url")+1]
		url.Value = hubURL
	}
	data, err := encodeDocument(doc)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := Parse(data, path); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	if err := writeConfig(path, data, mode, !force); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// AddMCP appends an MCP without replacing any existing entry.
func AddMCP(path string, e Entry) error {
	err := editConfig(path, func(root *yaml.Node) error {
		mcps, err := mcpMapping(root)
		if err != nil {
			return err
		}
		if mappingIndex(mcps, e.Name) >= 0 {
			return ErrMCPExists
		}
		if !mcpNamePattern.MatchString(e.Name) {
			return errors.New("MCP name must match [A-Za-z0-9_-]+")
		}
		if (e.Command == "") == (e.URL == "") {
			return errors.New("exactly one of command or url is required")
		}
		entry, err := entryNode(e)
		if err != nil {
			return err
		}
		mcps.Content = append(mcps.Content, stringNode(e.Name), entry)
		return nil
	})
	if err != nil {
		return fmt.Errorf("add MCP %q to %s: %w", e.Name, path, err)
	}
	return nil
}

// RemoveMCP deletes the named entry, including disabled MCPs.
func RemoveMCP(path, name string) error {
	err := editConfig(path, func(root *yaml.Node) error {
		mcps, err := mcpMapping(root)
		if err != nil {
			return err
		}
		i := mappingIndex(mcps, name)
		if i < 0 {
			return ErrNoMCP
		}
		removePair(mcps, i)
		return nil
	})
	if err != nil {
		return fmt.Errorf("remove MCP %q from %s: %w", name, path, err)
	}
	return nil
}

// SetDisabled toggles an entry; enabling removes the disabled key.
func SetDisabled(path, name string, disabled bool) error {
	err := editConfig(path, func(root *yaml.Node) error {
		mcps, err := mcpMapping(root)
		if err != nil {
			return err
		}
		i := mappingIndex(mcps, name)
		if i < 0 {
			return ErrNoMCP
		}
		entry := mcps.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: MCP entry must be a mapping", entry.Line)
		}
		i = mappingIndex(entry, "disabled")
		if !disabled {
			if i >= 0 {
				removePair(entry, i)
			}
		} else if i >= 0 {
			value := entry.Content[i+1]
			value.Kind, value.Tag, value.Value, value.Style = yaml.ScalarNode, "!!bool", "true", 0
			value.Content, value.Alias = nil, nil
		} else {
			entry.Content = append(entry.Content, stringNode("disabled"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set disabled for MCP %q in %s: %w", name, path, err)
	}
	return nil
}

func readDocument(data []byte, path string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, decodeError(path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, decodeError(path, err)
		}
		return nil, fmt.Errorf("%s:%d: only one YAML document is supported", path, extra.Line)
	}
	return &doc, nil
}

func editConfig(path string, edit func(*yaml.Node) error) error {
	// Write through a symlinked config (e.g. into a dotfiles repo) instead of
	// replacing the link with a regular file.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	// The CLI and the TUI (or two CLIs) may edit at once; without the lock
	// each reads the same file and the second write drops the first one's
	// change. The config itself is replaced on write, so the lock is on its
	// directory.
	unlock, err := lockDir(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer unlock()
	file, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := readDocument(data, path)
	if err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Tag == "!!null" {
		return errors.New("config is empty; use Create to write a starter config")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s:%d: config must be a mapping", path, root.Line)
	}
	_, originalErr := Parse(data, path)
	if err := edit(root); err != nil {
		return err
	}
	data, err = encodeDocument(doc)
	if err != nil {
		return err
	}
	if _, err := Parse(data, path); err != nil {
		if originalErr != nil {
			return fmt.Errorf("node.yaml is invalid; fix it first: %w", originalErr)
		}
		return err
	}
	return writeConfig(target, data, permissionBits(info.Mode()), false)
}

func mcpMapping(root *yaml.Node) (*yaml.Node, error) {
	i := mappingIndex(root, "mcps")
	if i < 0 {
		mcps := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, stringNode("mcps"), mcps)
		return mcps, nil
	}
	mcps := root.Content[i+1]
	if mcps.Tag == "!!null" {
		// Retain comments attached to a formerly empty mcps key.
		root.Content[i].HeadComment = joinComments(root.Content[i].HeadComment, mcps.LineComment)
		mcps.LineComment = ""
		mcps.Kind, mcps.Tag, mcps.Value, mcps.Style = yaml.MappingNode, "!!map", "", 0
	}
	if mcps.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: mcps must be a mapping", mcps.Line)
	}
	return mcps, nil
}

func mappingIndex(mapping *yaml.Node, key string) int {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func entryNode(e Entry) (*yaml.Node, error) {
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, field := range []struct {
		key     string
		value   any
		include bool
	}{
		{"command", e.Command, e.Command != ""},
		{"args", e.Args, len(e.Args) > 0},
		{"env", e.Env, len(e.Env) > 0},
		{"dir", e.Dir, e.Dir != ""},
		{"url", e.URL, e.URL != ""},
		{"transport", e.Transport, e.Transport != ""},
		{"headers", e.Headers, len(e.Headers) > 0},
		{"start", e.Start, e.Start != ""},
		{"idle_timeout", e.IdleTimeout, e.IdleTimeout != ""},
		{"sharing", e.Sharing, e.Sharing != ""},
		{"max_clients", e.MaxClients, e.MaxClients != 0},
		{"call_timeout", e.CallTimeout, e.CallTimeout != ""},
		// An explicit empty allowlist denies every tool; nil allows all tools.
		{"allow_tools", e.AllowTools, e.AllowTools != nil},
		{"disabled", e.Disabled, e.Disabled},
	} {
		if !field.include {
			continue
		}
		var value yaml.Node
		if err := value.Encode(field.value); err != nil {
			return nil, fmt.Errorf("encode %s: %w", field.key, err)
		}
		if value.Kind == yaml.SequenceNode {
			value.Style = yaml.FlowStyle
		}
		entry.Content = append(entry.Content, stringNode(field.key), &value)
	}
	return entry, nil
}

func removePair(mapping *yaml.Node, i int) {
	// Comments inside the pair describe it and go with it, but YAML attaches
	// a following unindented commented-out block to the pair as a foot
	// comment; keep that one.
	comments := joinComments(mapping.Content[i].FootComment, mapping.Content[i+1].FootComment)
	mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
	switch {
	case i < len(mapping.Content):
		mapping.Content[i].HeadComment = joinComments(comments, mapping.Content[i].HeadComment)
	case len(mapping.Content) > 0:
		// The new last pair keeps them, as the starter config has them
		// (SetKeyFile reads them back from there). On the mapping itself the
		// encoder moves them into whatever key follows it, and again on
		// every edit.
		last := mapping.Content[len(mapping.Content)-1]
		last.FootComment = joinComments(last.FootComment, comments)
	default:
		mapping.FootComment = joinComments(comments, mapping.FootComment)
	}
}

func joinComments(comments ...string) string {
	var result string
	for _, comment := range comments {
		if comment != "" {
			if result != "" {
				result += "\n"
			}
			result += comment
		}
	}
	return result
}

func encodeDocument(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close YAML encoder: %w", err)
	}
	return buf.Bytes(), nil
}

func permissionBits(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

func writeConfig(path string, data []byte, mode os.FileMode, exclusive bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".node.yaml-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if exclusive {
		// A normal rename could overwrite a config created concurrently.
		err = renameExclusive(file.Name(), path)
		if errors.Is(err, os.ErrExist) {
			return ErrConfigExists
		}
	} else {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}
