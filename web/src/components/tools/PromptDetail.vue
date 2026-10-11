<script setup lang="ts">
import type { Prompt } from '@/lib/types'
import UiBadge from '@/components/ui/UiBadge.vue'
import ItemDetail from './ItemDetail.vue'

defineProps<{ prompt: Prompt }>()
defineEmits<{ close: [] }>()
</script>

<template>
  <ItemDetail kind="Prompt" :title="prompt.original_name" :copy-value="prompt.name" copy-label="Copy exposed name" @close="$emit('close')">
    <p class="mono gen muted" :title="prompt.name">{{ prompt.name }}</p>
    <p class="text">{{ prompt.description || 'No description.' }}</p>
    <section class="sec">
      <span class="label">Source</span>
      <p class="mono src" :title="`${prompt.node} / ${prompt.mcp}`">{{ prompt.node }} / {{ prompt.mcp }}</p>
    </section>
    <section class="sec">
      <h3 class="section-title">Arguments</h3>
      <p v-if="!prompt.arguments?.length" class="none muted">No arguments.</p>
      <ul v-else class="plist">
        <li v-for="a in prompt.arguments" :key="a.name">
          <div class="pname">
            <span class="mono truncate" :title="a.name">{{ a.name }}</span>
            <UiBadge v-if="a.required" tone="info">Required</UiBadge>
          </div>
          <p v-if="a.description" class="pdesc muted">{{ a.description }}</p>
        </li>
      </ul>
    </section>
  </ItemDetail>
</template>

<style scoped>
.gen { margin-top: 8px; font-size: 12px; overflow-wrap: anywhere; }
.text { margin-top: 28px; font-size: 14px; line-height: 1.45; }
.sec { margin-top: 28px; padding-top: 24px; border-top: 1px solid var(--rule); }
.src { margin-top: 14px; font-size: 12px; overflow-wrap: anywhere; }
.none { margin-top: 16px; font-size: 13px; }
.plist { list-style: none; margin: 8px 0 0; padding: 0; }
.plist li { padding: 18px 0; border-bottom: 1px solid var(--rule); }
.plist li:last-child { border-bottom: 0; }
.pname { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 26px; }
.pname .mono { font-size: 12px; }
.pdesc { margin-top: 10px; font-size: 12px; line-height: 1.45; }
</style>
