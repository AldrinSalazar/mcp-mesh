import { describe, expect, it } from 'vitest'
import { display, pairCommand, snippet, wrapCommand } from './snippets'

const i = {
  url: 'https://mesh.local/mcp',
  secret: 'mm_client_x',
  claudeCode: 'claude mcp add --transport http mesh https://mesh.local/mcp --header "Authorization: Bearer mm_client_x"',
}

describe('snippet', () => {
  it('passes the Claude Code command through untouched', () => {
    expect(snippet('claude-code', i)).toBe(i.claudeCode)
  })
  it('builds Claude Desktop JSON through mcp-remote', () => {
    expect(JSON.parse(snippet('claude-desktop', i))).toEqual({
      mcpServers: {
        'mcp-mesh': {
          command: 'npx',
          args: ['-y', 'mcp-remote', i.url, '--header', 'Authorization:${MCPMESH_AUTH}'],
          env: { MCPMESH_AUTH: 'Bearer mm_client_x' },
        },
      },
    })
  })
  it('builds Cursor JSON with url and headers', () => {
    expect(JSON.parse(snippet('cursor', i))).toEqual({
      mcpServers: { 'mcp-mesh': { url: i.url, headers: { Authorization: 'Bearer mm_client_x' } } },
    })
  })
  it('builds the curl test', () => {
    const s = snippet('curl', i)
    expect(s.startsWith('curl -H "Authorization: Bearer mm_client_x"')).toBe(true)
    expect(s).toContain('"method":"initialize"')
    expect(s.endsWith(` ${i.url}`)).toBe(true)
  })
})

describe('wrapCommand', () => {
  it('wraps before the URL and flags', () => {
    expect(wrapCommand(i.claudeCode)).toBe(
      'claude mcp add \\\n  --transport http mesh \\\n  https://mesh.local/mcp \\\n  --header "Authorization: Bearer mm_client_x"',
    )
  })
  it('only affects the Claude Code display', () => {
    expect(display('curl', i)).toBe(snippet('curl', i))
  })
})

describe('pairCommand', () => {
  it('points the node at the hub and starts it', () => {
    expect(pairCommand('ws://hub.lan:7001/node')).toBe('mcp-mesh-node init --hub ws://hub.lan:7001/node && mcp-mesh-node start')
  })
  it('quotes a URL the shell would split', () => {
    expect(pairCommand("wss://hub/a b's")).toBe("mcp-mesh-node init --hub 'wss://hub/a b'\\''s' && mcp-mesh-node start")
  })
})
