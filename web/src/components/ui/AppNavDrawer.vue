<script setup lang="ts">
/**
 * AppNavDrawer — 移动端抽屉导航（方案 §4.4，2026-09-13）。
 *
 * 基于 ui/AppDrawer（direction="right"，inset-inline-end 定位在 dir=rtl
 * 下自动换边）。数据与 AppTopbar 同源（useAppNav），保证桌面下拉与移动
 * 抽屉菜单完全一致（静态分组 + 插件/运维中心动态合并 + 角色过滤）。
 *
 * 交互：分组手风琴（当前路由对应分组默认展开）、激活项高亮（与 topbar
 * 下拉同口径 isNavItemActive(resolved.path)）、点击菜单项路由跳转后自动
 * 收起。宽度 min(80vw, 320px)；ESC/遮罩关闭由 AppDrawer 提供。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppDrawer from './AppDrawer.vue'
import { navDrawerOpen, useAppNav } from '../../composables/useAppNav'
import { useRequestAnomalyBadge } from '../../composables/useRequestAnomalyBadge'
import { isNavItemActive } from '../../config/appNav'

const props = withDefaults(
  defineProps<{
    /**
     * 访客壳静态链接（2026-09-13 P1c）：提供时以扁平列表渲染并跳过登录态
     * useAppNav 菜单，供 App.vue guest-header 的汉堡按钮复用同一抽屉。
     */
    guestLinks?: { labelKey: string; href: string }[]
  }>(),
  { guestLinks: undefined },
)

const { t } = useI18n()
const { route, navGroups, navPrimaryResolved, navLabel } = useAppNav()
// 2026-09-21: 「格式异常监控」未解决请求侧异常徽标（与 AppTopbar 同源）。
const { counts: requestAnomalyCounts } = useRequestAnomalyBadge()
const requestAnomalyCount = computed(() => requestAnomalyCounts.value.unresolved)

type ResolvedItem = { item: { path: string; label: string; icon?: string; exact?: boolean; labelKey?: string }, resolved: { path: string; external?: boolean; activateAction?: boolean } }
type Section = { id: string; flat: boolean; label?: string; labelKey?: string; items: ResolvedItem[] }

const sections = computed<Section[]>(() => {
  if (props.guestLinks) {
    // 访客模式：6 个静态链接扁平列表（<a href> 外链语义）
    return [{
      id: '__guest',
      flat: true,
      items: props.guestLinks.map((l) => ({
        item: { path: l.href, label: t(l.labelKey), icon: '•' },
        resolved: { path: l.href, external: true, activateAction: false },
      })) as unknown as ResolvedItem[],
    }]
  }
  return [
    { id: '__primary', flat: true, items: navPrimaryResolved.value as unknown as ResolvedItem[] },
    ...navGroups.value.map((g) => ({
      id: g.id,
      flat: false,
      label: g.label,
      labelKey: g.labelKey,
      items: g.items as unknown as ResolvedItem[],
    })),
  ]
})

const drawerTitle = computed(() =>
  props.guestLinks ? t('landing.guestNavAria', '产品导航') : t('nav.mainAria', '主导航'),
)

const expanded = ref<Set<string>>(new Set())

function groupActive(items: ResolvedItem[]): boolean {
  return items.some(({ item }) => isNavItemActive(item.path, route.path, item.exact))
}

function ensureActiveGroupsExpanded() {
  const next = new Set(expanded.value)
  for (const s of sections.value) {
    if (!s.flat && s.items.length > 0 && groupActive(s.items)) next.add(s.id)
  }
  expanded.value = next
}

watch(
  () => navDrawerOpen.value,
  (open) => {
    if (open) ensureActiveGroupsExpanded()
  },
  { immediate: true },
)

// 点击菜单项路由跳转后自动收起
watch(
  () => route.path,
  () => {
    if (navDrawerOpen.value) navDrawerOpen.value = false
  },
)

function toggleGroup(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

function itemLabel(item: ResolvedItem): string {
  return navLabel(
    item.resolved.activateAction ? undefined : item.item.labelKey,
    item.resolved.activateAction ? t('nav.item.activateAction', '激活') : item.item.label,
  )
}
</script>

<template>
  <AppDrawer
    v-model="navDrawerOpen"
    direction="right"
    width="min(80vw, 320px)"
    :title="drawerTitle"
  >
    <nav class="app-nav-drawer" :aria-label="t('nav.mainAria', '主导航')">
      <template v-for="section in sections" :key="section.id">
        <button
          v-if="!section.flat"
          type="button"
          class="app-nav-drawer__group-header"
          :class="{ 'app-nav-drawer__group-header--active': groupActive(section.items) }"
          :aria-expanded="expanded.has(section.id)"
          @click="toggleGroup(section.id)"
        >
          <span>{{ navLabel(section.labelKey, section.label ?? '') }}</span>
          <span
            class="app-nav-drawer__chevron"
            :class="{ 'app-nav-drawer__chevron--open': expanded.has(section.id) }"
            aria-hidden="true"
          >▾</span>
        </button>

        <ul v-show="section.flat || expanded.has(section.id)" class="app-nav-drawer__list" :class="{ 'app-nav-drawer__list--sub': !section.flat }">
          <li v-for="{ item, resolved } in section.items" :key="section.id + item.path + item.label">
            <a
              v-if="resolved.external"
              :href="resolved.path"
              class="app-nav-drawer__item"
              :class="{
                active: isNavItemActive(resolved.path, route.path, item.exact),
                'app-nav-drawer__item--activate': resolved.activateAction,
              }"
              :target="resolved.path.startsWith('http') ? '_blank' : undefined"
              :rel="resolved.path.startsWith('http') ? 'noopener' : undefined"
              @click="navDrawerOpen = false"
            >
              <span class="app-nav-drawer__icon" aria-hidden="true">{{ item.icon }}</span>
              <span>{{ itemLabel({ item, resolved }) }}</span>
              <span
                v-if="item.path === '/format-anomalies' && requestAnomalyCount > 0"
                class="app-nav-drawer__badge"
              >{{ requestAnomalyCount > 99 ? '99+' : requestAnomalyCount }}</span>
            </a>
            <router-link
              v-else
              :to="resolved.path"
              class="app-nav-drawer__item"
              :class="{
                active: isNavItemActive(resolved.path, route.path, item.exact),
                'app-nav-drawer__item--activate': resolved.activateAction,
              }"
              @click="navDrawerOpen = false"
            >
              <span class="app-nav-drawer__icon" aria-hidden="true">{{ item.icon }}</span>
              <span>{{ itemLabel({ item, resolved }) }}</span>
              <span
                v-if="item.path === '/format-anomalies' && requestAnomalyCount > 0"
                class="app-nav-drawer__badge"
              >{{ requestAnomalyCount > 99 ? '99+' : requestAnomalyCount }}</span>
            </router-link>
          </li>
        </ul>
      </template>
    </nav>
  </AppDrawer>
</template>

<style scoped>
.app-nav-drawer {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  margin: calc(-1 * var(--kx-space-2));
}

.app-nav-drawer__group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--kx-space-2);
  padding: 10px var(--kx-space-2);
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--kx-muted, var(--muted));
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  cursor: pointer;
  font-family: inherit;
  transition: background 0.15s ease, color 0.15s ease;
}
.app-nav-drawer__group-header:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-text, var(--text));
}
.app-nav-drawer__group-header--active {
  color: var(--kx-primary, var(--accent));
}

.app-nav-drawer__chevron {
  transition: transform 0.15s ease;
}
.app-nav-drawer__chevron--open {
  transform: rotate(180deg);
}

.app-nav-drawer__list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.app-nav-drawer__list--sub {
  padding: 0 var(--kx-space-1) var(--kx-space-1);
}

.app-nav-drawer__item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px var(--kx-space-2);
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--kx-text, var(--text));
  font-size: 13.5px;
  text-decoration: none;
  cursor: pointer;
  font-family: inherit;
  transition: background 0.15s ease, color 0.15s ease;
}
/* 2026-09-21: 导航项计数徽标（格式异常监控等）。 */
.app-nav-drawer__badge {
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
.app-nav-drawer__item:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
}
.app-nav-drawer__item.active {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
  font-weight: 600;
}
.app-nav-drawer__item--activate {
  color: var(--kx-primary, var(--accent));
  font-weight: 700;
}

.app-nav-drawer__icon {
  width: 20px;
  text-align: center;
  font-size: 14px;
  flex-shrink: 0;
}
</style>
