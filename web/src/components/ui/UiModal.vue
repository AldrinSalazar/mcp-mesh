<script setup lang="ts">
// Dialog: centred surface over a scrim. Eyebrow (small label or a tinted
// tag), display title, body, and a footer separated by a rule. Escape and
// the scrim close it unless `persistent` (the one-time secret screen).
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { X } from 'lucide-vue-next'
import type { Tone } from '@/lib/status'

const props = withDefaults(
  defineProps<{
    title: string
    eyebrow?: string
    eyebrowTone?: Tone // set to show the eyebrow as a tinted tag
    width?: number
    persistent?: boolean
    closable?: boolean // show the X
  }>(),
  { width: 560, closable: false },
)
const emit = defineEmits<{ close: [] }>()
const box = ref<HTMLElement>()
let opener: Element | null = null

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && !props.persistent) {
    e.stopPropagation()
    emit('close')
  }
  if (e.key === 'Tab' && box.value) trapTab(e, box.value)
}

// trapTab keeps keyboard focus inside the dialog.
function trapTab(e: KeyboardEvent, root: HTMLElement) {
  const els = [...root.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')]
    .filter((el) => !el.hasAttribute('disabled'))
  if (!els.length) return
  const first = els[0]!
  const last = els[els.length - 1]!
  if (e.shiftKey && document.activeElement === first) {
    last.focus()
    e.preventDefault()
  } else if (!e.shiftKey && document.activeElement === last) {
    first.focus()
    e.preventDefault()
  }
}

onMounted(async () => {
  opener = document.activeElement
  document.addEventListener('keydown', onKey)
  document.body.style.overflow = 'hidden'
  await nextTick()
  const auto = box.value?.querySelector<HTMLElement>('[autofocus], input, select, textarea')
  ;(auto ?? box.value)?.focus()
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  document.body.style.overflow = ''
  if (opener instanceof HTMLElement) opener.focus()
})
</script>

<template>
  <Teleport to="body">
    <div class="scrim" @mousedown.self="!persistent && emit('close')">
      <div
        ref="box"
        class="modal"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
        tabindex="-1"
        :style="{ width: `min(${width}px, calc(100vw - 32px))` }"
      >
        <div class="head">
          <span v-if="eyebrow && eyebrowTone" class="tag" :class="`tone-${eyebrowTone}`">{{ eyebrow }}</span>
          <span v-else-if="eyebrow" class="label">{{ eyebrow }}</span>
          <h2 class="title">{{ title }}</h2>
          <button v-if="closable" type="button" class="close" aria-label="Close" @click="emit('close')">
            <X :size="22" :stroke-width="1.6" />
          </button>
        </div>
        <div class="body"><slot /></div>
        <div v-if="$slots.footer" class="foot"><slot name="footer" /></div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.scrim {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 16px;
  background: var(--scrim);
  overflow-y: auto;
}
.modal {
  position: relative;
  max-height: calc(100vh - 48px);
  overflow-y: auto;
  background: var(--surface);
  border: 1px solid var(--rule);
  border-radius: var(--radius);
  outline: none;
}
.head { padding: 32px 32px 0; }
.label { display: block; margin-bottom: 14px; }
.tag {
  display: inline-flex;
  align-items: center;
  height: 26px;
  padding: 0 10px;
  margin-bottom: 18px;
  font-size: 12px;
  text-transform: uppercase;
  border-radius: var(--radius);
}
.tone-ok { background: var(--ok-soft); color: var(--ok); }
.tone-err { background: var(--err-soft); color: var(--err); }
.tone-warn { background: var(--warn-soft); color: var(--warn); }
.title { font-size: 30px; line-height: 1.2; padding-right: 40px; }
.close {
  position: absolute;
  top: 26px;
  right: 26px;
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border: 0;
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.close:hover { color: var(--ink); }
.body { padding: 20px 32px 32px; }
.foot {
  display: flex;
  justify-content: flex-end;
  gap: 16px;
  padding: 20px 32px;
  border-top: 1px solid var(--rule);
}
@media (max-width: 600px) {
  .head { padding: 24px 20px 0; }
  .body { padding: 16px 20px 24px; }
  .foot { padding: 16px 20px; }
  .title { font-size: 24px; }
}
</style>
