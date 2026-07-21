<script setup lang="ts">
// 2026-07-21: 登录态的顶部水平 topbar（替代原侧栏 sidebar）。
// - 左：品牌（logo + 标题，logo 跳首页 /）
// - 中：水平菜单（NAV_PRIMARY + NAV_GROUPS 经 mergeNav 扁平化）
// - 右：ThemeToggle + LanguageSelector + slot #actions（登录态业务按钮）
// 二级菜单：每个 group 的 items 用 <details>/hover-popup 显示。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import LanguageSelector from '../LanguageSelector.vue'
import ThemeToggle from '../ThemeToggle.vue'
import { detectTheme, logoSrc } from '../../theme'
import { SITE_LOGO_SIZE, SITE_TITLE } from '../../config/brand'
import { NAV_GROUPS, NAV_PRIMARY_ITEMS, isNavItemActive, mergeNav } from '../../config/appNav'
import { store, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, isDefaultTenant } from '../../store'
import { usePluginNav } from '../../composables/usePluginNav'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const brandLogo = ref(logoSrc(detectTheme()))
let themeObserver: MutationObserver | null = null

const { pluginNav, reload: reloadPluginNav } = usePluginNav()

const isSuperAdmin = computed(() => checkSuperAdmin())
const isPlatformOps = computed(() => checkPlatformOps())
const isTenantPortal = computed(() => !isPlatformOps.value)

const topbarGroups = computed(() =>
  mergeNav(NAV_PRIMARY_ITEMS, NAV_GROUPS, {
    isSuperAdmin: isSuperAdmin.value,
    isPlatformOps: isPlatformOps.value,
    isTenantPortal: isTenantPortal.value,
  }),
)

// "总览" 组隐藏：用户已经在 dashboard 页面时，标题列在 topbar 就够冗余。
// 把 NAV_PRIMARY_ITEMS（"/dashboard"）的组直接拍平成"首页"快捷链接。
const flatPrimary = computed(() =>
  NAV_PRIMARY_ITEMS.filter((it) =>
    !(it.platformOps && !isPlatformOps.value) && !(it.super && !isSuperAdmin.value),
  ),
)

onMounted(() => {
  brandLogo.value = logoSrc(detectTheme())
  themeObserver = new MutationObserver(() => {
    brandLogo.value = logoSrc(detectTheme())
  })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  reloadPluginNav().catch(() => { /* ignore — 插件菜单拉取失败不影响主壳 */ })
})

onBeforeUnmount(() => themeObserver?.disconnect())

function goHome(e: MouseEvent) {
  e.preventDefault()
  if (route.path === '/' || route.path === '/dashboard') {
    // 已在首页 → 软刷新（router.go(0) 不会丢失 in-memory state）
    return
  }
  router.push('/dashboard').catch(() => { /* duplicate nav */ })
}

function activeClass(path: string, exact?: boolean) {
  return isNavItemActive(path, route.path, exact) ? 'is-active' : ''
}

// "总览"快捷链接（导航栏最左）
const primaryShortcuts = computed(() =>
  flatPrimary.value.map((it) => ({
    path: it.path,
    label: it.labelKey ? t(it.labelKey, it.label) : it.label,
    icon: it.icon,
    exact: it.exact,
  })),
)
</script>

<template>
  <header class="app-topbar">
    <div class="topbar-left">
      <a class="brand" href="/" :title="SITE_TITLE" @click="goHome">
        <img
          class="brand-logo"
          :src="brandLogo"
          :width="SITE_LOGO_SIZE"
          :height="SITE_LOGO_SIZE"
          :alt="SITE_TITLE"
        />
        <span class="brand-title">{{ SITE_TITLE }}</span>
      </a>
    </div>

    <nav class="topnav" :aria-label="t('nav.mainAria', '主导航')">
      <!-- 总览快捷链接（扁平） -->
      <a
        v-for="p in primaryShortcuts"
        :key="p.path"
        :href="p.path"
        class="topnav-link topnav-link--primary"
        :class="activeClass(p.path, p.exact)"
        @click.prevent="router.push(p.path)"
      >
        <span v-if="p.icon" class="topnav-icon" aria-hidden="true">{{ p.icon }}</span>
        <span>{{ p.label }}</span>
      </a>

      <!-- 分组：每个 group 一个顶层按钮 + 二级 dropdown -->
      <div
        v-for="group in topbarGroups.filter(g => g.id !== 'primary')"
        :key="group.id"
        class="topnav-group"
      >
        <a
          v-if="group.primaryPath"
          :href="group.primaryPath"
          class="topnav-link topnav-link--group"
          :class="activeClass(group.primaryPath)"
          @click.prevent="router.push(group.primaryPath)"
        >{{ group.labelKey ? t(group.labelKey, group.label) : group.label }}</a>
        <details v-else class="topnav-group-details">
          <summary class="topnav-link topnav-link--group">
            <span>{{ group.labelKey ? t(group.labelKey, group.label) : group.label }}</span>
            <span class="topnav-chevron" aria-hidden="true">▾</span>
          </summary>
          <ul class="topnav-dropdown" role="menu">
            <li v-for="item in group.items" :key="item.path + item.label">
              <a
                v-if="item.external"
                :href="item.path"
                class="topnav-dropdown-link"
                role="menuitem"
              >
                <span v-if="item.icon" class="topnav-icon" aria-hidden="true">{{ item.icon }}</span>
                <span>{{ item.labelKey ? t(item.labelKey, item.label) : item.label }}</span>
              </a>
              <a
                v-else
                :href="item.path"
                class="topnav-dropdown-link"
                :class="activeClass(item.path, item.exact)"
                role="menuitem"
                @click.prevent="router.push(item.path)"
              >
                <span v-if="item.icon" class="topnav-icon" aria-hidden="true">{{ item.icon }}</span>
                <span>{{ item.labelKey ? t(item.labelKey, item.label) : item.label }}</span>
              </a>
            </li>
          </ul>
        </details>
      </div>

      <!-- 插件菜单（来自 usePluginNav） -->
      <a
        v-for="entry in pluginNav"
        :key="entry.route_url + entry.label_key"
        :href="entry.route_url"
        class="topnav-link"
        :class="{ 'is-active': route.path.startsWith(entry.route_url) }"
      >
        {{ t(entry.label_key) }}
      </a>
    </nav>

    <div class="topbar-right">
      <span v-if="store.userInfo" class="topbar-user">
        <span class="topbar-user-name">{{ store.userInfo.display_name || store.userInfo.username }}</span>
        <span v-if="store.userInfo.role" class="topbar-user-role">{{ t(`app.role.${store.userInfo.role}`) }}</span>
      </span>
      <ThemeToggle />
      <LanguageSelector />
      <slot name="actions" />
    </div>
  </header>
</template>

<style scoped>
.app-topbar {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 10px 24px;
  background: var(--kx-surface-soft, var(--card));
  border-bottom: 1px solid var(--kx-border, var(--border));
  backdrop-filter: blur(12px);
  position: sticky;
  top: 0;
  z-index: 100;
  min-height: 64px;
}
.topbar-left { display: inline-flex; align-items: center; flex-shrink: 0; }
.brand {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  text-decoration: none;
  color: var(--kx-text, var(--text));
}
.brand-logo {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  object-fit: contain;
  background: var(--kx-surface, var(--card));
}
.brand-title {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: -0.01em;
  line-height: 1.3;
  white-space: nowrap;
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.topnav {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex: 1 1 auto;
  flex-wrap: wrap;
  min-width: 0;
}
.topnav-link,
.topnav-group-details > summary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 12px;
  border-radius: 6px;
  font-size: 13px;
  color: var(--kx-muted, var(--muted));
  text-decoration: none;
  cursor: pointer;
  user-select: none;
  transition: background 0.15s ease, color 0.15s ease;
}
.topnav-link:hover,
.topnav-group-details[open] > summary,
.topnav-group-details > summary:hover {
  color: var(--kx-primary, var(--accent));
  background: var(--kx-primary-soft, var(--bg-subtle));
}
.topnav-link.is-active {
  color: var(--kx-primary, var(--accent));
  background: var(--kx-primary-soft, var(--bg-subtle));
  font-weight: 600;
}
.topnav-link--primary { font-weight: 600; }
.topnav-icon { font-size: 13px; line-height: 1; }

.topnav-group { position: relative; }
.topnav-group-details { position: relative; }
.topnav-group-details > summary { list-style: none; }
.topnav-group-details > summary::-webkit-details-marker { display: none; }
.topnav-chevron { font-size: 10px; opacity: 0.7; margin-left: 2px; }
.topnav-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  min-width: 200px;
  margin: 0;
  padding: 6px;
  background: var(--kx-surface, var(--card));
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 8px;
  box-shadow: var(--kx-shadow-md, 0 8px 24px rgba(0,0,0,0.15));
  list-style: none;
  z-index: 110;
  max-height: 70vh;
  overflow-y: auto;
}
.topnav-dropdown-link {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 13px;
  color: var(--kx-text, var(--text));
  text-decoration: none;
  white-space: nowrap;
  transition: background 0.15s ease;
}
.topnav-dropdown-link:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
}
.topnav-dropdown-link.is-active {
  color: var(--kx-primary, var(--accent));
  background: var(--kx-primary-soft, var(--bg-subtle));
  font-weight: 600;
}

.topbar-right {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.topbar-user {
  display: inline-flex;
  flex-direction: column;
  line-height: 1.2;
  margin-right: 4px;
}
.topbar-user-name { font-size: 12px; font-weight: 600; color: var(--kx-text, var(--text)); }
.topbar-user-role { font-size: 10px; color: var(--kx-muted, var(--muted)); }

@media (max-width: 720px) {
  .app-topbar { flex-wrap: wrap; padding: 8px 14px; }
  .topnav { order: 3; flex-basis: 100%; }
  .brand-title { max-width: 140px; }
}
</style>
