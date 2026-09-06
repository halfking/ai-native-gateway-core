import { createRouter, createWebHistory } from 'vue-router'
import { store, isDefaultTenant } from './store'
import { showOpsPlatform } from './config/edition'

// Critical views loaded immediately (login, home, layout)
import LoginView from './views/LoginView.vue'
import HomeView from './views/HomeView.vue'
import ForbiddenView from './views/ForbiddenView.vue'

// All other views are lazy-loaded to reduce initial bundle size
const MaintainUnavailableView = () => import('./views/MaintainUnavailableView.vue')
const ProvidersView = () => import('./views/ProvidersView.vue')
const KeysView = () => import('./views/KeysView.vue')
const KeyDetailView = () => import('./views/KeyDetailView.vue')
const KeyApplicationsView = () => import('./views/KeyApplicationsView.vue')
const ExamplesView = () => import('./views/ExamplesView.vue')
const ChatView = () => import('./views/ChatView.vue')
const RoutingOverviewView = () => import('./views/RoutingOverviewView.vue')
const RoutingPolicyView = () => import('./views/RoutingPolicyView.vue')
const DecisionsView = () => import('./views/DecisionsView.vue')
const CorrelationsView = () => import('./views/CorrelationsView.vue')
const RoutingOverrideView = () => import('./views/RoutingOverrideView.vue')
const QualityCorrelationsView = () => import('./views/QualityCorrelationsView.vue')
const RoutingAuditView = () => import('./views/RoutingAuditView.vue')
const RequestLogsView = () => import('./views/RequestLogsView.vue')
const RequestDetailFullscreenView = () => import('./views/RequestDetailFullscreenView.vue')
const DispatchWaterfallView = () => import('./views/DispatchWaterfallView.vue')
const ModelsView = () => import('./views/ModelsView.vue')
const ProviderDetailView = () => import('./views/ProviderDetailView.vue')
const PricingManagementView = () => import('./views/PricingManagementView.vue')
const StandardModelPricingView = () => import('./views/StandardModelPricingView.vue')
const FreePoolView = () => import('./views/FreePoolView.vue')
const TenantsView = () => import('./views/TenantsView.vue')
const TenantDetailView = () => import('./views/TenantDetailView.vue')
const RoutingDashboardView = () => import('./views/RoutingDashboardView.vue')
const WorkTypesView = () => import('./views/WorkTypesView.vue')
const UsersView = () => import('./views/UsersView.vue')
const AuditLogView = () => import('./views/AuditLogView.vue')
const CompressionView = () => import('./views/CompressionView.vue')
const DataLifecycleView = () => import('./views/DataLifecycleView.vue')
const SettingsView = () => import('./views/SettingsView.vue')
const MaaSAccountView = () => import('./views/tenant/MaaSAccountView.vue')
const MaaSPricingView = () => import('./views/tenant/MaaSPricingView.vue')
const MaaSUsageView = () => import('./views/tenant/MaaSUsageView.vue')
const MaaSOrderView = () => import('./views/tenant/MaaSOrderView.vue')
const TenantModelsView = () => import('./views/tenant/TenantModelsView.vue')
const CredentialMonitorView = () => import('./views/CredentialMonitorWithTabs.vue')
const ProbeHealthView = () => import('./views/ProbeHealthView.vue')
const ProbeHealthDetailView = () => import('./views/ProbeHealthDetailView.vue')
const AgentRegistryView = () => import('./views/AgentRegistryView.vue')
const FormatAnomaliesView = () => import('./views/FormatAnomaliesView.vue')
const ModelIntegrityView = () => import('./views/ModelIntegrityView.vue')
const ModulesView = () => import('./views/ModulesView.vue')
const PromptInjectionSettingsView = () => import('./views/PromptInjectionSettingsView.vue')
const ApprovalConfigView = () => import('./views/ApprovalConfigView.vue')
const ApprovalListView = () => import('./views/ApprovalListView.vue')
const ApprovalDetailView = () => import('./views/ApprovalDetailView.vue')
const OutputComplianceView = () => import('./views/OutputComplianceView.vue')
const UsageCostView = () => import('./views/admin/UsageCost.vue')
// 2026-07-24: V2-P4 admin session detail page (dual-column turns + drawer).
const SessionDetailView = () => import('./views/admin/SessionDetailPage.vue')
// 2026-08-09: 跨会话轮次列表页
const TurnsListView = () => import('./views/TurnsListView.vue')
// 2026-08-29: 代理管理
const ProxyView = () => import('./views/ProxyView.vue')
const ClientAnalyticsView = () => import('./views/ClientAnalyticsView.vue')
const TaskAnalyticsView = () => import('./views/TaskAnalyticsView.vue')
const UserProfileListView = () => import('./views/UserProfileListView.vue')
const UserProfileView = () => import('./views/UserProfileView.vue')

// P2.1+ Human Annotation Web workflow (2026-09-06)
const AnnotationView = () => import('./views/AnnotationView.vue')
const AnnotationStatsView = () => import('./views/AnnotationStatsView.vue')

// T9 — 请求注册表 / Journey 详情 / 连接注册台 / 节点恢复时间线（mock stage）
const RequestRegistryView = () => import('./views/RequestRegistryView.vue')
const RequestJourneyDetailView = () => import('./views/RequestJourneyDetailView.vue')
const ConnectionRegistryView = () => import('./views/ConnectionRegistryView.vue')
const NodeHealthTimelineView = () => import('./views/NodeHealthTimelineView.vue')

// Customer lifecycle — merged「更新与激活」+ offline fallback
const CustomerUpdateActivateView = () => import('./views/lifecycle/UpdateActivateView.vue')
const CustomerOfflineActivationView = () => import('./views/lifecycle/OfflineActivationView.vue')
const BootstrapWizardView = () => import('./views/bootstrap/BootstrapWizardView.vue')

// Operations Platform — vibecoding still lives in Gateway; other /ops/*
// pages redirect to ai-native-maintain SPA under /maintain/ops/*.
const VibeCodingView = () => import('./views/ops/VibeCodingView.vue')

/** Full-page jump to maintain SPA (nginx serves /maintain/* separately). */
function externalMaintainRedirect(path: string, target: string) {
  return {
    path,
    component: { render: () => null },
    beforeEnter() {
      if (typeof window !== 'undefined') {
        window.location.replace(target)
      }
    },
  }
}

function isAuthed(): boolean {
  if (store.jwtToken || store.apiKey || store.userInfo) return true
  // 2026-07-09 (handoff task UI verification): localStorage fallback.
  // store.userInfo is the in-memory source of truth but it only re-inits
  // from localStorage at module load. If the user lands on the SPA via deep
  // link (or after a stale tab), the in-memory store can be empty while
  // localStorage still has valid credentials. Read it directly without
  // mutating the store, so we don't trigger reactivity in the guard.
  try {
    const raw = typeof localStorage !== 'undefined'
      ? localStorage.getItem('llmgw_user_info')
      : null
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed && parsed.role && parsed.id) return true
    }
  } catch { /* corrupt cache */ }
  return false
}

function isSuperAdmin(): boolean {
  // Legacy API key auth: no JWT but has apiKey → super_admin
  if (!store.jwtToken && store.apiKey) return true
  // 2026-07-09 (handoff task UI verification): read role from
  // localStorage when the in-memory store hasn't been hydrated yet,
  // mirroring the localStorage fallback in isAuthed().
  let role = store.userInfo?.role
  if (!role) {
    try {
      const raw = typeof localStorage !== 'undefined'
        ? localStorage.getItem('llmgw_user_info')
        : null
      if (raw) {
        const parsed = JSON.parse(raw)
        role = parsed?.role
        if (parsed && !store.userInfo) store.userInfo = parsed
      }
    } catch { /* corrupt cache */ }
  }
  return role === 'super_admin'
}

function isPlatformOpsView(): boolean {
  return isSuperAdmin() && isDefaultTenant()
}

// 2026-09-04: 供应商控制台 —— super_admin，或 default 租户的 tenant_admin
// （凭据 API Key 修改对该角色开放，见 store.isProviderConsoleView）。
// 与 isSuperAdmin 一样带 localStorage 兜底，防深链进入时 userInfo 未水合。
function isProviderConsoleView(): boolean {
  if (isSuperAdmin()) return true
  let info = store.userInfo
  if (!info?.role) {
    try {
      const raw = typeof localStorage !== 'undefined'
        ? localStorage.getItem('llmgw_user_info')
        : null
      if (raw) info = JSON.parse(raw)
    } catch { /* corrupt cache */ }
  }
  return info?.role === 'tenant_admin' && info?.tenant_id === 'default'
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login',              component: LoginView, meta: { public: true } },
    { path: '/forbidden',          component: ForbiddenView, meta: { public: true } },
    // 2026-07-21: 总览搬到 /dashboard；根路径 / 仍走 HomeView（未登录 redirect 到 maintain，已登录显示 DashboardView）。
    { path: '/dashboard',          component: HomeView, meta: { requiresAuth: true } },
    { path: '/',                   component: HomeView, meta: { public: true } },

    // super_admin only — providers, catalog, free pool, tenants, audit logs
    // 2026-09-04: 供应商列表/详情改挂 providerConsole —— super_admin，或
    // default 租户 tenant_admin（凭据 API Key 修改对其开放）。
    { path: '/providers',          component: ProvidersView,       meta: { requiresProviderConsole: true } },
    { path: '/providers/:id',      component: ProviderDetailView,  meta: { requiresProviderConsole: true } },
    { path: '/key-applications',   component: KeyApplicationsView, meta: { requiresSuper: true } },
    { path: '/catalog',            redirect: (to) => ({ path: '/models', query: { ...to.query, tab: 'catalog' } }) },
    { path: '/routing-v2',         component: RoutingDashboardView, meta: { requiresSuper: true } },
    { path: '/routing-v2/credentials', component: CredentialMonitorView }, // 2026-07-04: 允许 tenant_admin 访问
    { path: '/probe-health',       component: ProbeHealthView,      meta: { requiresSuper: true } },
    { path: '/probe-health/detail', component: ProbeHealthDetailView, meta: { requiresSuper: true } },
    // 2026-07-23: 系统监测面板（v1）—— 入站需 super_admin 才能操作。
    // 设计依据 docs/会话优化v2/32-系统监测模块设计.md §5
    { path: '/system-monitor', redirect: { path: '/dashboard', query: { tab: 'selfcheck' } } },
    { path: '/routing-v2/work-types',         component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/settings', component: WorkTypesView, meta: { requiresSuper: true } },
    { path: '/routing-v2/work-types/:key',     component: WorkTypesView, meta: { requiresSuper: true } },
    // P2.1+ Human annotation Web workflow (2026-09-06): accessible by any authenticated user
    { path: '/routing-v2/annotations',        component: AnnotationView },
    { path: '/routing-v2/annotations/stats',  component: AnnotationStatsView },
    { path: '/routing-policy',     component: RoutingPolicyView,   meta: { requiresSuper: true } },
    { path: '/free-pool',          component: FreePoolView,        meta: { requiresSuper: true } },
    { path: '/tenants',            component: TenantsView,         meta: { requiresSuper: true } },
    { path: '/tenants/:tenantId',  component: TenantDetailView,    meta: { requiresSuper: true } },
    { path: '/audit-logs',        component: AuditLogView,         meta: { requiresSuper: true } },
    { path: '/format-anomalies',  component: FormatAnomaliesView,  meta: { requiresSuper: true } },
    { path: '/model-integrity',  component: ModelIntegrityView,   meta: { requiresSuper: true } },

    // Platform ops only (super_admin + default tenant)
    { path: '/users',              component: UsersView },
    { path: '/models',             component: ModelsView, meta: { requiresPlatformOps: true } },
    { path: '/pricing',            component: PricingManagementView, meta: { requiresPlatformOps: true } },
    { path: '/model-pricing',      component: StandardModelPricingView, meta: { requiresPlatformOps: true } },

    // Tenant portal (non-default tenant self-service; admin uses ?tenant=code)
    { path: '/tenant/models',      component: TenantModelsView },
    { path: '/tenant/account',     component: MaaSAccountView },
    { path: '/tenant/pricing',     component: MaaSPricingView },
    { path: '/tenant/orders/:id',  component: MaaSOrderView },
    { path: '/tenant/usage',       component: MaaSUsageView },

    // Legacy MaaS paths → tenant portal
    { path: '/maas/models',        redirect: (to) => ({ path: '/tenant/models', query: to.query }) },
    { path: '/maas/account',       redirect: (to) => ({ path: '/tenant/account', query: to.query }) },
    { path: '/maas/pricing',       redirect: (to) => ({ path: '/tenant/pricing', query: to.query }) },
    { path: '/maas/orders/:id',    redirect: (to) => ({ path: `/tenant/orders/${to.params.id}`, query: to.query }) },
    { path: '/maas/usage',         redirect: (to) => ({ path: '/tenant/usage', query: to.query }) },

    // Tenant-isolated (any authenticated user, scoped to own tenant for tenant_admin)
    { path: '/keys',               component: KeysView },
    { path: '/keys/:id',           component: KeyDetailView },
    { path: '/routing',            redirect: { path: '/routing-v2', query: { tab: 'resolve' } } },
    { path: '/routing-overview',   component: RoutingOverviewView, meta: { requiresPlatformOps: true } },
    { path: '/routing-decisions',  component: DecisionsView, meta: { requiresPlatformOps: true } },
    { path: '/correlations',       component: CorrelationsView, meta: { requiresSuper: true } },
    { path: '/routing/overrides',  component: RoutingOverrideView, meta: { requiresSuper: true } },
    { path: '/routing/overrides/audit', component: RoutingAuditView, meta: { requiresSuper: true } },
    { path: '/quality-correlations',  component: QualityCorrelationsView, meta: { requiresSuper: true } },
    { path: '/request-logs',       component: RequestLogsView },
    {
      path: '/request-detail/:requestId',
      name: 'request-detail',
      component: RequestDetailFullscreenView,
    },
    { path: '/dispatch/waterfall', component: DispatchWaterfallView, meta: { requiresPlatformOps: true } },
    { path: '/admin/session-analytics/users', component: UserProfileListView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/users/:owner', component: UserProfileView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/clients/:id', component: ClientAnalyticsView, meta: { requiresAuth: true } },
    { path: '/admin/session-analytics/tasks/:id', component: TaskAnalyticsView, meta: { requiresAuth: true } },
    { path: '/admin/compression',   component: CompressionView, meta: { requiresPlatformOps: true } },
    { path: '/admin/data-lifecycle', component: DataLifecycleView, meta: { requiresPlatformOps: true } },
    { path: '/admin/settings',     component: SettingsView, meta: { requiresSuper: true } },
    { path: '/admin/agents',       component: AgentRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/modules',      component: ModulesView, meta: { requiresSuper: true } },
    { path: '/admin/prompt-injection', component: PromptInjectionSettingsView, meta: { requiresSuper: true } },
    { path: '/admin/approval-config', component: ApprovalConfigView, meta: { requiresSuper: true } },
    { path: '/admin/approvals',    component: ApprovalListView, meta: { requiresSuper: true } },
    { path: '/admin/approvals/:id', component: ApprovalDetailView, meta: { requiresSuper: true } },
    { path: '/admin/output-compliance', component: OutputComplianceView, meta: { requiresSuper: true } },
    { path: '/admin/usage',        component: UsageCostView }, // 用量成本视图 (T2.4)
    { path: '/admin/sessions/:id', component: SessionDetailView, meta: { requiresSuper: true } }, // 2026-07-24: V2-P4 session detail
    { path: '/admin/turns',        component: TurnsListView, meta: { requiresSuper: true } }, // 2026-08-09: 跨会话轮次列表
    { path: '/admin/proxy',        component: ProxyView, meta: { requiresSuper: true } }, // 2026-08-29: 代理管理
    // T9 — 请求注册表 / Journey 详情 / 连接注册台 / 节点恢复时间线
    { path: '/admin/request-registry', component: RequestRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/request-registry/journey/:requestId', name: 'request-journey-detail', component: RequestJourneyDetailView, meta: { requiresSuper: true } },
    { path: '/admin/connection-registry', component: ConnectionRegistryView, meta: { requiresSuper: true } },
    { path: '/admin/connection-registry/:credentialId/recovery', name: 'node-health-timeline', component: NodeHealthTimelineView, meta: { requiresSuper: true } },
    { path: '/examples',           component: ExamplesView },
    { path: '/chat',               component: ChatView },

    // Local first-boot bootstrap (public; works offline)
    { path: '/bootstrap', component: BootstrapWizardView, meta: { public: true } },

    // Customer lifecycle — merged update & activate (public for guest homepage flow)
    { path: '/customer/update-activate', component: CustomerUpdateActivateView, meta: { public: true } },
    { path: '/customer/site', redirect: '/customer/update-activate' },
    { path: '/customer/activate', redirect: '/customer/update-activate' },
    { path: '/customer/license', redirect: '/customer/update-activate' },
    { path: '/customer/agreement', redirect: '/customer/update-activate' },
    { path: '/customer/offline-activation', component: CustomerOfflineActivationView, meta: { public: true } },

    // Maintain paths must never fall through to the Gateway catch-all.
    { path: '/maintain', component: MaintainUnavailableView, meta: { public: true } },
    { path: '/maintain/:pathMatch(.*)*', component: MaintainUnavailableView, meta: { public: true } },

    // Operations Platform — legacy /ops/* bookmarks → maintain SPA (full page)
    externalMaintainRedirect('/ops/licenses', '/maintain/ops/licenses'),
    externalMaintainRedirect('/ops/faults', '/maintain/ops/faults'),
    externalMaintainRedirect('/ops/autoupdate', '/maintain/ops/autoupdate'),
    externalMaintainRedirect('/ops/center', '/maintain/ops/center'),
    externalMaintainRedirect('/ops', '/maintain/ops/overview'),
    { path: '/ops/vibecoding', component: VibeCodingView, meta: { requiresSuper: true, requiresOpsPlatform: true } },

    ...(import.meta.env.DEV
      ? [{ path: '/dev/waterfall-preview', component: () => import('./views/DispatchWaterfallPreview.vue'), meta: { public: true } }]
      : []),
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

/** Soft bootstrap gate: fail-open when status cannot be fetched. */
let bootstrapGateCache: { at: number; redirect: boolean } | null = null
const BOOTSTRAP_GATE_TTL_MS = 15_000

async function shouldRedirectToBootstrap(toPath: string): Promise<boolean> {
  if (toPath === '/bootstrap' || toPath === '/forbidden' || toPath === '/login') return false
  if (toPath.startsWith('/customer/')) return false
  if (import.meta.env.DEV && toPath.startsWith('/dev/')) return false
  try {
    if (localStorage.getItem('llmgw_require_bootstrap') === '0') return false
    if (localStorage.getItem('llmgw_activated') === '1') return false
  } catch {
    return false
  }
  const now = Date.now()
  if (bootstrapGateCache && now - bootstrapGateCache.at < BOOTSTRAP_GATE_TTL_MS) {
    return bootstrapGateCache.redirect
  }
  try {
    const controller = typeof AbortController !== 'undefined' ? new AbortController() : null
    const timer = controller ? window.setTimeout(() => controller.abort(), 2500) : 0
    const res = await fetch('/api/system/bootstrap/status', {
      method: 'GET',
      credentials: 'same-origin',
      cache: 'no-store',
      signal: controller?.signal,
    })
    if (timer) window.clearTimeout(timer)
    if (!res.ok) {
      bootstrapGateCache = { at: now, redirect: false }
      return false // fail-open
    }
    const body = await res.json().catch(() => null)
    if (!body || typeof body !== 'object') {
      bootstrapGateCache = { at: now, redirect: false }
      return false
    }
    if (body.activated) {
      try {
        localStorage.setItem('llmgw_activated', '1')
      } catch { /* ignore */ }
      bootstrapGateCache = { at: now, redirect: false }
      return false
    }
    bootstrapGateCache = { at: now, redirect: true }
    return true
  } catch {
    bootstrapGateCache = { at: now, redirect: false }
    return false // fail-open on network / missing API
  }
}

router.beforeEach(async (to) => {
  // Soft redirect: local first-boot when not activated (fail-open).
  if (await shouldRedirectToBootstrap(to.path)) {
    return { path: '/bootstrap', query: { redirect: to.fullPath } }
  }

  // 2026-07-09: auth probe 还没完成时不要做任何 redirect，否则会在 cookie 登录后
  // 把用户弹回首页 / login，App.vue 的 hydration 永远没机会切到 app-layout。
  // 让 /api/auth/me 先 settle（store.authHydrated=true）再评估 auth。
  if (!store.authHydrated) {
    // 把目标 path 保存到 query，hydration 完成后会重定向过去
    if (to.path === '/' && to.query.login) {
      // Already going to home with login=1, allow
      return
    }
    // 第一次访问：等 hydration 完成。
    // 加超时上限：若 auth 始终未就绪（接口挂掉 / 竞态），30ms 轮询会永远
    // 卡住、Promise 永不 resolve，导航死锁。超过阈值则 fail-open 放行，
    // 由后续 isAuthed() 检查兜底（未登录走 login 流程），不再无限轮询。
    const AUTH_HYDRATE_MAX_WAIT_MS = 10_000
    const startedAt = Date.now()
    return new Promise<void>((resolve) => {
      const check = () => {
        if (store.authHydrated && store.userInfo && store.userInfo.id) {
          resolve()
        } else if (Date.now() - startedAt >= AUTH_HYDRATE_MAX_WAIT_MS) {
          resolve()
        } else {
          setTimeout(check, 30)
        }
      }
      check()
    })
  }
  // 1. Auth check — unauthenticated users land on home, not full-page login
  if (!to.meta.public && !isAuthed()) {
    return { path: '/', query: { login: '1', redirect: to.fullPath } }
  }
  // 2. Bounce authed users away from /login
  if (to.path === '/login' && isAuthed()) {
    return { path: '/' }
  }
  // 3. Super-admin role check
  if (to.meta.requiresSuper && !isSuperAdmin()) {
    return { path: '/forbidden' }
  }
  // 3a. Provider console (super_admin 或 default 租户 tenant_admin —
  // 凭据 API Key 修改 2026-09-04)
  if (to.meta.requiresProviderConsole && !isProviderConsoleView()) {
    return { path: '/forbidden' }
  }
  // 3b. Ops-center routes only when Maintain is available (or forced on)
  if (to.meta.requiresOpsPlatform && !showOpsPlatform()) {
    return { path: '/customer/update-activate' }
  }
  if (to.path.startsWith('/ops') && !showOpsPlatform()) {
    return { path: '/customer/update-activate' }
  }
  // 4. Platform ops (super_admin on default tenant) for运维向页面
  if (to.meta.requiresPlatformOps && !isPlatformOpsView()) {
    return { path: '/' }
  }
  // 5. Default-tenant ops must not browse tenant portal without ?tenant= context
  if (
    to.path.startsWith('/tenant/') &&
    isPlatformOpsView() &&
    typeof to.query.tenant !== 'string'
  ) {
    return { path: '/' }
  }
})
