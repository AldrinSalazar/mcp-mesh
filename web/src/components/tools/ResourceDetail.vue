<script setup lang="ts">
import type { ResourceItem } from '@/lib/types'
import UiBadge from '@/components/ui/UiBadge.vue'
import ItemDetail from './ItemDetail.vue'

defineProps<{ resource: ResourceItem }>()
defineEmits<{ close: [] }>()
</script>

<template>
  <ItemDetail kind="Resource" :title="resource.name || resource.original_uri" :copy-value="resource.uri" copy-label="Copy exposed URI" @close="$emit('close')">
    <div v-if="resource.template" class="badges"><UiBadge tone="info">Template</UiBadge></div>
    <p class="text">{{ resource.description || 'No description.' }}</p>
    <section class="sec">
      <span class="label">Exposed URI</span>
      <p class="mono val">{{ resource.uri }}</p>
    </section>
    <section class="sec">
      <span class="label">Original URI</span>
      <p class="mono val">{{ resource.original_uri }}</p>
    </section>
    <section class="sec">
      <span class="label">Source</span>
      <p class="mono val" :title="`${resource.node} / ${resource.mcp}`">{{ resource.node }} / {{ resource.mcp }}</p>
      <p class="mono muted val2">MIME type: {{ resource.mime_type || 'unknown' }}</p>
    </section>
  </ItemDetail>
</template>

<style scoped>
.badges { display: flex; gap: 8px; margin-top: 12px; }
.text { margin-top: 28px; font-size: 14px; line-height: 1.45; }
.sec { margin-top: 28px; padding-top: 24px; border-top: 1px solid var(--rule); }
.val { margin-top: 14px; font-size: 12px; overflow-wrap: anywhere; }
.val2 { margin-top: 10px; font-size: 12px; }
</style>
