<script setup lang="ts">
// Fresh hub: the steps to get the first machine and agent connected. With
// pairing on, a machine joins by asking and being approved; otherwise it
// needs a node key made here first.
import { computed } from 'vue'
import { Plus } from 'lucide-vue-next'
import type { Info, KeyKind, PairingRequest } from '@/lib/types'
import UiButton from '@/components/ui/UiButton.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import CopyField from '@/components/ui/CopyField.vue'
import { pairCommand } from '@/lib/snippets'

const props = withDefaults(defineProps<{ info: Info | null; authOff: boolean; pairing?: boolean; requests?: PairingRequest[] }>(), { requests: () => [] })
const emit = defineEmits<{ key: [kind: KeyKind] }>()
const pairs = computed(() => !props.authOff && props.pairing)
</script>

<template>
  <section class="panel guide" aria-labelledby="guide-title">
    <div class="art" aria-hidden="true"><span class="circle" /><span class="square"><span class="hole" /></span></div>
    <h2 id="guide-title" class="title">Connect your first machine</h2>
    <ol class="steps">
      <template v-if="pairs">
        <li>
          <span class="num mono">01</span>
          <div class="step">
            <h3>Start the node on that machine</h3>
            <p>Install mcp-mesh-node on the machine that has your MCPs, then run:</p>
            <CopyField v-if="info" :value="pairCommand(info.node_url)" label="Copy setup command" class="cmd" />
          </div>
        </li>
        <li>
          <span class="num mono">02</span>
          <div>
            <h3>Approve it here</h3>
            <p>It asks to join and shows a code. Under Approvals, check the code matches and approve it: no key to copy.</p>
            <ul v-if="requests.length" class="asking">
              <li v-for="r in requests" :key="r.id">
                <span class="who truncate">{{ r.node }} is asking to join</span>
                <span class="mono code">{{ r.code }}</span>
                <RouterLink to="/approvals" class="arrow-link">Review</RouterLink>
              </li>
            </ul>
          </div>
        </li>
      </template>
      <template v-else>
        <li>
          <span class="num mono">01</span>
          <div>
            <h3>{{ authOff ? 'Install the node' : 'Create a node key' }}</h3>
            <p>{{ authOff ? 'Run mcp-mesh-node on the machine that has your MCPs.' : 'Bind it to the machine’s name, like workstation.' }}</p>
          </div>
        </li>
        <li>
          <span class="num mono">02</span>
          <div>
            <h3>{{ authOff ? 'Point it at the node URL' : 'Run the setup command' }}</h3>
            <p>{{ authOff ? 'Nodes connect to the node URL below directly; no key is needed.' : 'Run the mcp-mesh-node init command it shows on that machine: it sets the hub, the machine’s name and the key.' }}</p>
          </div>
        </li>
      </template>
      <li>
        <span class="num mono">03</span>
        <div>
          <h3>Connect your agent</h3>
          <p>{{ authOff ? 'Add the MCP URL below to Claude Code.' : 'Create a client key and add the MCP URL to Claude Code.' }}</p>
        </div>
      </li>
    </ol>
    <div v-if="!authOff" class="buttons">
      <template v-if="pairs">
        <UiButton variant="primary" :icon="Plus" @click="emit('key', 'client')">Create a client key</UiButton>
        <UiButton @click="emit('key', 'node')">Use a node key instead</UiButton>
      </template>
      <template v-else>
        <UiButton variant="primary" :icon="Plus" @click="emit('key', 'node')">Create a node key</UiButton>
        <UiButton @click="emit('key', 'client')">Create a client key</UiButton>
      </template>
    </div>
  </section>
  <footer v-if="info" class="urls">
    <div class="url"><span class="mono muted">Node URL</span><code class="val truncate" :title="info.node_url">{{ info.node_url }}</code><CopyButton :value="info.node_url" label="Copy node URL" /></div>
    <div class="url"><span class="mono muted">MCP URL</span><code class="val truncate" :title="info.client_url">{{ info.client_url }}</code><CopyButton :value="info.client_url" label="Copy MCP URL" /></div>
  </footer>
</template>

<style scoped>
.guide { position: relative; padding: 70px 40px 36px; overflow: hidden; }
.title { font-size: 36px; line-height: 1.2; margin-bottom: 92px; }
.steps { list-style: none; margin: 0; padding: 0; display: grid; gap: 38px; }
.steps li { display: flex; gap: 20px; align-items: center; }
.num {
  flex: none; width: 42px; height: 42px; border-radius: 50%; border: 1px solid var(--rule);
  background: var(--paper); color: var(--cobalt); display: grid; place-items: center; font-size: 12px;
}
h3 { font-size: 18px; }
.buttons > * { min-width: 232px; }
.steps p { font-size: 13px; color: var(--muted); margin-top: 8px; }
.step { min-width: 0; flex: 1; max-width: 640px; }
.cmd { margin-top: 12px; }
.asking { list-style: none; margin: 12px 0 0; padding: 0; display: grid; gap: 8px; }
.asking li { display: flex; align-items: center; gap: 14px; padding: 10px 14px; background: var(--warn-soft); border-radius: var(--radius); font-size: 13px; min-width: 0; }
.asking .who { color: var(--ink); font-weight: 500; }
.asking .code { color: var(--cobalt); letter-spacing: 0.08em; }
.asking .arrow-link { margin-left: auto; }
.buttons { display: flex; gap: 16px; margin-top: 64px; flex-wrap: wrap; }
.art { position: absolute; top: 56px; right: 56px; width: 166px; height: 176px; }
.circle { position: absolute; left: 0; top: 0; width: 132px; height: 132px; border-radius: 50%; background: var(--cobalt-soft); }
.square { position: absolute; right: 0; bottom: 0; width: 108px; height: 108px; background: var(--cobalt-fill); display: grid; place-items: center; }
.hole { width: 52px; height: 52px; border-radius: 50%; background: #fff; }
.urls { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 12px 40px; margin-top: 100px; color: var(--muted); }
.url { display: flex; align-items: center; gap: 14px; min-width: 0; }
.val { font-size: 12px; color: var(--muted); }

@media (max-width: 700px) {
  .guide { padding: 28px 20px 24px; }
  .art { display: none; }
  .title { font-size: 26px; margin-bottom: 32px; }
  .buttons { margin-top: 32px; }
  .buttons > * { flex: 1 1 100%; min-width: 0; }
  .urls { margin-top: 32px; flex-direction: column; }
}
</style>
