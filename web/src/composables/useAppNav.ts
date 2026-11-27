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
import { throttledFetch } from '../api/_core'
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
import { buildSimpleNav } from '../config/simpleNav'
import {
  onPIAnalysisAvailabilityChange,
  probePIAnalysisAvailable,
  showPIAnalysis,
} from '../config/piAnalysis'
import { usePluginNav } from './usePluginNav'
import { useWorkMode } from './useWorkMode'
import {
  LOCAL_OPS_MENU,
  onMaintainAvailabilityChange,
  resolveOpsMenu,
  type OpsMenuGroup,
} from '../config/edition'
import { isHiddenNavPath } from '../config/navHidden'
import { isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps, isProviderConsoleView as checkProviderConsole } from '../store'

/** 移动导航抽屉共享开关（AppTopbar 汉堡 ⇄ App.vue 的 AppNavDrawer） */
export const navDrawerOpen = ref(false)

// —— 模块级共享背景状态（多次 useAppNav() 只加载一次） ——
const isActivated = ref(false)
const opsMenuOverrides = ref<OpsMenuGroup[] | null>(null)
// ★ 2026-11-18 审计轮 P2：PI 分析（/admin/pi-analysis）的运行时可用性。
//	该页背后的 API 只在「有 DB + admin 鉴权材料齐备」时才注册（后端
//	fail-closed）⇒ 无 DB 的部署上菜单项点了就是 404 空页 ⇒ 探测到
//	不可用就隐藏该项。unknown（未探测）⇒ 展示（避免菜单闪烁）。
const piAnalysisAvailable = ref<boolean | undefined>(undefined)
let backgroundStarted = false
let platformModeRefreshStarted = false
let stopMaintainWatch: (() => void) | null = null
let stopPIAnalysisWatch: (() => void) | null = null

async function checkActivationStatus() {
  try {
    const resp = await throttledFetch('/api/system/bootstrap/status')
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
  // ★ 2026-11-18 审计轮 P2：探测 PI 分析 API 可用性 ⇒ 不可用则隐藏菜单项。
  void probePIAnalysisAvailable()
  stopPIAnalysisWatch = onPIAnalysisAvailabilityChange(() => {
    piAnalysisAvailable.value = showPIAnalysis() ? true : false
  })
}

/** 仅供测试：复位单例背景状态与共享开关。 */
export function _resetUseAppNavForTests() {
  backgroundStarted = false
  platformModeRefreshStarted = false
  stopMaintainWatch?.()
  stopMaintainWatch = null
  stopPIAnalysisWatch?.()
  stopPIAnalysisWatch = null
  isActivated.value = false
  opsMenuOverrides.value = null
  piAnalysisAvailable.value = undefined
  navDrawerOpen.value = false
}

export function mergeRemoteOps(localGroups: NavGroup[], remote: OpsMenuGroup[] | null): NavGroup[] {
  if (!remote || !remote.length) return localGroups
  // ★ 2026-11-27 非管理员菜单重构轮（owner 指令「非管理员不需要运维中心」）：
  //   远端运维菜单是 refreshOpsMenu **后台无凭据拉取**的整组管理面数据——
  //   它对所有角色拉取，合并与否是纯前端决策 ⇒ 非管理视角必须**整组不合并**
  //   （本地 opsplatform 条目已带 management 标记走 canShowNavItem 裁剪；
  //   远端条目没有本地 NavItem 可挂标记，只能在合并入口处按视角拦截）。
  //   ❌ 不能只靠 isHiddenNavPath 逐条筛：那只藏单条路径，藏不住整组。
  if (!checkPlatformOps()) return localGroups
  const merged: NavGroup[] = []
  for (const g of localGroups) {
    if (g.id === 'opsplatform') continue
    merged.push(g)
  }
  for (const g of remote) {
    // ★ 2026-11-23 owner 指令：远端运维中心菜单的条目由后端 JSON 构造
    //   （没有本地 NavItem 可挂标记）⇒ 隐藏只能按**路径**执行。
    //   线上实测该组含 `/maintain/ops/donations`（赞助与捐赠）⇒ 在此剔除。
    //   剔除后整组为空则不渲染（避免留下一个空下拉）。
    const items = g.items
      .filter((it) => !isHiddenNavPath(it.path))
      .map((it) => ({
        path: it.path,
        label: it.label,
        labelKey: it.labelKey,
        icon: it.icon || '•',
        super: it.super,
        hideForTenant: it.hide_for_tenant,
        external: it.external,
        exact: false,
      }))
    if (items.length === 0) continue
    merged.push({
      id: g.id,
      label: g.label,
      labelKey: g.labelKey,
      items,
    })
  }
  return merged
}

export function useAppNav() {
  const { t } = useI18n()
  const route = useRoute()
  startBackgroundLoading()

  // 2026-11-17：拉一次平台默认 gateway.mode，否则本机无覆盖时永远落 expert。
  const { isSimple, refreshPlatformDefault } = useWorkMode()
  if (!platformModeRefreshStarted) {
    platformModeRefreshStarted = true
    void refreshPlatformDefault()
  }

  const isSuperAdmin = computed(() => checkSuperAdmin())
  const isPlatformOps = computed(() => checkPlatformOps())
  // 2026-09-04: 供应商控制台（super_admin 或 default 租户 tenant_admin）
  const isProviderConsole = computed(() => checkProviderConsole())
  const isTenantPortal = computed(() => !isPlatformOps.value)
  // ★ 2026-11-27 非管理员菜单重构轮：管理视角 = isPlatformOps
  //   （super_admin ∧ default 租户）。owner 需求原文把 tenant_admin 明确列为
  //   「非系统管理员」被裁剪对象 ⇒ tenant_admin **不**进管理视角
  //   （maintain-web navLoader 同轮已对齐该口径）。
  //   非管理视角（tenant_admin / user / 非 default 租户门户）⇒ management
  //   条目（运营工具/运维中心/管理设置）全部隐藏；用户面（userFacing /
  //   无标记）条目不受影响。
  const isManagementView = computed(() => isPlatformOps.value)

  const visibilityOpts = computed(() => ({
    isSuperAdmin: isSuperAdmin.value,
    isPlatformOps: isPlatformOps.value,
    isTenantPortal: isTenantPortal.value,
    isProviderConsole: isProviderConsole.value,
    isActivated: isActivated.value,
    piAnalysisAvailable: piAnalysisAvailable.value,
    isManagementView: isManagementView.value,
  }))

  // V5.1: dynamic plugin nav (plugin-runtime /api/v1/plugin-nav). The merge
  // step runs after the opsPlatform override so a plugin can't displace the
  // maintain nav, but plugin entries land inside the matching static group
  // (e.g. 'requests-sessions') or in a synthetic 'plugins' group if no match.
  const { pluginNav } = usePluginNav()

  const expertPrimary = computed(() => visibleNavItems(NAV_PRIMARY_ITEMS, visibilityOpts.value))
  const expertGroups = computed(() => {
    let local = visibleNavGroups(NAV_GROUPS, visibilityOpts.value)
    local = mergeRemotePluginNav(local, pluginNav.value)
    return opsMenuOverrides.value ? mergeRemoteOps(local, opsMenuOverrides.value) : local
  })

  const projected = computed(() => {
    if (!isSimple.value) {
      return { direct: expertPrimary.value, groups: expertGroups.value }
    }
    return buildSimpleNav(expertPrimary.value, expertGroups.value)
  })

  // 2026-09-02: 在渲染前一次性把每个菜单项解析成最终 path / 是否为激活动作,
  // 避免模板内反复调用 resolveNavItemActivation。
  const navPrimaryResolved = computed(() =>
    projected.value.direct.map((item) => ({
      item,
      resolved: resolveNavItemActivation(item, { isActivated: isActivated.value }),
    })),
  )

  const navGroups = computed(() =>
    projected.value.groups.map((g) => ({
      ...g,
      items: g.items.map((item) => ({
        item,
        resolved: resolveNavItemActivation(item, { isActivated: isActivated.value }),
      })),
    })),
  )

  /**
   * 菜单项标签的**唯一**解析口：优先用 i18n 键（语言包是唯一真值），
   * 没有键时才退回调用方给的兜底字符串。
   *
   * ★ 2026-10-27（第 10 增量）：`NavItem.label` 已从必填改为可选 ——
   *   静态菜单全部只带 `labelKey`，代码里不再留一份中文副本；
   *   `fallback` 因此允许为空（空串兜底，而不是渲染出 "undefined"）。
   */
  function navLabel(labelKey: string | undefined, fallback?: string): string {
    return labelKey ? t(labelKey) : (fallback ?? '')
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
