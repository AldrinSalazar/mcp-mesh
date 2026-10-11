<script setup lang="ts">
// Text input, 42 px. `search` adds the magnifier; type=password gets a
// Show/Hide toggle.
import { computed, ref } from 'vue'
import { Search } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    type?: string
    placeholder?: string
    search?: boolean
    mono?: boolean
    invalid?: boolean
    autocomplete?: string
    id?: string
    disabled?: boolean
  }>(),
  { type: 'text' },
)
const model = defineModel<string>({ default: '' })
const shown = ref(false)
const inputType = computed(() => (props.type === 'password' && shown.value ? 'text' : props.type))
const input = ref<HTMLInputElement>()
defineExpose({ focus: () => input.value?.focus() })
</script>

<template>
  <div class="field" :class="{ search, invalid, disabled }">
    <Search v-if="search" class="search-icon" :size="20" :stroke-width="1.6" aria-hidden="true" />
    <input
      :id="id"
      ref="input"
      v-model="model"
      :type="inputType"
      :placeholder="placeholder"
      :autocomplete="autocomplete"
      :class="{ mono }"
      :disabled="disabled"
      :aria-invalid="invalid || undefined"
      spellcheck="false"
    />
    <button v-if="type === 'password'" type="button" class="reveal" @click="shown = !shown">
      {{ shown ? 'Hide' : 'Show' }}
    </button>
  </div>
</template>

<style scoped>
.field {
  display: flex;
  align-items: center;
  height: var(--control-h);
  background: var(--surface);
  border: 1px solid var(--rule);
  border-radius: var(--radius);
  padding: 0 14px;
  gap: 10px;
  transition: border-color 0.12s;
}
.field:focus-within { border-color: var(--cobalt); }
.field.invalid { border-color: var(--err); }
.field.disabled { opacity: 0.6; }
.search { padding-left: 14px; }
.search-icon { color: var(--muted); flex: none; }
input {
  flex: 1;
  min-width: 0;
  height: 100%;
  border: 0;
  background: transparent;
  outline: none;
  font-size: 14px;
  color: var(--ink);
}
input::placeholder { color: var(--muted); }
input.mono { font-family: var(--font-mono); font-size: 13px; }
.reveal {
  border: 0;
  background: none;
  color: var(--cobalt);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  padding: 4px 0 4px 8px;
}
</style>
