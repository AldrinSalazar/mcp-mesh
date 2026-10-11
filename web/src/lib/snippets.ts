// Copy-paste connection snippets for a freshly created client key. Pure so
// they can be tested; the dialog only picks one.

export type SnippetId = 'claude-code' | 'claude-desktop' | 'cursor' | 'curl'

export const snippetOptions: { value: SnippetId; label: string; title: string }[] = [
  { value: 'claude-code', label: 'Claude Code', title: 'Claude Code' },
  { value: 'claude-desktop', label: 'Claude Desktop', title: 'Claude Desktop (claude_desktop_config.json)' },
  { value: 'cursor', label: 'Cursor', title: 'Cursor' },
  { value: 'curl', label: 'curl test', title: 'curl test' },
]

export interface SnippetInput {
  url: string
  secret: string
  // claudeCode is the command exactly as the API gives it.
  claudeCode: string
}

// cursorJSON is Cursor's mcp.json entry for a remote Streamable HTTP server.
function cursorJSON(url: string, secret: string): string {
  return JSON.stringify({ mcpServers: { 'mcp-mesh': { url, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2)
}

// desktopJSON is a claude_desktop_config.json entry. That file only launches
// local commands, so a remote server goes through the mcp-remote bridge; the
// header value travels in an env var because args with spaces get mangled on
// Windows.
function desktopJSON(url: string, secret: string): string {
  return JSON.stringify({
    mcpServers: {
      'mcp-mesh': {
        command: 'npx',
        args: ['-y', 'mcp-remote', url, '--header', 'Authorization:${MCPMESH_AUTH}'],
        env: { MCPMESH_AUTH: `Bearer ${secret}` },
      },
    },
  }, null, 2)
}

// curlTest starts an MCP session, which proves the key and URL work. It
// sends initialize because a session accepts nothing else first.
function curlTest(url: string, secret: string): string {
  return [
    `curl -H "Authorization: Bearer ${secret}"`,
    '-H "Content-Type: application/json"',
    '-H "Accept: application/json, text/event-stream"',
    `-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'`,
    url,
  ].join(' ')
}

// snippet returns the text to copy.
export function snippet(id: SnippetId, i: SnippetInput): string {
  switch (id) {
    case 'claude-code': return i.claudeCode
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
  return id === 'claude-code' ? wrapCommand(i.claudeCode) : snippet(id, i)
}
