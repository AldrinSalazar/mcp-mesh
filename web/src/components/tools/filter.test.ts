import { describe, expect, it } from 'vitest'
import type { Tool } from '@/lib/types'
import { filterTools, mcpOptions } from './filter'

const t = (node: string, mcp: string, orig: string, description = ''): Tool => ({
  name: `${node}__${mcp}__${orig}`, node, mcp, original_name: orig, description, input_schema: {}, approval: 'approved', alias: false, generated_name: `${node}__${mcp}__${orig}`, description_override: '',
})
const tools = [t('workstation', 'blender', 'render', 'Render the scene'), t('laptop', 'browser', 'navigate', 'Go to a URL'), t('laptop', 'fs', 'read')]
const nodes = [
  { name: 'workstation', mcps: [{ name: 'blender' }] },
  { name: 'laptop', mcps: [{ name: 'browser' }, { name: 'fs' }] },
]

describe('filterTools', () => {
  it('matches every field case-insensitively', () => {
    expect(filterTools(tools, { q: 'RENDER', node: '', mcp: '' })).toHaveLength(1)
    expect(filterTools(tools, { q: 'url', node: '', mcp: '' })).toHaveLength(1)
    expect(filterTools(tools, { q: 'laptop', node: '', mcp: '' })).toHaveLength(2)
    expect(filterTools(tools, { q: 'blender', node: '', mcp: '' })).toHaveLength(1)
  })
  it('keeps only tools waiting for approval', () => {
    const list = [...tools, { ...t('nas', 'fs', 'ls'), approval: 'new' as const }]
    expect(filterTools(list, { q: '', node: '', mcp: '', pending: true }).map((x) => x.node)).toEqual(['nas'])
  })
  it('applies node and mcp', () => {
    expect(filterTools(tools, { q: '', node: 'laptop', mcp: 'fs' })).toHaveLength(1)
    expect(filterTools(tools, { q: '', node: 'workstation', mcp: 'fs' })).toHaveLength(0)
  })
})

describe('mcpOptions', () => {
  it('limits to the node', () => {
    expect(mcpOptions(nodes, tools, 'laptop')).toEqual(['browser', 'fs'])
    expect(mcpOptions(nodes, tools, '')).toEqual(['blender', 'browser', 'fs'])
  })
})
