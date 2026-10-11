// User-facing names and one-line descriptions of the node trust levels.
import type { TrustLevel } from '@/lib/types'

export const trustLevels: { value: TrustLevel; label: string; text: string }[] = [
  { value: 'trust', label: 'Trust', text: 'Every tool is available at once.' },
  {
    value: 'review',
    label: 'Review changes',
    text: 'New tools are available at once; a tool whose definition changes waits for approval.',
  },
  { value: 'approve', label: 'Approve everything', text: 'The node and every new or changed tool wait for approval.' },
]

export function trustLabel(level: TrustLevel): string {
  return trustLevels.find((l) => l.value === level)?.label ?? level
}

export function trustText(level: TrustLevel): string {
  return trustLevels.find((l) => l.value === level)?.text ?? ''
}

// The select value for a node: "" follows the hub default.
export function trustChoice(t: { level: TrustLevel; explicit: boolean }): string {
  return t.explicit ? t.level : ''
}

export function trustOptions(defaultLevel: TrustLevel | undefined): { value: string; label: string }[] {
  return [
    { value: '', label: defaultLevel ? `Hub default (${trustLabel(defaultLevel)})` : 'Hub default' },
    ...trustLevels.map((l) => ({ value: l.value, label: l.label })),
  ]
}

// Badge for a tool that clients can't see yet.
export const approvalBadges = {
  new: { label: 'New', tone: 'info' },
  changed: { label: 'Changed', tone: 'warn' },
  node_pending: { label: 'Waiting for node', tone: 'idle' },
} as const
