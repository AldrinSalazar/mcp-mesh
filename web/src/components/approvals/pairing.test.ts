import { describe, expect, it } from 'vitest'
import { nameError, nameTaken } from './pairing'

describe('pairing names', () => {
  it('follows the hub rules for node key names', () => {
    expect(nameError('studio-pc')).toBe('')
    expect(nameError('')).toBe('Enter a name.')
    expect(nameError('my desk')).toBe('Use a name without spaces.')
    expect(nameError('tab\there')).toBe('Use a name without spaces.')
    expect(nameError('x'.repeat(64))).toBe('')
    expect(nameError('x'.repeat(65))).toBe('Use at most 64 characters.')
    // The hub counts bytes.
    expect(nameError('é'.repeat(33))).toBe('Use at most 64 characters.')
  })
  it('knows a name bound to a node key', () => {
    const keys = [{ kind: 'node' as const, name: 'laptop' }, { kind: 'client' as const, name: 'desk' }]
    expect(nameTaken('laptop', keys)).toBe(true)
    expect(nameTaken('desk', keys)).toBe(false)
  })
})
