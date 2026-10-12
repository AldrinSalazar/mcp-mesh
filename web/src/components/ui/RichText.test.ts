import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import RichText from './RichText.vue'

describe('RichText', () => {
  it('renders structure as elements and markup as text', () => {
    const w = mount(RichText, { props: { text: '=== TYPES ===\n- run `code`\n<b>not bold</b>' } })
    expect(w.find('h4').text()).toBe('TYPES')
    expect(w.find('li code').text()).toBe('code')
    expect(w.find('b').exists()).toBe(false)
    expect(w.text()).toContain('<b>not bold</b>')
  })

  it('folds a long description behind Show all', async () => {
    const w = mount(RichText, { props: { text: 'x'.repeat(800), foldAt: 700 } })
    expect(w.find('.rich').classes()).toContain('folded')
    await w.find('button').trigger('click')
    expect(w.find('.rich').classes()).not.toContain('folded')
    expect(w.find('button').text()).toBe('Show less')
    expect(mount(RichText, { props: { text: 'short' } }).find('button').exists()).toBe(false)
  })
})
