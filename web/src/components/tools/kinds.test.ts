import { describe, expect, it } from 'vitest'
import type { Prompt, ResourceItem } from '@/lib/types'
import { filterPrompts, filterResources, parseKind } from './kinds'

const p = (node: string, mcp: string, orig: string, description = ''): Prompt => ({ name: `${node}__${mcp}__${orig}`, node, mcp, original_name: orig, description })
const r = (node: string, mcp: string, name: string, template = false): ResourceItem => ({
  uri: `mcp-mesh://${node}/${mcp}/file:///${name}`, template, node, mcp, original_uri: `file:///${name}`, name, description: '', mime_type: 'text/markdown',
})
const none = { q: '', node: '', mcp: '' }

describe('parseKind', () => {
  it('defaults to tools', () => {
    expect(parseKind('prompts')).toBe('prompts')
    expect(parseKind('resources')).toBe('resources')
    expect(parseKind('')).toBe('tools')
    expect(parseKind('nope')).toBe('tools')
  })
})

describe('filterPrompts', () => {
  const list = [p('mac-mini', 'notes', 'summarize', 'Summarize a note'), p('workstation', 'blender', 'render_preview')]
  it('searches name, description and source', () => {
    expect(filterPrompts(list, { ...none, q: 'SUMMAR' })).toHaveLength(1)
    expect(filterPrompts(list, { ...none, q: 'blender' })).toHaveLength(1)
  })
  it('applies node and mcp', () => {
    expect(filterPrompts(list, { ...none, node: 'mac-mini' })).toHaveLength(1)
    expect(filterPrompts(list, { ...none, node: 'mac-mini', mcp: 'blender' })).toHaveLength(0)
  })
})

describe('filterResources', () => {
  const list = [r('mac-mini', 'notes', 'today.md'), r('mac-mini', 'notes', '{title}.md', true), r('nas', 'docs', 'a.txt')]
  it('searches uri, name and mime type', () => {
    expect(filterResources(list, { ...none, q: 'today' })).toHaveLength(1)
    expect(filterResources(list, { ...none, q: 'markdown' })).toHaveLength(3)
    expect(filterResources(list, { ...none, q: '{title}' })).toHaveLength(1)
  })
  it('applies node and mcp', () => {
    expect(filterResources(list, { ...none, node: 'nas' })).toHaveLength(1)
    expect(filterResources(list, { ...none, mcp: 'notes' })).toHaveLength(2)
  })
})
