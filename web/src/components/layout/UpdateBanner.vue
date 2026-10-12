<script setup lang="ts">
// "mcp-mesh 1.3.0 is available": shown while the hub runs an older release,
// until dismissed. Dismissing hides that release only; the next one shows
// again.
import { computed, ref } from 'vue'
import { CircleArrowUp, X } from 'lucide-vue-next'
import { live } from '@/lib/live'
import { versionLabel } from '@/lib/format'
import CopyField from '@/components/ui/CopyField.vue'

const storeKey = 'mcpmesh.update.dismissed'
function remembered(): string {
  try {
    return localStorage.getItem(storeKey) ?? ''
  } catch {
    return ''
  }
}
const dismissed = ref(remembered())

const update = computed(() => live.update)
const show = computed(() => !!update.value?.update_available && update.value.latest !== dismissed.value)

function dismiss() {
  const latest = update.value?.latest ?? ''
  dismissed.value = latest
  try {
    localStorage.setItem(storeKey, latest)
  } catch {
    // no storage: hidden until the page reloads
  }
}
</script>

<template>
  <div v-if="show && update" class="banner" role="status">
    <CircleArrowUp :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
    <div class="body">
      <p class="title">
        mcp-mesh {{ versionLabel(update.latest) }} is available <span class="muted">(this hub runs {{ versionLabel(update.version) }})</span>
      </p>
      <p class="text">
        <a v-if="update.release_url" :href="update.release_url" target="_blank" rel="noopener">Release notes</a>
        <template v-if="update.release_url"> · </template>Upgrade a hub run with Docker Compose:
      </p>
      <CopyField class="cmd" value="docker compose pull && docker compose up -d" label="Upgrade command" />
    </div>
    <button type="button" class="close" aria-label="Dismiss until the next release" title="Dismiss until the next release" @click="dismiss">
      <X :size="18" :stroke-width="1.6" aria-hidden="true" />
    </button>
  </div>
</template>

<style scoped>
.banner {
  display: flex;
  gap: 14px;
  align-items: flex-start;
  margin-bottom: 24px;
  padding: 16px 20px;
  background: var(--cobalt-soft);
  border: 1px solid var(--rule);
}
.icon { flex: none; color: var(--cobalt); margin-top: 1px; }
.body { flex: 1; min-width: 0; }
.title { font-size: 14px; font-weight: 600; }
.title .muted { font-weight: 400; }
.text { font-size: 12px; margin-top: 6px; }
.cmd { margin-top: 10px; max-width: 520px; }
.close {
  flex: none;
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  border: 0;
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.close:hover { color: var(--ink); }
</style>
