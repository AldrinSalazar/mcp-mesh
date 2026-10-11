<script setup lang="ts">
// The access tree: All nodes → node → MCP → tool, with tri-state check boxes.
// It edits the grant list (v-model); see grants.ts for the rules.
import { computed, ref, watch } from 'vue'
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
import UiInput from '@/components/ui/UiInput.vue'
import UiButton from '@/components/ui/UiButton.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { plural } from '@/lib/format'
import { live } from '@/lib/live'
import type { Grant } from '@/lib/types'
import CheckBox from './CheckBox.vue'
import { buildTree, check, countTools, filterTree, future, toggle, type Path } from './grants'

const model = defineModel<Grant[]>({ required: true })

// The tree is built once from the grants the editor opened with, so rows for
// disconnected things stay put when their last grant is unchecked.
const initial = model.value.slice()
const full = computed(() => buildTree(live.nodes, live.tools, initial))

const search = ref('')
const open = ref(new Set<string>())
const searching = computed(() => search.value.trim() !== '')
const shown = computed(() => filterTree(full.value, search.value))
const counts = computed(() => countTools(full.value, model.value))
const rootState = computed(() => check(model.value, full.value, []))

const key = (p: Path) => p.join('/')
const isOpen = (p: Path) => searching.value || open.value.has(key(p))
function flip(p: Path) {
  const s = new Set(open.value)
  if (!s.delete(key(p))) s.add(key(p))
  open.value = s
}
function set(p: Path, on: boolean) {
  model.value = toggle(model.value, full.value, p, on)
}
const state = (p: Path) => check(model.value, full.value, p)
// whole marks a node or MCP granted whole: what it adds later is granted too.
const whole = (p: Path) => future(model.value, p)

function expandAll(on: boolean) {
  const s = new Set<string>()
  if (on) {
    for (const n of full.value) {
      s.add(n.name)
      for (const m of n.mcps) s.add(`${n.name}/${m.name}`)
    }
  }
  open.value = s
}

// Start with nodes that have a partial selection open, so what a group
// grants is visible when its editor opens.
watch(
  () => full.value.length,
  () => {
    if (open.value.size) return
    const s = new Set<string>()
    for (const n of full.value) {
      if (check(model.value, full.value, [n.name]) !== 'partial') continue
      s.add(n.name)
      for (const m of n.mcps) if (check(model.value, full.value, [n.name, m.name]) === 'partial') s.add(`${n.name}/${m.name}`)
    }
    open.value = s
  },
  { immediate: true },
)
</script>

<template>
  <div class="tree">
    <div class="bar">
      <div class="find"><UiInput v-model="search" search placeholder="Filter nodes, MCPs and tools" autocomplete="off" /></div>
      <div class="exp">
        <UiButton variant="link" @click="expandAll(true)">Expand all</UiButton>
        <UiButton variant="link" @click="expandAll(false)">Collapse</UiButton>
      </div>
    </div>

    <div class="box">
      <div class="row root">
        <CheckBox :state="rootState" label="All nodes" @change="(on) => set([], on)">
          <strong>All nodes</strong>
          <span class="muted small">grants every node, now and later</span>
        </CheckBox>
        <span class="count muted small">{{ counts.granted }} of {{ plural(counts.total, 'tool') }}</span>
      </div>

      <EmptyState v-if="shown.length === 0" :title="searching ? 'Nothing matches' : 'No nodes yet'"
        :text="searching ? 'Try another name.' : 'Nodes and their tools appear here once they connect.'" />

      <ul v-else class="lvl">
        <li v-for="n in shown" :key="n.name">
          <div class="row">
            <button type="button" class="chev" :aria-expanded="isOpen([n.name])" :aria-label="`${isOpen([n.name]) ? 'Collapse' : 'Expand'} ${n.name}`" @click="flip([n.name])">
              <component :is="isOpen([n.name]) ? ChevronDown : ChevronRight" :size="18" :stroke-width="1.6" />
            </button>
            <CheckBox :state="state([n.name])" :label="n.name" @change="(on) => set([n.name], on)">
              <span class="truncate" :title="n.name">{{ n.name }}</span>
              <span v-if="!n.connected" class="nc">not connected</span>
              <span v-if="whole([n.name]) && rootState !== 'on'" class="muted small">now and later</span>
            </CheckBox>
          </div>
          <ul v-if="isOpen([n.name])" class="lvl">
            <li v-for="m in n.mcps" :key="m.name">
              <div class="row">
                <button type="button" class="chev" :aria-expanded="isOpen([n.name, m.name])" :aria-label="`${isOpen([n.name, m.name]) ? 'Collapse' : 'Expand'} ${m.name}`" @click="flip([n.name, m.name])">
                  <component :is="isOpen([n.name, m.name]) ? ChevronDown : ChevronRight" :size="18" :stroke-width="1.6" />
                </button>
                <CheckBox :state="state([n.name, m.name])" :label="`${m.name} on ${n.name}`" @change="(on) => set([n.name, m.name], on)">
                  <span class="mono truncate" :title="m.name">{{ m.name }}</span>
                  <span v-if="!m.connected && n.connected" class="nc">not connected</span>
                  <span v-if="whole([n.name, m.name]) && !whole([n.name])" class="muted small">now and later</span>
                </CheckBox>
                <span class="count muted small">{{ plural(m.tools.length, 'tool') }}</span>
              </div>
              <ul v-if="isOpen([n.name, m.name])" class="lvl">
                <li v-for="t in m.tools" :key="t.name" class="row leaf">
                  <CheckBox :state="state([n.name, m.name, t.name])" :label="t.name" @change="(on) => set([n.name, m.name, t.name], on)">
                    <span class="mono small truncate" :title="t.name">{{ t.name }}</span>
                    <span v-if="!t.connected && m.connected" class="nc">not connected</span>
                  </CheckBox>
                </li>
                <li v-if="m.tools.length === 0" class="row leaf muted small">No tools right now.</li>
              </ul>
            </li>
          </ul>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.bar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; flex-wrap: wrap; }
.find { flex: 1; min-width: 200px; }
.exp { display: flex; gap: 4px; }
.exp :deep(.btn) { height: 32px; padding: 0 8px; font-size: 12px; }
.box { border: 1px solid var(--rule); border-radius: var(--radius); max-height: 252px; overflow-y: auto; background: var(--surface); }
.lvl { list-style: none; margin: 0; padding: 0; }
.lvl .lvl { padding-left: 28px; }
.row { display: flex; align-items: center; gap: 8px; min-height: 40px; padding: 4px 14px 4px 10px; }
.root { position: sticky; top: 0; z-index: 1; background: var(--surface); border-bottom: 1px solid var(--rule); padding-left: 14px; justify-content: space-between; }
.leaf { padding-left: 38px; }
.chev { flex: none; display: grid; place-items: center; width: 28px; height: 28px; border: 0; background: none; color: var(--muted); cursor: pointer; }
.chev:hover { color: var(--ink); }
.count { margin-left: auto; white-space: nowrap; }
.nc { font-size: 11px; padding: 2px 6px; border-radius: var(--radius); background: var(--warn-soft); color: var(--warn); white-space: nowrap; }
</style>
