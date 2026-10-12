import { describe, expect, it } from 'vitest'
import { inline, parseRichText } from './richtext'

const t = (text: string) => [{ kind: 'text', text }]

describe('parseRichText', () => {
  it('reads a Fusion-style description', () => {
    const src = [
      'Execute operations in the active Fusion model.',
      '=== FEATURE TYPES ===',
      'purchase:',
      '- Opens the in-app Purchase Manager',
      '  - Invoke when the user asks to buy',
      '- featureType: `"purchase"` (required)',
      '',
      'Before calling this tool:',
      '- Call Read {"queryType":"licensing"} first',
    ].join('\n')
    expect(parseRichText(src)).toEqual([
      { kind: 'paragraph', text: t('Execute operations in the active Fusion model.') },
      { kind: 'heading', text: t('FEATURE TYPES') },
      { kind: 'paragraph', text: t('purchase:') },
      { kind: 'list', items: [
        { depth: 0, text: t('Opens the in-app Purchase Manager') },
        { depth: 1, text: t('Invoke when the user asks to buy') },
        { depth: 0, text: [{ kind: 'text', text: 'featureType: ' }, { kind: 'code', text: '"purchase"' }, { kind: 'text', text: ' (required)' }] },
      ] },
      { kind: 'paragraph', text: t('Before calling this tool:') },
      { kind: 'list', items: [{ depth: 0, text: t('Call Read {"queryType":"licensing"} first') }] },
    ])
  })

  it('reads Markdown headings, numbered lists and code fences', () => {
    expect(parseRichText('## Usage\n1. first\n2. second\n```\nx = 1\n```')).toEqual([
      { kind: 'heading', text: t('Usage') },
      { kind: 'list', items: [{ depth: 0, text: t('first') }, { depth: 0, text: t('second') }] },
      { kind: 'code', text: 'x = 1' },
    ])
  })

  it('keeps a plain description as one paragraph, line breaks and all', () => {
    expect(parseRichText('Read a file.\nReturns its text.')).toEqual([{ kind: 'paragraph', text: t('Read a file.\nReturns its text.') }])
  })

  it('never turns markup into anything but text', () => {
    const blocks = parseRichText('<img src=x onerror=alert(1)> **bold**')
    expect(blocks).toEqual([{ kind: 'paragraph', text: [{ kind: 'text', text: '<img src=x onerror=alert(1)> ' }, { kind: 'strong', text: 'bold' }] }])
  })

  it('does not take a lone dash line or a negative number for a list', () => {
    expect(parseRichText('-5 degrees')).toEqual([{ kind: 'paragraph', text: t('-5 degrees') }])
  })
})

describe('inline', () => {
  it('splits code and bold', () => {
    expect(inline('a `b` **c** d')).toEqual([
      { kind: 'text', text: 'a ' }, { kind: 'code', text: 'b' }, { kind: 'text', text: ' ' }, { kind: 'strong', text: 'c' }, { kind: 'text', text: ' d' },
    ])
  })
})
