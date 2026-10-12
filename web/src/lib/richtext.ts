// A small, safe reading of the text MCP servers put in descriptions: many
// write Markdown or Markdown-ish structure ("=== SECTION ===" headings, "- "
// lists, `code`). It only splits the text into blocks and inline spans; the
// component renders them as text, so nothing in a description becomes HTML.

export type Inline = { kind: 'text' | 'code' | 'strong'; text: string }

export type Block =
  | { kind: 'heading'; text: Inline[] }
  | { kind: 'paragraph'; text: Inline[] }
  | { kind: 'list'; items: { depth: number; text: Inline[] }[] }
  | { kind: 'code'; text: string }

const fence = /^\s*```/
// "## Title", or "=== Title ===" (also with - or # runs) on a line of its own.
const mdHeading = /^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$/
const ruleHeading = /^\s*(?:={2,}|-{3,}|#{3,})\s*([^=#-].*?)\s*(?:={2,}|-{3,}|#{3,})\s*$/
const bullet = /^(\s*)(?:[-*•]|\d+[.)])\s+(.*)$/

export function parseRichText(src: string): Block[] {
  const blocks: Block[] = []
  const lines = src.replace(/\r\n?/g, '\n').split('\n')
  let para: string[] = []
  const flush = () => {
    if (para.length) blocks.push({ kind: 'paragraph', text: inline(para.join('\n')) })
    para = []
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!
    if (fence.test(line)) {
      flush()
      const code: string[] = []
      while (++i < lines.length && !fence.test(lines[i]!)) code.push(lines[i]!)
      blocks.push({ kind: 'code', text: code.join('\n') })
      continue
    }
    const h = mdHeading.exec(line) ?? ruleHeading.exec(line)
    if (h) {
      flush()
      blocks.push({ kind: 'heading', text: inline(h[1]!) })
      continue
    }
    const b = bullet.exec(line)
    if (b) {
      flush()
      const depth = Math.min(3, Math.floor(b[1]!.replace(/\t/g, '  ').length / 2))
      const last = blocks.at(-1)
      const item = { depth, text: inline(b[2]!) }
      if (last?.kind === 'list') last.items.push(item)
      else blocks.push({ kind: 'list', items: [item] })
      continue
    }
    if (line.trim() === '') {
      flush()
      continue
    }
    // An indented line right after a list item continues it.
    const last = blocks.at(-1)
    if (!para.length && last?.kind === 'list' && /^\s+\S/.test(line)) {
      last.items.at(-1)!.text.push({ kind: 'text', text: ' ' + line.trim() })
      continue
    }
    para.push(line)
  }
  flush()
  return blocks
}

// inline finds `code` and **bold** spans; everything else is text.
export function inline(s: string): Inline[] {
  const out: Inline[] = []
  const re = /`([^`\n]+)`|\*\*([^*\n]+)\*\*/g
  let at = 0
  for (let m = re.exec(s); m; m = re.exec(s)) {
    if (m.index > at) out.push({ kind: 'text', text: s.slice(at, m.index) })
    out.push(m[1] !== undefined ? { kind: 'code', text: m[1] } : { kind: 'strong', text: m[2]! })
    at = m.index + m[0].length
  }
  if (at < s.length) out.push({ kind: 'text', text: s.slice(at) })
  return out
}
