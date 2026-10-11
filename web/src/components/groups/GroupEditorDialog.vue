<script setup lang="ts">
// Create or edit a group: name, description, the access tree and which client
// keys belong to it. Membership lives on the keys, so saving also updates
// each key whose membership changed.
import { computed, ref } from 'vue'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import { ApiError, api } from '@/lib/api'
import { live, refresh } from '@/lib/live'
import { toast } from '@/lib/toast'
import type { Grant, Group } from '@/lib/types'
import AccessTree from './AccessTree.vue'
import CheckBox from './CheckBox.vue'
import { membershipChanges } from './membership'

const props = defineProps<{ group: Group | null }>()
const emit = defineEmits<{ close: [] }>()

const name = ref(props.group?.name ?? '')
const description = ref(props.group?.description ?? '')
const grants = ref<Grant[]>(props.group?.grants.map((g) => ({ ...g })) ?? [])
const members = ref(new Set(props.group?.keys ?? []))
const error = ref('')
const busy = ref(false)

const clientKeys = computed(() =>
  live.keys.filter((k) => k.kind === 'client').slice().sort((a, b) => a.name.localeCompare(b.name)),
)

function setMember(id: string, on: boolean) {
  const s = new Set(members.value)
  if (on) s.add(id)
  else s.delete(id)
  members.value = s
}

async function save() {
  if (busy.value) return
  const n = name.value.trim()
  if (!n) {
    error.value = 'Enter a name for the group.'
    return
  }
  error.value = ''
  busy.value = true
  const body = { name: n, description: description.value.trim(), grants: grants.value }
  let saved: Group
  try {
    saved = props.group ? await api.updateGroup(props.group.id, body) : await api.createGroup(body)
  } catch (e) {
    error.value = e instanceof ApiError && e.status === 409
      ? 'A group with that name exists.'
      : e instanceof ApiError ? e.message : 'Could not save the group.'
    busy.value = false
    return
  }
  // The group is saved; membership is best effort and reported separately.
  const changes = membershipChanges(live.keys, saved.id, members.value)
  const results = await Promise.allSettled(changes.map((c) => api.setKeyGroups(c.key.id, c.groups)))
  const failed = results.filter((r) => r.status === 'rejected').length
  void refresh('groups')
  if (changes.length) void refresh('keys')
  if (failed) toast('Group saved', `${failed} ${failed === 1 ? 'key' : 'keys'} could not be updated.`, 'err')
  else toast(props.group ? 'Group saved' : 'Group created', `${saved.name} is ready.`)
  busy.value = false
  emit('close')
}
</script>

<template>
  <UiModal
    :title="group ? `Edit ${group.name}` : 'New group'"
    :eyebrow="group ? 'Edit group' : 'Control access'"
    :width="760"
    closable
    @close="emit('close')"
  >
    <form id="group-form" novalidate @submit.prevent="save">
      <UiField label="Name" for="group-name" :error="error">
        <UiInput id="group-name" v-model="name" placeholder="Read only" :invalid="!!error" autocomplete="off" />
      </UiField>
      <UiField label="Description" for="group-desc" hint="Optional. Shown in the group list.">
        <UiInput id="group-desc" v-model="description" placeholder="Files and screenshots, nothing that changes anything" autocomplete="off" />
      </UiField>

      <section class="sec">
        <h3 class="lbl">Tools this group grants</h3>
        <AccessTree v-model="grants" />
      </section>

      <section class="sec">
        <h3 class="lbl">Keys in this group</h3>
        <p v-if="clientKeys.length === 0" class="muted small">No client keys yet. Create one on the Keys page and add it here.</p>
        <ul v-else class="keys">
          <li v-for="k in clientKeys" :key="k.id">
            <CheckBox :state="members.has(k.id) ? 'on' : 'off'" @change="(on) => setMember(k.id, on)">
              <span class="truncate" :title="k.name">{{ k.name }}</span>
              <span v-if="k.oauth_client" class="muted small">OAuth · {{ k.oauth_client.name }}</span>
            </CheckBox>
          </li>
        </ul>
      </section>
    </form>
    <template #footer>
      <UiButton @click="emit('close')">Cancel</UiButton>
      <UiButton variant="primary" type="submit" form="group-form" :loading="busy">
        {{ group ? 'Save group' : 'Create group' }}
      </UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.sec { margin-top: 28px; }
.lbl { font-size: 14px; font-weight: 500; margin-bottom: 12px; font-family: var(--font-body); }
.keys { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 24px; }
@media (max-width: 600px) {
  .keys { grid-template-columns: 1fr; }
}
</style>
