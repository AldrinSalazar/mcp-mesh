<script setup lang="ts">
// Signed-in frame: sidebar, top bar, the page, and the dialogs and banners
// any page can raise.
import { onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import SideNav from './SideNav.vue'
import TopBar from './TopBar.vue'
import ConnectionBanner from './ConnectionBanner.vue'
import GlobalDialogs from '@/components/dialogs/GlobalDialogs.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { live, start } from '@/lib/live'

const drawer = ref(false)
const route = useRoute()
watch(() => route.fullPath, () => (drawer.value = false))
onMounted(start)
</script>

<template>
  <div class="shell" :class="{ drawer }">
    <aside class="sidebar"><SideNav @navigate="drawer = false" /></aside>
    <div v-if="drawer" class="drawer-scrim" @click="drawer = false" />
    <div class="main">
      <TopBar @menu="drawer = true" />
      <!-- Tables with a detail panel (Tools, Activity, Approvals) use the whole width. -->
      <main class="content" :class="{ wide: route.meta.wide }">
        <UiNotice v-if="live.authOff" tone="warn" class="auth-off">
          <strong>Authentication is off: anyone who can reach this hub can use every tool.</strong>
        </UiNotice>
        <ConnectionBanner />
        <RouterView />
      </main>
    </div>
    <GlobalDialogs />
  </div>
</template>

<style scoped>
.shell { display: flex; min-height: 100vh; }
.sidebar {
  position: sticky;
  top: 0;
  flex: none;
  width: var(--sidebar-w);
  height: 100vh;
  overflow-y: auto;
}
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.content { flex: 1; padding: 40px var(--inset) 48px; width: 100%; max-width: 1440px; }
.content.wide { max-width: 2400px; }
.auth-off { margin-bottom: 28px; padding: 24px 20px; font-size: 14px; }
.drawer-scrim { display: none; }

@media (max-width: 900px) {
  .sidebar {
    position: fixed;
    z-index: 45;
    left: 0;
    top: 0;
    transform: translateX(-100%);
    transition: transform 0.2s;
  }
  .drawer .sidebar { transform: none; }
  .drawer-scrim { display: block; position: fixed; inset: 0; z-index: 44; background: var(--scrim); }
  .content { padding: 24px 20px 40px; }
}
</style>
