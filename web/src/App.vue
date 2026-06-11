<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { store, clearApiKey } from './store'

const route  = useRoute()
const router = useRouter()
const isLoggedIn = computed(() => !!store.apiKey)

const versionInfo = ref<{
  version?: string
  git_sha?: string
  build_date?: string
  build_seq?: number
}>({})

onMounted(async () => {
  try {
    const resp = await fetch('/api/system/version', {
      headers: { 'Authorization': `Bearer ${store.apiKey}` },
    })
    if (resp.ok) {
      versionInfo.value = await resp.json()
    }
  } catch {
    // ignore — version display is non-critical
  }
})

const nav = [
  { path: '/',                  label: '仪表盘',  icon: '📊' },
  { path: '/providers',         label: '提供商',   icon: '🔌' },
  { path: '/keys',              label: 'API 密钥', icon: '🔑' },
  { path: '/key-applications',  label: '密钥申请', icon: '📬' },
  { path: '/tenants',           label: '租户管理', icon: '👥' },
  { path: '/catalog',           label: '模型目录',  icon: '📋' },
  { path: '/models',            label: '模型与标签', icon: '🏷️' },
  { path: '/examples',          label: '请求示例',  icon: '📝' },
  { path: '/routing',           label: '路由测试',  icon: '🔍' },
  { path: '/routing-overview',  label: '路由总览',  icon: '🗺️' },
  { path: '/routing-policy',    label: '路由策略',  icon: '⚙️' },
  { path: '/routing-decisions', label: '决策日志',  icon: '📜' },
  { path: '/free-pool',         label: '免费资源',  icon: '🎁' },
  { path: '/request-logs',      label: '请求日志',  icon: '📋' },
  { path: '/pricing',           label: '定价管理',  icon: '💰' },
]

function logout() {
  clearApiKey()
  router.push('/login')
}
</script>

<template>
  <div class="app-layout" v-if="isLoggedIn">
    <aside class="sidebar">
      <div class="sidebar-logo">
        <svg width="24" height="24" viewBox="0 0 32 32" fill="none">
          <circle cx="16" cy="16" r="14" fill="#6366f1"/>
          <text x="16" y="21" text-anchor="middle" font-size="16" fill="white"
            font-family="Arial,sans-serif" font-weight="bold">G</text>
        </svg>
        <span>LLM Gateway</span>
      </div>
      <nav class="sidebar-nav">
        <RouterLink
          v-for="item in nav"
          :key="item.path"
          :to="item.path"
          class="nav-item"
          :class="{ active: route.path === item.path }"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>
      <div class="sidebar-footer">
        <div class="version-info" v-if="versionInfo.version">
          <span class="version-tag">v{{ versionInfo.version }}</span>
          <span class="version-build" v-if="versionInfo.build_seq != null">build #{{ versionInfo.build_seq }}</span>
          <span class="version-date" v-if="versionInfo.build_date">{{ versionInfo.build_date }}</span>
        </div>
        <button class="btn btn-ghost btn-sm" @click="logout">退出登录</button>
      </div>
    </aside>
    <main class="main-content">
      <RouterView />
    </main>
  </div>
  <RouterView v-else />
</template>

<style scoped>
.app-layout {
  display: flex;
  height: 100vh;
  overflow: hidden;
}

.sidebar {
  width: 200px;
  flex-shrink: 0;
  background: var(--sidebar);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
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
}

.sidebar-nav {
  flex: 1;
  padding: 12px 8px;
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
  transition: background .15s, color .15s;
}
.nav-item:hover { background: rgba(255,255,255,.05); color: var(--text); }
.nav-item.active { background: rgba(99,102,241,.15); color: var(--accent-h); }

.nav-icon { font-size: 15px; }

.sidebar-footer {
  padding: 12px;
  border-top: 1px solid var(--border);
}

.version-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-bottom: 8px;
  padding: 6px 8px;
  background: rgba(99, 102, 241, 0.08);
  border-radius: 6px;
}

.version-tag {
  font-size: 11px;
  font-weight: 600;
  color: var(--accent-h);
  font-family: 'SF Mono', 'Fira Code', monospace;
}

.version-build {
  font-size: 10px;
  color: var(--text);
  font-family: 'SF Mono', 'Fira Code', monospace;
  opacity: 0.85;
}

.version-date {
  font-size: 10px;
  color: var(--muted);
}

.main-content {
  flex: 1;
  overflow-y: auto;
  padding: 24px;
}
</style>
