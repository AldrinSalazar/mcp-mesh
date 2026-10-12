import { describe, expect, it } from 'vitest'
import { actionLabel, actorLabel, isFailure, targetView } from './adminlog'

describe('adminlog', () => {
  it('labels actions, passing unknown codes through', () => {
    expect(actionLabel('key.groups')).toBe("Changed a key's groups")
    expect(actionLabel('sign_in_failed')).toBe('Failed sign-in')
    expect(actionLabel('future.thing')).toBe('future.thing')
  })
  it('labels actors', () => {
    expect(actorLabel('cli')).toBe('command line')
    expect(actorLabel('')).toBe('—')
    expect(actorLabel('alice')).toBe('alice')
  })
  it('flags failures', () => {
    expect(isFailure({ action: 'sign_in_failed' })).toBe(true)
    expect(isFailure({ action: 'sign_in' })).toBe(false)
  })
  it('resolves key ids to names', () => {
    const keys = [{ id: 'k_1', name: 'laptop' }]
    expect(targetView({ action: 'key.revoke', target: 'k_1', detail: '' }, keys)).toEqual({ text: 'laptop', mono: false })
    expect(targetView({ action: 'key.create', target: 'k_9', detail: 'node nas' }, keys)).toEqual({ text: 'k_9 · node nas', mono: true })
    expect(targetView({ action: 'sign_in_failed', target: '', detail: '' }, keys)).toEqual({ text: '—', mono: false })
    expect(targetView({ action: 'trust.level', target: 'nas', detail: 'approve' }, keys)).toEqual({ text: 'nas · approve', mono: false })
  })
  it('shows pairings', () => {
    expect(actionLabel('pairing.approve')).toBe('Approved a machine')
    expect(actionLabel('pairing.deny')).toBe('Declined a machine')
    // An approval's target is the key it created.
    expect(targetView({ action: 'pairing.approve', target: 'k_1', detail: 'node laptop from 10.0.0.9' }, [{ id: 'k_1', name: 'laptop' }]))
      .toEqual({ text: 'laptop · node laptop from 10.0.0.9', mono: false })
  })
})
