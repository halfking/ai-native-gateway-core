<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { login, getAuthMe, getCaptcha } from '../api'
import { setApiKey, setJwtToken, setUserInfo, userInfoFromAuthMe } from '../store'
import { useLoginModal } from '../composables/useLoginModal'
import { locationFromInternalPath } from '../utils/safeRedirect'
import { currentTheme, detectTheme, logoSrc } from '../theme'
import { SITE_LOGO_SIZE } from '../config/brand'
import AppModal from './ui/AppModal.vue'
import CaptchaInput from './CaptchaInput.vue'
import { Close } from '@element-plus/icons-vue'

const { t } = useI18n()

const brandLogo = ref(logoSrc())
let logoObserver: MutationObserver | null = null
onMounted(() => {
  brandLogo.value = logoSrc(detectTheme())
  logoObserver = new MutationObserver(() => { brandLogo.value = logoSrc(currentTheme()) })
  logoObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})
onUnmounted(() => logoObserver?.disconnect())

const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
}>()

const router = useRouter()
const { closeLogin } = useLoginModal()

const username = ref('admin')
const password = ref('')
const loading = ref(false)
const error = ref('')
const rememberMe = ref(false)

// Captcha state
const showCaptcha = ref(false)
const captchaId = ref('')
const captchaCode = ref('')
const captchaRequired = ref(false)

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      error.value = ''
      password.value = ''
      captchaCode.value = ''
      checkCaptchaRequired()
    }
  },
)

async function checkCaptchaRequired() {
  try {
    const resp = await getCaptcha()
    captchaId.value = resp.captcha_id
    captchaRequired.value = resp.required
    showCaptcha.value = resp.required
  } catch {
    // Silently fail - captcha will be requested on login error if needed
  }
}

function close() {
  emit('update:modelValue', false)
  closeLogin()
}

async function handleLogin() {
  error.value = ''
  if (!username.value || !password.value) {
    error.value = t('login.error.required')
    return
  }
  
  if (captchaRequired.value && !captchaCode.value) {
    error.value = t('login.captcha.required')
    showCaptcha.value = true
    return
  }
  
  loading.value = true
  try {
    const resp = await login({
      username: username.value,
      password: password.value,
      captcha_id: captchaId.value || undefined,
      captcha_code: captchaCode.value || undefined,
      remember_me: rememberMe.value,
    })
    if (resp.access_token) {
      setJwtToken(resp.access_token)
      if (resp.user) {
        setUserInfo(resp.user)
      } else {
        // 兜底探测：判身份与 App.vue 水合同源（store.userInfoFromAuthMe）。
        // 此前把 /api/auth/me 的**整个响应**直接喂给 setUserInfo ——
        // 该端点的用户嵌在 `.user` 下，靠 setUserInfo 内部的 normalize 才碰巧没出错；
        // 一旦响应是维护信封就会造出「假登录态」（role 为空的 userInfo）。
        try {
          setUserInfo(userInfoFromAuthMe(await getAuthMe()))
        } catch { /* ignore */ }
      }
      close()
      const restored = locationFromInternalPath(router.currentRoute.value.query.redirect)
      const target = restored ?? { path: '/' }
      if (router.currentRoute.value.path === '/login' || router.currentRoute.value.query.login) {
        await router.replace(target)
      }
    } else if (resp.api_key) {
      // 2026-07-10: Legacy sk-* admin key path. Admin routes no longer accept
      // sk-* keys (unified JWT auth). Show an explicit error so the user
      // knows they need a regular user account.
      error.value = t('login.error.apiKeyUnsupported')
    } else {
      error.value = t('login.error.badResponse')
    }
  } catch (e: unknown) {
    const errorMsg = surfaceError(e, { key: 'login.error.failed' })
    
    // Check if captcha is required based on error
    if (errorMsg.includes('captcha') || errorMsg.includes('验证码')) {
      showCaptcha.value = true
      captchaRequired.value = true
      captchaCode.value = ''
      await checkCaptchaRequired()
    }
    
    error.value = errorMsg
  } finally {
    loading.value = false
  }
}
// 2026-09-13: 壳层迁移到 ui/AppModal（方案 §4.5.7）。
// ESC/滚动锁定/焦点圈闭由 AppModal 提供；AppModal 内部关闭（ESC/遮罩）
// 发出 update:modelValue=false，在此统一走 close() 补做 closeLogin()。
function onModelUpdate(v: boolean) {
  if (!v) close()
}
</script>

<template>
  <AppModal
    :model-value="modelValue"
    size="sm"
    panel-class="login-modal"
    :closable="false"
    @update:model-value="onModelUpdate"
  >
    <div class="login-modal__header">
      <div class="login-modal__brand">
        <img
          :src="brandLogo"
          :width="SITE_LOGO_SIZE"
          :height="SITE_LOGO_SIZE"
          :alt="t('app.brand.alt')"
          class="login-modal__brand-img"
        />
        <div>
          <h2 id="login-modal-title">{{ t('login.title') }}</h2>
          <p class="login-modal__subtitle">{{ t('app.brand.siteTitle') }}</p>
          <p class="login-modal__hint">{{ t('login.hint') }}</p>
        </div>
      </div>
      <button type="button" class="btn btn-ghost btn-sm login-modal__close" :aria-label="$t('common.close')" @click="close">
        <Close />
      </button>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <form @submit.prevent="handleLogin">
      <div class="form-group">
        <label for="login-username">{{ $t('common.terms.username') }}</label>
        <input
          id="login-username"
          v-model="username"
          type="text"
          placeholder="admin"
          autocomplete="username"
        />
      </div>
      <div class="form-group">
        <label for="login-password">{{ t('login.password') }}</label>
        <input
          id="login-password"
          v-model="password"
          type="password"
          placeholder="••••••••"
          autocomplete="current-password"
        />
      </div>
      
      <CaptchaInput
        v-if="showCaptcha"
        v-model="captchaCode"
        v-model:captcha-id="captchaId"
      />
      
      <div class="form-group form-group-checkbox">
        <label>
          <input v-model="rememberMe" type="checkbox" />
          <span>{{ t('login.rememberMe') }}</span>
        </label>
      </div>
      
      <div class="login-modal__actions">
        <button type="button" class="btn btn-ghost" @click="close">{{ $t('common.button.cancel') }}</button>
        <button class="btn btn-primary" type="submit" :disabled="loading">
          {{ loading ? t('login.submitting') : t('login.submit') }}
        </button>
      </div>
    </form>
  </AppModal>
</template>

<!-- .login-modal 基础皮肤作用于 AppModal 渲染的面板元素（不在本组件模板内），
     必须全局样式；min-width:0 抵消全局 .modal 的 360px 下限，保持旧版窄屏收缩行为。 -->
<style>
.modal.login-modal {
  background: var(--card);
  border-radius: 12px;
  padding: 24px;
  width: min(400px, calc(100vw - 32px));
  min-width: 0;
  box-shadow: var(--kx-shadow-md);
}
</style>

<style scoped>
.login-modal__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.login-modal__brand {
  display: flex;
  align-items: center;
  gap: 12px;
}

.login-modal__brand-img {
  flex-shrink: 0;
  display: block;
}

.login-modal__brand h2 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}

.login-modal__subtitle {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--muted);
}

.login-modal__close {
  flex-shrink: 0;
  min-width: 32px;
  padding: 4px 8px;
}

.login-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

.form-group-checkbox {
  margin: 12px 0;
}

.form-group-checkbox label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 14px;
}

.form-group-checkbox input[type="checkbox"] {
  cursor: pointer;
  width: auto;
  margin: 0;
}
</style>
