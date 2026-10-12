<script setup lang="ts">
// Approve a machine that asked to join: check its code, choose its name,
// and the hub creates a node key and sends it to the machine. A name that
// belongs to a node key already replaces that key.
import { computed, ref } from 'vue'
import { TriangleAlert } from 'lucide-vue-next'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { ApiError, api } from '@/lib/api'
import { live, refresh } from '@/lib/live'
import { osLabel } from '@/lib/format'
import { toast } from '@/lib/toast'
import type { PairingRequest } from '@/lib/types'
import { nameError, nameTaken } from './pairing'

const props = defineProps<{ request: PairingRequest }>()
const emit = defineEmits<{ close: [] }>()

const name = ref(props.request.node)
const busy = ref(false)
const error = ref('')
const trimmed = computed(() => name.value.trim())
const invalid = computed(() => nameError(trimmed.value))
// The keys list knows; the request's flag covers a list not loaded yet.
const taken = computed(() => nameTaken(trimmed.value, live.keys) || (trimmed.value === props.request.node && props.request.name_taken))

const facts = computed(() => [
  ['Host name', props.request.hostname || '–'],
  ['System', [osLabel(props.request.os), props.request.node_version && `node ${props.request.node_version}`].filter(Boolean).join(' · ') || '–'],
  ['Address', props.request.ip],
])

async function approve() {
  if (busy.value || invalid.value) return
  busy.value = true
  error.value = ''
  try {
    await api.approvePairing(props.request.id, { name: trimmed.value, replace: taken.value })
    toast('Machine approved', `${trimmed.value} received its key and is connecting.`)
    void refresh('pairings')
    void refresh('keys')
    emit('close')
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 410)) {
      toast('The machine is gone', e.message)
      void refresh('pairings')
      emit('close')
      return
    }
    error.value = e instanceof ApiError ? e.message : 'Could not approve the machine.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal :title="`Let ${request.node} join?`" eyebrow="Pairing request" @close="emit('close')">
    <p class="lede muted">Approve only a machine you set up. Check that it shows this code:</p>
    <p class="code mono" aria-label="Pairing code">{{ request.code }}</p>
    <dl class="facts">
      <template v-for="[k, v] in facts" :key="k">
        <dt class="muted">{{ k }}</dt>
        <dd class="truncate" :title="v">{{ v }}</dd>
      </template>
    </dl>
    <form id="approve-pairing" @submit.prevent="approve">
      <UiField label="Name in the mesh" for="pairing-name" :error="invalid" :hint="`Its tools are named after it, like ${trimmed || 'name'}__mcp__tool.`">
        <UiInput id="pairing-name" v-model="name" :invalid="!!invalid" autocomplete="off" />
      </UiField>
    </form>
    <UiNotice v-if="taken && !invalid" tone="warn" :icon="TriangleAlert" class="notice">
      A node key for {{ trimmed }} exists. Approving replaces it: that key is revoked and the machine using it disconnects.
    </UiNotice>
    <p v-if="error" class="err-text small" role="alert">{{ error }}</p>
    <template #footer>
      <UiButton @click="emit('close')">Cancel</UiButton>
      <UiButton :variant="taken ? 'danger' : 'primary'" type="submit" form="approve-pairing" :loading="busy" :disabled="!!invalid">
        {{ taken ? 'Replace key and approve' : 'Approve' }}
      </UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.lede { font-size: 14px; margin-bottom: 14px; }
.code { font-size: 32px; letter-spacing: 0.12em; color: var(--cobalt); margin-bottom: 24px; }
.facts { display: grid; grid-template-columns: max-content 1fr; gap: 8px 20px; font-size: 13px; margin: 0 0 28px; }
.facts dd { margin: 0; min-width: 0; }
.notice { margin-top: 20px; }
.err-text { margin-top: 16px; }
</style>
