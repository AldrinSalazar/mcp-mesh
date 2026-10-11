<script setup lang="ts">
import { ref } from 'vue'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import { ApiError, api } from '@/lib/api'
import { refresh } from '@/lib/live'
import { toast } from '@/lib/toast'
import type { Key } from '@/lib/types'

const props = defineProps<{ keyInfo: Key }>()
const emit = defineEmits<{ close: [] }>()

const busy = ref(false)
const error = ref('')

async function revoke() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.revokeKey(props.keyInfo.id)
    toast('Key revoked', `${props.keyInfo.name} has been disconnected.`)
    void refresh('keys')
    emit('close')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'Could not revoke the key.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal :title="`Revoke ${keyInfo.name}?`" eyebrow="Revoke access" eyebrow-tone="err" @close="emit('close')">
    <p class="lede muted">This key will stop working immediately.</p>
    <div class="box">
      Revoking this key disconnects {{ keyInfo.name }} immediately and cancels its calls in progress.
      {{ keyInfo.kind === 'node' ? 'To reconnect, the machine will need a new key.' : 'To reconnect, it will need a new key.' }}
    </div>
    <p class="id mono muted">Key ID: {{ keyInfo.id }}</p>
    <p v-if="error" class="err-text small" role="alert">{{ error }}</p>
    <template #footer>
      <UiButton @click="emit('close')">Keep key</UiButton>
      <UiButton variant="danger" :loading="busy" @click="revoke">Revoke key</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.lede { font-size: 14px; margin-bottom: 26px; }
.box { padding: 20px 24px; background: var(--err-soft); color: var(--err); font-size: 14px; line-height: 1.5; border-radius: var(--radius); }
.id { margin-top: 28px; font-size: 12px; overflow-wrap: anywhere; }
</style>
