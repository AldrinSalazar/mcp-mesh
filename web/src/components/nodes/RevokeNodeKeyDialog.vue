<script setup lang="ts">
// Confirm before cutting a node off: revoking its key disconnects it now.
import { ref } from 'vue'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { api, ApiError } from '@/lib/api'
import { refresh } from '@/lib/live'
import { shortID } from '@/lib/format'
import { toast } from '@/lib/toast'

const props = defineProps<{ node: string; keyId: string }>()
const emit = defineEmits<{ close: [] }>()
const busy = ref(false)

async function revoke() {
  busy.value = true
  try {
    await api.revokeKey(props.keyId)
    toast('Key revoked', `${props.node} has been disconnected.`)
    void refresh('keys')
    emit('close')
  } catch (e) {
    toast('Could not revoke the key', e instanceof ApiError ? e.message : 'Try again.', 'err')
    busy.value = false
  }
}
</script>

<template>
  <UiModal :title="`Revoke ${node}?`" eyebrow="Revoke access" eyebrow-tone="err" :width="560" @close="emit('close')">
    <p class="lead muted">This key will stop working immediately.</p>
    <UiNotice tone="err">
      Revoking this key disconnects {{ node }} immediately and cancels its calls in progress. To reconnect, the machine will need a new key.
    </UiNotice>
    <p class="id mono muted">Key ID: {{ shortID(keyId) }}</p>
    <template #footer>
      <UiButton @click="emit('close')">Keep key</UiButton>
      <UiButton variant="danger" :loading="busy" @click="revoke">Revoke key</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.lead { margin-bottom: 24px; font-size: 14px; }
.id { margin-top: 28px; }
</style>
