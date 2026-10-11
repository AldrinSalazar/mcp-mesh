<script setup lang="ts">
// Tools: searchable list of every exposed tool with a detail panel. Filters
// and the selection live in the URL so other screens can link here.
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryValue } from 'vue-router'
import { BookText, Code2, MessageSquareText } from 'lucide-vue-next'
import { live } from '@/lib/live'
import { plural } from '@/lib/format'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiSelect from '@/components/ui/UiSelect.vue'
import UiSegmented from '@/components/ui/UiSegmented.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import ToolRow from '@/components/tools/ToolRow.vue'
import ToolDetail from '@/components/tools/ToolDetail.vue'
import PromptRow from '@/components/tools/PromptRow.vue'
import PromptDetail from '@/components/tools/PromptDetail.vue'
import ResourceRow from '@/components/tools/ResourceRow.vue'
import ResourceDetail from '@/components/tools/ResourceDetail.vue'
import { filterTools, mcpOptions } from '@/components/tools/filter'
import { filterPrompts, filterResources, parseKind } from '@/components/tools/kinds'

const PAGE = 50
const route = useRoute()
const router = useRouter()

const first = (v: LocationQueryValue | LocationQueryValue[] | undefined) => (Array.isArray(v) ? v[0] : v) ?? ''
const q = computed(() => first(route.query.q))
const node = computed(() => first(route.query.node))
const mcp = computed(() => first(route.query.mcp))
const toolName = computed(() => first(route.query.tool))
const promptName = computed(() => first(route.query.prompt))
const resourceURI = computed(() => first(route.query.resource))
const kind = computed(() => parseKind(first(route.query.kind)))
const kindOptions = computed(() => [
  { value: 'tools', label: `Tools · ${live.tools.length}` },
  { value: 'prompts', label: `Prompts · ${live.prompts.length}` },
  { value: 'resources', label: `Resources · ${live.resources.length}` },
])
// Each kind keeps its own selection out of the URL when you switch.
const kindModel = computed({
  get: () => kind.value,
  set: (v: string) => setQuery({ kind: v === 'tools' ? '' : v, tool: '', prompt: '', resource: '', pending: '' }),
})
const pending = computed(() => first(route.query.pending) === '1')
const waitingCount = computed(() => live.tools.filter((t) => t.approval !== 'approved').length)

// The search box is typed into; the URL follows it.
const search = ref(q.value)
watch(q, (v) => { if (v !== search.value) search.value = v })

function setQuery(patch: Record<string, string>) {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...route.query, ...patch })) {
    const s = first(v as LocationQueryValue)
    if (s) next[k] = s
  }
  router.replace({ query: next })
}
watch(search, (v) => { if (v !== q.value) setQuery({ q: v }) })

const nodeModel = computed({
  get: () => node.value,
  // A node change drops an MCP that doesn't belong to it.
  set: (v: string) => setQuery({ node: v, mcp: '' }),
})
const mcpModel = computed({ get: () => mcp.value, set: (v: string) => setQuery({ mcp: v }) })

const nodeOptions = computed(() => {
  const names = new Set([...live.nodes.map((n) => n.name), ...live.tools.map((t) => t.node)])
  return [{ value: '', label: 'All nodes' }, ...[...names].sort().map((n) => ({ value: n, label: n }))]
})
const mcpChoices = computed(() => [
  { value: '', label: 'All MCPs' },
  ...mcpOptions(live.nodes, live.tools, node.value).map((m) => ({ value: m, label: m })),
])

const filtered = computed(() => filterTools(live.tools, { q: q.value, node: node.value, mcp: mcp.value, pending: pending.value }))
const filteredPrompts = computed(() => filterPrompts(live.prompts, { q: q.value, node: node.value, mcp: mcp.value }))
const filteredResources = computed(() => filterResources(live.resources, { q: q.value, node: node.value, mcp: mcp.value }))
const kindWord = computed(() => ({ tools: 'tools', prompts: 'prompts', resources: 'resources' })[kind.value])
const countLabel = computed(() => {
  if (kind.value === 'prompts') return plural(filteredPrompts.value.length, 'prompt')
  if (kind.value === 'resources') return plural(filteredResources.value.length, 'resource')
  return plural(filtered.value.length, 'tool')
})
const lazyNote = 'Lazy MCPs list them after their first start.'

// Paging resets when the filters change.
const shown = ref(PAGE)
watch([q, node, mcp, pending, kind], () => { shown.value = PAGE })
const visible = computed(() => filtered.value.slice(0, shown.value))
const visiblePrompts = computed(() => filteredPrompts.value.slice(0, shown.value))
const visibleResources = computed(() => filteredResources.value.slice(0, shown.value))

// Nothing is selected until asked: the list then uses the full width, which
// reads better than a detail panel for a tool nobody picked.
const selected = computed(() => live.tools.find((t) => t.name === toolName.value) ?? null)
function select(name: string) {
  setQuery({ tool: name === toolName.value ? '' : name })
}
const selectedPrompt = computed(() => live.prompts.find((p) => p.name === promptName.value) ?? null)
const selectedResource = computed(() => live.resources.find((r) => r.uri === resourceURI.value) ?? null)
function selectPrompt(name: string) {
  setQuery({ prompt: name === promptName.value ? '' : name })
}
function selectResource(uri: string) {
  setQuery({ resource: uri === resourceURI.value ? '' : uri })
}
const detailOpen = computed(() => (kind.value === 'tools' ? !!selected.value : kind.value === 'prompts' ? !!selectedPrompt.value : !!selectedResource.value))

function clear() {
  search.value = ''
  router.replace({ query: kind.value === 'tools' ? {} : { kind: kind.value } })
}
</script>

<template>
  <PageHeader title="Tools" />

  <UiSegmented v-model="kindModel" :options="kindOptions" label="What the MCPs offer" class="kinds" />

  <div class="toolbar">
    <UiInput v-model="search" search :placeholder="`Search ${kindWord}...`" class="search" :aria-label="`Search ${kindWord}`" />
    <UiSelect v-model="nodeModel" :options="nodeOptions" label="Filter by node" class="sel" />
    <UiSelect v-model="mcpModel" :options="mcpChoices" label="Filter by MCP" class="sel" />
    <label v-if="kind === 'tools' && (waitingCount || pending)" class="toggle">
      <input type="checkbox" :checked="pending" @change="setQuery({ pending: ($event.target as HTMLInputElement).checked ? '1' : '' })" />
      Waiting for approval ({{ waitingCount }})
    </label>
    <span class="count mono muted" aria-live="polite">{{ countLabel }}</span>
  </div>

  <div class="layout" :class="{ split: detailOpen }">
    <section v-if="kind === 'tools'" class="list panel" :class="{ hidden: selected }" aria-label="Tools">
      <SkeletonRows v-if="!live.loaded.tools" :rows="6" />
      <EmptyState v-else-if="!live.tools.length" :icon="Code2" title="No tools available" text="Tools appear when an MCP is ready." action="View nodes" @action="router.push('/nodes')" />
      <EmptyState v-else-if="!filtered.length" :icon="Code2" title="No tools match" text="Try a different search, or widen the filters." action="Clear filters" @action="clear" />
      <template v-else>
        <div class="head label">
          <span class="h1">Exposed tool / description</span>
          <span class="h2">Source</span>
        </div>
        <ul class="rows">
          <ToolRow v-for="t in visible" :key="t.name" :tool="t" :selected="t.name === toolName" @select="select(t.name)" />
        </ul>
        <div class="foot">
          <span class="muted">Showing {{ visible.length }} of {{ filtered.length }} {{ filtered.length === 1 ? 'tool' : 'tools' }}</span>
          <button v-if="visible.length < filtered.length" type="button" class="arrow-link more" @click="shown += PAGE">Load more ↓</button>
        </div>
      </template>
    </section>
    <ToolDetail v-if="kind === 'tools' && selected" :tool="selected" @close="select(selected.name)" @renamed="(n) => setQuery({ tool: n })" />

    <section v-if="kind === 'prompts'" class="list panel" :class="{ hidden: selectedPrompt }" aria-label="Prompts">
      <SkeletonRows v-if="!live.loaded.prompts" :rows="4" />
      <EmptyState v-else-if="!live.prompts.length" :icon="MessageSquareText" title="No prompts" :text="`MCPs that offer prompts show them here. ${lazyNote}`" />
      <EmptyState v-else-if="!filteredPrompts.length" :icon="MessageSquareText" title="No prompts match" text="Try a different search, or widen the filters." action="Clear filters" @action="clear" />
      <template v-else>
        <div class="head label">
          <span class="h1">Exposed prompt / description</span>
          <span class="h2">Source</span>
        </div>
        <ul class="rows">
          <PromptRow v-for="p in visiblePrompts" :key="p.name" :prompt="p" :selected="p.name === promptName" @select="selectPrompt(p.name)" />
        </ul>
        <div class="foot">
          <span class="muted">Showing {{ visiblePrompts.length }} of {{ filteredPrompts.length }} {{ filteredPrompts.length === 1 ? 'prompt' : 'prompts' }}</span>
          <button v-if="visiblePrompts.length < filteredPrompts.length" type="button" class="arrow-link more" @click="shown += PAGE">Load more ↓</button>
        </div>
      </template>
    </section>
    <PromptDetail v-if="kind === 'prompts' && selectedPrompt" :prompt="selectedPrompt" @close="selectPrompt(selectedPrompt.name)" />

    <section v-if="kind === 'resources'" class="list panel" :class="{ hidden: selectedResource }" aria-label="Resources">
      <SkeletonRows v-if="!live.loaded.resources" :rows="4" />
      <EmptyState v-else-if="!live.resources.length" :icon="BookText" title="No resources" :text="`MCPs that offer resources show them here. ${lazyNote}`" />
      <EmptyState v-else-if="!filteredResources.length" :icon="BookText" title="No resources match" text="Try a different search, or widen the filters." action="Clear filters" @action="clear" />
      <template v-else>
        <div class="head label res">
          <span class="h1">Resource / exposed URI</span>
          <span class="h2">Source</span>
          <span class="h3">MIME type</span>
        </div>
        <ul class="rows">
          <ResourceRow v-for="r in visibleResources" :key="r.uri" :resource="r" :selected="r.uri === resourceURI" @select="selectResource(r.uri)" />
        </ul>
        <div class="foot">
          <span class="muted">Showing {{ visibleResources.length }} of {{ filteredResources.length }} {{ filteredResources.length === 1 ? 'resource' : 'resources' }}</span>
          <button v-if="visibleResources.length < filteredResources.length" type="button" class="arrow-link more" @click="shown += PAGE">Load more ↓</button>
        </div>
      </template>
    </section>
    <ResourceDetail v-if="kind === 'resources' && selectedResource" :resource="selectedResource" @close="selectResource(selectedResource.uri)" />
  </div>
</template>

<style scoped>
.kinds { margin-bottom: 20px; }
.toolbar { display: grid; grid-template-columns: minmax(0, 1fr) 232px 232px auto auto; gap: 16px; align-items: center; margin-bottom: 24px; }
.toggle { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; white-space: nowrap; cursor: pointer; }
.count { font-size: 12px; padding: 0 12px 0 16px; white-space: nowrap; }
.layout { display: grid; grid-template-columns: minmax(0, 1fr); gap: 24px; align-items: start; }
.layout.split { grid-template-columns: minmax(0, 1fr) 370px; }
.head { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 190px) 44px; gap: 16px; align-items: center; height: 40px; padding-left: 24px; background: var(--paper); }
.head .h2 { grid-column: 2; }
.head span { white-space: nowrap; }
.head .h1 { grid-column: 1; }
.head.res { grid-template-columns: minmax(0, 1fr) minmax(0, 190px) minmax(0, 120px) 44px; }
.head.res .h3 { grid-column: 3; }
.rows { list-style: none; margin: 0; padding: 0; }
.foot { display: flex; align-items: center; justify-content: space-between; padding: 22px 24px; font-size: 12px; }
.more { border: 0; background: none; cursor: pointer; padding: 0; }
.more::after { content: none; }

@media (max-width: 1100px) {
  .layout.split { grid-template-columns: minmax(0, 1fr); }
  .list.hidden { display: none; }
}
@media (max-width: 900px) {
  .toolbar { grid-template-columns: 1fr 1fr; }
  .search { grid-column: 1 / -1; }
  .count { grid-column: 1 / -1; padding: 0; }
  .toggle { grid-column: 1 / -1; }
}
@media (max-width: 700px) {
  .head .h2, .head .h3 { display: none; }
  .head { padding-left: 16px; }
  .foot { padding: 16px; }
}
</style>
