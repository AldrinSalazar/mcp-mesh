<script setup lang="ts">
// Admins: who can sign in to this hub, plus the hub's own addresses.
import { computed, ref } from 'vue'
import { Plus } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiBadge from '@/components/ui/UiBadge.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import UiSelect from '@/components/ui/UiSelect.vue'
import { api } from '@/lib/api'
import { toast } from '@/lib/toast'
import { trustLevels, trustText } from '@/lib/trust'
import type { TrustLevel } from '@/lib/types'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import AddAdminDialog from '@/components/admins/AddAdminDialog.vue'
import DeleteAdminDialog from '@/components/admins/DeleteAdminDialog.vue'
import { openPassword } from '@/lib/dialogs'
import { date, dayTime } from '@/lib/format'
import { live, refresh, useNow } from '@/lib/live'

const now = useNow(30_000)
const adding = ref(false)
const deleting = ref<string | null>(null)

// "Hub default" is not a choice here: this sets what the default is.
const levelOptions = trustLevels.map((l) => ({ value: l.value, label: l.label }))
const defaultLevel = computed(() => live.settings?.trust_default ?? 'trust')
async function saveDefault(level: TrustLevel) {
  try {
    await api.setTrustDefault(level)
    toast('Default trust level changed')
    await refresh('settings')
  } catch (e) {
    toast("Couldn't change the default", e instanceof Error ? e.message : undefined, 'err')
    await refresh('settings')
  }
}
</script>

<template>
  <PageHeader title="Admins">
    <UiButton variant="primary" :icon="Plus" @click="adding = true">Add admin</UiButton>
  </PageHeader>

  <div class="panel tablebox">
    <SkeletonRows v-if="!live.loaded.admins" :rows="2" />
    <template v-else>
      <table class="table">
        <thead>
          <tr>
            <th>Username</th>
            <th class="hide-s">Created</th>
            <th>Last sign-in</th>
            <th class="hide-s">Password changed</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in live.admins" :key="a.username">
            <td>
              <div class="user">
                <span class="avatar" aria-hidden="true">{{ a.username.charAt(0).toUpperCase() }}</span>
                <span class="nm truncate" :title="a.username">{{ a.username }}</span>
                <UiBadge v-if="a.username === live.username" tone="idle" class="you">You</UiBadge>
              </div>
            </td>
            <td class="hide-s muted small">{{ date(a.created) }}</td>
            <td class="muted small">{{ a.last_login ? dayTime(a.last_login, now) : 'Never' }}</td>
            <td class="hide-s muted small">{{ date(a.password_changed) }}</td>
            <td>
              <div class="actions">
                <button type="button" class="link" @click="openPassword(a.username)">Change password</button>
                <button
                  type="button"
                  class="link del"
                  :disabled="live.admins.length <= 1"
                  :title="live.admins.length <= 1 ? 'At least one admin must remain on the hub.' : undefined"
                  @click="deleting = a.username"
                >Delete</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="caption muted">At least one admin must remain on the hub.</p>
    </template>
  </div>

  <section class="panel info">
    <h2 class="section-title">Hub information</h2>
    <dl>
      <div class="row">
        <dt>Network name</dt>
        <dd>{{ live.info?.discovery_name || 'Not announced' }}</dd>
      </div>
      <div class="row">
        <dt>Hub version</dt>
        <dd class="mono">{{ live.info?.version ?? '–' }}</dd>
      </div>
      <div class="row">
        <dt>Node URL</dt>
        <dd class="mono">{{ live.info?.node_url }}<CopyButton v-if="live.info" :value="live.info.node_url" label="Copy node URL" /></dd>
      </div>
      <div class="row">
        <dt>MCP URL</dt>
        <dd class="mono">{{ live.info?.client_url }}<CopyButton v-if="live.info" :value="live.info.client_url" label="Copy MCP URL" /></dd>
      </div>
      <div class="row">
        <dt>Default trust level</dt>
        <dd class="trust">
          <span class="pick"><UiSelect :model-value="defaultLevel" :options="levelOptions" label="Default trust level" @update:model-value="saveDefault($event as TrustLevel)" /></span>
          <span class="muted note">{{ trustText(defaultLevel) }} Nodes follow this unless set on the Nodes screen.</span>
        </dd>
      </div>
      <div class="row">
        <dt>OAuth</dt>
        <dd v-if="live.settings?.oauth.enabled">On — apps can add <span class="mono url">{{ live.info?.client_url }}</span> and sign in here</dd>
        <dd v-else class="muted">Off</dd>
      </div>
    </dl>
  </section>

  <AddAdminDialog v-if="adding" @close="adding = false" />
  <DeleteAdminDialog v-if="deleting" :username="deleting" @close="deleting = null" />
</template>

<style scoped>
.tablebox { margin-bottom: 24px; overflow-x: auto; }
.table td { padding-top: 24px; padding-bottom: 24px; }
.user { display: flex; align-items: center; gap: 14px; min-width: 0; }
.avatar {
  display: grid;
  place-items: center;
  flex: none;
  width: 34px;
  height: 34px;
  border-radius: 50%;
  background: var(--ink);
  color: var(--surface);
  font-size: 13px;
  font-weight: 600;
}
.nm { font-size: 14px; font-weight: 500; max-width: 200px; }
.you { min-width: 0; margin-left: 10px; }
.actions { display: flex; flex-direction: column; align-items: flex-start; gap: 14px; }
.link { border: 0; background: none; padding: 0; font-size: 12px; color: var(--cobalt); cursor: pointer; }
.link:hover:not(:disabled) { text-decoration: underline; }
.del { color: var(--err); }
.link:disabled { opacity: 0.45; cursor: not-allowed; }
.caption { padding: 14px 24px; font-size: 12px; border-top: 1px solid var(--rule); }
.info { padding: 24px; }
dl { margin-top: 20px; }
.row { display: flex; align-items: center; min-height: 58px; border-bottom: 1px solid var(--rule); font-size: 13px; }
.row:last-child { border-bottom: 0; }
dt { width: 286px; flex: none; color: var(--muted); }
dd { display: flex; align-items: center; flex: 1; min-width: 0; justify-content: space-between; overflow-wrap: anywhere; }
dd.mono { font-size: 13px; }
dd.trust { gap: 16px; flex-wrap: wrap; padding: 8px 0; }
.pick { width: 200px; flex: none; }
.note { font-size: 12px; flex: 1 1 260px; line-height: 1.5; }
.url { margin: 0 4px; font-size: 12px; }
dd:not(.mono) { justify-content: flex-start; flex-wrap: wrap; }
@media (max-width: 700px) {
  .hide-s { display: none; }
  dt { width: 110px; }
}
</style>
