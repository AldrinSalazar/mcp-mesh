<script setup lang="ts">
// Copy icon button; shows "Copied" for a moment after copying.
import { ref } from 'vue'
import { Copy } from 'lucide-vue-next'
import { copyText } from '@/lib/clipboard'
import { toast } from '@/lib/toast'

const props = defineProps<{ value: string; label?: string; inverse?: boolean }>()
const copied = ref(false)
let t: ReturnType<typeof setTimeout> | undefined

async function copy() {
  if (await copyText(props.value)) {
    copied.value = true
    clearTimeout(t)
    t = setTimeout(() => (copied.value = false), 1600)
  } else {
    toast("Couldn't copy", 'Select the text and copy it by hand.', 'err')
  }
}
</script>

<template>
  <button type="button" class="copy" :class="{ inverse }" :aria-label="label ?? 'Copy'" :title="label ?? 'Copy'" @click="copy">
    <span v-if="copied" class="copied" role="status">Copied</span>
    <Copy v-else :size="20" :stroke-width="1.6" aria-hidden="true" />
  </button>
</template>

<style scoped>
.copy {
  display: inline-grid;
  place-items: center;
  min-width: 32px;
  height: 32px;
  padding: 0 4px;
  border: 0;
  background: none;
  color: var(--muted);
  cursor: pointer;
  flex: none;
}
.copy:hover { color: var(--ink); }
.copied { font-size: 12px; font-weight: 600; color: var(--ink); }
.inverse { color: var(--surface); }
.inverse:hover, .inverse .copied { color: var(--surface); }
</style>
