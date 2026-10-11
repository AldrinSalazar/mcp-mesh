import { describe, expect, it } from 'vitest'
import { display, snippet, wrapCommand } from './snippets'

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
