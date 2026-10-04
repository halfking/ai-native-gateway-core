<script setup lang="ts">
// LoginView.vue — 全屏表单（16px 输入防 iOS 缩放；48px 触控目标）。
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { login } from '../api/auth'
import { setToken, setUserInfo } from '../api/_core'
import { nextSessionEpoch } from '../runtime/epochs'
import { useHyperPage } from '../composables/useHyperPage'
import { t } from '../i18n'

const route = useRoute()
const router = useRouter()
useHyperPage({ routeTitle: t('login.title') })

const username = ref('')
const password = ref('')
const submitting = ref(false)
const error = ref('')

async function submit(): Promise<void> {
  if (!username.value || !password.value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    const res = await login(username.value, password.value)
    if (res.access_token) setToken(res.access_token)
    if (res.user) setUserInfo(res.user)
    nextSessionEpoch()
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    router.replace(redirect.startsWith('/') ? redirect : '/')
  } catch (err) {
    error.value = err instanceof Error ? `${t('login.failed')}: ${err.message}` : t('login.failed')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <p class="login-brand">{{ t('app.name') }}</p>
    <div class="m-card">
      <label class="m-field">
        <span class="m-field__label">{{ t('login.username') }}</span>
        <input
          v-model="username"
          class="m-input"
          type="text"
          autocomplete="username"
          autocapitalize="none"
          enterkeyhint="next"
        />
      </label>
      <label class="m-field">
        <span class="m-field__label">{{ t('login.password') }}</span>
        <input
          v-model="password"
          class="m-input"
          type="password"
          autocomplete="current-password"
          enterkeyhint="go"
          @keyup.enter="submit"
        />
      </label>
      <p v-if="error" class="login-error">{{ error }}</p>
      <button type="button" class="m-btn login-btn" :disabled="submitting" @click="submit">
        {{ submitting ? t('common.loading') : t('login.submit') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  padding-top: 18vh;
  max-width: 420px;
  margin: 0 auto;
}

.login-brand {
  text-align: center;
  font-size: 1.375rem;
  font-weight: 700;
  color: var(--app-primary);
  margin-bottom: var(--app-space-5);
}

.login-error {
  color: var(--app-danger);
  font-size: 0.8125rem;
  margin-bottom: var(--app-space-2);
}

.login-btn {
  width: 100%;
  margin-top: var(--app-space-2);
}
</style>
