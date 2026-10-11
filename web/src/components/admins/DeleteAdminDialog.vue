<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { ApiError, api } from '@/lib/api'
import { live, refresh, signedOut } from '@/lib/live'
import { toast } from '@/lib/toast'

const props = defineProps<{ username: string }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const own = computed(() => props.username === live.username)
const others = computed(() => live.admins.filter((a) => a.username !== props.username))
const remain = computed(() =>
  others.value.length === 1 ? `${others.value[0]!.username} will remain as the hub’s admin.` : `${others.value.length} admins will remain.`,
)
const busy = ref(false)
const lastAdmin = ref(false)
const error = ref('')

async function remove() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteAdmin(props.username)
    if (own.value) {
      toast('Admin deleted', 'You have been signed out.')
      emit('close')
      signedOut()
      await router.push({ name: 'sign-in' })
    } else {
      toast('Admin deleted', `${props.username} can no longer sign in.`)
      void refresh('admins')
      emit('close')
    }
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) lastAdmin.value = true
    else error.value = e instanceof ApiError ? e.message : 'Could not delete the admin.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal
    :title="own ? 'Delete your admin account?' : `Delete ${username}?`"
    eyebrow="Remove admin"
    eyebrow-tone="err"
    @close="emit('close')"
  >
    <UiNotice v-if="lastAdmin" tone="err" title="The last admin can’t be deleted.">
      Keep one admin so you can continue managing this hub.
    </UiNotice>
    <template v-else>
      <p v-if="own" class="text">You will be signed out and lose admin access.<br />Your client and node keys will keep working.</p>
      <p v-else class="text">{{ username }} will no longer be able to sign in to this hub. This does not revoke their client or node keys.</p>
      <p v-if="others.length" class="remain muted">{{ remain }}</p>
      <p v-if="error" class="err-text small" role="alert">{{ error }}</p>
    </template>
    <template #footer>
      <UiButton @click="emit('close')">Keep admin</UiButton>
      <UiButton v-if="!lastAdmin" variant="danger" :loading="busy" @click="remove">Delete admin</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.text { font-size: 14px; line-height: 1.5; color: var(--muted); }
.remain { margin-top: 36px; font-size: 12px; }
</style>
