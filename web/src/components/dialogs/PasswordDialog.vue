<script setup lang="ts">
// Change your own password (needs the current one, ends your session) or
// reset another admin's (no current password needed).
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import { ApiError, api } from '@/lib/api'
import { live, refresh, signedOut } from '@/lib/live'
import { toast } from '@/lib/toast'

const props = defineProps<{ username: string }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const own = computed(() => props.username === live.username)
const current = ref('')
const next = ref('')
const confirm = ref('')
const busy = ref(false)
const errors = ref({ current: '', next: '', confirm: '', form: '' })

const longEnough = computed(() => next.value.length >= 12)
const mismatch = computed(() => confirm.value !== '' && confirm.value !== next.value)

async function submit() {
  if (busy.value) return
  errors.value = {
    current: own.value && !current.value ? 'Enter your current password.' : '',
    next: longEnough.value ? '' : 'Use at least 12 characters.',
    confirm: own.value && next.value !== confirm.value ? 'Passwords don’t match' : '',
    form: '',
  }
  if (errors.value.current || errors.value.next || errors.value.confirm) return
  busy.value = true
  try {
    await api.setPassword(props.username, next.value, own.value ? current.value : undefined)
    if (own.value) {
      // The hub ended this session along with the old password.
      toast('Password changed', 'Sign in with your new password.')
      emit('close')
      signedOut()
      await router.push({ name: 'sign-in' })
    } else {
      toast('Password reset', `${props.username} will need to sign in again.`)
      void refresh('admins')
      emit('close')
    }
  } catch (e) {
    if (e instanceof ApiError && e.status === 403 && own.value) errors.value.current = 'Current password is wrong.'
    else errors.value.form = e instanceof ApiError ? e.message : 'Could not change the password.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal
    :title="own ? 'Change password' : `Reset ${username}’s password`"
    :eyebrow="own ? `Your account / ${username}` : `Admin access / ${username}`"
    @close="emit('close')"
  >
    <form id="password-form" novalidate @submit.prevent="submit">
      <p class="muted lede">{{ own ? 'You’ll be signed out after changing your password.' : 'Set a new password for this admin.' }}</p>
      <UiField v-if="own" label="Current password" for="pw-current" :error="errors.current">
        <UiInput id="pw-current" v-model="current" type="password" autocomplete="current-password" :invalid="!!errors.current" />
      </UiField>
      <UiField
        label="New password"
        for="pw-new"
        :hint="own ? 'At least 12 characters' : 'At least 12 characters. No current password is needed.'"
        :ok="longEnough"
        :error="errors.next"
      >
        <UiInput id="pw-new" v-model="next" type="password" autocomplete="new-password" :invalid="!!errors.next" />
      </UiField>
      <UiField
        v-if="own"
        label="Confirm new password"
        for="pw-confirm"
        :error="errors.confirm || (mismatch ? 'Passwords don’t match' : '')"
      >
        <UiInput id="pw-confirm" v-model="confirm" type="password" autocomplete="new-password" :invalid="mismatch || !!errors.confirm" />
      </UiField>
      <p v-if="errors.form" class="form-err" role="alert">{{ errors.form }}</p>
    </form>
    <template #footer>
      <UiButton @click="emit('close')">Cancel</UiButton>
      <UiButton variant="primary" type="submit" form="password-form" :loading="busy">
        {{ own ? 'Change password' : 'Reset password' }}
      </UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.lede { font-size: 14px; margin-bottom: 26px; }
.form-err { margin-top: 18px; font-size: 12px; color: var(--err); }
</style>
