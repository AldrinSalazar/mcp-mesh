<script setup lang="ts">
import { computed, ref } from 'vue'
import UiModal from '@/components/ui/UiModal.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import { ApiError, api } from '@/lib/api'
import { refresh } from '@/lib/live'
import { toast } from '@/lib/toast'

const emit = defineEmits<{ close: [] }>()

const username = ref('')
const password = ref('')
const busy = ref(false)
const errors = ref({ username: '', password: '' })
const longEnough = computed(() => password.value.length >= 12)

async function submit() {
  if (busy.value) return
  const u = username.value.trim()
  errors.value = {
    username: u ? '' : 'Enter a username.',
    password: longEnough.value ? '' : 'Use at least 12 characters.',
  }
  if (errors.value.username || errors.value.password) return
  busy.value = true
  try {
    await api.createAdmin(u, password.value)
    toast('Admin added', `${u} can now sign in.`)
    void refresh('admins')
    emit('close')
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) errors.value.username = 'That username is taken.'
    else if (e instanceof ApiError && e.status === 400) errors.value.password = e.message
    else errors.value.password = e instanceof ApiError ? e.message : 'Could not add the admin.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiModal title="Add an admin" eyebrow="Admin access" @close="emit('close')">
    <form id="add-admin" novalidate @submit.prevent="submit">
      <p class="muted lede">Admins can manage all machines, tools and keys.</p>
      <UiField label="Username" for="admin-name" :error="errors.username">
        <UiInput id="admin-name" v-model="username" autocomplete="off" :invalid="!!errors.username" />
      </UiField>
      <UiField label="Password" for="admin-pw" hint="At least 12 characters" :ok="longEnough" :error="errors.password">
        <UiInput id="admin-pw" v-model="password" type="password" autocomplete="new-password" :invalid="!!errors.password" />
      </UiField>
    </form>
    <template #footer>
      <UiButton @click="emit('close')">Cancel</UiButton>
      <UiButton variant="primary" type="submit" form="add-admin" :loading="busy">Add admin</UiButton>
    </template>
  </UiModal>
</template>

<style scoped>
.lede { font-size: 14px; margin-bottom: 26px; }
</style>
