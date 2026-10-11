<script setup lang="ts">
import { computed, ref } from 'vue'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { ApiError, api } from '@/lib/api'
import { plural } from '@/lib/format'
import { live, refresh } from '@/lib/live'
import { toast } from '@/lib/toast'
import type { Group } from '@/lib/types'

const props = defineProps<{ group: Group }>()
const emit = defineEmits<{ close: [] }>()

const busy = ref(false)
const error = ref('')

const members = computed(() => live.keys.filter((k) => props.group.keys.includes(k.id)))
// Keys that would be left in no group, so they would see nothing.
const orphaned = computed(() => members.value.filter((k) => (k.groups ?? []).every((id) => id === props.group.id)))
const names = (ks: { name: string }[]) => ks.map((k) => k.name).join(', ')
const everything = computed(() => props.group.grants.some((g) => g.node === '*'))

async function remove() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteGroup(props.group.id)
    toast('Group deleted', `${props.group.name} no longer grants anything.`)
    void refresh('groups')
    void refresh('keys')
    emit('close')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'Could not delete the group.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal :title="`Delete ${group.name}?`" eyebrow="Delete group" eyebrow-tone="err" @close="emit('close')">
    <p v-if="members.length === 0" class="text">No keys are in this group, so no client loses access.</p>
    <p v-else class="text">
      {{ plural(members.length, 'key') }} will lose what this group grants: {{ names(members) }}.
    </p>
    <UiNotice v-if="orphaned.length" tone="warn" title="Some keys will see nothing" class="warn">
      {{ names(orphaned) }} {{ orphaned.length === 1 ? 'is' : 'are' }} in no other group.
      <template v-if="everything">This is the Everything group: keys only in it will see no tools until you add them to another group.</template>
    </UiNotice>
    <p v-if="error" class="err-text small" role="alert">{{ error }}</p>
    <template #footer>
      <UiButton @click="emit('close')">Keep group</UiButton>
      <UiButton variant="danger" :loading="busy" @click="remove">Delete group</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.text { font-size: 14px; line-height: 1.5; color: var(--muted); overflow-wrap: anywhere; }
.warn { margin-top: 24px; }
</style>
