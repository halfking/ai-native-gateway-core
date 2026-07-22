<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { store, clearAll, clearJwt, clearMustChangePasswordFlag, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, markAuthHydrated, setJwtToken, setUserInfo, authBearer } from './store'
import { logout as apiLogout } from './api/auth'
import { getAuthMe } from './api/admin'
import LoginModal from './components/LoginModal.vue'
import ChangePasswordDialog from './components/ChangePasswordDialog.vue'
import UserInfoDialog from './components/UserInfoDialog.vue'
import LanguageSelector from './components/LanguageSelector.vue'
import ThemeToggle from './components/ThemeToggle.vue'
import AppTopbar from './components/shell/AppTopbar.vue'
import { detectTheme, logoSrc } from './theme'
import { SITE_LOGO_SIZE, SITE_TITLE } from './config/brand'
import { useLoginModal } from './composables/useLoginModal'
import { onMaintainAvailabilityChange, probeMaintainAvailable } from './config/edition'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { showLoginModal, openLogin, closeLogin } = useLoginModal()
const showChangePassword = ref(false)
const showUserInfo = ref(false)
const passwordSuccessMessage = ref('')
const mustChangePassword = computed(() => !!store.jwtToken && !!store.userInfo?.must_change_password)

// 2026-07-09: isHydrating 防止页面在 auth probe 完成前误判为未登录。
// 与 store.authHydrated 配合：App.vue onMounted 触发 /api/auth/me，settle 后翻为 true。
const isHydrating = computed(() => !store.authHydrated)

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey || store.userInfo))
const brandLogo = ref(logoSrc(detectTheme()))
const logoObserver = typeof MutationObserver !== 'undefined'
  ? new MutationObserver(() => { brandLogo.value = logoSrc(detectTheme()) })
  : null

onMounted(async () => {
  brandLogo.value = logoSrc(detectTheme())
  logoObserver?.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  // 2026-07-10: Auth hydration — probe /api/auth/me if JWT not already in localStorage.
  try {
    if (!store.jwtToken && !store.apiKey) {
      try {
        const me = await getAuthMe()
        const meAny = me as any
        if (meAny?.access_token) {
          setJwtToken(meAny.access_token)
        }
        setUserInfo(me)
      } catch {
        clearJwt()
      }
    }
  } finally {
    markAuthHydrated()
  }

  void probeMaintainAvailable()
  // 保留 maintain 可用性订阅入口以便未来 topbar 内 onUnmounted 正确清理。
  // 当前无回调（topbar 自取），不会泄漏 — onMaintainAvailabilityChange 在静态模块层仅保留全局 listener。
  onMaintainAvailabilityChange(() => { /* noop */ })
})

onUnmounted(() => {
  logoObserver?.disconnect()
})

const versionInfo = ref<{
  version?: string
  git_sha?: string
  build_date?: string
  build_seq?: number
}>({})

async function loadVersion() {
  if (!isLoggedIn.value) return
  const token = authBearer()
  try {
    const resp = await fetch('/api/system/version', {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    })
    if (resp.status === 401) {
      clearAll()
      router.push('/')
      openLogin()
      return
    }
    if (resp.ok) {
      versionInfo.value = await resp.json()
    }
  } catch {
    // ignore — version display is non-critical
  }
}

watch(
  isLoggedIn,
  (loggedIn) => {
    if (loggedIn) {
      closeLogin()
      loadVersion()
    }
  },
  { immediate: true },
)

watch(
  mustChangePassword,
  (required) => {
    if (required) {
      passwordSuccessMessage.value = ''
      showChangePassword.value = true
    }
  },
  { immediate: true },
)

watch(
  () => route.query.login,
  (login) => {
    if (login && !isLoggedIn.value) openLogin()
  },
  { immediate: true },
)

async function logout() {
  try { await apiLogout() } catch { /* ignore */ }
  clearAll()
  markAuthHydrated() // 2026-07-09: 登出后保持 hydrated=true，下一次 mount 才会重新探测
  router.push('/')
}

function openUserInfo() {
  showUserInfo.value = true
}

function openChangePassword() {
  passwordSuccessMessage.value = ''
  showChangePassword.value = true
}

function handleChangePasswordSuccess() {
  clearMustChangePasswordFlag()
  showChangePassword.value = false
  passwordSuccessMessage.value = t('login.passwordChangeSuccess')
}
</script>

<template>
  <!--
    2026-07-21: 三态渲染 — hydrating / logged-in / guest.
    - isHydrating=true 时显示加载中
    - 已登录态：顶部水平菜单（AppTopbar）+ 业务页（RouterView，业务页内部保持原貌）
    - 未登录态：保持原 LifecycleShell / LandingView 体系
    - 老板："其它页面风格保持不变" → 业务页 (DashboardView 等) 不动，只换外壳
  -->
  <div v-if="isHydrating" class="auth-loading">
    <div class="auth-loading-spinner" />
    <div class="auth-loading-text">{{ t('login.checking') || '正在检测登录状态…' }}</div>
  </div>
  <div v-else-if="isLoggedIn" class="app-layout app-layout--topbar">
    <AppTopbar
      :version-info="versionInfo"
      @user-info="openUserInfo"
      @change-password="openChangePassword"
      @logout="logout"
    />
    <main class="main-content">
      <div v-if="passwordSuccessMessage" class="main-banner">
        <div class="alert alert-success header-alert">{{ passwordSuccessMessage }}</div>
      </div>
      <section class="main-body">
        <RouterView />
      </section>
    </main>
  </div>
  <div v-else class="guest-layout">
    <header class="guest-header">
      <a href="/" class="guest-brand" :aria-label="SITE_TITLE">
        <img
          :src="brandLogo"
          :width="SITE_LOGO_SIZE"
          :height="SITE_LOGO_SIZE"
          alt="开轩启圭"
          class="guest-brand-img"
        />
        <span class="guest-brand-text">{{ SITE_TITLE }}</span>
      </a>
      <nav class="guest-nav" :aria-label="t('landing.guestNavAria') || '产品导航'">
        <a href="/customer/update-activate">{{ t('landing.navDownload') }}</a>
        <a href="/bootstrap">{{ t('landing.navSetup') || '安装激活' }}</a>
        <a href="/customer/update-activate">{{ t('landing.navActivate') }}</a>
        <a href="/customer/update-activate">{{ t('landing.navLicense') }}</a>
        <a href="/customer/update-activate">{{ t('landing.navAgreement') }}</a>
        <a href="/customer/update-activate">{{ t('landing.navSupport') }}</a>
      </nav>
      <div class="guest-header-right">
        <ThemeToggle />
        <LanguageSelector />
        <button type="button" class="btn btn-primary btn-sm guest-login-btn" @click="openLogin">{{ t('login.submit') }}</button>
      </div>
    </header>
    <main class="guest-main">
      <RouterView />
    </main>
    <LoginModal v-model="showLoginModal" />
  </div>
  <ChangePasswordDialog v-model="showChangePassword" :forced="mustChangePassword" @success="handleChangePasswordSuccess" />
  <UserInfoDialog v-model="showUserInfo" />
</template>

<style scoped>
/* 2026-07-09: 首次进入时的 auth 探测加载中状态 */
.auth-loading {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100vh;
  background: var(--bg-card);
  color: var(--text-secondary);
  font-size: 13px;
}
.auth-loading-spinner {
  width: 32px;
  height: 32px;
  border: 3px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: auth-spin 0.8s linear infinite;
  margin-bottom: 14px;
}
.auth-loading-text {
  letter-spacing: 0.05em;
}
@keyframes auth-spin {
  to { transform: rotate(360deg); }
}

.app-layout {
  display: flex;
  flex-direction: column;
  height: 100vh;
  overflow: hidden;
}

/* 2026-07-21: topbar 模式下，.app-layout 改为纵向 flex（topbar 在上 + main-content 在下）。
 * overflow 必须 visible，否则绝对定位二级下拉会被裁切成「窄条内滚动」。
 * 滚动约束下放到 .main-content / .main-body。 */
.app-layout--topbar {
  flex-direction: column;
  overflow: visible;
}
.app-layout--topbar .main-content {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  border-inline-start: 0;
  overflow: hidden;
}

.sidebar {
  width: 220px;
  flex-shrink: 0;
  background: var(--sidebar);
  /* border-right: in LTR this is the inline-end of the sidebar (the
   * edge facing the main content); in RTL the sidebar moves to the
   * right side of the viewport and the facing edge flips. postcss-rtlcss
   * handles `border-right` automatically, but we use the logical form
   * here so the intent is explicit to future readers. */
  border-inline-end: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  transition: width 0.2s ease;
}

.header-alert {
  margin: 0;
  padding: 6px 10px;
  font-size: 12px;
}

.app-layout.sidebar-collapsed .sidebar {
  width: 64px;
}

.sidebar-logo {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px 14px 14px;
  font-size: 12px;
  font-weight: 700;
  color: var(--text);
  border-bottom: 1px solid var(--border);
  min-height: 76px;
  text-decoration: none;
  cursor: pointer;
}
.sidebar-logo:hover {
  color: var(--accent-h, var(--text));
}

.app-layout.sidebar-collapsed .sidebar-logo {
  justify-content: center;
  padding: 20px 8px 16px;
}

.sidebar-logo-text {
  line-height: 1.35;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: -0.01em;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
}

.sidebar-logo-img {
  flex-shrink: 0;
  display: block;
  width: 60px;
  height: 60px;
  object-fit: contain;
  border-radius: 12px;
}

.sidebar-nav {
  flex: 1;
  padding: 8px 8px 12px;
  overflow-y: auto;
  /* Slim themed scrollbar — the default browser scrollbar is glaringly
   * wide inside the 64px collapsed sidebar. Firefox uses the longhand
   * `scrollbar-*` properties; WebKit/Blink need the pseudo-elements. */
  scrollbar-width: thin;
  scrollbar-color: color-mix(in srgb, var(--accent) 40%, transparent) transparent;
}

.sidebar-nav::-webkit-scrollbar {
  width: 4px;
}

.sidebar-nav::-webkit-scrollbar-track {
  background: transparent;
}

.sidebar-nav::-webkit-scrollbar-thumb {
  background: color-mix(in srgb, var(--accent) 40%, transparent);
  border-radius: 4px;
  transition: background 0.15s;
}

.sidebar-nav::-webkit-scrollbar-thumb:hover {
  background: color-mix(in srgb, var(--accent) 75%, transparent);
}

.nav-primary {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-bottom: 8px;
  margin-bottom: 4px;
  border-bottom: 1px solid var(--border);
}

.nav-item-primary {
  font-weight: 500;
}

.nav-group + .nav-group {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}

.app-layout.sidebar-collapsed .nav-group + .nav-group {
  margin-top: 4px;
  padding-top: 4px;
}

.nav-group-label {
  padding: 4px 10px 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  color: var(--muted);
  text-transform: none;
  user-select: none;
}

.nav-group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  padding: 6px 10px;
  margin-bottom: 2px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  cursor: pointer;
  text-align: left;
  transition: background 0.15s, color 0.15s;
}

.nav-group-header:hover {
  background: rgba(255, 255, 255, 0.05);
  color: var(--text);
}

.nav-group-header.expanded,
.nav-group-header.has-active {
  color: var(--text);
}

.nav-group-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.nav-group-chevron {
  flex-shrink: 0;
  width: 0;
  height: 0;
  border-top: 4px solid transparent;
  border-bottom: 4px solid transparent;
  /* In LTR the caret points right (border-left → `>`). postcss-rtlcss
   * mirrors this to `border-right`, making it point left (the
   * directionally-correct chevron for RTL). */
  border-inline-start: 5px solid currentColor;
  opacity: 0.55;
  transition: transform 0.15s ease;
}

/* When expanded, rotate so the caret points down. The base rotation
 * (90deg → clockwise) produces a downward chevron in LTR; in RTL the
 * starting caret already points the other way, so we need a different
 * rotation to keep the "expanded = points down" affordance. */
.nav-group-header.expanded .nav-group-chevron {
  transform: rotate(90deg);
}
[dir="rtl"] .nav-group-header.expanded .nav-group-chevron {
  transform: rotate(-90deg);
}

.nav-group-items {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 13px;
  color: var(--muted);
  transition: background 0.15s, color 0.15s;
}

.app-layout.sidebar-collapsed .nav-item {
  justify-content: center;
  padding: 8px;
}

.nav-item:hover {
  background: rgba(255, 255, 255, 0.05);
  color: var(--text);
}

.nav-item.active {
  background: color-mix(in srgb, var(--accent) 15%, transparent);
  color: var(--accent-h);
}

.nav-icon {
  font-size: 15px;
  flex-shrink: 0;
  width: 20px;
  text-align: center;
}

.nav-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sidebar-footer {
  padding: 8px;
  border-top: 1px solid var(--border);
}

.sidebar-user-badge {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  margin-bottom: 4px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  min-width: 0;
}

.app-layout.sidebar-collapsed .sidebar-user-badge {
  justify-content: center;
  padding: 8px;
  gap: 0;
}

.sidebar-user-avatar {
  flex-shrink: 0;
  width: 28px;
  height: 28px;
  border-radius: 50%;
  background: linear-gradient(135deg, var(--accent), var(--accent-h));
  color: white;
  font-size: 12px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  font-family: 'PingFang SC', 'Noto Sans SC', 'Microsoft YaHei', sans-serif;
}

.sidebar-user-info {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 1px;
  flex: 1;
}

.sidebar-user-info .user-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.2;
}

.sidebar-user-info .user-role {
  font-size: 10px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.2;
}

.sidebar-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 10px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--muted);
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}

.app-layout.sidebar-collapsed .sidebar-toggle {
  justify-content: center;
  padding: 8px;
}

.sidebar-toggle:hover {
  background: rgba(255, 255, 255, 0.05);
  color: var(--text);
}

.toggle-icon {
  font-size: 14px;
  line-height: 1;
  flex-shrink: 0;
}

/* The sidebar-collapse caret is a single glyph (« or »). postcss-rtlcss
 * cannot mirror glyphs, so we flip the whole character with scaleX(-1).
 * LTR rendering: « collapsed (drawer stays open) | » collapsed (close it).
 * RTL rendering: the meanings swap because the sidebar sits on the right,
 * so the SAME characters now point the wrong way; mirroring them restores
 * the correct affordance. */
[dir="rtl"] .toggle-icon {
  transform: scaleX(-1);
}
/* Same treatment for the header sidebar toggle button on mobile. */
[dir="rtl"] .header-sidebar-toggle {
  transform: scaleX(-1);
}

.user-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  white-space: nowrap;
}

.user-role {
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
}

.meta-sep {
  color: var(--muted);
  opacity: 0.5;
  user-select: none;
}

.version-tag {
  font-size: 11px;
  font-weight: 600;
  color: var(--accent-h);
  font-family: 'SF Mono', 'Fira Code', monospace;
  white-space: nowrap;
}

.version-build {
  font-size: 11px;
  color: var(--muted);
  font-family: 'SF Mono', 'Fira Code', monospace;
  white-space: nowrap;
}

.main-content {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.main-banner {
  padding: 8px 24px 0;
}

.main-body {
  flex: 1;
  overflow-y: auto;
  padding: 24px;
}

.guest-layout {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  width: 100%;
  background: var(--bg);
}

.guest-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px clamp(20px, 4vw, 48px);
  border-bottom: 1px solid var(--border);
  background: var(--sidebar);
  position: sticky;
  top: 0;
  z-index: 10;
  width: 100%;
  box-sizing: border-box;
}

.guest-nav {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 14px;
  flex: 1;
  min-width: 0;
  margin-inline-start: 12px;
}

.guest-nav a {
  color: var(--muted);
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
  white-space: nowrap;
  transition: color 0.15s ease;
}

.guest-nav a:hover {
  color: var(--accent-h);
}

.guest-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  font-weight: 700;
  color: var(--text);
  text-decoration: none;
  cursor: pointer;
}

.guest-brand-img {
  display: block;
  width: 60px;
  height: 60px;
  object-fit: contain;
  border-radius: 12px;
  flex-shrink: 0;
}

.guest-brand-text {
  font-size: 14px;
  font-weight: 700;
  line-height: 1.35;
  letter-spacing: -0.01em;
  color: var(--text);
  max-width: min(52vw, 420px);
}

.guest-header-right {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  margin-left: auto;
}

.guest-login-btn {
  flex-shrink: 0;
}

.guest-main {
  flex: 1;
  overflow-y: auto;
  width: 100%;
}

@media (max-width: 640px) {
  .main-header {
    padding: 6px 12px;
  }

  .main-body {
    padding: 12px;
  }

  .main-header-right {
    gap: 8px;
  }

  .header-meta {
    padding: 4px 8px;
    gap: 4px;
    font-size: 10px;
  }

  .guest-header {
    padding: 12px 16px;
    flex-wrap: wrap;
  }

  .guest-nav {
    order: 3;
    width: 100%;
    margin: 4px 0 0;
    gap: 8px 12px;
  }
}
</style>
