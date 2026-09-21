<script setup lang="ts">
/**
 * AppTopbar — 顶部水平导航 + 二级下拉菜单。
 * 替代原有 sidebar。结构与 ai-native-maintain 的 topbar + OpsShell 同源：
 *   [logo 标题] [主菜单水平条 (group label)] [操作区]
 *   hover/click 展开当前 group 的二级下拉（绝对定位 absolute）。
 *
 * 2026-07-21: 统一三项目导航规范（llm-gateway-go / ai-native-maintain / ai-session-manager）。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import LanguageSelector from '../LanguageSelector.vue'
import ThemeToggle from '../ThemeToggle.vue'
import SystemStatusIndicator from '../SystemStatusIndicator.vue'
import UserMenuDropdown from './UserMenuDropdown.vue'
import { detectTheme, logoSrc } from '../../theme'
import { SITE_LOGO_SIZE, SITE_TITLE, SITE_TITLE_LINE_ONE, SITE_TITLE_LINE_TWO } from '../../config/brand'
import { isNavItemActive } from '../../config/appNav'
// 2026-09-13: 菜单构建（静态分组 + 插件/运维中心合并 + 角色过滤）抽到
// useAppNav，与 AppNavDrawer（移动抽屉）共用；行为不变。
import { navDrawerOpen, useAppNav } from '../../composables/useAppNav'
import { useRequestAnomalyBadge } from '../../composables/useRequestAnomalyBadge'
import { useBreakpoint } from '../../composables/useBreakpoint'

export interface VersionInfo {
  version?: string
  git_sha?: string
  build_date?: string
  build_seq?: number
}

const props = withDefaults(defineProps<{
  versionInfo?: VersionInfo | null
}>(), {
  versionInfo: null,
})

const emit = defineEmits<{
  'user-info': []
  'change-password': []
  logout: []
}>()

const { t } = useI18n()
const route = useRoute()

const brandLogo = ref(logoSrc(detectTheme()))
let logoObserver: MutationObserver | null = null

onMounted(() => {
  brandLogo.value = logoSrc(detectTheme())
  logoObserver = new MutationObserver(() => { brandLogo.value = logoSrc(detectTheme()) })
  logoObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  document.addEventListener('click', handleOutside)
  window.addEventListener('resize', onWindowChange)
  window.addEventListener('scroll', onWindowChange, true)
})
onBeforeUnmount(() => {
  logoObserver?.disconnect()
  document.removeEventListener('click', handleOutside)
  window.removeEventListener('resize', onWindowChange)
  window.removeEventListener('scroll', onWindowChange, true)
})

// 菜单数据与激活状态解析来自 useAppNav（共享单例，运维中心/插件菜单
// 的后台加载也由其统一触发，见 composable 内 startBackgroundLoading）。
const { navGroups, navPrimaryResolved, navLabel } = useAppNav()
// 2026-09-21: 「格式异常监控」导航徽标——未解决的请求侧异常计数
// （/format-anomalies 请求错误 tab 数据源），60s 轮询、仅 super_admin。
const { counts: requestAnomalyCounts } = useRequestAnomalyBadge()
const requestAnomalyCount = computed(() => requestAnomalyCounts.value.unresolved)
// <1024 布局决策口径：菜单区替换为汉堡按钮，抽屉导航接管（方案 §4.4）
const { isMobile } = useBreakpoint()

const openGroupId = ref<string | null>(null)
const dropdownStyle = ref<Record<string, string>>({})
const groupTriggerRefs = ref<Record<string, HTMLElement | null>>({})
let leaveTimer: number | null = null

function setGroupTriggerRef(id: string, el: Element | null) {
  if (el instanceof HTMLElement) {
    groupTriggerRefs.value[id] = el
  }
}

function syncDropdownPosition(id: string) {
  const el = groupTriggerRefs.value[id]
  if (!el || typeof window === 'undefined') return
  const rect = el.getBoundingClientRect()
  dropdownStyle.value = {
    position: 'fixed',
    top: `${rect.bottom + 6}px`,
    left: `${Math.max(8, rect.left)}px`,
    zIndex: '1000',
  }
}

function openGroupMenu(id: string) {
  cancelLeave()
  openGroupId.value = id
  nextTick(() => syncDropdownPosition(id))
}

function toggleGroup(id: string) {
  if (openGroupId.value === id) {
    closeAll()
    return
  }
  openGroupMenu(id)
}

function closeAll() {
  openGroupId.value = null
}

function leaveGroup(id: string) {
  if (leaveTimer) clearTimeout(leaveTimer)
  leaveTimer = window.setTimeout(() => {
    if (openGroupId.value === id) closeAll()
    leaveTimer = null
  }, 120)
}

function cancelLeave() {
  if (leaveTimer) {
    clearTimeout(leaveTimer)
    leaveTimer = null
  }
}

function handleOutside(event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (!target) return
  if (target.closest('.app-topbar__nav') || target.closest('.app-topbar__dropdown') || target.closest('.user-menu')) return
  closeAll()
}

function onWindowChange() {
  if (openGroupId.value) syncDropdownPosition(openGroupId.value)
}

watch(openGroupId, (id) => {
  if (id) nextTick(() => syncDropdownPosition(id))
})

const activeGroup = computed(() =>
  navGroups.value.find((g) => g.id === openGroupId.value) ?? null,
)

function groupActive(id: string): boolean {
  return navGroups.value
    .find((g) => g.id === id)?.items
    // items 现在是 { item, resolved } 包装；分组高亮按原始 path 匹配，
    // 因为未激活时 resolved.path 会重定向到激活页，不应点亮所属分组。
    .some(({ item }) => isNavItemActive(item.path, route.path, item.exact)) ?? false
}

</script>

<template>
  <header class="app-topbar">
    <!-- 品牌区跳产品首页（ai-native-maintain /maintain/home），不是总览 /dashboard -->
    <a href="/maintain/home" class="app-topbar__brand" :title="SITE_TITLE">
      <img
        :src="brandLogo"
        :width="SITE_LOGO_SIZE"
        :height="SITE_LOGO_SIZE"
        alt="开轩启圭"
        class="app-topbar__brand-img"
      />
      <span class="app-topbar__brand-text">
        <span>{{ SITE_TITLE_LINE_ONE }}</span>
        <span>{{ SITE_TITLE_LINE_TWO }}</span>
      </span>
    </a>

    <!-- 2026-09-13 方案 §4.4：<1024（isMobile 布局决策口径）菜单区替换为汉堡
         按钮，抽屉导航（AppNavDrawer，App.vue 挂载）接管；>=1024 渲染保持
         原 DOM 结构不变（桌面零回归红线）。 -->
    <button
      v-if="isMobile"
      type="button"
      class="app-topbar__hamburger"
      :aria-label="t('nav.mainAria', '主导航')"
      aria-haspopup="dialog"
      :aria-expanded="navDrawerOpen"
      @click="navDrawerOpen = true"
    >
      <span class="app-topbar__hamburger-bar" aria-hidden="true"></span>
      <span class="app-topbar__hamburger-bar" aria-hidden="true"></span>
      <span class="app-topbar__hamburger-bar" aria-hidden="true"></span>
    </button>

    <nav v-else class="app-topbar__nav" :aria-label="t('nav.mainAria', '主导航')">
      <template v-for="{ item, resolved } in navPrimaryResolved" :key="item.path + item.label">
        <a
          v-if="resolved.external"
          :href="resolved.path"
          class="app-topbar__link app-topbar__link--primary"
          :class="{
            active: isNavItemActive(resolved.path, route.path, item.exact),
            'app-topbar__link--activate': resolved.activateAction,
          }"
        >{{ navLabel(item.labelKey, resolved.activateAction ? t('nav.item.activateAction', '激活') : item.label) }}</a>
        <router-link
          v-else
          :to="resolved.path"
          class="app-topbar__link app-topbar__link--primary"
          :class="{
            active: isNavItemActive(resolved.path, route.path, item.exact),
            'app-topbar__link--activate': resolved.activateAction,
          }"
        >{{ navLabel(item.labelKey, resolved.activateAction ? t('nav.item.activateAction', '激活') : item.label) }}</router-link>
      </template>

      <div
        v-for="group in navGroups"
        :key="group.id"
        class="app-topbar__group"
        :class="{ 'app-topbar__group--open': openGroupId === group.id, 'app-topbar__group--active': groupActive(group.id) }"
        @mouseenter="openGroupMenu(group.id)"
        @mouseleave="leaveGroup(group.id)"
      >
        <button
          type="button"
          class="app-topbar__link app-topbar__group-trigger"
          :ref="(el) => setGroupTriggerRef(group.id, el as Element | null)"
          :aria-expanded="openGroupId === group.id"
          aria-haspopup="menu"
          @click="toggleGroup(group.id)"
        >
          {{ navLabel(group.labelKey, group.label) }}
        </button>
      </div>
    </nav>

    <Teleport to="body">
      <div
        v-if="activeGroup && openGroupId"
        class="app-topbar__dropdown"
        :style="dropdownStyle"
        role="menu"
        @mouseenter="cancelLeave"
        @mouseleave="closeAll()"
      >
        <template v-for="{ item, resolved } in activeGroup.items" :key="item.path + item.label">
          <a
            v-if="resolved.external"
            :href="resolved.path"
            class="app-topbar__dropdown-item"
            :class="{ 'app-topbar__dropdown-item--activate': resolved.activateAction }"
            :title="resolved.activateAction ? t('nav.item.activateActionTip', '点击前往激活本机') : undefined"
            role="menuitem"
            :target="resolved.path.startsWith('http') ? '_blank' : undefined"
            :rel="resolved.path.startsWith('http') ? 'noopener' : undefined"
            @click="closeAll()"
          >
            <span class="app-topbar__dropdown-icon" aria-hidden="true">{{ item.icon }}</span>
            <span>{{ navLabel(item.labelKey, resolved.activateAction ? t('nav.item.activateAction', '激活') : item.label) }}</span>
            <span
              v-if="item.path === '/format-anomalies' && requestAnomalyCount > 0"
              class="app-topbar__item-badge"
              :title="t('nav.badge.formatAnomalies', '未解决的请求侧异常')"
            >{{ requestAnomalyCount > 99 ? '99+' : requestAnomalyCount }}</span>
          </a>
          <router-link
            v-else
            :to="resolved.path"
            class="app-topbar__dropdown-item"
            :class="{
              active: isNavItemActive(resolved.path, route.path, item.exact),
              'app-topbar__dropdown-item--activate': resolved.activateAction,
            }"
            :title="resolved.activateAction ? t('nav.item.activateActionTip', '点击前往激活本机') : undefined"
            role="menuitem"
            @click="closeAll()"
          >
            <span class="app-topbar__dropdown-icon" aria-hidden="true">{{ item.icon }}</span>
            <span>{{ navLabel(item.labelKey, resolved.activateAction ? t('nav.item.activateAction', '激活') : item.label) }}</span>
            <span
              v-if="item.path === '/format-anomalies' && requestAnomalyCount > 0"
              class="app-topbar__item-badge"
              :title="t('nav.badge.formatAnomalies', '未解决的请求侧异常')"
            >{{ requestAnomalyCount > 99 ? '99+' : requestAnomalyCount }}</span>
          </router-link>
        </template>
      </div>
    </Teleport>

    <div class="app-topbar__actions">
      <div class="app-topbar__meta">
        <SystemStatusIndicator />
        <div v-if="props.versionInfo?.version" class="app-topbar__version" :title="props.versionInfo.git_sha">
          <span class="app-topbar__version-tag">{{ props.versionInfo.version }}</span>
          <template v-if="props.versionInfo.build_seq != null">
            <span class="app-topbar__meta-sep" aria-hidden="true">·</span>
            <span class="app-topbar__version-build">#{{ props.versionInfo.build_seq }}</span>
          </template>
        </div>
      </div>
      <ThemeToggle />
      <LanguageSelector />
      <UserMenuDropdown
        @user-info="emit('user-info')"
        @change-password="emit('change-password')"
        @logout="emit('logout')"
      />
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
  /* 必须 visible，否则二级下拉会被 header 裁切 */
  overflow: visible;
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
  box-shadow: var(--kx-shadow-sm);
  flex-shrink: 0;
}

.app-topbar__brand-text {
  display: inline-flex;
  flex-direction: column;
  gap: 1px;
  font-size: 13px;
  font-weight: 700;
  line-height: 1.2;
  letter-spacing: 0;
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
  /* 禁止 overflow:auto — 会裁切绝对定位的二级下拉，导致菜单在窄条内滚动看不见 */
  overflow: visible;
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

/* 2026-09-04: 未激活时「激活」CTA — 强调色 + 加粗，引导用户先完成激活。
   与 .active 状态共用主题 token，保证明暗主题一致。 */
.app-topbar__link--activate {
  color: var(--kx-primary, var(--accent));
  font-weight: 700;
}
.app-topbar__dropdown-item--activate {
  color: var(--kx-primary, var(--accent));
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
.app-topbar__dropdown {
  min-width: 220px;
  max-height: min(70vh, 480px);
  overflow-x: hidden;
  overflow-y: auto;
  padding: 6px;
  margin: 0;
  background: var(--kx-surface, var(--card));
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 12px;
  box-shadow: var(--kx-shadow-md);
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
/* 2026-09-21: 导航项计数徽标（格式异常监控等）。 */
.app-topbar__item-badge {
  margin-left: auto;
  min-width: 20px;
  height: 18px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--danger-bg);
  border: 1px solid var(--danger-bd);
  color: var(--danger-bd);
  font-size: 11px;
  font-weight: 700;
  line-height: 16px;
  text-align: center;
  flex-shrink: 0;
}

.app-topbar__actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
  margin-left: auto;
}

.app-topbar__meta {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  border-radius: 8px;
  background: color-mix(in srgb, var(--accent) 6%, transparent);
  flex-shrink: 0;
}

.app-topbar__version {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  line-height: 1.2;
  white-space: nowrap;
}

.app-topbar__version-tag {
  font-weight: 600;
  color: var(--kx-primary, var(--accent));
  font-family: 'SF Mono', 'Fira Code', monospace;
}

.app-topbar__version-build {
  color: var(--kx-muted, var(--muted));
  font-family: 'SF Mono', 'Fira Code', monospace;
}

.app-topbar__meta-sep {
  color: var(--kx-muted, var(--muted));
  opacity: 0.5;
}

@keyframes app-topbar-dropdown-enter {
  from { opacity: 0; transform: translateY(-4px); }
  to { opacity: 1; transform: translateY(0); }
}

/* 2026-09-13 方案 §4.4：移动壳层汉堡按钮（仅 <1024 由 v-if 渲染，桌面不出现）。
   触摸目标 ≥44px（对齐 responsive-base.css 的移动端兜底口径）。 */
.app-topbar__hamburger {
  display: inline-flex;
  flex-direction: column;
  justify-content: center;
  gap: 4px;
  width: 40px;
  height: 40px;
  padding: 0 9px;
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  flex-shrink: 0;
  font-family: inherit;
}
.app-topbar__hamburger-bar {
  display: block;
  width: 100%;
  height: 2px;
  border-radius: 999px;
  background: var(--kx-text, var(--text));
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
    flex-wrap: nowrap;
    overflow-x: auto;
    overflow-y: hidden;
    scrollbar-width: thin;
    -webkit-overflow-scrolling: touch;
  }
  .app-topbar__link {
    flex: 0 0 auto;
  }
  .app-topbar__version {
    display: none;
  }
  .app-topbar__brand-text {
    font-size: 12px;
  }
}

@media (max-width: 480px) {
  .app-topbar {
    gap: 8px;
  }
  .app-topbar__brand-text,
  .app-topbar__meta {
    display: none;
  }
  .app-topbar__actions {
    gap: 6px;
    min-width: 0;
  }
}
</style>
