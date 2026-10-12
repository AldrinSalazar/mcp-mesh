<script setup lang="ts">
// Create a key in two steps: pick the type and name, then show the secret
// once with what to paste where. Step 2 is persistent so the secret can't
// be lost to a stray Escape or click.
import { computed, ref, watch } from 'vue'
import { Check, KeyRound, Monitor } from 'lucide-vue-next'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import UiSelect from '@/components/ui/UiSelect.vue'
import CopyField from '@/components/ui/CopyField.vue'
import { ApiError, api } from '@/lib/api'
import { live, refresh } from '@/lib/live'
import { display, pairCommand, snippet, snippetOptions, type SnippetId } from '@/lib/snippets'
import type { ClientSetup, CreatedKey, KeyKind, NodeSetup, TrustLevel } from '@/lib/types'
import GroupPicker from '@/components/groups/GroupPicker.vue'
import { trustLabel, trustLevels } from '@/lib/trust'

const props = defineProps<{ kind: KeyKind }>()
const emit = defineEmits<{ close: [] }>()

const kind = ref<KeyKind>(props.kind)
const name = ref('')
const error = ref('')
const busy = ref(false)
const created = ref<CreatedKey | null>(null)
const which = ref<SnippetId>('claude-code')

const types = [
  { value: 'node' as const, title: 'Node key', text: 'Connect a machine', icon: Monitor },
  { value: 'client' as const, title: 'Client key', text: 'Connect an agent or app', icon: KeyRound },
]

const isNode = computed(() => kind.value === 'node')

// Client keys: groups to join (Everything preselected, as the hub would do).
// The list may still be loading when the dialog opens, so preselect once.
const groups = ref<string[]>([])
let preselected = false
watch(
  () => live.groups,
  (list) => {
    if (preselected || list.length === 0) return
    preselected = true
    const all = list.find((g) => g.name.toLowerCase() === 'everything')
    if (all) groups.value = [all.id]
  },
  { immediate: true },
)

// Node keys: the node's trust level, or 'default' to follow the hub's.
const trust = ref<'default' | TrustLevel>('default')
const trustOptions = computed(() => [
  {
    value: 'default' as const,
    title: `Hub default (${trustLabel(live.settings?.trust_default ?? 'trust')})`,
    text: 'Follows the level set for the whole hub.',
  },
  ...trustLevels.map((l) => ({ value: l.value, title: l.label, text: l.text })),
])

async function submit() {
  if (busy.value) return
  const n = name.value.trim()
  if (!n) {
    error.value = isNode.value ? 'Enter the machine name.' : 'Enter a client name.'
    return
  }
  error.value = ''
  busy.value = true
  try {
    created.value = await api.createKey(
      kind.value,
      n,
      isNode.value ? (trust.value === 'default' ? {} : { trust: trust.value }) : { groups: groups.value },
    )
    void refresh('keys')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'Could not create the key.'
  } finally {
    busy.value = false
  }
}

const nodeSetup = computed(() => created.value?.setup as NodeSetup | undefined)
const clientSetup = computed(() => created.value?.setup as ClientSetup | undefined)
const input = computed(() => ({
  url: clientSetup.value?.mcp_url ?? '',
  secret: created.value?.secret ?? '',
  claudeCode: clientSetup.value?.claude_code ?? '',
}))
const snippetTitle = computed(() => snippetOptions.find((o) => o.value === which.value)?.title ?? '')
</script>

<template>
  <UiModal
    v-if="!created"
    title="Create a key"
    eyebrow="Connect to your mesh"
    closable
    @close="emit('close')"
  >
    <form id="create-key" novalidate @submit.prevent="submit">
      <p class="lbl" id="key-type">Key type</p>
      <div class="types" role="radiogroup" aria-labelledby="key-type">
        <button
          v-for="t in types"
          :key="t.value"
          type="button"
          role="radio"
          class="type"
          :class="{ on: kind === t.value }"
          :aria-checked="kind === t.value"
          @click="kind = t.value"
        >
          <span class="type-title"><component :is="t.icon" :size="22" :stroke-width="1.6" aria-hidden="true" />{{ t.title }}</span>
          <span class="type-text">{{ t.text }}</span>
        </button>
      </div>
      <UiNotice v-if="isNode && live.settings?.pairing" tone="info" class="pair" title="A machine doesn't need a key made here">
        Run this on it instead, and approve it under Approvals when it asks to join:
        <CopyField :value="pairCommand(live.info?.node_url ?? '')" label="Copy setup command" class="pair-cmd" multiline />
      </UiNotice>
      <UiField
        :label="isNode ? 'Machine name' : 'Client name'"
        for="key-name"
        :hint="isNode ? 'The key only works for a node with this name; the setup command names the machine for you.' : 'No spaces. Use a name you’ll recognize in the activity log.'"
        :error="error"
      >
        <UiInput
          id="key-name"
          v-model="name"
          :placeholder="isNode ? 'studio-pc' : 'claude-code'"
          :invalid="!!error"
          autocomplete="off"
        />
      </UiField>

      <section v-if="isNode" class="opt">
        <p class="lbl" id="key-trust">Trust</p>
        <div class="trusts" role="radiogroup" aria-labelledby="key-trust">
          <button
            v-for="t in trustOptions"
            :key="t.value"
            type="button"
            role="radio"
            class="trust"
            :class="{ on: trust === t.value }"
            :aria-checked="trust === t.value"
            @click="trust = t.value"
          >
            <span class="type-title">{{ t.title }}</span>
            <span class="type-text">{{ t.text }}</span>
          </button>
        </div>
      </section>
      <section v-else class="opt">
        <p class="lbl" id="key-groups">Groups</p>
        <GroupPicker v-if="live.groups.length" v-model="groups" aria-labelledby="key-groups" />
        <p v-else class="hint">There are no groups yet. Create one on the Groups page.</p>
        <p v-if="groups.length === 0" class="hint warn">A key in no group sees no tools.</p>
      </section>
    </form>
    <template #footer>
      <UiButton @click="emit('close')">Cancel</UiButton>
      <UiButton variant="primary" type="submit" form="create-key" :loading="busy">
        {{ isNode ? 'Create node key' : 'Create client key' }}
      </UiButton>
    </template>
  </UiModal>

  <UiModal
    v-else
    :title="`Connect ${created.key.name}`"
    eyebrow="Key created"
    eyebrow-tone="ok"
    persistent
    :width="720"
  >
    <template v-if="created.key.kind === 'node' && nodeSetup">
      <UiNotice :icon="KeyRound" title="You won’t see this secret again.">
        Copy it now and store it somewhere safe before closing.
      </UiNotice>
      <section class="step">
        <p class="label">01 / Save your secret</p>
        <CopyField :value="created.secret" secret label="Copy secret" />
      </section>
      <section class="step">
        <p class="label">02 / Set up {{ created.key.name.toUpperCase() }}</p>
        <CopyField :value="nodeSetup.init_command" multiline label="Copy setup command" />
        <p class="note muted">Run it on the new machine: it points the node at this hub, names it {{ created.key.name }} and saves the key. Then add MCPs and run mcp-mesh-node start.</p>
      </section>
      <section class="step">
        <p class="label">03 / Already set up?</p>
        <CopyField :value="nodeSetup.command" multiline label="Copy key command" />
        <p class="note muted">On a machine that is already a node named {{ created.key.name }}, this only replaces its key.</p>
      </section>
      <p class="note muted">This key works only for the node named {{ created.key.name }}.</p>
    </template>

    <template v-else-if="clientSetup">
      <UiNotice title="You won’t see this secret again.">
        Save it before closing. Anyone with this key can use your tools.
      </UiNotice>
      <section class="step">
        <p class="label">Client secret</p>
        <CopyField :value="created.secret" secret label="Copy secret" />
      </section>
      <section class="step">
        <p class="label">MCP URL</p>
        <CopyField :value="clientSetup.mcp_url" label="Copy MCP URL" />
      </section>
      <section class="step">
        <p class="label">Authorization header</p>
        <CopyField :value="clientSetup.header" label="Copy header" />
      </section>
      <section class="step">
        <div class="snip-head">
          <p class="label">{{ snippetTitle }}</p>
          <div class="other">
            <UiSelect v-model="which" label="Client" :options="snippetOptions.map((o) => ({ value: o.value, label: o.label }))" />
          </div>
        </div>
        <CopyField
          :value="snippet(which, input)"
          :display="display(which, input)"
          multiline
          label="Copy snippet"
        />
      </section>
      <p v-if="live.info?.oauth" class="note muted">Apps that support OAuth can add the MCP URL instead of a key.</p>
    </template>

    <template #footer>
      <UiButton variant="primary" :icon="Check" @click="emit('close')">I’ve saved the key</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.pair { margin: -4px 0 26px; }
.pair-cmd { margin-top: 10px; }
.pair :deep(.content) { min-width: 0; flex: 1; }
.lbl { font-size: 14px; font-weight: 500; margin: 20px 0 10px; }
.types { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 26px; }
.type {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  min-height: 80px;
  text-align: left;
  background: var(--surface);
  border: 1px solid var(--rule);
  border-radius: var(--radius);
  cursor: pointer;
}
.type:hover { border-color: var(--muted); }
.type.on { background: var(--cobalt-soft); border-color: var(--cobalt); color: var(--cobalt); }
.type-title { display: flex; align-items: center; gap: 10px; font-size: 14px; font-weight: 500; }
.type-text { font-size: 12px; color: var(--muted); }
.type.on .type-text { color: var(--muted); }
.opt { margin-top: 26px; }
.opt .lbl { margin: 0 0 10px; }
.hint { font-size: 12px; color: var(--muted); margin-top: 10px; }
.hint.warn { color: var(--warn); }
.trusts { display: flex; flex-direction: column; gap: 10px; }
.trust {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 16px;
  text-align: left;
  background: var(--surface);
  border: 1px solid var(--rule);
  border-radius: var(--radius);
  cursor: pointer;
}
.trust:hover { border-color: var(--muted); }
.trust.on { background: var(--cobalt-soft); border-color: var(--cobalt); color: var(--cobalt); }
.step { margin-top: 26px; }
.step .label { margin-bottom: 10px; }
.note { margin-top: 18px; font-size: 12px; }
.snip-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.snip-head .label { margin: 0; }
.other { width: 170px; }
.other :deep(select) { height: 32px; font-size: 12px; border-color: transparent; background: transparent; text-align: right; }
@media (max-width: 600px) {
  .types { grid-template-columns: 1fr; }
}
</style>
