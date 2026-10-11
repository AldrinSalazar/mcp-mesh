<script setup lang="ts">
// Penpot board "Sign in": the poster on the left, the form on the right.
// Wrong credentials and the rate-limit lockout (with a live countdown) are
// shown under the form (board "States & patterns", 04).
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight } from 'lucide-vue-next'
import BrandMark from '@/components/layout/BrandMark.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiField from '@/components/ui/UiField.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import poster from '@/assets/poster-mark.svg'
import { ApiError } from '@/lib/api'
import { hubName, live, signIn } from '@/lib/live'

const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')
const lockedUntil = ref(0)
const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | undefined

const lockLeft = computed(() => Math.max(0, Math.ceil((lockedUntil.value - now.value) / 1000)))
const lockText = computed(() => {
  const s = lockLeft.value
  const m = Math.floor(s / 60)
  return m > 0 ? `${m}m${String(s % 60).padStart(2, '0')}s` : `${s}s`
})

function startLock(seconds: number) {
  lockedUntil.value = Date.now() + seconds * 1000
  now.value = Date.now()
  clearInterval(tick)
  tick = setInterval(() => {
    now.value = Date.now()
    if (lockLeft.value === 0) clearInterval(tick)
  }, 500)
}
onBeforeUnmount(() => clearInterval(tick))

async function submit() {
  if (busy.value || lockLeft.value > 0) return
  error.value = ''
  if (!username.value || !password.value) {
    error.value = 'Enter your username and password.'
    return
  }
  busy.value = true
  try {
    await signIn(username.value.trim(), password.value)
    const next = typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : '/'
    await router.replace(next)
  } catch (e) {
    password.value = ''
    if (e instanceof ApiError && e.status === 429) startLock(e.retryAfter ?? 60)
    else if (e instanceof ApiError && e.status === 401) error.value = 'Wrong username or password.'
    else error.value = e instanceof Error ? e.message : 'Sign-in failed.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="signin">
    <section class="poster" aria-hidden="true">
      <BrandMark class="poster-brand" :size="40" />
      <img :src="poster" alt="" class="mark" width="468" height="421" />
    </section>

    <section class="side">
      <form class="form" novalidate @submit.prevent="submit">
        <p class="crumb mono">{{ hubName().toUpperCase() }} / ADMIN ACCESS</p>
        <h1 class="title">Sign in</h1>

        <UiField label="Username" for="username">
          <UiInput id="username" v-model="username" autocomplete="username" :invalid="!!error" />
        </UiField>
        <UiField label="Password" for="password">
          <UiInput id="password" v-model="password" type="password" autocomplete="current-password" :invalid="!!error" />
        </UiField>

        <UiButton type="submit" variant="primary" block :icon="ArrowRight" :loading="busy" :disabled="lockLeft > 0" class="submit">
          Sign in
        </UiButton>

        <UiNotice v-if="lockLeft > 0" tone="warn" class="msg">
          Too many failed sign-ins; try again in {{ lockText }}
        </UiNotice>
        <UiNotice v-else-if="error" tone="err" class="msg">{{ error }}</UiNotice>

        <div class="first">
          <p class="first-title">First time here?</p>
          <p class="muted">The initial password is printed in your hub’s log.</p>
        </div>
      </form>
      <p class="foot mono">{{ hubName() }} · Hub {{ live.info?.version ?? '' }}</p>
    </section>
  </div>
</template>

<style scoped>
.signin { display: grid; grid-template-columns: 1fr 1fr; min-height: 100vh; }
.poster {
  position: relative;
  display: grid;
  place-items: center;
  background: #252922;
  padding: 48px;
}
/* The poster is always dark, so the mark is the reverse one. */
.poster-brand { position: absolute; top: 40px; left: 46px; }
.poster-brand :deep(.ink) { fill: #f2f2e9; }
.poster-brand :deep(.letter) { fill: #252922; }
.poster-brand :deep(.word) { color: #f2f2e9; }
.mark { width: min(468px, 80%); height: auto; }
.side { position: relative; display: flex; flex-direction: column; justify-content: center; padding: 64px 124px; }
.form { width: 100%; max-width: 452px; }
.crumb { font-size: 11px; color: var(--muted); letter-spacing: 0.04em; }
.title { font-size: 36px; margin: 34px 0 56px; }
.submit { margin-top: 28px; }
.msg { margin-top: 16px; font-size: 13px; }
.first { margin-top: 40px; padding-top: 30px; border-top: 1px solid var(--rule); font-size: 13px; }
.first-title { font-weight: 500; margin-bottom: 14px; }
.foot { position: absolute; bottom: 60px; left: 124px; color: var(--muted); }
@media (max-width: 900px) {
  .signin { grid-template-columns: 1fr; }
  .poster { min-height: 180px; padding: 24px; }
  .mark { width: 140px; margin-top: 36px; }
  .side { padding: 40px 24px 80px; }
  .foot { left: 24px; bottom: 28px; }
}
</style>
