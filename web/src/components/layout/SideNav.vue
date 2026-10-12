<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { Sun, Moon } from 'lucide-vue-next'
import BrandMark from './BrandMark.vue'
import { navItems, plannedItems } from './nav'
import { live, waitingCount } from '@/lib/live'
import { theme, toggleTheme } from '@/lib/theme'

defineEmits<{ navigate: [] }>()
const route = useRoute()
const items = computed(() => navItems.filter((i) => !i.authOnly || !live.authOff))
</script>

<template>
  <nav class="side" aria-label="Main">
    <RouterLink :to="{ name: 'overview' }" class="logo" aria-label="mcp-mesh overview">
      <BrandMark />
    </RouterLink>

    <ul class="items">
      <li v-for="i in items" :key="i.name">
        <!-- Matched by route name: "/" is a prefix of every path, so the link's
             own active class would keep Overview lit everywhere. -->
        <RouterLink
          :to="{ name: i.name }"
          class="item"
          :class="{ on: route.name === i.name }"
          active-class=""
          exact-active-class=""
          :aria-current="route.name === i.name ? 'page' : undefined"
          @click="$emit('navigate')"
        >
          <component :is="i.icon" :size="22" :stroke-width="1.6" aria-hidden="true" />
          <span>{{ i.label }}</span>
          <span v-if="i.count === 'approvals' && waitingCount()" class="count" :aria-label="`${waitingCount()} waiting`">{{ waitingCount() }}</span>
          <span class="marker" aria-hidden="true" />
        </RouterLink>
      </li>
    </ul>

    <div v-if="plannedItems.length" class="later">
      <p class="label">Coming later</p>
      <div v-for="p in plannedItems" :key="p" class="planned">
        <span>{{ p }}</span><span class="tag">Planned</span>
      </div>
    </div>

    <button type="button" class="theme" @click="toggleTheme">
      <Sun v-if="theme === 'light'" :size="22" :stroke-width="1.6" aria-hidden="true" />
      <Moon v-else :size="22" :stroke-width="1.6" aria-hidden="true" />
      {{ theme === 'light' ? 'Light theme' : 'Dark theme' }}
    </button>
  </nav>
</template>

<style scoped>
.side {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--surface);
  border-right: 1px solid var(--rule);
  padding: 24px 12px 28px;
}
.logo { display: block; padding: 0 12px; margin-bottom: 64px; text-decoration: none; }
.items { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  height: 42px;
  padding: 0 16px;
  border-radius: var(--radius);
  color: var(--ink);
  font-size: 14px;
  font-weight: 500;
  text-decoration: none;
}
.item svg { color: var(--muted); flex: none; }
.item:hover { background: var(--paper); text-decoration: none; }
.item.on { background: var(--ink); color: var(--surface); font-weight: 600; }
.item.on svg { color: var(--surface); }
.marker { display: none; position: absolute; right: 14px; width: 6px; height: 6px; border-radius: 50%; background: var(--cobalt); }
.item.on .marker { display: block; }
.count {
  margin-left: auto;
  margin-right: 14px;
  min-width: 22px;
  height: 20px;
  padding: 0 6px;
  display: inline-grid;
  place-items: center;
  border-radius: 10px;
  background: var(--warn-soft);
  color: var(--warn);
  font-size: 11px;
  font-weight: 600;
}
.item.on .count { margin-right: 4px; }
.later { margin: 40px 12px 0; padding-top: 24px; border-top: 1px solid var(--rule); }
.later .label { margin-bottom: 14px; }
.planned { display: flex; align-items: center; justify-content: space-between; height: 40px; color: var(--muted); font-size: 14px; }
.tag { display: inline-flex; align-items: center; height: 26px; padding: 0 10px; background: var(--rule); color: var(--muted); font-size: 12px; border-radius: var(--radius); }
.theme {
  margin: auto 0 0;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 28px 2px 0;
  border: 0;
  border-top: 1px solid var(--rule);
  margin-inline: 12px;
  background: none;
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
}
.theme:hover { color: var(--ink); }
</style>
