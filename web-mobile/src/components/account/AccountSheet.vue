<script setup lang="ts">
// AccountSheet.vue — 账户 Sheet（UI 规范 02 §5：用户/外观/语言/登出全屏 Sheet）。
import { computed, ref } from 'vue'
import Sheet from '../ui/Sheet.vue'
import { t, setLocale, getLocale, type Locale } from '../../i18n'
import { getUserInfo } from '../../api/_core'
import { logout } from '../../api/auth'
import { nextSessionEpoch } from '../../runtime/epochs'

const emit = defineEmits<{ (e: 'close'): void }>()

const user = computed(() => getUserInfo())

const theme = ref<'light' | 'dark'>(
  (typeof document !== 'undefined' &&
    document.documentElement.getAttribute('data-theme') === 'dark')
    ? 'dark'
    : 'light',
)

const locale = ref<Locale>(getLocale())

function applyTheme(next: 'light' | 'dark'): void {
  theme.value = next
  document.documentElement.setAttribute('data-theme', next)
  document.documentElement.style.colorScheme = next
  try {
    localStorage.setItem('llmgw_theme', next)
  } catch {
    /* ignore */
  }
}

function applyLocale(next: Locale): void {
  locale.value = next
  setLocale(next)
  // 语言切换后关闭 Sheet 让调用方刷新标签文案。
  emit('close')
}

async function onLogout(): Promise<void> {
  try {
    await logout()
  } catch {
    /* 登出 best-effort：cookie 清理失败也走本地登出 */
  }
  nextSessionEpoch()
  window.location.replace('/m/login')
}

function goDesktop(): void {
  try {
    sessionStorage.setItem('llmgw_ui_mode', 'desktop')
  } catch {
    /* ignore */
  }
  window.location.href = '/'
}
</script>

<template>
  <Sheet :title="t('account.title')" @close="emit('close')">
    <div class="m-card">
      <div class="account-row">
        <span class="m-kv__k">{{ t('login.username') }}</span>
        <span class="m-kv__v">{{ user?.username ?? user?.display_name ?? '—' }}</span>
      </div>
      <div v-if="user?.role" class="m-kv">
        <span class="m-kv__k">Role</span>
        <span class="m-kv__v">{{ String(user.role) }}</span>
      </div>
    </div>

    <p class="m-group-label">{{ t('account.theme') }}</p>
    <div class="seg">
      <button type="button" class="seg__item" :class="{ 'is-active': theme === 'light' }" @click="applyTheme('light')">
        {{ t('account.theme.light') }}
      </button>
      <button type="button" class="seg__item" :class="{ 'is-active': theme === 'dark' }" @click="applyTheme('dark')">
        {{ t('account.theme.dark') }}
      </button>
    </div>

    <p class="m-group-label">{{ t('account.language') }}</p>
    <div class="seg">
      <button type="button" class="seg__item" :class="{ 'is-active': locale === 'zh-CN' }" @click="applyLocale('zh-CN')">
        简体中文
      </button>
      <button type="button" class="seg__item" :class="{ 'is-active': locale === 'en-US' }" @click="applyLocale('en-US')">
        English
      </button>
    </div>

    <div class="stack">
      <button type="button" class="m-btn m-btn--ghost" @click="goDesktop">
        {{ t('account.desktop') }}
      </button>
      <button type="button" class="m-btn m-btn--danger" @click="onLogout">
        {{ t('account.logout') }}
      </button>
    </div>
  </Sheet>
</template>

<style scoped>
.account-row {
  display: flex;
  justify-content: space-between;
  padding: 0.4rem 0;
  font-size: 0.9375rem;
}

.seg {
  display: flex;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}

.seg__item {
  flex: 1;
  min-height: 48px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-weight: 600;
}

.seg__item.is-active {
  border-color: var(--app-primary);
  color: var(--app-primary);
  background: var(--app-info-soft);
}

.stack {
  display: grid;
  gap: var(--app-space-2);
  margin-top: var(--app-space-4);
}
</style>
