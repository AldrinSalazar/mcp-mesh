<script setup lang="ts">
// The MCP mark (Penpot "MCP / Bauhaus mark"). Circles take the ink colour and
// letters the surface colour, so the dark theme gets the reverse mark.
withDefaults(defineProps<{ size?: number; wordmark?: boolean }>(), { size: 40, wordmark: true })
</script>

<template>
  <span class="brand">
    <svg :width="size" :height="size * 0.9" viewBox="0 0 44 39.6" fill="none" aria-hidden="true">
      <path class="ink" d="M10.18 25.85 21.17 12.65l1.66 1.1-11 13.2zM21.17 13.75l11 13.2 1.66-1.1-11-13.2zM11 25.44h22v1.92H11z" />
      <circle class="ink" cx="11" cy="26.4" r="6.875" />
      <circle class="c" cx="22" cy="13.2" r="6.875" />
      <circle class="ink" cx="33" cy="26.4" r="6.875" />
      <path class="letter" d="M7.43 29.7v-6.6h1.92L11 25.85l1.65-2.75h1.92v6.6h-1.92v-3.3L11 28.88 9.35 26.4v3.3z" />
      <path class="c-letter" d="M24.75 10.45a3.82 3.82 0 1 0 0 5.5l-1.38-1.38a1.88 1.88 0 1 1 0-2.75z" />
      <path class="letter" fill-rule="evenodd" d="M30.8 29.7v-6.6h3.03a2.75 2.75 0 0 1 0 5.5h-1.1v1.1zm1.93-4.68v1.65h1.1a.83.83 0 0 0 0-1.65z" />
    </svg>
    <span v-if="wordmark" class="word">mcp-mesh</span>
  </span>
</template>

<style scoped>
.brand { display: inline-flex; align-items: center; gap: 10px; }
.ink { fill: var(--ink); }
.c { fill: #3555e8; }
.letter { fill: var(--surface); }
.c-letter { fill: #fbfaf6; }
.word { font-family: var(--font-display); font-size: 22px; font-weight: 600; color: var(--ink); }
</style>
