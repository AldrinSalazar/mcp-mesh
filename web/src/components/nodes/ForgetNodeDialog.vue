<script setup lang="ts">
// Confirm before dropping an offline node from the list. Its key, trust
// level and approvals stay; it is listed again when it connects.
import { ref } from 'vue'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import { api, ApiError } from '@/lib/api'
import { refresh } from '@/lib/live'
import { toast } from '@/lib/toast'

const props = defineProps<{ node: string }>()
const emit = defineEmits<{ close: [] }>()
const busy = ref(false)

async function forget() {
  busy.value = true
  try {
    await api.forgetNode(props.node)
    toast(`${props.node} forgotten`)
    void refresh('nodes')
    emit('close')
  } catch (e) {
    toast(`Couldn't forget ${props.node}`, e instanceof ApiError ? e.message : 'Try again.', 'err')
    busy.value = false
  }
}
</script>

<template>
  <UiModal :title="`Forget ${node}?`" eyebrow="Offline machine" :width="520" @close="emit('close')">
    <p class="lead muted">
      {{ node }} is no longer listed while it is offline. Its key, trust level and approved tools stay: if it connects again, it shows up again.
      To keep it out, revoke its key in Keys.
    </p>
    <template #footer>
      <UiButton @click="emit('close')">Keep it</UiButton>
      <UiButton variant="primary" :loading="busy" @click="forget">Forget</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.lead { font-size: 14px; line-height: 1.5; }
</style>
