package nodectl

import (
	"errors"
	"net"
	"net/url"
	"path"
	"strings"
	"unicode"
)

// SuggestName derives an MCP name from a command or URL, e.g.
// ["npx","-y","@modelcontextprotocol/server-filesystem","~/code"] -> "filesystem",
// "http://127.0.0.1:8931/mcp" -> "127-0-0-1-8931". The result matches
// config's MCP name pattern and is never empty.
func SuggestName(command []string, rawURL string) string {
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || u.Hostname() == "" {
			return "mcp"
		}
		host := strings.ToLower(u.Hostname())
		if net.ParseIP(host) != nil || host == "localhost" {
			if u.Port() != "" {
				host += "-" + u.Port()
			}
			return cleanName(host)
		}
		labels := strings.Split(host, ".")
		for len(labels) > 0 && (labels[0] == "mcp" || labels[0] == "api" || labels[0] == "www") {
			labels = labels[1:]
		}
		if len(labels) > 1 {
			labels = labels[:len(labels)-1]
		}
		return cleanName(strings.Join(labels, "-"))
	}
	module := false
	candidate := ""
	for i := 0; i < len(command); i++ {
		word := command[i]
		base := strings.ToLower(path.Base(word))
		switch base {
		case "npx", "uvx", "bunx", "pipx", "pnpm", "yarn", "bun", "node", "deno", "python", "python3", "docker", "podman":
			continue
		case "dlx", "x", "run":
			continue
		}
		if word == "-m" {
			module = true
			continue
		}
		if strings.HasPrefix(word, "-") {
			if flagValue(word) && i+1 < len(command) {
				i++
			}
			continue
		}
		candidate = word
		break
	}
	if candidate == "" {
		return "mcp"
	}
	// A leading @ is an npm scope, while a later @ introduces its version.
	if at := strings.LastIndex(candidate, "@"); at > 0 {
		candidate = candidate[:at]
	}
	parts := strings.Split(strings.TrimRight(candidate, "/"), "/")
	if module {
		parts = strings.Split(candidate, ".")
	}
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if !module {
			part = strings.TrimSuffix(part, path.Ext(part))
		}
		part = strings.TrimPrefix(part, "@")
		if genericName(part) {
			continue
		}
		for _, prefix := range []string{"mcp-server-", "server-", "mcp-"} {
			if strings.HasPrefix(strings.ToLower(part), prefix) {
				part = part[len(prefix):]
				break
			}
		}
		for _, suffix := range []string{"-mcp-server", "-mcp", "-server"} {
			if strings.HasSuffix(strings.ToLower(part), suffix) {
				part = part[:len(part)-len(suffix)]
				break
			}
		}
		if genericName(part) {
			continue
		}
		return cleanName(part)
	}
	return "mcp"
}

func genericName(s string) bool {
	switch strings.ToLower(s) {
	case "", ".", "..", "~", "mcp", "server", "index", "main", "cli", "build", "dist", "src", "bin", "out":
		return true
	}
	return false
}

func flagValue(s string) bool {
	switch s {
	case "-e", "--env", "--env-file", "-v", "--volume", "--mount", "--name", "--entrypoint", "--network", "--hostname", "-h", "-p", "--publish", "-w", "--workdir", "-u", "--user", "--platform", "--label", "-l", "--cpus", "--memory", "--from", "--package", "--index-url", "--registry":
		return true
	}
	return false
}

func cleanName(s string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "mcp"
	}
	return result
}

// SplitCommand splits a command line the way a POSIX shell would for plain
// words, single and double quotes and backslash escapes; it does not expand
// variables or globs.
func SplitCommand(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\\' {
			if i+1 == len(runes) {
				return nil, errors.New("unterminated backslash escape")
			}
			next := runes[i+1]
			if quote == '"' && next != '"' && next != '\\' && next != '$' && next != '`' && next != '\n' {
				word.WriteRune(r)
				continue
			}
			i++
			if next != '\n' {
				word.WriteRune(next)
				started = true
			}
			continue
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}
