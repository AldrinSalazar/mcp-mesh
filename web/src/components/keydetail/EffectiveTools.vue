<script setup lang="ts">
// What a client key can actually do right now: the hub's effective tool list
// for the key, grouped by node and MCP.
import { computed, ref, watch } from 'vue'
import { ShieldOff } from 'lucide-vue-next'
import { RouterLink } from 'vue-router'
import UiInput from '@/components/ui/UiInput.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import { ApiError, api } from '@/lib/api'
import { plural } from '@/lib/format'
import { live } from '@/lib/live'
import type { Key, Tool } from '@/lib/types'
import { emptyReason, groupTools } from './effective'

const props = defineProps<{ keyInfo: Key }>()

const tools = ref<Tool[] | null>(null)
const error = ref('')
const search = ref('')

let seq = 0
async function load() {
  const mine = ++seq
  try {
    const list = await api.keyTools(props.keyInfo.id)
    if (mine !== seq) return
    tools.value = list
    error.value = ''
  } catch (e) {
    if (mine !== seq) return
    error.value = e instanceof ApiError ? e.message : 'Could not load the tool list.'
  }
}

// The list depends on groups, connected tools and approvals; the hub pushes
// each as it changes, so refetch on any of them (and when the key's own
// groups change).
watch(() => [props.keyInfo.id, props.keyInfo.groups, live.groups, live.tools, live.approvals], load, { immediate: true })

const keyGroups = computed(() => live.groups.filter((g) => (props.keyInfo.groups ?? []).includes(g.id)))
const grouped = computed(() => groupTools(tools.value ?? [], search.value))
const total = computed(() => tools.value?.length ?? 0)
const reason = computed(() => emptyReason(keyGroups.value))
const pending = computed(() => live.approvals.length)
</script>

<template>
  <section class="panel eff">
    <div class="head">
      <div>
        <h2 class="section-title">Effective tools</h2>
        <p class="muted sub">What this client can use right now, from its groups.</p>
      </div>
      <span v-if="tools" class="count">{{ plural(total, 'tool') }}</span>
    </div>

    <UiNotice v-if="pending" tone="warn" class="pend">
      {{ plural(pending, 'tool') }} {{ pending === 1 ? 'is' : 'are' }} waiting for approval and {{ pending === 1 ? 'isn’t' : 'aren’t' }} included.
      <RouterLink to="/approvals" class="arrow-link">Review approvals</RouterLink>
    </UiNotice>

    <p v-if="error" class="err-text small" role="alert">{{ error }}</p>
    <SkeletonRows v-else-if="!tools" :rows="4" />

    <EmptyState v-else-if="total === 0" :icon="ShieldOff" :title="reason.title" :text="reason.text" />

    <template v-else>
      <div class="find"><UiInput v-model="search" search placeholder="Search this key’s tools" autocomplete="off" /></div>
      <EmptyState v-if="grouped.length === 0" title="No matching tools" text="Try a different name." />
      <div v-for="n in grouped" :key="n.node" class="node">
        <h3 class="nh">{{ n.node }} <span class="muted small">{{ plural(n.count, 'tool') }}</span></h3>
        <div v-for="m in n.mcps" :key="m.mcp" class="mcp">
          <p class="label mh">{{ m.mcp }}</p>
          <ul class="tl">
            <li v-for="t in m.tools" :key="t.name" class="t">
              <span class="what">
                <span class="mono nm truncate" :title="t.name">{{ t.name }}</span>
                <span class="muted small truncate" :title="t.description">{{ t.description || 'No description' }}</span>
              </span>
              <CopyButton :value="t.name" label="Copy exposed name" />
            </li>
          </ul>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.eff { padding: 24px; margin-top: 24px; }
.head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; margin-bottom: 20px; }
.sub { font-size: 13px; margin-top: 8px; }
.count { font-size: 13px; font-weight: 500; white-space: nowrap; }
.pend { margin-bottom: 20px; }
.pend .arrow-link { margin-left: 8px; }
.find { margin-bottom: 8px; max-width: 456px; }
.nh { font-size: 16px; margin: 24px 0 4px; display: flex; align-items: baseline; gap: 10px; }
.mh { margin: 14px 0 4px; }
.tl { list-style: none; margin: 0; padding: 0; }
.t { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 12px 0; border-bottom: 1px solid var(--rule); }
.what { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.nm { font-size: 12px; }
</style>
