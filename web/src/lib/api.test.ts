import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('approve', () => {
  it('posts the refs and surfaces the stale list on 409', async () => {
    const stale = [{ node: 'n', mcp: 'a', tool: 't', hash: 'h1' }]
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'changed', stale }), { status: 409 }))
    vi.stubGlobal('fetch', fetchMock)
    const err = await api.approve([stale[0]!]).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).stale).toEqual(stale)
    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ tools: stale })
  })

  it('approveNode sends an empty tool list', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await api.approveNode('n', [])
    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ tools: [] })
  })
})
