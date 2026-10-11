<script setup lang="ts">
// A client key's groups: each check box saves at once.
import { ref } from 'vue'
import { RouterLink } from 'vue-router'
import GroupPicker from '@/components/groups/GroupPicker.vue'
import { ApiError, api } from '@/lib/api'
import { live, refresh } from '@/lib/live'
import { toast } from '@/lib/toast'
import type { Key } from '@/lib/types'

const props = defineProps<{ keyInfo: Key }>()

const busy = ref(false)

async function change(groups: string[]) {
  if (busy.value) return
  busy.value = true
  try {
    await api.setKeyGroups(props.keyInfo.id, groups)
    toast('Groups updated', groups.length === 0
      ? `${props.keyInfo.name} is in no group and sees no tools.`
      : `${props.keyInfo.name} is in ${groups.length} ${groups.length === 1 ? 'group' : 'groups'}.`)
    await Promise.all([refresh('keys'), refresh('groups')])
  } catch (e) {
    toast('Could not update groups', e instanceof ApiError ? e.message : undefined, 'err')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="panel grp">
    <div class="head">
      <h2 class="section-title">Groups</h2>
      <RouterLink to="/groups" class="arrow-link">Manage groups</RouterLink>
    </div>
    <p class="muted sub">A key sees the tools its groups grant. In no group, it sees nothing.</p>
    <GroupPicker v-if="live.groups.length" :model-value="keyInfo.groups ?? []" :disabled="busy" @update:model-value="change" />
    <p v-else class="muted small">There are no groups yet.</p>
  </section>
</template>

<style scoped>
.grp { padding: 24px; margin-top: 24px; }
.head { display: flex; justify-content: space-between; align-items: center; gap: 16px; }
.sub { font-size: 13px; margin: 8px 0 20px; }
</style>
