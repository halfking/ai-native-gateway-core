/**
 * useAppNav — 应用导航数据 composable（方案 §4.4，2026-09-13）。
 *
 * 把 AppTopbar 内的菜单构建逻辑（静态分组 + 插件菜单合并 + 运维中心远端
 * 菜单合并 + 角色过滤 + 激活状态解析）抽成共享 composable，供 AppTopbar
 * （桌面下拉）与 AppNavDrawer（移动抽屉）共用，逻辑不改行为。
 *
 * 背景数据（激活状态 / 运维中心菜单 / 插件菜单）为模块级单例，多处挂载
 * 只拉取一次；维护可用性变化监听也只注册一份。
 *
 * navDrawerOpen：移动导航抽屉的共享开关，AppTopbar 汉堡按钮打开、
 * App.vue 挂载的 AppNavDrawer 消费。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import {
  NAV_GROUPS,
  NAV_PRIMARY_ITEMS,
  visibleNavGroups,
  visibleNavItems,
  mergeRemotePluginNav,
  resolveNavItemActivation,
  type NavGroup,
} from '../config/appNav'
import { usePluginNav } from './usePluginNav'
import {
  LOCAL_OPS_MENU,
  onMaintainAvailabilityChange,
  resolveOpsMenu,
  type OpsMenuGroup,
} from '../config/edition'
import { isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, isProviderConsoleView as checkProviderConsole } from '../store'

/** 移动导航抽屉共享开关（AppTopbar 汉堡 ⇄ App.vue 的 AppNavDrawer） */
export const navDrawerOpen = ref(false)

// —— 模块级共享背景状态（多次 useAppNav() 只加载一次） ——
const isActivated = ref(false)
const opsMenuOverrides = ref<OpsMenuGroup[] | null>(null)
let backgroundStarted = false
let stopMaintainWatch: (() => void) | null = null

async function checkActivationStatus() {
  try {
    const resp = await fetch('/api/system/bootstrap/status')
    if (resp.ok) {
      const data = await resp.json()
      isActivated.value = data.activated === true
    }
  } catch {
    // 忽略错误，默认未激活
  }
}

async function refreshOpsMenu() {
  try {
    opsMenuOverrides.value = await resolveOpsMenu()
  } catch {
    opsMenuOverrides.value = LOCAL_OPS_MENU
  }
}

function startBackgroundLoading() {
  if (backgroundStarted) return
  backgroundStarted = true
  void checkActivationStatus()
  void refreshOpsMenu()
  stopMaintainWatch = onMaintainAvailabilityChange(() => {
    void refreshOpsMenu()
  })
}

/** 仅供测试：复位单例背景状态与共享开关。 */
export function _resetUseAppNavForTests() {
  backgroundStarted = false
  stopMaintainWatch?.()
  stopMaintainWatch = null
  isActivated.value = false
  opsMenuOverrides.value = null
  navDrawerOpen.value = false
}

export function mergeRemoteOps(localGroups: NavGroup[], remote: OpsMenuGroup[] | null): NavGroup[] {
  if (!remote || !remote.length) return localGroups
  const merged: NavGroup[] = []
  for (const g of localGroups) {
    if (g.id === 'opsplatform') continue
    merged.push(g)
  }
  for (const g of remote) {
    merged.push({
      id: g.id,
      label: g.label,
      items: g.items.map((it) => ({
        path: it.path,
        label: it.label,
        labelKey: it.labelKey,
        icon: it.icon || '•',
        super: it.super,
        hideForTenant: it.hide_for_tenant,
        external: it.external,
        exact: false,
      })),
    })
  }
  return merged
}

export function useAppNav() {
  const { t } = useI18n()
  const route = useRoute()
  startBackgroundLoading()

  const isSuperAdmin = computed(() => checkSuperAdmin())
  const isPlatformOps = computed(() => checkPlatformOps())
  // 2026-09-04: 供应商控制台（super_admin 或 default 租户 tenant_admin）
  const isProviderConsole = computed(() => checkProviderConsole())
  const isTenantPortal = computed(() => !isPlatformOps.value)

  const visibilityOpts = computed(() => ({
    isSuperAdmin: isSuperAdmin.value,
    isPlatformOps: isPlatformOps.value,
    isTenantPortal: isTenantPortal.value,
    isProviderConsole: isProviderConsole.value,
    isActivated: isActivated.value,
  }))

  const navPrimaryItems = computed(() => visibleNavItems(NAV_PRIMARY_ITEMS, visibilityOpts.value))

  // 2026-09-02: 在渲染前一次性把每个菜单项解析成最终 path / 是否为激活动作,
  // 避免模板内反复调用 resolveNavItemActivation。
  const navPrimaryResolved = computed(() =>
    navPrimaryItems.value.map((item) => ({
      item,
      resolved: resolveNavItemActivation(item, { isActivated: isActivated.value }),
    })),
  )

  // V5.1: dynamic plugin nav (plugin-runtime /api/v1/plugin-nav). The merge
  // step runs after the opsPlatform override so a plugin can't displace the
  // maintain nav, but plugin entries land inside the matching static group
  // (e.g. 'requests-sessions') or in a synthetic 'plugins' group if no match.
  const { pluginNav } = usePluginNav()

  const navGroups = computed(() => {
    let local = visibleNavGroups(NAV_GROUPS, visibilityOpts.value)
    local = mergeRemotePluginNav(local, pluginNav.value)
    const base = opsMenuOverrides.value ? mergeRemoteOps(local, opsMenuOverrides.value) : local
    // 2026-09-02: 每个 item 都附带上「按激活状态解析后的 path / 是否为激活动作」,
    // 模板直接读 resolved.path 渲染即可。
    return base.map((g) => ({
      ...g,
      items: g.items.map((item) => ({
        item,
        resolved: resolveNavItemActivation(item, { isActivated: isActivated.value }),
      })),
    }))
  })

  function navLabel(labelKey: string | undefined, fallback: string): string {
    return labelKey ? t(labelKey) : fallback
  }

  return {
    route,
    isSuperAdmin,
    isPlatformOps,
    isTenantPortal,
    isProviderConsole,
    isActivated,
    navPrimaryResolved,
    navGroups,
    navLabel,
    pluginNav,
  }
}
