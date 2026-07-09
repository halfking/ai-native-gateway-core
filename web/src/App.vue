<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { store, clearAll, clearJwt, clearMustChangePasswordFlag, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, markAuthHydrated, setJwtToken, setUserInfo } from './store'
import { logout as apiLogout } from './api/auth'
import { getAuthMe } from './api/admin'
import LoginModal from './components/LoginModal.vue'
import ChangePasswordDialog from './components/ChangePasswordDialog.vue'
import LanguageSelector from './components/LanguageSelector.vue'
import SystemStatusIndicator from './components/SystemStatusIndicator.vue'
import { useLoginModal } from './composables/useLoginModal'
import { useSidebar } from './composables/useSidebar'
import { useNavAccordion } from './composables/useNavAccordion'
import { NAV_GROUPS, NAV_PRIMARY_ITEMS, visibleNavGroups, visibleNavItems, isNavItemActive } from './config/appNav'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { showLoginModal, openLogin, closeLogin } = useLoginModal()
const { collapsed, toggleSidebar } = useSidebar()
const showChangePassword = ref(false)
const passwordSuccessMessage = ref('')
const mustChangePassword = computed(() => !!store.jwtToken && !!store.userInfo?.must_change_password)

// 2026-07-09: isHydrating 防止页面在 auth probe 完成前误判为未登录。
// 与 store.authHydrated 配合：App.vue onMounted 触发 /api/auth/me，settle 后翻为 true。
const isHydrating = computed(() => !store.authHydrated)

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey || store.userInfo))
const isSuperAdmin = computed(() => checkSuperAdmin())
const isPlatformOps = computed(() => checkPlatformOps())
const isTenantPortal = computed(() => !isPlatformOps.value)

onMounted(async () => {
  // 2026-07-09: 始终探测一次 /api/auth/me（无论 store.userInfo 是否已有）。
  // 这修复了「页面渲染时 store 为空，router 弹回首页，cookie auth 永远登不上」
  // 的问题。authHydrated 必须先翻为 true 模板才会切到 app-layout。
  // 流程：
  //   1. userInfo 有 + apiKey/jwtToken 都无 → 可能 cookie 登录（async 探一下）
  //   2. userInfo 无 + apiKey/jwtToken 都无 → 肯定没登录，直接 mark 完
  //   3. 其他情况（apiKey 或 jwtToken 在）→ 已登录，直接 mark
  try {
    if (store.userInfo && !store.jwtToken && !store.apiKey) {
      // 仅有 userInfo 的情况：探一下 cookie
      try {
        const me = await getAuthMe()
        setUserInfo(me)
        setJwtToken('cookie')
      } catch {
        clearJwt()
      }
    } else if (!store.userInfo && !store.jwtToken && !store.apiKey) {
      // 完全没凭据：探一下看 cookie 是不是还活着
      try {
        const me = await getAuthMe()
        setUserInfo(me)
        setJwtToken('cookie')
      } catch {
        // 401 → 真的没登录，userInfo 保持 null
      }
    }
    // else: apiKey 或 jwtToken 至少有一个 → 已知登录
  } finally {
    markAuthHydrated()
  }
})

const navPrimaryItems = computed(() =>
  visibleNavItems(NAV_PRIMARY_ITEMS, {
    isSuperAdmin: isSuperAdmin.value,
    isPlatformOps: isPlatformOps.value,
    isTenantPortal: isTenantPortal.value,
  }),
)

const navGroups = computed(() =>
  visibleNavGroups(NAV_GROUPS, {
    isSuperAdmin: isSuperAdmin.value,
    isPlatformOps: isPlatformOps.value,
    isTenantPortal: isTenantPortal.value,
  }),
)

const { toggleGroup, isGroupExpanded, groupHasActive } = useNavAccordion(
  navGroups,
  computed(() => route.path),
)

const versionInfo = ref<{
  version?: string
  git_sha?: string
  build_date?: string
  build_seq?: number
}>({})

async function loadVersion() {
  if (!isLoggedIn.value) return
  const token = store.jwtToken || store.apiKey
  try {
    const resp = await fetch('/api/system/version', {
      headers: { Authorization: `Bearer ${token}` },
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
    2026-07-09: 三态渲染 — hydrating / logged-in / guest.
    - isHydrating=true 时显示加载中，避免在 /api/auth me 探测完成前误判为未登录
    - 否则按 isLoggedIn 切 app-layout / guest-layout
  -->
  <div v-if="isHydrating" class="auth-loading">
    <div class="auth-loading-spinner" />
    <div class="auth-loading-text">{{ t('login.checking') || '正在检测登录状态…' }}</div>
  </div>
  <div v-else-if="isLoggedIn" class="app-layout" :class="{ 'sidebar-collapsed': collapsed }">
    <aside class="sidebar">
      <div class="sidebar-logo">
        <img
          src="/logo-icon.svg"
          width="28"
          height="28"
          alt="开轩启圭"
          class="sidebar-logo-img"
        />
        <span v-show="!collapsed" class="sidebar-logo-text">LLM Gateway</span>
      </div>

      <nav class="sidebar-nav">
        <div v-if="navPrimaryItems.length" class="nav-primary">
          <RouterLink
            v-for="item in navPrimaryItems"
            :key="item.path + item.label"
            :to="item.path"
            class="nav-item nav-item-primary"
            :class="{ active: isNavItemActive(item.path, route.path, item.exact) }"
            :title="collapsed ? (item.labelKey ? t(item.labelKey) : item.label) : undefined"
          >
            <span class="nav-icon">{{ item.icon }}</span>
            <span v-show="!collapsed" class="nav-label">{{ item.labelKey ? t(item.labelKey) : item.label }}</span>
          </RouterLink>
        </div>

        <section v-for="group in navGroups" :key="group.id" class="nav-group">
          <button
            v-if="!collapsed"
            type="button"
            class="nav-group-header"
            :class="{
              expanded: isGroupExpanded(group.id),
              'has-active': groupHasActive(group.id),
            }"
            :aria-expanded="isGroupExpanded(group.id)"
            @click="toggleGroup(group.id)"
          >
            <span class="nav-group-title">{{ group.labelKey ? t(group.labelKey) : group.label }}</span>
            <span class="nav-group-chevron" aria-hidden="true" />
          </button>
          <div
            v-show="collapsed || isGroupExpanded(group.id)"
            class="nav-group-items"
          >
            <RouterLink
              v-for="item in group.items"
              :key="item.path + item.label"
              :to="item.path"
              class="nav-item"
              :class="{ active: isNavItemActive(item.path, route.path, item.exact) }"
              :title="collapsed ? (item.labelKey ? t(item.labelKey) : item.label) : undefined"
            >
              <span class="nav-icon">{{ item.icon }}</span>
              <span v-show="!collapsed" class="nav-label">{{ item.labelKey ? t(item.labelKey) : item.label }}</span>
            </RouterLink>
          </div>
        </section>
      </nav>

      <div class="sidebar-footer">
        <div v-if="store.userInfo" class="sidebar-user-badge">
          <div class="sidebar-user-avatar" aria-hidden="true">
            {{ (store.userInfo.display_name || store.userInfo.username || '?').charAt(0).toUpperCase() }}
          </div>
          <div v-show="!collapsed" class="sidebar-user-info">
            <span class="user-name">{{ store.userInfo.display_name || store.userInfo.username }}</span>
            <span class="user-role">{{ store.userInfo.role ? t(`app.role.${store.userInfo.role}`) : '' }}</span>
          </div>
        </div>
        <button
          type="button"
          class="sidebar-toggle"
          :title="collapsed ? t('nav.expandSidebar') : t('nav.collapseSidebar')"
          :aria-label="collapsed ? t('nav.expandSidebar') : t('nav.collapseSidebar')"
          @click="toggleSidebar"
        >
          <span class="toggle-icon" aria-hidden="true">{{ collapsed ? '»' : '«' }}</span>
          <span v-show="!collapsed" class="toggle-label">{{ t('nav.collapseSidebar') }}</span>
        </button>
      </div>
    </aside>

    <main class="main-content">
      <header class="main-header">
        <button
          type="button"
          class="header-sidebar-toggle btn btn-ghost btn-sm"
          :title="collapsed ? t('nav.expandSidebar') : t('nav.collapseSidebar')"
          @click="toggleSidebar"
        >
          {{ collapsed ? '»' : '«' }}
        </button>
        <SystemStatusIndicator />
        <div class="main-header-right">
          <div v-if="passwordSuccessMessage" class="alert alert-success header-alert">{{ passwordSuccessMessage }}</div>
          <div class="header-meta">
            <template v-if="store.userInfo">
              <span class="user-name">{{ store.userInfo.display_name || store.userInfo.username }}</span>
              <span class="meta-sep" aria-hidden="true">·</span>
              <span class="user-role">{{ store.userInfo.role ? t(`app.role.${store.userInfo.role}`) : '' }}</span>
            </template>
            <template v-if="versionInfo.version">
              <span v-if="store.userInfo" class="meta-sep" aria-hidden="true">·</span>
              <span class="version-tag">v{{ versionInfo.version }}</span>
              <template v-if="versionInfo.build_seq != null">
                <span class="meta-sep" aria-hidden="true">·</span>
                <span class="version-build">#{{ versionInfo.build_seq }}</span>
              </template>
            </template>
          </div>
          <LanguageSelector />
          <button v-if="store.jwtToken" class="btn btn-ghost btn-sm" @click="openChangePassword">{{ t('login.changePassword') }}</button>
          <button class="btn btn-ghost btn-sm" @click="logout">{{ t('app.logout') }}</button>
        </div>
      </header>
      <section class="main-body">
        <RouterView />
      </section>
    </main>
  </div>
  <div v-else class="guest-layout">
    <header class="guest-header">
      <div class="guest-brand">
        <img
          src="/logo-unified.svg"
          height="28"
          alt="开轩启圭 Qigui · AI-Native LLM Gateway"
          class="guest-brand-img"
        />
      </div>
      <div class="guest-header-right">
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
</template>

<style scoped>
/* 2026-07-09: 首次进入时的 auth 探测加载中状态 */
.auth-loading {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100vh;
  background: var(--bg-card, #161b22);
  color: var(--text-secondary, #8b949e);
  font-size: 13px;
}
.auth-loading-spinner {
  width: 32px;
  height: 32px;
  border: 3px solid var(--border, #30363d);
  border-top-color: var(--accent, #6366f1);
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
  height: 100vh;
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
  gap: 10px;
  padding: 20px 16px 16px;
  font-size: 14px;
  font-weight: 700;
  color: var(--text);
  border-bottom: 1px solid var(--border);
  min-height: 60px;
}

.app-layout.sidebar-collapsed .sidebar-logo {
  justify-content: center;
  padding: 20px 8px 16px;
}

.sidebar-logo-text {
  white-space: nowrap;
  overflow: hidden;
}

.sidebar-logo-img {
  flex-shrink: 0;
  display: block;
}

.sidebar-nav {
  flex: 1;
  padding: 8px 8px 12px;
  overflow-y: auto;
  /* Slim themed scrollbar — the default browser scrollbar is glaringly
   * wide inside the 64px collapsed sidebar. Firefox uses the longhand
   * `scrollbar-*` properties; WebKit/Blink need the pseudo-elements. */
  scrollbar-width: thin;
  scrollbar-color: rgba(99, 102, 241, 0.4) transparent;
}

.sidebar-nav::-webkit-scrollbar {
  width: 4px;
}

.sidebar-nav::-webkit-scrollbar-track {
  background: transparent;
}

.sidebar-nav::-webkit-scrollbar-thumb {
  background: rgba(99, 102, 241, 0.4);
  border-radius: 4px;
  transition: background 0.15s;
}

.sidebar-nav::-webkit-scrollbar-thumb:hover {
  background: rgba(99, 102, 241, 0.75);
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
  background: rgba(99, 102, 241, 0.15);
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
  background: rgba(99, 102, 241, 0.08);
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
  background: linear-gradient(135deg, #6366f1, #8b5cf6);
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

.main-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  min-height: 40px;
  padding: 6px 24px;
  border-bottom: 1px solid var(--border);
  background: var(--sidebar);
  gap: 12px;
}

.header-sidebar-toggle {
  flex-shrink: 0;
  min-width: 32px;
  font-family: inherit;
}

.main-header-right {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  justify-content: flex-end;
  min-width: 0;
  /* Push the cluster to the inline-end of the header in both directions.
   * `margin-inline-start: auto` is the logical equivalent of `margin-left: auto`
   * and works regardless of <html dir>. */
  margin-inline-start: auto;
}

.header-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: rgba(99, 102, 241, 0.08);
  border-radius: 6px;
  font-size: 11px;
  line-height: 1.2;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
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
  background: var(--bg);
}

.guest-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 24px;
  border-bottom: 1px solid var(--border);
  background: var(--sidebar);
  position: sticky;
  top: 0;
  z-index: 10;
}

.guest-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  font-weight: 700;
  color: var(--text);
}

.guest-brand-img {
  display: block;
  height: 28px;
  width: auto;
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
  }
}
</style>
