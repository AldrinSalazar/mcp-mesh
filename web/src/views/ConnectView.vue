<script setup lang="ts">
// Connect: everything needed to add a machine (a node) or an agent (a
// client), always at hand rather than only on a fresh hub's Overview.
import { computed, ref } from 'vue'
import { Plus } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiSegmented from '@/components/ui/UiSegmented.vue'
import CopyField from '@/components/ui/CopyField.vue'
import HarnessSetups from '@/components/connect/HarnessSetups.vue'
import { live } from '@/lib/live'
import { openCreateKey } from '@/lib/dialogs'
import { installOptions, keyPlaceholder, pairCommand, type InstallId } from '@/lib/snippets'

const nodeURL = computed(() => live.info?.node_url ?? '')
const clientURL = computed(() => live.info?.client_url ?? '')
// With auth off a machine needs no key, so the same command just connects it.
const pairing = computed(() => live.authOff || !!live.settings?.pairing)

const install = ref<InstallId>('brew')
const installing = computed(() => installOptions.find((o) => o.value === install.value)!)

const input = computed(() => ({ url: clientURL.value, secret: live.authOff ? '' : keyPlaceholder }))
</script>

<template>
  <PageHeader title="Connect" />

  <section class="panel block" aria-labelledby="add-machine">
    <h2 id="add-machine" class="section-title">Add a machine</h2>
    <p class="muted lead">A machine runs mcp-mesh-node, which shares its MCP servers through this hub.</p>

    <ol class="steps">
      <li>
        <span class="num mono">01</span>
        <div class="step">
          <h3>Install mcp-mesh-node</h3>
          <UiSegmented v-model="install" label="How to install" :options="installOptions.map((o) => ({ value: o.value, label: o.label }))" class="seg" />
          <CopyField v-if="install !== 'windows'" :value="installing.command" multiline label="Copy install command" />
          <p v-else class="win"><a :href="installing.command" target="_blank" rel="noopener">Latest release</a></p>
          <p class="note muted">{{ installing.note }}</p>
        </div>
      </li>
      <li v-if="pairing">
        <span class="num mono">02</span>
        <div class="step">
          <h3>Point it at this hub and start it</h3>
          <CopyField :value="pairCommand(nodeURL)" multiline label="Copy setup command" />
          <p class="note muted">
            Or run <code>mcp-mesh-node</code> alone and press <strong>p</strong>: it finds this hub on the network.
          </p>
        </div>
      </li>
      <li v-if="pairing && !live.authOff">
        <span class="num mono">03</span>
        <div class="step">
          <h3>Approve it here</h3>
          <p class="note muted">It asks to join and shows a code. Under Approvals, check the code matches and approve it: no key to copy.</p>
          <ul v-if="live.pairings.length" class="asking">
            <li v-for="r in live.pairings" :key="r.id">
              <span class="who truncate">{{ r.node }} is asking to join</span>
              <span class="mono code">{{ r.code }}</span>
              <RouterLink to="/approvals" class="arrow-link">Review</RouterLink>
            </li>
          </ul>
        </div>
      </li>
      <li v-if="!pairing">
        <span class="num mono">02</span>
        <div class="step">
          <h3>Create a node key for it</h3>
          <p class="note muted">Pairing is turned off on this hub. The key comes with a one-line setup command to run on the machine.</p>
        </div>
      </li>
    </ol>

    <div class="field">
      <p class="label">Node URL</p>
      <CopyField :value="nodeURL" label="Copy node URL" />
    </div>
    <div v-if="!live.authOff" class="buttons">
      <UiButton :variant="pairing ? 'secondary' : 'primary'" :icon="pairing ? undefined : Plus" @click="openCreateKey('node')">
        {{ pairing ? 'Use a node key instead' : 'Create a node key' }}
      </UiButton>
      <span v-if="pairing" class="muted small">For scripted setups, with nobody here to approve.</span>
    </div>
  </section>

  <section class="panel block" aria-labelledby="add-agent">
    <h2 id="add-agent" class="section-title">Add an agent</h2>
    <p class="muted lead">
      Agents connect to one MCP URL and see every tool their key's groups allow.
      <template v-if="!live.authOff">Create a client key, then put it where the setup below says <code>{{ keyPlaceholder }}</code>.</template>
    </p>
    <div class="field">
      <p class="label">MCP URL</p>
      <CopyField :value="clientURL" label="Copy MCP URL" />
    </div>
    <div v-if="!live.authOff" class="buttons">
      <UiButton variant="primary" :icon="Plus" @click="openCreateKey('client')">Create a client key</UiButton>
      <span v-if="live.info?.oauth" class="muted small">Apps that sign in with OAuth (claude.ai, the Claude apps) need only the MCP URL.</span>
    </div>
    <HarnessSetups :input="input" class="harness" />
  </section>
</template>

<style scoped>
.block { padding: 32px 36px; }
.block + .block { margin-top: 24px; }
.lead { margin-top: 8px; font-size: 14px; max-width: 760px; }
.steps { list-style: none; margin: 28px 0 0; padding: 0; display: grid; gap: 28px; }
.steps > li { display: flex; gap: 20px; align-items: flex-start; }
.num {
  flex: none; width: 42px; height: 42px; border-radius: 50%; border: 1px solid var(--rule);
  background: var(--paper); color: var(--cobalt); display: grid; place-items: center; font-size: 12px;
}
.step { flex: 1; min-width: 0; max-width: 760px; padding-top: 10px; }
h3 { font-size: 16px; margin-bottom: 12px; }
.seg { margin-bottom: 12px; }
.note { margin-top: 8px; font-size: 13px; }
.win { font-size: 14px; }
.asking { list-style: none; margin: 12px 0 0; padding: 0; display: grid; gap: 8px; }
.asking li { display: flex; align-items: center; gap: 14px; padding: 10px 14px; background: var(--warn-soft); border-radius: var(--radius); font-size: 13px; min-width: 0; }
.asking .who { color: var(--ink); font-weight: 500; }
.asking .code { color: var(--cobalt); letter-spacing: 0.08em; }
.asking .arrow-link { margin-left: auto; }
.field { margin-top: 28px; max-width: 760px; }
.label { font-size: 12px; font-weight: 500; color: var(--muted); margin-bottom: 8px; }
.buttons { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; margin-top: 20px; }
.small { font-size: 13px; }
.harness { margin-top: 28px; max-width: 900px; }
code { font-family: var(--font-mono); font-size: 0.92em; }

@media (max-width: 700px) {
  .block { padding: 24px 18px; }
  .steps > li { gap: 12px; }
  .num { width: 34px; height: 34px; }
}
</style>
