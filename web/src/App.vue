<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { store, clearAll, clearJwt, clearMustChangePasswordFlag, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, markAuthHydrated, setJwtToken, setUserInfo, authBearer } from './store'
import { logout as apiLogout, login } from './api/auth'
import { getAuthMe } from './api/admin'
import LoginModal from './components/LoginModal.vue'
import ChangePasswordDialog from './components/ChangePasswordDialog.vue'
import LanguageSelector from './components/LanguageSelector.vue'
import SystemStatusIndicator from './components/SystemStatusIndicator.vue'
import SystemHealthBadge from './components/SystemHealthBadge.vue'
import UpgradeBanner from './components/UpgradeBanner.vue'
import GuestHeader from './components/GuestHeader.vue'
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

const PUBLIC_GUEST_PATHS = new Set([
  '/',
  '/login',
  '/forbidden',
  '/activate',
  '/license',
  '/upgrade',
  '/download',
  '/support',
  '/offline-activation',
])

function isGuestPublicRoute(path: string) {
  return PUBLIC_GUEST_PATHS.has(path)
}

onMounted(async () => {
  // 2026-07-10: Auth hydration — probe /api/auth/me if JWT not already in localStorage.
  // If store.jwtToken is already populated (from localStorage), we're authenticated.
  // Otherwise, check if the HttpOnly cookie is still valid (for users who logged in
  // before this JWT-persistence change). The server's /api/auth/me now returns a
  // fresh access_token in the response so we can persist it to localStorage.
  //
  // 2026-07-13: Skip the probe on customer-facing public routes (/activate,
  // /license, /upgrade). The activation wizard must remain reachable without a
  // login, and the API client's 401 handler would otherwise trigger a redirect
  // to /login before App.vue's own redirect-suppression runs.
  const onPublicRoute = route.meta.public === true || isGuestPublicRoute(route.path)
  try {
    if (!onPublicRoute && !store.jwtToken && !store.apiKey) {
      // No JWT in localStorage, no API key — check if cookie is still valid
      try {
        const me = await getAuthMe()
        // Server may return {user, access_token, expires_at} or just user
        const meAny = me as any
        if (meAny?.access_token) {
          setJwtToken(meAny.access_token)
        }
        setUserInfo(meAny?.user ?? me)
      } catch {
        // 401 → no valid cookie either, user is logged out
        clearJwt()
      }
    }
    // else: store.jwtToken or store.apiKey already present → authenticated
  } finally {
    markAuthHydrated()
    if (!isLoggedIn.value && !onPublicRoute) {
      router.replace({ path: '/', query: { login: '1', redirect: route.fullPath } })
    }
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

function formatVersionDisplay(v?: string): string {
  if (!v) return ''
  return v.replace(/^v+/i, '')
}

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
    if (!login || isLoggedIn.value) return
    if (isGuestPublicRoute(route.path) || route.meta.public === true) {
      const q = { ...route.query }
      delete q.login
      router.replace({ path: route.path, query: q, hash: route.hash })
      return
    }
    openLogin()
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

async function handleChangePasswordSuccess(payload?: { oldPassword: string; newPassword: string }) {
  if (mustChangePassword.value) {
    // 强制修改密码（首次登录）流程：注销当前会话 → 重新登录 → 刷新页面。
    // 这是必须的，因为改密后服务端会撤销旧会话 / 旧 token 的有效性，
    // 也避免用户带着一个挂着旧身份的页面继续操作。
    // 重要：clearAll() 之后 store.userInfo 会被清空，所以必须在 clearAll 之前
    // 拿到 username，否则下面的自动重登会因为 store.userInfo 为 null 而跳过。
    const username = store.userInfo?.username ?? ''
    const newPassword = payload?.newPassword ?? ''
    try {
      await apiLogout()
    } catch {
      /* ignore — server may have already invalidated the session */
    }
    clearAll()
    markAuthHydrated()
    // 重新登录以触发 store.jwtToken / store.userInfo.must_change_password=false 重新拉取
    if (newPassword && username) {
      try {
        const resp = await login(username, newPassword)
        const respAny = resp as any
        if (respAny?.access_token) {
          setJwtToken(respAny.access_token)
        }
        if (respAny?.user) {
          setUserInfo(respAny.user)
        }
      } catch {
        // 自动登录失败 → 用户需要手动登录
        passwordSuccessMessage.value = t('login.passwordChangedReLogin')
        router.push('/')
        return
      }
    } else {
      // 没有 username 也没法自动重登，让用户手动登录
      passwordSuccessMessage.value = t('login.passwordChangedReLogin')
      router.push('/')
      return
    }
    passwordSuccessMessage.value = t('login.passwordChangedReloading')
    // 强制刷新整个页面，确保所有 store / i18n / 数据视图都按新身份重建
    window.setTimeout(() => {
      window.location.reload()
    }, 600)
    return
  }
  // 非强制改密：仅清除标志，留在当前页继续使用
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
          src="/logo-icon-dark.png"
          width="36"
          height="36"
          alt="开轩启圭"
          class="sidebar-logo-img"
        />
        <span v-show="!collapsed" class="sidebar-logo-text">{{ $t('app.brand') }}</span>
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
            <span v-if="item.icon" class="nav-icon">{{ item.icon }}</span>
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
              <span v-if="item.icon" class="nav-icon">{{ item.icon }}</span>
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
      <UpgradeBanner />
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
        <SystemHealthBadge />
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
              <span class="version-tag">v{{ formatVersionDisplay(versionInfo.version) }}</span>
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
    <GuestHeader @login="openLogin" />
    <main class="guest-main">
      <UpgradeBanner />
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

}
</style>
