<script setup lang="ts">
// LoginView — 登录（headerless）：16px 输入防 iOS 缩放；失败信息内联展示。
//
// 2026-10-10 新增「保持登录」与图形验证码：
//   - 「保持登录」勾选 → 后端下发 24h 持久 cookie；不勾选 → 会话级 cookie。
//     落盘位置的选择在 auth store 里（localStorage vs sessionStorage）。
//   - 验证码是**自适应**的：后端未达失败阈值时 `required=false`，此时输入框
//     可见但留空也能提交；连续失败 3 次后 `required=true`，必须输入。
//     所以这里是「显示但可空」，而不是一上来就拦人。
import { nextTick, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { fetchCaptcha } from '@/api/auth'
import { ApiError } from '@/api/client'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const username = ref('')
const password = ref('')
const rememberMe = ref(false)
const captchaCode = ref('')
const captchaId = ref('')
const captchaImage = ref('')
/** 后端判定「本次来源已被强制要求验证码」。false 时可留空提交。 */
const captchaRequired = ref(false)
const captchaFailed = ref(false)
const submitting = ref(false)
/** 内联错误文案。空串 = 不显示（比 `failed: boolean` 更好：验证码错误与
 *  密码错误需要**不同**的话术，一个布尔量表达不了两件事）。 */
const errorText = ref('')
const usernameRef = ref<HTMLInputElement | null>(null)

void nextTick(() => {
  if (window.matchMedia?.('(pointer: coarse)').matches || window.matchMedia?.('(hover: none)').matches) return
  usernameRef.value?.focus()
})

/** 拉一张新验证码。失败时只提示，不阻断登录（此时验证码仍是可选的）。 */
async function refreshCaptcha(): Promise<void> {
  captchaCode.value = ''
  try {
    const resp = await fetchCaptcha()
    captchaId.value = resp.captcha_id
    captchaImage.value = resp.image
    captchaRequired.value = resp.required
    captchaFailed.value = false
  } catch {
    captchaId.value = ''
    captchaImage.value = ''
    captchaFailed.value = true
  }
}

onMounted(() => {
  void refreshCaptcha()
})

async function submit(): Promise<void> {
  if (submitting.value || !username.value || !password.value) return
  // 已被强制要求验证码却留空：本地拦下并换一张，省掉一次注定失败的往返。
  if (captchaRequired.value && !captchaCode.value.trim()) {
    errorText.value = t('login.captchaRequired')
    await refreshCaptcha()
    return
  }
  submitting.value = true
  errorText.value = ''
  try {
    await auth.login(username.value, password.value, {
      rememberMe: rememberMe.value,
      captchaId: captchaId.value,
      captchaCode: captchaCode.value.trim(),
    })
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
    // 401 → 登录是 replace；登录成功也 replace（不把登录页留在栈里，06 §4）
    await router.replace(redirect || '/')
  } catch (err) {
    const code = err instanceof ApiError ? err.code : ''
    if (code === 'captcha_required' || code === 'captcha_invalid') {
      errorText.value = code === 'captcha_required' ? t('login.captchaRequired') : t('login.captchaInvalid')
      // 验证码是一次性的，服务端此刻已经作废了刚才那张 —— 失败后必须换图，
      // 否则用户对着一个死验证码反复输，必然第二次也失败。
      // refreshCaptcha 会用服务端返回的 required 把「是否强制」同步回来。
      await refreshCaptcha()
    } else {
      errorText.value = t('login.failed')
    }
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
            id="mobile-login-username"
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
          <input
            id="mobile-login-password"
            v-model="password"
            type="password"
            name="password"
            autocomplete="current-password"
            required
          />
        </label>

        <div class="login__captcha">
          <img
            v-if="captchaImage"
            id="mobile-login-captcha-image"
            class="login__captcha-image"
            :src="captchaImage"
            :alt="t('login.captchaAlt')"
            @click="refreshCaptcha"
          />
          <button
            v-else
            id="mobile-login-captcha-reload"
            type="button"
            class="login__captcha-image login__captcha-image--missing"
            :aria-label="t('login.captchaReload')"
            @click="refreshCaptcha"
          >
            {{ captchaFailed ? t('login.captchaLoadFailed') : t('login.captchaLoading') }}
          </button>
          <label class="login__field login__field--grow">
            <span class="login__label">{{ t('login.captcha') }}</span>
            <input
              id="mobile-login-captcha"
              v-model="captchaCode"
              type="text"
              name="captcha"
              autocomplete="off"
              autocapitalize="characters"
              spellcheck="false"
              :aria-required="captchaRequired"
              :placeholder="t('login.captchaPlaceholder')"
            />
          </label>
        </div>
        <p v-if="captchaFailed" class="login__error" role="alert">{{ t('login.captchaLoadFailed') }}</p>

        <label class="login__remember">
          <input id="mobile-login-remember" v-model="rememberMe" type="checkbox" name="remember" />
          <span class="login__remember-text">{{ t('login.rememberMe') }}</span>
        </label>

        <p v-if="errorText" class="login__error" role="alert">{{ errorText }}</p>

        <button
          id="mobile-login-submit"
          type="submit"
          class="btn btn--primary btn--block"
          :disabled="submitting || !username || !password"
        >
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

/* 验证码行：图 + 输入框并排。热区按 R1 取 48px —— 图本身可点刷新，
   所以它也是触控控件，不能因为「只是个图」就缩到 40px。 */
.login__captcha {
  display: flex;
  align-items: flex-end;
  gap: var(--app-space-3);
}

.login__field--grow {
  flex: 1;
  min-width: 0;
}

.login__captcha-image {
  min-height: 48px;
  width: 140px;
  height: 48px;
  flex: none;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: #f2f5f9;
  object-fit: cover;
  cursor: pointer;
}

.login__captcha-image--missing {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0 var(--app-space-2);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  text-align: center;
  line-height: 1.2;
}

/* 「保持登录」整行都是热区：勾选框只有十几像素，单独一个 label 会让
   移动端点不中（R1）。 */
.login__remember {
  display: flex;
  align-items: center;
  gap: var(--app-space-3);
  min-height: 48px;
}

.login__remember input {
  width: 20px;
  height: 20px;
  flex: none;
  accent-color: var(--kx-primary);
}

.login__remember-text {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.login__error {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-danger);
}
</style>