// Formatting shared by every screen. Functions that depend on the current
// time take `now` so components can pass the ticking clock from useNow().

const pad = (n: number) => String(n).padStart(2, '0')
const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

const toDate = (t: string | Date) => (t instanceof Date ? t : new Date(t))

// clock formats a time of day as 14:32:08 (local time).
export function clock(t: string | Date): string {
  const d = toDate(t)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// date formats as 08 Oct 2026.
export function date(t: string | Date): string {
  const d = toDate(t)
  return `${pad(d.getDate())} ${months[d.getMonth()]} ${d.getFullYear()}`
}

// dayTime formats as "Today, 09:12", "Yesterday, 18:40" or "08 Oct 2026, 09:12".
export function dayTime(t: string | Date, now: number = Date.now()): string {
  const d = toDate(t)
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const start = new Date(now)
  start.setHours(0, 0, 0, 0)
  if (d.getTime() >= start.getTime()) return `Today, ${hm}`
  if (d.getTime() >= start.getTime() - 86_400_000) return `Yesterday, ${hm}`
  return `${date(d)}, ${hm}`
}

// duration formats a call duration: 48 ms, 1.2 s, 2m 05s.
export function duration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`
  if (ms < 60_000) {
    const s = ms / 1000
    return `${s < 10 ? s.toFixed(1) : Math.round(s)} s`
  }
  const s = Math.round(ms / 1000)
  return `${Math.floor(s / 60)}m ${pad(s % 60)}s`
}

// span formats an elapsed time coarsely: 18s, 42m, 3h 24m, 2d 5h.
export function span(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}

export function since(t: string | Date, now: number = Date.now()): string {
  return span(now - toDate(t).getTime())
}

// ago formats a past time relative to now: "Just now", "2m ago", "3h ago",
// "4d ago", then a date.
export function ago(t: string | Date, now: number = Date.now()): string {
  const s = Math.floor((now - toDate(t).getTime()) / 1000)
  if (s < 60) return 'Just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d}d ago`
  return date(t)
}

// agoLong is ago() spelled out for sentences: "18 seconds ago".
export function agoLong(t: string | Date, now: number = Date.now()): string {
  const s = Math.max(0, Math.floor((now - toDate(t).getTime()) / 1000))
  const unit = (n: number, u: string) => `${n} ${u}${n === 1 ? '' : 's'} ago`
  if (s < 60) return unit(s, 'second')
  const m = Math.floor(s / 60)
  if (m < 60) return unit(m, 'minute')
  const h = Math.floor(m / 60)
  if (h < 24) return unit(h, 'hour')
  return unit(Math.floor(h / 24), 'day')
}

export function osLabel(os: string): string {
  switch (os) {
    case 'linux': return 'Linux'
    case 'darwin': return 'macOS'
    case 'windows': return 'Windows'
    case 'freebsd': return 'FreeBSD'
    default: return os
  }
}

// host drops the port from a remote address: "10.0.0.5:51234" → "10.0.0.5".
export function host(addr: string): string {
  if (!addr) return ''
  if (addr.startsWith('[')) return addr.slice(1, addr.indexOf(']'))
  const i = addr.lastIndexOf(':')
  return i > 0 && addr.indexOf(':') === i ? addr.slice(0, i) : addr
}

// shortID abbreviates a key ID for tables: k_n8d2a1b3 → k_n8d2…
export function shortID(id: string): string {
  return id.length > 6 ? `${id.slice(0, 6)}…` : id
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

export function percent(part: number, whole: number): string {
  if (whole <= 0) return '100%'
  const p = (part / whole) * 100
  return `${p >= 99.95 || p === 0 ? Math.round(p) : p.toFixed(1)}%`
}
