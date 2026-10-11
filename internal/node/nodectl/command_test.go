package nodectl

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSuggestName(t *testing.T) {
	tests := []struct{ input, want string }{
		{"npx -y @modelcontextprotocol/server-filesystem ~/code", "filesystem"},
		{"npx -y @playwright/mcp@latest", "playwright"},
		{"uvx mcp-server-fetch", "fetch"},
		{"npx -y firecrawl-mcp", "firecrawl"},
		{"docker run -i --rm -e GITHUB_TOKEN ghcr.io/github/github-mcp-server", "github"},
		{"node /home/me/weather-mcp/build/index.js", "weather"},
		{"python -m my_tool.server", "my_tool"},
		{"./bin/my-tool --verbose", "my-tool"},
		{"http://127.0.0.1:8931/mcp", "127-0-0-1-8931"},
		{"http://localhost:3000/mcp", "localhost-3000"},
		{"https://mcp.notion.com/mcp", "notion"},
		{"", "mcp"},
		{"pnpm dlx @scope/server-weather@1.2", "weather"},
		{"yarn dlx mcp-fetch", "fetch"},
		{"bun x FIRECRAWL-MCP", "firecrawl"},
		{"deno run --allow-net ./src/weather.ts", "weather"},
		{"podman run --name test -v /tmp:/data --env=TOKEN image/my-mcp", "my"},
		{"pipx run mcp-server-fetch", "fetch"},
		{"uvx --from pkg mcp-server-fetch", "fetch"},
		{"https://www.api.mcp.notion.com/mcp", "notion"},
		{"http://[::1]:80/mcp", "1-80"},
		{"node ./dist/index.js", "mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var command []string
			rawURL := ""
			if strings.Contains(tt.input, "://") {
				rawURL = tt.input
			} else {
				command, _ = SplitCommand(tt.input)
			}
			if got := SuggestName(command, rawURL); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		line string
		want []string
		fail bool
	}{
		{"", nil, false}, {" \t\n", nil, false},
		{"npx -y package", []string{"npx", "-y", "package"}, false},
		{`cmd 'hello world' "two words"`, []string{"cmd", "hello world", "two words"}, false},
		{`cmd '' "" a"b"'c'`, []string{"cmd", "", "", "abc"}, false},
		{`cmd one\ two \"quoted\"`, []string{"cmd", "one two", `"quoted"`}, false},
		{`cmd "a\"b\\c\$d"`, []string{"cmd", `a"b\c$d`}, false},
		{`cmd "a\qb"`, []string{"cmd", `a\qb`}, false},
		{`cmd '$HOME * \x'`, []string{"cmd", `$HOME * \x`}, false},
		{"cmd a\\\nb", []string{"cmd", "ab"}, false},
		{`cmd 'unfinished`, nil, true}, {`cmd "unfinished`, nil, true}, {`cmd \`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, err := SplitCommand(tt.line)
			if (err != nil) != tt.fail || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, %v; want %#v, fail=%v", got, err, tt.want, tt.fail)
			}
		})
	}
}

func TestEditorCommand(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Symlinks let LookPath use real executables without running an editor.
	for _, name := range []string{"visual", "editor", "nano", "vi"} {
		if err := os.Symlink(executable, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	n := &Node{opts: Options{ConfigPath: "/tmp/node.yaml"}}
	tests := []struct {
		visual, editor, want string
		args                 []string
	}{
		{"visual --wait", "editor", "visual", []string{"--wait", "/tmp/node.yaml"}},
		{"", "editor 'two words'", "editor", []string{"two words", "/tmp/node.yaml"}},
		{"missing", "editor", "editor", []string{"/tmp/node.yaml"}},
		{"", "", "nano", []string{"/tmp/node.yaml"}},
	}
	for _, tt := range tests {
		t.Run(tt.want+tt.visual, func(t *testing.T) {
			t.Setenv("VISUAL", tt.visual)
			t.Setenv("EDITOR", tt.editor)
			cmd, err := n.EditorCommand()
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(cmd.Path) != tt.want || !reflect.DeepEqual(cmd.Args[1:], tt.args) {
				t.Fatalf("command: %#v", cmd.Args)
			}
			if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
				t.Fatal("editor stdio was set")
			}
		})
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if err := os.Remove(filepath.Join(dir, "nano")); err != nil {
		t.Fatal(err)
	}
	cmd, err := n.EditorCommand()
	if err != nil || filepath.Base(cmd.Path) != "vi" {
		t.Fatalf("fallback: %v, %v", cmd, err)
	}
	if err := os.Remove(filepath.Join(dir, "vi")); err != nil {
		t.Fatal(err)
	}
	if _, err := n.EditorCommand(); err == nil {
		t.Fatal("expected missing editor error")
	}
}
