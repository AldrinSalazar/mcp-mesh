<script setup lang="ts">
import { computed } from 'vue'
import { Menu } from 'lucide-vue-next'
import AccountMenu from './AccountMenu.vue'
import StatusDot from '@/components/ui/StatusDot.vue'
import { hubName, live } from '@/lib/live'

defineEmits<{ menu: [] }>()
const conn = computed(() =>
  live.connection === 'live'
    ? { tone: 'ok' as const, text: 'Live' }
    : live.connection === 'reconnecting'
      ? { tone: 'warn' as const, text: 'Reconnecting…' }
      : { tone: 'idle' as const, text: 'Connecting…' },
)
</script>

<template>
  <header class="top">
    <button type="button" class="burger" aria-label="Open navigation" @click="$emit('menu')">
      <Menu :size="24" :stroke-width="1.6" />
    </button>
    <div class="hub">
      <StatusDot :tone="conn.tone === 'ok' ? 'ok' : 'warn'" />
      <span class="hub-name">{{ hubName() }}</span>
      <span class="ver mono">Hub / {{ live.info?.version ?? '…' }}</span>
    </div>
    <div class="right">
      <span class="live" :class="conn.tone" role="status">
        <StatusDot :tone="conn.tone" />{{ conn.text }}
      </span>
      <template v-if="!live.authOff">
        <span class="sep" aria-hidden="true" />
        <AccountMenu />
      </template>
    </div>
  </header>
</template>

<style scoped>
.top {
  display: flex;
  align-items: center;
  height: var(--topbar-h);
  padding: 0 40px;
  background: var(--surface);
  border-bottom: 1px solid var(--rule);
}
.burger { display: none; border: 0; background: none; padding: 6px; margin-left: -6px; margin-right: 8px; cursor: pointer; color: var(--ink); }
.hub { display: flex; align-items: center; gap: 12px; min-width: 0; }
.hub-name { font-size: 14px; font-weight: 600; }
.ver { color: var(--muted); margin-left: 42px; white-space: nowrap; }
.right { margin-left: auto; display: flex; align-items: center; gap: 24px; }
.live { display: inline-flex; align-items: center; gap: 10px; font-size: 12px; font-weight: 500; color: var(--muted); }
.live.ok { color: var(--ok); }
.live.warn { color: var(--warn); }
.sep { width: 1px; height: 32px; background: var(--rule); }
@media (max-width: 900px) {
  .top { padding: 0 20px; }
  .burger { display: block; }
  .ver { display: none; }
}
@media (max-width: 500px) {
  .right { gap: 14px; }
  .sep { display: none; }
}
</style>
