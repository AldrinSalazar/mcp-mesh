import { describe, expect, it } from 'vitest'
import { aliasError, descriptionError, MAX_ALIAS, MAX_DESCRIPTION } from './alias'

describe('aliasError', () => {
  it.each(['', 'render', 'a', 'Take_Screenshot-2', 'x'.repeat(MAX_ALIAS)])('accepts %j', (a) => {
    expect(aliasError(a)).toBe('')
  })
  it.each(['a b', 'a/b', 'né', 'a.b', ' x', 'x'.repeat(MAX_ALIAS + 1)])('rejects %j', (a) => {
    expect(aliasError(a)).not.toBe('')
  })
})

describe('descriptionError', () => {
  it('accepts text with newlines and tabs', () => {
    expect(descriptionError('line one\n\tline two')).toBe('')
    expect(descriptionError('')).toBe('')
  })
  it('rejects control characters and long text', () => {
    expect(descriptionError('a\u0000b')).not.toBe('')
    expect(descriptionError('a\u001bb')).not.toBe('')
    expect(descriptionError('x'.repeat(MAX_DESCRIPTION + 1))).not.toBe('')
    expect(descriptionError('x'.repeat(MAX_DESCRIPTION))).toBe('')
  })
})
