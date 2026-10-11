<script setup lang="ts">
// Dialogs shared across screens (see lib/dialogs.ts), plus the session
// expired notice.
import { useRouter } from 'vue-router'
import CreateKeyDialog from './CreateKeyDialog.vue'
import PasswordDialog from './PasswordDialog.vue'
import UiModal from '@/components/ui/UiModal.vue'
import { dialogs } from '@/lib/dialogs'
import { live } from '@/lib/live'

const router = useRouter()
function toSignIn() {
  router.push({ name: 'sign-in', query: { next: router.currentRoute.value.fullPath } })
}
</script>

<template>
  <CreateKeyDialog v-if="dialogs.createKey" :kind="dialogs.createKey.kind" @close="dialogs.createKey = null" />
  <PasswordDialog v-if="dialogs.password" :username="dialogs.password.username" @close="dialogs.password = null" />
  <UiModal v-if="live.expired" title="Your session has expired." persistent :width="480">
    <p class="muted">Return to sign-in to continue.</p>
    <template #footer>
      <button type="button" class="arrow-link expired-link" @click="toSignIn">Sign in</button>
    </template>
  </UiModal>
</template>

<style scoped>
.expired-link { border: 0; background: none; cursor: pointer; font-size: 14px; }
</style>
