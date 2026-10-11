<script setup lang="ts">
// Empty list: icon, title, one line of help and an optional next step.
import type { Component } from 'vue'

defineProps<{ icon?: Component; title: string; text?: string; action?: string; framed?: boolean }>()
const emit = defineEmits<{ action: [] }>()
</script>

<template>
  <div class="empty" :class="{ framed }">
    <component :is="icon" v-if="icon" :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
    <h3 class="title">{{ title }}</h3>
    <p v-if="text" class="text">{{ text }}</p>
    <button v-if="action" type="button" class="arrow-link act" @click="emit('action')">{{ action }}</button>
    <slot />
  </div>
</template>

<style scoped>
.empty { padding: 28px 24px; }
.framed { background: var(--surface); border: 1px solid var(--rule); }
.icon { color: var(--muted); display: block; margin-bottom: 26px; }
.title { font-size: 18px; margin-bottom: 10px; }
.text { font-size: 13px; color: var(--muted); }
.act { margin-top: 44px; border: 0; background: none; padding: 0; cursor: pointer; }
</style>
