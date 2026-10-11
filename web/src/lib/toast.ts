// Toasts: short confirmations in the bottom-right corner.
import { reactive } from 'vue'

export interface Toast {
  id: number
  title: string
  body?: string
  tone: 'ok' | 'err'
}

export const toasts = reactive<Toast[]>([])
let next = 1

export function toast(title: string, body?: string, tone: Toast['tone'] = 'ok', ms = 4000): void {
  const t = { id: next++, title, body, tone }
  toasts.push(t)
  setTimeout(() => dismiss(t.id), ms)
}

export function dismiss(id: number): void {
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}
