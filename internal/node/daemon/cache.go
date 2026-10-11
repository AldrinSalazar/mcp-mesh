package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

type toolCache struct {
	dir        string
	definition config.MCP
}
type cacheFile struct {
	Fingerprint string          `json:"fingerprint"`
	Tools       json.RawMessage `json:"tools"`
}

func (c toolCache) fingerprint() string {
	d := c.definition
	// Lifecycle policy does not change the tool definitions.
	raw, _ := json.Marshal(struct {
		Transport  proto.Transport
		Command    string
		Args, Env  []string
		Dir, URL   string
		Headers    map[string]string
		AllowTools []string
	}{d.Transport, d.Command, d.Args, d.Env, d.Dir, d.URL, d.Headers, d.AllowTools})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (c toolCache) path() string { return filepath.Join(c.dir, c.definition.Name+".json") }
func (c toolCache) read() json.RawMessage {
	if c.dir == "" {
		return nil
	}
	raw, err := os.ReadFile(c.path())
	if err != nil {
		return nil
	}
	var file cacheFile
	if json.Unmarshal(raw, &file) != nil || file.Fingerprint != c.fingerprint() {
		return nil
	}
	var tools []json.RawMessage
	if len(file.Tools) == 0 || file.Tools[0] != '[' || json.Unmarshal(file.Tools, &tools) != nil {
		return nil
	}
	return file.Tools
}
func (c toolCache) write(tools json.RawMessage) error {
	if c.dir == "" {
		return nil
	}
	raw, err := json.Marshal(cacheFile{c.fingerprint(), tools})
	if err != nil {
		return fmt.Errorf("encode tool cache: %w", err)
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return fmt.Errorf("create tool cache directory: %w", err)
	}
	f, err := os.CreateTemp(c.dir, ".tools-*")
	if err != nil {
		return fmt.Errorf("create tool cache: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("chmod tool cache: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		return fmt.Errorf("write tool cache: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync tool cache: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tool cache: %w", err)
	}
	if err := os.Rename(f.Name(), c.path()); err != nil {
		return fmt.Errorf("replace tool cache: %w", err)
	}
	return nil
}
