<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ChevronDown } from 'lucide-vue-next'
import { live, signOut } from '@/lib/live'
import { openPassword } from '@/lib/dialogs'

const open = ref(false)
const root = ref<HTMLElement>()
const router = useRouter()

function onDoc(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}
onMounted(() => {
  document.addEventListener('mousedown', onDoc)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDoc)
  document.removeEventListener('keydown', onKey)
})

function changePassword() {
  open.value = false
  openPassword(live.username)
}
async function doSignOut() {
  open.value = false
  await signOut()
  router.push({ name: 'sign-in' })
}
</script>

<template>
  <div ref="root" class="account">
    <button type="button" class="trigger" :aria-expanded="open" aria-haspopup="menu" @click="open = !open">
      <span class="avatar" aria-hidden="true">{{ live.username.slice(0, 1).toUpperCase() }}</span>
      <span class="name">{{ live.username }}</span>
      <ChevronDown :size="18" :stroke-width="1.4" class="chev" aria-hidden="true" />
    </button>
    <div v-if="open" class="menu" role="menu">
      <p class="who">Signed in as {{ live.username }}</p>
      <button type="button" role="menuitem" @click="changePassword">Change password</button>
      <button type="button" role="menuitem" class="danger" @click="doSignOut">Sign out</button>
    </div>
  </div>
</template>

<style scoped>
.account { position: relative; }
.trigger {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 190px;
  height: 44px;
  padding: 0 4px;
  border: 0;
  background: none;
  cursor: pointer;
}
.avatar {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: var(--ink);
  color: var(--surface);
  font-family: var(--font-display);
  font-weight: 600;
  font-size: 15px;
  flex: none;
}
.name { font-size: 14px; font-weight: 500; }
.chev { margin-left: auto; color: var(--muted); }
.menu {
  position: absolute;
  right: -20px;
  top: calc(100% + 8px);
  z-index: 40;
  width: 216px;
  padding: 6px 16px 10px;
  background: var(--surface);
  border: 1px solid var(--rule);
}
.who { padding: 10px 0 12px; border-bottom: 1px solid var(--rule); font-size: 12px; color: var(--muted); }
.menu button {
  display: block;
  width: 100%;
  padding: 12px 0 8px;
  border: 0;
  background: none;
  text-align: left;
  font-size: 13px;
  cursor: pointer;
}
.menu button:hover { text-decoration: underline; }
.danger { color: var(--err); }
@media (max-width: 700px) {
  .trigger { min-width: 0; }
  .name, .chev { display: none; }
  .menu { right: 0; }
}
</style>
