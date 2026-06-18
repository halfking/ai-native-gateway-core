<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { store, clearAll, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps } from './store'
import LoginModal from './components/LoginModal.vue'
import { useLoginModal } from './composables/useLoginModal'
import { useSidebar } from './composables/useSidebar'
import { NAV_GROUPS, visibleNavGroups, isNavItemActive } from './config/appNav'

const route = useRoute()
const router = useRouter()
const { showLoginModal, openLogin, closeLogin } = useLoginModal()
const { collapsed, toggleSidebar } = useSidebar()

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey))
const isSuperAdmin = computed(() => checkSuperAdmin())
const isPlatformOps = computed(() => checkPlatformOps())
const isTenantPortal = computed(() => !isPlatformOps.value)

const navGroups = computed(() =>
  visibleNavGroups(NAV_GROUPS, {
    isSuperAdmin: isSuperAdmin.value,
    isPlatformOps: isPlatformOps.value,
    isTenantPortal: isTenantPortal.value,
  }),
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
  () => route.query.login,
  (login) => {
    if (login && !isLoggedIn.value) openLogin()
  },
  { immediate: true },
)

function logout() {
  clearAll()
  router.push('/')
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
        <section v-for="group in navGroups" :key="group.id" class="nav-group">
          <div v-if="!collapsed" class="nav-group-label">{{ group.label }}</div>
          <RouterLink
            v-for="item in group.items"
            :key="item.path + item.label"
            :to="item.path"
            class="nav-item"
            :class="{ active: isNavItemActive(item.path, route.path) }"
            :title="collapsed ? item.label : undefined"
          >
            <span class="nav-icon">{{ item.icon }}</span>
            <span v-show="!collapsed" class="nav-label">{{ item.label }}</span>
          </RouterLink>
        </section>
      </nav>

      <div class="sidebar-footer">
        <button
          type="button"
          class="sidebar-toggle"
          :title="collapsed ? '展开侧栏' : '收起侧栏'"
          :aria-label="collapsed ? '展开侧栏' : '收起侧栏'"
          @click="toggleSidebar"
        >
          <span class="toggle-icon" aria-hidden="true">{{ collapsed ? '»' : '«' }}</span>
          <span v-show="!collapsed" class="toggle-label">收起菜单</span>
        </button>
      </div>
    </aside>

    <main class="main-content">
      <header class="main-header">
        <button
          type="button"
          class="header-sidebar-toggle btn btn-ghost btn-sm"
          :title="collapsed ? '展开侧栏' : '收起侧栏'"
          @click="toggleSidebar"
        >
          {{ collapsed ? '»' : '«' }}
        </button>
        <div class="main-header-right">
          <div class="header-meta">
            <template v-if="store.userInfo">
              <span class="user-name">{{ store.userInfo.display_name || store.userInfo.username }}</span>
              <span class="meta-sep" aria-hidden="true">·</span>
              <span class="user-role">{{ store.userInfo.role === 'super_admin' ? '超级管理员' : '租户管理员' }}</span>
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
          <button class="btn btn-ghost btn-sm" @click="logout">退出</button>
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
      <button type="button" class="btn btn-primary btn-sm guest-login-btn" @click="openLogin">登录</button>
    </header>
    <main class="guest-main">
      <RouterView />
    </main>
    <LoginModal v-model="showLoginModal" />
  </div>
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
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  transition: width 0.2s ease;
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
  gap: 10px;
  flex-wrap: nowrap;
  justify-content: flex-end;
  min-width: 0;
  margin-left: auto;
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
