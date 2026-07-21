<script setup lang="ts">
/**
 * AppTopbar — 顶部水平导航 + 二级下拉菜单。
 * 替代原有 sidebar。结构与 ai-native-maintain 的 topbar + OpsShell 同源：
 *   [logo 标题] [主菜单水平条 (group label)] [操作区]
 *   hover/click 展开当前 group 的二级下拉（绝对定位 absolute）。
 *
 * 2026-07-21: 统一三项目导航规范（llm-gateway-go / ai-native-maintain / ai-session-manager）。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import LanguageSelector from '../LanguageSelector.vue'
import ThemeToggle from '../ThemeToggle.vue'
import SystemStatusIndicator from '../SystemStatusIndicator.vue'
import { detectTheme, logoSrc } from '../../theme'
import { SITE_LOGO_SIZE, SITE_TITLE } from '../../config/brand'
import { store, isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps } from '../../store'
import { NAV_GROUPS, NAV_PRIMARY_ITEMS, isNavItemActive, visibleNavGroups, visibleNavItems } from '../../config/appNav'

const { t } = useI18n()
const route = useRoute()

const brandLogo = ref(logoSrc(detectTheme()))
let logoObserver: MutationObserver | null = null

onMounted(() => {
  brandLogo.value = logoSrc(detectTheme())
  logoObserver = new MutationObserver(() => { brandLogo.value = logoSrc(detectTheme()) })
  logoObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  document.addEventListener('click', handleOutside)
})
onBeforeUnmount(() => {
  logoObserver?.disconnect()
  document.removeEventListener('click', handleOutside)
})

const isSuperAdmin = computed(() => checkSuperAdmin())
const isPlatformOps = computed(() => checkPlatformOps())
const isTenantPortal = computed(() => !isPlatformOps.value)

const navPrimaryItems = computed(() => visibleNavItems(NAV_PRIMARY_ITEMS, {
  isSuperAdmin: isSuperAdmin.value,
  isPlatformOps: isPlatformOps.value,
  isTenantPortal: isTenantPortal.value,
}))

const navGroups = computed(() => visibleNavGroups(NAV_GROUPS, {
  isSuperAdmin: isSuperAdmin.value,
  isPlatformOps: isPlatformOps.value,
  isTenantPortal: isTenantPortal.value,
}))

const openGroupId = ref<string | null>(null)

function toggleGroup(id: string) {
  openGroupId.value = openGroupId.value === id ? null : id
}

function closeAll() {
  openGroupId.value = null
}

function leaveGroup(id: string) {
  // Only close when the cursor leaves the group that was opened by hover;
  // click-opened groups stay open until the user clicks again or outside.
  if (openGroupId.value === id) closeAll()
}

function handleOutside(event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (!target) return
  if (target.closest('.app-topbar__nav') || target.closest('.app-topbar__dropdown')) return
  closeAll()
}

function groupActive(id: string): boolean {
  return navGroups.value
    .find((g) => g.id === id)?.items
    .some((it) => isNavItemActive(it.path, route.path, it.exact)) ?? false
}

function navLabel(labelKey: string | undefined, fallback: string): string {
  return labelKey ? t(labelKey) : fallback
}
</script>

<template>
  <header class="app-topbar">
    <router-link to="/" class="app-topbar__brand" :title="SITE_TITLE">
      <img
        :src="brandLogo"
        :width="SITE_LOGO_SIZE"
        :height="SITE_LOGO_SIZE"
        alt="开轩启圭"
        class="app-topbar__brand-img"
      />
      <span class="app-topbar__brand-text">{{ SITE_TITLE }}</span>
    </router-link>

    <nav class="app-topbar__nav" :aria-label="t('nav.mainAria', '主导航')">
      <template v-for="item in navPrimaryItems" :key="item.path + item.label">
        <a
          v-if="item.external"
          :href="item.path"
          class="app-topbar__link app-topbar__link--primary"
          :class="{ active: isNavItemActive(item.path, route.path, item.exact) }"
        >{{ navLabel(item.labelKey, item.label) }}</a>
        <router-link
          v-else
          :to="item.path"
          class="app-topbar__link app-topbar__link--primary"
          :class="{ active: isNavItemActive(item.path, route.path, item.exact) }"
        >{{ navLabel(item.labelKey, item.label) }}</router-link>
      </template>

      <div
        v-for="group in navGroups"
        :key="group.id"
        class="app-topbar__group"
        :class="{ 'app-topbar__group--open': openGroupId === group.id, 'app-topbar__group--active': groupActive(group.id) }"
        @mouseenter="openGroupId = group.id"
        @mouseleave="leaveGroup(group.id)"
      >
        <button
          type="button"
          class="app-topbar__link app-topbar__group-trigger"
          :aria-expanded="openGroupId === group.id"
          @click="toggleGroup(group.id)"
        >
          {{ navLabel(group.labelKey, group.label) }}
          <span class="app-topbar__chevron" aria-hidden="true">▾</span>
        </button>
        <div
          v-show="openGroupId === group.id"
          class="app-topbar__dropdown"
          role="menu"
        >
          <template v-for="item in group.items" :key="item.path + item.label">
            <a
              v-if="item.external"
              :href="item.path"
              class="app-topbar__dropdown-item"
              role="menuitem"
              :target="item.path.startsWith('http') ? '_blank' : undefined"
              :rel="item.path.startsWith('http') ? 'noopener' : undefined"
              @click="closeAll()"
            >
              <span class="app-topbar__dropdown-icon" aria-hidden="true">{{ item.icon }}</span>
              <span>{{ navLabel(item.labelKey, item.label) }}</span>
            </a>
            <router-link
              v-else
              :to="item.path"
              class="app-topbar__dropdown-item"
              :class="{ active: isNavItemActive(item.path, route.path, item.exact) }"
              role="menuitem"
              @click="closeAll()"
            >
              <span class="app-topbar__dropdown-icon" aria-hidden="true">{{ item.icon }}</span>
              <span>{{ navLabel(item.labelKey, item.label) }}</span>
            </router-link>
          </template>
        </div>
      </div>
    </nav>

    <div class="app-topbar__actions">
      <SystemStatusIndicator />
      <slot name="actions" />
      <template v-if="store.userInfo">
        <span class="app-topbar__user-name">{{ store.userInfo.display_name || store.userInfo.username }}</span>
        <span class="app-topbar__user-role">{{ store.userInfo.role ? t(`app.role.${store.userInfo.role}`) : '' }}</span>
      </template>
      <ThemeToggle />
      <LanguageSelector />
    </div>
  </header>
</template>

<style scoped>
.app-topbar {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 10px clamp(16px, 3vw, 32px);
  background: var(--kx-surface-soft, var(--sidebar));
  border-bottom: 1px solid var(--kx-border, var(--border));
  backdrop-filter: blur(14px);
  position: sticky;
  top: 0;
  z-index: 30;
  width: 100%;
  box-sizing: border-box;
  min-height: 64px;
}

.app-topbar__brand {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  text-decoration: none;
  color: var(--kx-text, var(--text));
  flex-shrink: 0;
}

.app-topbar__brand-img {
  width: 36px;
  height: 36px;
  border-radius: 8px;
  object-fit: contain;
  background: var(--kx-surface, var(--card));
  box-shadow: var(--kx-shadow-sm, 0 2px 8px rgba(0, 0, 0, 0.06));
  flex-shrink: 0;
}

.app-topbar__brand-text {
  font-size: 14px;
  font-weight: 700;
  letter-spacing: -0.01em;
  white-space: nowrap;
  max-width: min(40vw, 320px);
  overflow: hidden;
  text-overflow: ellipsis;
}

.app-topbar__nav {
  display: flex;
  align-items: center;
  gap: 4px;
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: thin;
}

.app-topbar__link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 8px 12px;
  border: 0;
  background: transparent;
  color: var(--kx-muted, var(--muted));
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
  border-radius: 8px;
  white-space: nowrap;
  cursor: pointer;
  transition: color 0.15s ease, background 0.15s ease;
  font-family: inherit;
}
.app-topbar__link:hover {
  color: var(--kx-text, var(--text));
  background: var(--kx-primary-soft, var(--bg-subtle));
}
.app-topbar__link.active {
  color: var(--kx-primary, var(--accent));
  background: var(--kx-primary-soft, var(--bg-subtle));
}
.app-topbar__link--primary {
  color: var(--kx-text, var(--text));
  font-weight: 700;
}

.app-topbar__group {
  position: relative;
}
.app-topbar__group-trigger {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.app-topbar__group--active .app-topbar__group-trigger {
  color: var(--kx-primary, var(--accent));
}
.app-topbar__chevron {
  font-size: 10px;
  opacity: 0.7;
  transition: transform 0.15s ease;
}
.app-topbar__group--open .app-topbar__chevron {
  transform: rotate(180deg);
}

.app-topbar__dropdown {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  z-index: 40;
  min-width: 220px;
  padding: 6px;
  margin: 0;
  background: var(--kx-surface, var(--card));
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 12px;
  box-shadow: var(--kx-shadow-md, 0 16px 40px rgba(0, 0, 0, 0.1));
  display: flex;
  flex-direction: column;
  gap: 2px;
  animation: app-topbar-dropdown-enter 0.14s ease both;
}

.app-topbar__dropdown-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--kx-text, var(--text));
  font-size: 13px;
  text-decoration: none;
  white-space: nowrap;
  transition: background 0.15s ease, color 0.15s ease;
  cursor: pointer;
  font-family: inherit;
}
.app-topbar__dropdown-item:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
}
.app-topbar__dropdown-item.active {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
}
.app-topbar__dropdown-icon {
  width: 18px;
  text-align: center;
  font-size: 14px;
  flex-shrink: 0;
}

.app-topbar__actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
  margin-left: auto;
}

.app-topbar__user-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text, var(--text));
  white-space: nowrap;
}

.app-topbar__user-role {
  font-size: 11px;
  color: var(--kx-muted, var(--muted));
  white-space: nowrap;
}

@keyframes app-topbar-dropdown-enter {
  from { opacity: 0; transform: translateY(-4px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 768px) {
  .app-topbar {
    flex-wrap: wrap;
    padding: 10px 14px;
  }
  .app-topbar__nav {
    order: 3;
    width: 100%;
    margin-top: 6px;
  }
  .app-topbar__user-name,
  .app-topbar__user-role {
    display: none;
  }
}
</style>