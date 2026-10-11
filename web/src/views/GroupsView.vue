<script setup lang="ts">
// Groups: what each group of client keys may use, and who is in it.
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Plus, Users } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import GroupEditorDialog from '@/components/groups/GroupEditorDialog.vue'
import DeleteGroupDialog from '@/components/groups/DeleteGroupDialog.vue'
import KeyAvatar from '@/components/groups/KeyAvatar.vue'
import { summary } from '@/components/groups/grants'
import { plural } from '@/lib/format'
import { live } from '@/lib/live'
import type { Group } from '@/lib/types'

const route = useRoute()
const router = useRouter()

// editing: undefined = closed, null = new group, Group = that group.
const editing = ref<Group | null | undefined>(undefined)
const deleting = ref<Group | null>(null)

// /groups?group=<id> opens that group's editor once the groups have arrived.
watch(
  () => [route.query.group, live.groups] as const,
  ([id]) => {
    if (typeof id !== 'string' || !live.loaded.groups) return
    const g = live.groups.find((x) => x.id === id)
    if (g) editing.value = g
  },
  { immediate: true },
)

function closeEditor() {
  editing.value = undefined
  if (route.query.group) void router.replace({ query: { ...route.query, group: undefined } })
}

const rows = computed(() =>
  live.groups.map((g) => ({
    g,
    summary: summary(g.grants),
    keys: g.keys.map((id) => live.keys.find((k) => k.id === id)?.name ?? id),
  })),
)
</script>

<template>
  <PageHeader title="Groups">
    <UiButton variant="primary" :icon="Plus" @click="editing = null">New group</UiButton>
  </PageHeader>
  <p class="intro muted">A client sees only the tools its groups grant. A key in no group sees nothing.</p>

  <div v-if="live.loaded.groups && live.groups.length === 0" class="panel empty">
    <EmptyState :icon="Users" title="No groups yet" text="Create a group to choose which tools your client keys can use." action="New group" @action="editing = null" />
  </div>

  <div v-else class="panel tablebox">
    <SkeletonRows v-if="!live.loaded.groups" :rows="3" />
    <table v-else class="table">
      <thead>
        <tr>
          <th>Group</th>
          <th>Grants</th>
          <th class="hide-s">Keys</th>
          <th><span class="visually-hidden">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in rows" :key="r.g.id">
          <td>
            <div class="who">
              <span class="nm truncate" :title="r.g.name">{{ r.g.name }}</span>
              <span class="muted small desc" :title="r.g.description">{{ r.g.description || 'No description' }}</span>
            </div>
          </td>
          <td class="grants" :title="r.summary">{{ r.summary }}</td>
          <td class="hide-s">
            <div v-if="r.keys.length" class="keys">
              <span class="stack"><KeyAvatar v-for="n in r.keys.slice(0, 3)" :key="n" :name="n" /></span>
              <span class="muted small truncate" :title="r.keys.join(', ')">
                {{ r.keys.slice(0, 3).join(', ') }}<template v-if="r.keys.length > 3"> +{{ r.keys.length - 3 }}</template>
              </span>
            </div>
            <span v-else class="muted small">No keys</span>
            <span class="visually-hidden">{{ plural(r.keys.length, 'key') }}</span>
          </td>
          <td class="act">
            <button type="button" class="link" @click="editing = r.g">Edit</button>
            <button type="button" class="link del" @click="deleting = r.g">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>

  <GroupEditorDialog v-if="editing !== undefined" :group="editing" @close="closeEditor" />
  <DeleteGroupDialog v-if="deleting" :group="deleting" @close="deleting = null" />
</template>

<style scoped>
.intro { font-size: 14px; margin: -16px 0 24px; }
.tablebox { overflow-x: auto; }
.empty { padding: 8px; }
.table td { padding-top: 20px; padding-bottom: 20px; vertical-align: middle; }
.who { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.nm { font-size: 14px; font-weight: 500; max-width: 240px; }
.desc { max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.grants { font-size: 13px; max-width: 280px; }
.keys { display: flex; align-items: center; gap: 10px; min-width: 0; }
.stack { display: inline-flex; flex: none; }
.act { text-align: right; white-space: nowrap; }
.link { border: 0; background: none; padding: 0; margin-left: 16px; font-size: 12px; color: var(--cobalt); cursor: pointer; }
.link:hover { text-decoration: underline; }
.del { color: var(--err); }
@media (max-width: 700px) {
  .intro { margin-top: -8px; }
  .hide-s { display: none; }
  .grants { max-width: 140px; }
  .nm { max-width: 120px; }
  .desc { max-width: 120px; }
  .link { display: block; margin: 8px 0 0; }
}
</style>
