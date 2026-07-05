<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { store, clearAll, clearMustChangePasswordFlag, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps } from './store'
import { logout as apiLogout } from './api/auth'
import LoginModal from './components/LoginModal.vue'
import ChangePasswordDialog from './components/ChangePasswordDialog.vue'
import LanguageSelector from './components/LanguageSelector.vue'
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

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey))
const isSuperAdmin = computed(() => checkSuperAdmin())
const isPlatformOps = computed(() => checkPlatformOps())
const isTenantPortal = computed(() => !isPlatformOps.value)

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
  router.push('/')
}

function openChangePassword() {
  passwordSuccessMessage.value = ''
  showChangePassword.value = true
}

function handleChangePasswordSuccess() {
  clearMustChangePasswordFlag()
  showChangePassword.value = false
  passwordSuccessMessage.value = t('password.changeSuccess')
}
</script>

<template>
  <div class="app-layout" v-if="isLoggedIn" :class="{ 'sidebar-collapsed': collapsed }">
    <aside class="sidebar">
      <div class="sidebar-logo">
        <svg width="24" height="24" viewBox="0 0 32 32" fill="none" aria-hidden="true">
          <circle cx="16" cy="16" r="14" fill="#6366f1" />
          <text
            x="16"
            y="21"
            text-anchor="middle"
            font-size="16"
            fill="white"
            font-family="Arial,sans-serif"
            font-weight="bold"
          >
            G
          </text>
        </svg>
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
        <div class="main-header-right">
          <div v-if="passwordSuccessMessage" class="alert alert-success header-alert">{{ passwordSuccessMessage }}</div>
          <div class="header-meta">
            <template v-if="store.userInfo">
              <span class="user-name">{{ store.userInfo.display_name || store.userInfo.username }}</span>
              <span class="meta-sep" aria-hidden="true">·</span>
              <span class="user-role">{{ store.userInfo.role ? t(`role.${store.userInfo.role}`) : '' }}</span>
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
          <button v-if="store.jwtToken" class="btn btn-ghost btn-sm" @click="openChangePassword">{{ t('changePassword') }}</button>
          <button class="btn btn-ghost btn-sm" @click="logout">{{ t('logout') }}</button>
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
        <svg width="24" height="24" viewBox="0 0 32 32" fill="none" aria-hidden="true">
          <circle cx="16" cy="16" r="14" fill="#6366f1" />
          <text
            x="16"
            y="21"
            text-anchor="middle"
            font-size="16"
            fill="white"
            font-family="Arial,sans-serif"
            font-weight="bold"
          >
            G
          </text>
        </svg>
        <span>LLM Gateway</span>
      </div>
      <div class="guest-header-right">
        <LanguageSelector />
        <button type="button" class="btn btn-primary btn-sm guest-login-btn" @click="openLogin">{{ t('login') }}</button>
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

.sidebar-nav {
  flex: 1;
  padding: 8px 8px 12px;
  overflow-y: auto;
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
