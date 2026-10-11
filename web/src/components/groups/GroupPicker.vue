<script setup lang="ts">
// Check-box list of groups, for a client key's membership (key detail and
// the create-key dialog).
import { live } from '@/lib/live'
import CheckBox from './CheckBox.vue'
import { toggled } from './membership'

defineProps<{ disabled?: boolean }>()
const model = defineModel<string[]>({ required: true })
</script>

<template>
  <ul class="list">
    <li v-for="g in live.groups" :key="g.id">
      <CheckBox
        :state="model.includes(g.id) ? 'on' : 'off'"
        :disabled="disabled"
        @change="(on) => (model = toggled(model, g.id, on))"
      >
        <span class="nm truncate" :title="g.name">{{ g.name }}</span>
        <span class="muted small truncate" :title="g.description">{{ g.description }}</span>
      </CheckBox>
    </li>
  </ul>
</template>

<style scoped>
.list { list-style: none; display: flex; flex-direction: column; gap: 14px; margin: 0; padding: 0; }
.nm { font-size: 14px; flex: none; max-width: 40%; }
</style>
