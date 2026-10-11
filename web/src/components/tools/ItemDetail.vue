<script setup lang="ts">
// Right-hand panel for a prompt or a resource: the shell is shared, the body
// comes in the slot. Mirrors ToolDetail's header and footer.
import { ref } from 'vue'
import { ArrowLeft, Copy, X } from 'lucide-vue-next'
import { copyText } from '@/lib/clipboard'
import { toast } from '@/lib/toast'
import UiButton from '@/components/ui/UiButton.vue'

const props = defineProps<{ kind: string; title: string; copyValue: string; copyLabel: string }>()
defineEmits<{ close: [] }>()

const copied = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
async function copy() {
  if (await copyText(props.copyValue)) {
    copied.value = true
    clearTimeout(timer)
    timer = setTimeout(() => (copied.value = false), 1600)
  } else {
    toast("Couldn't copy", 'Select the text and copy it by hand.', 'err')
  }
}
</script>

<template>
  <aside class="detail panel" :aria-label="`${kind} detail`">
    <div class="top">
      <button type="button" class="back" @click="$emit('close')"><ArrowLeft :size="18" :stroke-width="1.6" aria-hidden="true" />Back to list</button>
      <span class="label">{{ kind }} detail</span>
      <button type="button" class="close" :aria-label="`Close ${kind.toLowerCase()} detail`" @click="$emit('close')"><X :size="22" :stroke-width="1.6" aria-hidden="true" /></button>
    </div>
    <h2 class="orig truncate" :title="title">{{ title }}</h2>
    <slot />
    <div class="foot">
      <UiButton block :icon="Copy" @click="copy">{{ copied ? 'Copied' : copyLabel }}</UiButton>
    </div>
  </aside>
</template>

<style scoped>
.detail { padding: 24px; min-width: 0; align-self: start; }
.top { display: flex; align-items: center; justify-content: space-between; min-height: 24px; }
.close { border: 0; background: none; color: var(--ink); cursor: pointer; padding: 0; display: grid; }
.back { display: none; align-items: center; gap: 6px; border: 0; background: none; color: var(--cobalt); font-size: 13px; font-weight: 500; cursor: pointer; padding: 0; }
.orig { font-family: var(--font-display); font-size: 28px; font-weight: 500; margin-top: 18px; }
.foot { margin-top: 12px; padding-top: 16px; border-top: 1px solid var(--rule); display: flex; flex-direction: column; gap: 10px; }
.foot :deep(.btn) { justify-content: center; }

@media (max-width: 1100px) {
  .back { display: inline-flex; }
  .close, .top .label { display: none; }
}
</style>
