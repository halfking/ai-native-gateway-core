<script setup lang="ts">
// LoginView — 登录（headerless）：16px 输入防 iOS 缩放；失败信息内联展示。
import { nextTick, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const failed = ref(false)
const usernameRef = ref<HTMLInputElement | null>(null)

void nextTick(() => usernameRef.value?.focus())

async function submit(): Promise<void> {
  if (submitting.value || !username.value || !password.value) return
  submitting.value = true
  failed.value = false
  try {
    await auth.login(username.value, password.value)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
    // 401 → 登录是 replace；登录成功也 replace（不把登录页留在栈里，06 §4）
    await router.replace(redirect || '/')
  } catch {
    failed.value = true
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login">
    <div class="login__card">
      <div class="login__brand">
        <AppIcon name="cpu" :size="34" class="login__logo" />
        <h1 class="login__title">{{ t('login.title') }}</h1>
        <p class="login__hint">{{ t('login.hint') }}</p>
      </div>

      <form class="login__form" @submit.prevent="submit">
        <label class="login__field">
          <span class="login__label">{{ t('login.username') }}</span>
          <input
            ref="usernameRef"
            v-model="username"
            type="text"
            name="username"
            autocomplete="username"
            autocapitalize="none"
            required
          />
        </label>
        <label class="login__field">
          <span class="login__label">{{ t('login.password') }}</span>
          <input v-model="password" type="password" name="password" autocomplete="current-password" required />
        </label>

        <p v-if="failed" class="login__error" role="alert">{{ t('login.failed') }}</p>

        <button type="submit" class="btn btn--primary btn--block" :disabled="submitting || !username || !password">
          {{ submitting ? t('login.submitting') : t('login.submit') }}
        </button>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login {
  flex: 1;
  min-height: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--app-space-5) var(--app-space-4) calc(var(--app-space-5) + var(--app-safe-bottom));
  overflow-y: auto;
}

.login__card {
  width: 100%;
  max-width: 380px;
  background: var(--app-surface);
  border: 1px solid var(--app-border-subtle);
  border-radius: var(--app-radius-lg);
  box-shadow: var(--app-shadow-card);
  padding: var(--app-space-6) var(--app-space-5);
}

.login__brand {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-5);
}

.login__logo {
  color: var(--kx-primary);
}

.login__title {
  font-size: 1.125rem;
  font-weight: 600;
}

.login__hint {
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.login__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.login__field {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
}

.login__label {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.login__field input {
  height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: var(--app-font-input); /* 16px：防 iOS 聚焦缩放（11 §1） */
}

.login__field input:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 0;
  border-color: transparent;
}

.login__error {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-danger);
}
</style>
