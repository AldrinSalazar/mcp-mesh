<script setup lang="ts">
// Give a tool an alias and/or a description for models. The alias replaces
// the long <node>__<mcp>__<tool> name; both are optional and survive the tool
// going offline.
import { computed, ref } from 'vue'
import type { Tool } from '@/lib/types'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import { ApiError, api } from '@/lib/api'
import { refresh } from '@/lib/live'
import { toast } from '@/lib/toast'
import { aliasError, descriptionError } from './alias'

const props = defineProps<{ tool: Tool }>()
const emit = defineEmits<{ close: []; saved: [exposedName: string] }>()

const startAlias = props.tool.alias ? props.tool.name : ''
const alias = ref(startAlias)
const description = ref(props.tool.description_override)
const busy = ref<'save' | 'reset' | ''>('')
const taken = ref('')
const form = ref('')

const aliasProblem = computed(() => taken.value || aliasError(alias.value))
const descProblem = computed(() => descriptionError(description.value))
const hasOverride = computed(() => props.tool.alias || !!props.tool.description_override)
const changed = computed(() => alias.value !== startAlias || description.value !== props.tool.description_override)

function onAlias(v: string) {
  alias.value = v
  taken.value = ''
}

async function save(clear = false) {
  if (busy.value) return
  if (!clear && (aliasProblem.value || descProblem.value)) return
  busy.value = clear ? 'reset' : 'save'
  form.value = ''
  const a = clear ? '' : alias.value
  const d = clear ? '' : description.value
  try {
    await api.setOverride(props.tool, a, d)
    void refresh('tools')
    void refresh('overrides')
    toast(
      clear ? 'Reset to original' : 'Saved',
      clear ? 'The tool uses its generated name and the MCP’s description.' : a ? `Clients now see this tool as ${a}.` : 'The description override is saved.',
    )
    emit('saved', a || props.tool.generated_name)
    emit('close')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) taken.value = 'Another tool already uses that name.'
    else form.value = e instanceof ApiError ? e.message : 'Could not save.'
    busy.value = ''
  }
}
</script>

<template>
  <UiModal title="Edit name and description" :eyebrow="`Tool / ${tool.node} / ${tool.mcp}`" :width="560" @close="emit('close')">
    <form id="edit-tool-form" novalidate @submit.prevent="save()">
      <UiField label="Alias" for="tool-alias" :error="aliasProblem" hint="Letters, digits, _ and -; up to 64. Leave empty to use the generated name.">
        <UiInput id="tool-alias" :model-value="alias" mono :placeholder="tool.generated_name" :invalid="!!aliasProblem" @update:model-value="onAlias" />
      </UiField>
      <p class="gen muted mono" :title="tool.generated_name">Generated name: {{ tool.generated_name }}</p>
      <UiField label="Description" for="tool-desc" :error="descProblem" hint="Shown to models instead of the MCP’s description. Leave empty to use the MCP’s.">
        <textarea id="tool-desc" v-model="description" class="area" rows="5" :aria-invalid="!!descProblem || undefined" />
      </UiField>
      <div class="orig">
        <span class="label">Original description</span>
        <p class="muted">{{ tool.description || 'No description.' }}</p>
      </div>
      <p v-if="form" class="form-err" role="alert">{{ form }}</p>
    </form>
    <template #footer>
      <UiButton v-if="hasOverride" :loading="busy === 'reset'" @click="save(true)">Reset to original</UiButton>
      <UiButton @click="emit('close')">Cancel</UiButton>
      <UiButton variant="primary" type="submit" form="edit-tool-form" :loading="busy === 'save'" :disabled="!changed || !!aliasProblem || !!descProblem">Save</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.gen { margin: 10px 0 26px; font-size: 12px; overflow-wrap: anywhere; }
.area {
  width: 100%; resize: vertical; padding: 12px 14px; border: 1px solid var(--rule); border-radius: var(--radius);
  background: var(--surface); color: var(--ink); font: inherit; font-size: 14px; line-height: 1.45;
}
.area:focus-visible { outline: 2px solid var(--cobalt); outline-offset: 1px; }
.area[aria-invalid='true'] { border-color: var(--err); }
.orig { margin-top: 18px; padding: 14px; background: var(--paper); border: 1px solid var(--rule); }
.orig p { margin-top: 8px; font-size: 12px; line-height: 1.45; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 120px; overflow: auto; }
.form-err { margin-top: 18px; font-size: 12px; color: var(--err); }
</style>
