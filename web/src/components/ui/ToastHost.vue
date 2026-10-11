<script setup lang="ts">
import { Check, X } from 'lucide-vue-next'
import { dismiss, toasts } from '@/lib/toast'
</script>

<template>
  <div class="toasts" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="t in toasts" :key="t.id" class="toast" :class="t.tone" @click="dismiss(t.id)">
        <Check v-if="t.tone === 'ok'" :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
        <X v-else :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
        <div>
          <p class="title">{{ t.title }}</p>
          <p v-if="t.body" class="body">{{ t.body }}</p>
        </div>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toasts {
  position: fixed;
  right: 40px;
  bottom: 72px;
  z-index: 60;
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: min(400px, calc(100vw - 32px));
}
.toast {
  display: flex;
  gap: 16px;
  align-items: flex-start;
  padding: 20px 24px;
  background: var(--ink);
  color: var(--surface);
  cursor: pointer;
}
.toast.err .icon { color: var(--err); }
.icon { flex: none; margin-top: 1px; }
.title { font-size: 14px; font-weight: 600; }
.body { font-size: 12px; margin-top: 6px; opacity: 0.85; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateY(8px); }
.toast-enter-active, .toast-leave-active { transition: opacity 0.18s, transform 0.18s; }
@media (max-width: 700px) { .toasts { right: 16px; bottom: 16px; } }
</style>
