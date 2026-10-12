// Copy-paste connection snippets for MCP clients ("harnesses"). Pure so
// they can be tested; the screens only pick one. secret is a client key, a
// placeholder before one exists, or "" when the hub runs with auth off (no
// header at all).

export type SnippetId = 'claude-code' | 'codex' | 'opencode' | 'claude-desktop' | 'cursor' | 'curl'

export interface SnippetOption {
  value: SnippetId
  label: string
  title: string
  // where says where the snippet goes.
  where: string
  // logo names its mark in components/connect/brands.ts.
  logo: 'claude' | 'openai' | 'opencode' | 'cursor' | 'terminal'
}

export const snippetOptions: SnippetOption[] = [
  { value: 'claude-code', label: 'Claude Code', title: 'Claude Code', where: 'Run in a terminal.', logo: 'claude' },
  { value: 'codex', label: 'Codex', title: 'Codex (~/.codex/config.toml)', where: 'Add to ~/.codex/config.toml (or .codex/config.toml in a trusted project).', logo: 'openai' },
  { value: 'opencode', label: 'OpenCode', title: 'OpenCode (opencode.json)', where: 'Merge into opencode.json in your project, or ~/.config/opencode/opencode.json.', logo: 'opencode' },
  { value: 'claude-desktop', label: 'Claude Desktop', title: 'Claude Desktop (claude_desktop_config.json)', where: 'Merge into claude_desktop_config.json; needs Node.js for mcp-remote.', logo: 'claude' },
  { value: 'cursor', label: 'Cursor', title: 'Cursor', where: 'Merge into .cursor/mcp.json (or ~/.cursor/mcp.json). Other clients that take a URL and headers use the same shape.', logo: 'cursor' },
  { value: 'curl', label: 'curl test', title: 'curl test', where: 'Run in a terminal: an answer with serverInfo means the URL and key work.', logo: 'terminal' },
]

// keyPlaceholder stands for a client key not created yet.
export const keyPlaceholder = '<client-key>'

export interface SnippetInput {
  url: string
  secret: string
  // claudeCode is the command exactly as the API gives it for a new key;
  // empty builds one.
  claudeCode?: string
}

function bearer(secret: string): string {
  return `Bearer ${secret}`
}

// claudeCodeCommand is what the hub's API answers for a new key.
function claudeCodeCommand(url: string, secret: string): string {
  const header = secret ? ` --header "Authorization: ${bearer(secret)}"` : ''
  return `claude mcp add --transport http mcp-mesh ${url}${header}`
}

// codexTOML is a Codex streamable HTTP server entry. TOML basic strings
// escape like JSON strings.
function codexTOML(url: string, secret: string): string {
  const lines = ['[mcp_servers.mcp-mesh]', `url = ${JSON.stringify(url)}`]
  if (secret) lines.push(`http_headers = { "Authorization" = ${JSON.stringify(bearer(secret))} }`)
  return lines.join('\n')
}

// opencodeJSON is an OpenCode remote MCP entry.
function opencodeJSON(url: string, secret: string): string {
  const server: Record<string, unknown> = { type: 'remote', url, enabled: true }
  if (secret) server.headers = { Authorization: bearer(secret) }
  return JSON.stringify({ $schema: 'https://opencode.ai/config.json', mcp: { 'mcp-mesh': server } }, null, 2)
}

// cursorJSON is Cursor's mcp.json entry for a remote Streamable HTTP server.
function cursorJSON(url: string, secret: string): string {
  const server: Record<string, unknown> = { url }
  if (secret) server.headers = { Authorization: bearer(secret) }
  return JSON.stringify({ mcpServers: { 'mcp-mesh': server } }, null, 2)
}

// desktopJSON is a claude_desktop_config.json entry. That file only launches
// local commands, so a remote server goes through the mcp-remote bridge; the
// header value travels in an env var because args with spaces get mangled on
// Windows.
function desktopJSON(url: string, secret: string): string {
  const server = secret
    ? { command: 'npx', args: ['-y', 'mcp-remote', url, '--header', 'Authorization:${MCPMESH_AUTH}'], env: { MCPMESH_AUTH: bearer(secret) } }
    : { command: 'npx', args: ['-y', 'mcp-remote', url] }
  return JSON.stringify({ mcpServers: { 'mcp-mesh': server } }, null, 2)
}

// curlTest starts an MCP session, which proves the key and URL work. It
// sends initialize because a session accepts nothing else first.
function curlTest(url: string, secret: string): string {
  return [
    ...(secret ? [`curl -H "Authorization: ${bearer(secret)}"`] : ['curl']),
    '-H "Content-Type: application/json"',
    '-H "Accept: application/json, text/event-stream"',
    `-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'`,
    url,
  ].join(' ')
}

// snippet returns the text to copy.
export function snippet(id: SnippetId, i: SnippetInput): string {
  switch (id) {
    case 'claude-code': return i.claudeCode || claudeCodeCommand(i.url, i.secret)
    case 'codex': return codexTOML(i.url, i.secret)
    case 'opencode': return opencodeJSON(i.url, i.secret)
    case 'claude-desktop': return desktopJSON(i.url, i.secret)
    case 'cursor': return cursorJSON(i.url, i.secret)
    case 'curl': return curlTest(i.url, i.secret)
  }
}

// wrapCommand breaks a one-line shell command before the URL and each --flag,
// with " \" continuations, as the design prints it. Display only: the copied
// text stays as the API gave it.
export function wrapCommand(cmd: string): string {
  const parts = cmd.match(/"[^"]*"|\S+/g) ?? []
  const lines: string[] = []
  let cur = ''
  for (const p of parts) {
    if (cur !== '' && (p.startsWith('--') || /^https?:\/\//.test(p))) {
      lines.push(cur)
      cur = `  ${p}`
    } else {
      cur = cur ? `${cur} ${p}` : p
    }
  }
  if (cur) lines.push(cur)
  return lines.join(' \\\n')
}

// display is what the well shows for a snippet.
export function display(id: SnippetId, i: SnippetInput): string {
  return id === 'claude-code' ? wrapCommand(snippet(id, i)) : snippet(id, i)
}

// pairCommand sets a machine up to join through pairing: it points the node
// at the hub and starts it, and the node asks to join (no key involved).
export function pairCommand(nodeURL: string): string {
  return `mcp-mesh-node init --hub ${shellWord(nodeURL)} && mcp-mesh-node start`
}

// shellWord quotes s for a POSIX shell when it needs it (as the hub does).
function shellWord(s: string): string {
  return /^[A-Za-z0-9\-_.:/@%+=,]+$/.test(s) ? s : `'${s.replaceAll("'", `'\\''`)}'`
}

// releaseURL is where node archives are published.
const releaseURL = 'https://github.com/AldrinSalazar/mcp-mesh/releases/latest'

export type InstallId = 'brew' | 'unix' | 'windows'

// installOptions are the ways to put mcp-mesh-node on a machine (as in
// docs/node.md).
export const installOptions: { value: InstallId; label: string; command: string; note: string }[] = [
  { value: 'brew', label: 'Homebrew', command: 'brew install aldrinsalazar/tap/mcp-mesh-node', note: 'macOS or Linux. Upgrade with brew upgrade mcp-mesh-node.' },
  {
    value: 'unix',
    label: 'Linux / macOS',
    command: 'p=mcp-mesh-node_$(uname -s | tr A-Z a-z)_$(uname -m | sed \'s/x86_64/amd64/;s/aarch64/arm64/;s/armv7l/armv7/\') && ' +
      `curl -fsSL ${releaseURL}/download/$p.tar.gz | tar -xz && mkdir -p ~/.local/bin && mv $p/mcp-mesh-node ~/.local/bin/`,
    note: 'Downloads the archive for this machine from the latest release into ~/.local/bin.',
  },
  { value: 'windows', label: 'Windows', command: releaseURL, note: 'Preview: download the windows archive from the latest release and put mcp-mesh-node.exe on your PATH.' },
]
