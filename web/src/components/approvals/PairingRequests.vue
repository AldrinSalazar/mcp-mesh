<script setup lang="ts">
// Machines without a key asking to join. Each waits, connected, with a code
// on its screen; Review opens the approval, Decline turns it away (it asks
// again only once restarted).
import { ref } from 'vue'
import { MonitorUp } from 'lucide-vue-next'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiButton from '@/components/ui/UiButton.vue'
import { ApiError, api } from '@/lib/api'
import { refresh } from '@/lib/live'
import { ago, osLabel } from '@/lib/format'
import { toast } from '@/lib/toast'
import type { PairingRequest } from '@/lib/types'
import ApprovePairingDialog from './ApprovePairingDialog.vue'

defineProps<{ requests: PairingRequest[]; now: number }>()

const reviewing = ref<PairingRequest | null>(null)
const busy = ref('')

async function decline(p: PairingRequest) {
  busy.value = p.id
  try {
    await api.denyPairing(p.id)
    toast('Request declined', `${p.node} was told no.`)
  } catch (e) {
    toast('Could not decline', e instanceof ApiError ? e.message : '')
  } finally {
    busy.value = ''
    void refresh('pairings')
  }
}
</script>

<template>
  <section class="panel pairing" aria-labelledby="pairing-title">
    <header class="head">
      <h2 id="pairing-title" class="section-title">Machines asking to join</h2>
      <span class="sub muted">Started without a key. Approving one gives it a node key.</span>
    </header>
    <ul class="list">
      <li v-for="p in requests" :key="p.id" class="row">
        <MonitorUp :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
        <div class="who">
          <div class="line">
            <span class="name truncate" :title="p.node">{{ p.node }}</span>
            <UiBadge v-if="p.name_taken" tone="warn">Name in use</UiBadge>
          </div>
          <span class="meta small muted truncate">
            {{ [p.hostname, osLabel(p.os), p.ip].filter(Boolean).join(' · ') }} · asked {{ ago(p.requested, now) }}
          </span>
        </div>
        <span class="code mono" :aria-label="`Code ${p.code}`">{{ p.code }}</span>
        <div class="actions">
          <UiButton variant="link" :loading="busy === p.id" @click="decline(p)">Decline</UiButton>
          <UiButton variant="primary" @click="reviewing = p">Review</UiButton>
        </div>
      </li>
    </ul>
    <ApprovePairingDialog v-if="reviewing" :request="reviewing" @close="reviewing = null" />
  </section>
</template>

<style scoped>
.pairing { padding-bottom: 8px; }
.head { display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; padding: 20px 24px 8px; }
.sub { font-size: 12px; }
.list { list-style: none; margin: 0; padding: 0 24px; }
.row { display: flex; align-items: center; gap: 16px; padding: 14px 0; min-width: 0; }
.row + .row { border-top: 1px solid var(--rule); }
.icon { color: var(--muted); flex: none; }
.who { display: flex; flex-direction: column; gap: 6px; min-width: 0; flex: 1; }
.line { display: flex; align-items: center; gap: 10px; min-width: 0; }
.name { font-family: var(--font-display); font-size: 18px; font-weight: 500; }
.code { font-size: 18px; letter-spacing: 0.08em; color: var(--cobalt); white-space: nowrap; }
.actions { display: flex; gap: 12px; align-items: center; flex: none; }
@media (max-width: 700px) {
  .head, .list { padding-left: 16px; padding-right: 16px; }
  .row { flex-wrap: wrap; }
  .who { flex-basis: calc(100% - 38px); }
  .actions { margin-left: auto; }
}
</style>
