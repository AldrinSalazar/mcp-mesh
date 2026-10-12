import type { Component } from 'vue'
import { Activity, Code, KeyRound, LayoutGrid, Monitor, Network, Plug, ShieldCheck, Users, UsersRound } from 'lucide-vue-next'

export interface NavItem {
  name: string // route name
  label: string
  icon: Component
  authOnly?: boolean // hidden when the hub runs with auth off
  count?: 'approvals' // show a count badge from live state
}

export const navItems: NavItem[] = [
  { name: 'overview', label: 'Overview', icon: LayoutGrid },
  { name: 'connect', label: 'Connect', icon: Plug },
  { name: 'mesh', label: 'Mesh map', icon: Network },
  { name: 'nodes', label: 'Nodes', icon: Monitor },
  { name: 'tools', label: 'Tools', icon: Code },
  { name: 'activity', label: 'Activity', icon: Activity },
  { name: 'approvals', label: 'Approvals', icon: ShieldCheck, count: 'approvals' },
  { name: 'keys', label: 'Keys', icon: KeyRound, authOnly: true },
  { name: 'groups', label: 'Groups', icon: UsersRound, authOnly: true },
  { name: 'admins', label: 'Admins', icon: Users, authOnly: true },
]

// Planned screens, shown greyed out under "Coming later".
export const plannedItems: string[] = []
