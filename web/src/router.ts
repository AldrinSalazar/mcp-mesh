import { createRouter, createWebHistory } from 'vue-router'
import AppShell from '@/components/layout/AppShell.vue'
import SignInView from '@/views/SignInView.vue'
import { bootstrap, live } from '@/lib/live'

// Query parameters screens understand (links between screens use them):
//   /nodes?node=<name>          expands that node
//   /tools?tool=<exposed name>  opens that tool's detail
//   /tools?node=<n>&mcp=<m>     filters
//   /activity?tab=audit|live|admin  selects the tab
//   /groups?group=<id>          opens that group
//   /keys/<id>                  a client key: its groups and effective tool list
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/sign-in', name: 'sign-in', component: SignInView, meta: { public: true } },
    {
      path: '/',
      component: AppShell,
      children: [
        { path: '', name: 'overview', component: () => import('@/views/OverviewView.vue') },
        { path: 'connect', name: 'connect', component: () => import('@/views/ConnectView.vue') },
        { path: 'mesh', name: 'mesh', component: () => import('@/views/MeshMapView.vue') },
        { path: 'nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
        { path: 'tools', name: 'tools', component: () => import('@/views/ToolsView.vue'), meta: { wide: true } },
        { path: 'activity', name: 'activity', component: () => import('@/views/ActivityView.vue'), meta: { wide: true } },
        { path: 'keys', name: 'keys', component: () => import('@/views/KeysView.vue'), meta: { authOnly: true } },
        { path: 'keys/:id', name: 'key', component: () => import('@/views/KeyDetailView.vue'), meta: { authOnly: true } },
        { path: 'groups', name: 'groups', component: () => import('@/views/GroupsView.vue'), meta: { authOnly: true } },
        { path: 'approvals', name: 'approvals', component: () => import('@/views/ApprovalsView.vue'), meta: { wide: true } },
        { path: 'admins', name: 'admins', component: () => import('@/views/AdminsView.vue'), meta: { authOnly: true } },
        { path: ':rest(.*)*', name: 'not-found', component: () => import('@/views/NotFoundView.vue') },
      ],
    },
  ],
})

let booted: Promise<void> | null = null

// A page file that fails to load almost always means the hub was upgraded
// while this tab was open: the old build asks for files the new one doesn't
// have. Load the page again (once per target, so a real outage can't loop).
const reloadKey = 'mcpmesh.reloadedFor'
const staleChunk = /dynamically imported module|Importing a module script failed|Unable to preload CSS/i

router.onError((err: unknown, to) => {
  if (!staleChunk.test(err instanceof Error ? err.message : String(err))) return
  try {
    if (sessionStorage.getItem(reloadKey) === to.fullPath) return
    sessionStorage.setItem(reloadKey, to.fullPath)
  } catch {
    // no storage: reload anyway
  }
  location.assign(to.fullPath)
})

router.afterEach(() => {
  try {
    sessionStorage.removeItem(reloadKey)
  } catch {
    // ignore
  }
})

router.beforeEach(async (to) => {
  booted ??= bootstrap()
  await booted
  if (to.meta.public) {
    // Already signed in (or nothing to sign in to): go to the app.
    if (live.signedIn) return { name: 'overview' }
    return true
  }
  if (!live.signedIn) {
    return { name: 'sign-in', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  }
  if (to.meta.authOnly && live.authOff) return { name: 'overview' }
  return true
})

declare module 'vue-router' {
  interface RouteMeta {
    public?: boolean
    authOnly?: boolean
  }
}
