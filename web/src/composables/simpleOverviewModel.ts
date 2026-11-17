// simpleOverviewModel.ts — 简易总览页的**纯派生层**（2026-11-17）
//
// ★ 为什么单独一个文件：总览页要回答「现在能不能用 / 今天跑了多少 / 我该做什么」，
//   这三问的**判据**必须可被单测钉住，而且**不能**混在模板与 i18n 里。
//   本文件只做「事实 → 结构化判据」，不碰 i18n、不碰 DOM ⇒ 同一份判据
//   可以被页面与测试共用，测试不需要 mount 组件。
//
// ★ 口径纪律（与专家面**完全一致**，简易模式不新造任何统计口径）：
//   · 请求数 / 成功率 / Token / 费用 ⇒ /api/admin/dashboard/board 的 summary
//   · 上游健康 ⇒ /api/providers 的 health_status / *_credential_count
//   · 密钥状态 ⇒ /api/keys 的 status / enabled / last_used_at
//   三个数据源都是既有端点，**零迁移、零新表**。
//
// ★ 诚实边界（本文件的核心职责之一）：
//   「读不到」与「读到了但是 0」必须分得开。deriveOverviewStatus 的
//   `confident` 字段就是这条边界：数据源读不到时 confident=false，
//   页面**不得**渲染成「一切正常」—— 那等于主动宣称健康。
//
// ★ 借鉴 magpie（`~/workspace/ai/magpie`）：magpie 的引导 checklist
//   每一步的完成状态都由**真实系统状态**推导（app.js:1735-1743
//   的 guideShown() 读 state.served / state.agents[].wired），
//   完成后自动消失（同上 :1740 `return null`）。
//   ⇒ 本文件的 deriveOnboardingSteps 同款：勾选状态从真实数据算，
//     不让用户手动打勾（手动打勾会立刻与现实不一致）。

/** 上游健康的一行 */
export interface ProviderHealthRow {
  id: number
  name: string
  /** ok=健康 / warn=有告警 / danger=不可达 / muted=未探活或已关停 */
  tone: 'ok' | 'warn' | 'danger' | 'muted'
  /** 该供应商挂着的凭据数 */
  credentialCount: number
  /** 健康凭据数 */
  healthyCount: number
  /** 最后一次探测时间（可能为空） */
  checkedAt: string | null
}

/** 上游健康汇总 */
export interface ProviderHealthSummary {
  /** 在册供应商总数 */
  total: number
  /** 真的能用（有健康凭据）的供应商数 */
  usable: number
  /** 有告警的供应商数 */
  warning: number
  /** 不可达的供应商数 */
  unreachable: number
  /** 未探活 / 状态未知 */
  unknown: number
  /** 已关停（enabled=false） */
  disabled: number
  /** 读不到（无权限 / 接口失败）⇒ 上面所有计数都**不可信** */
  unavailable: boolean
  /** 明细行（按严重度排序，最多 usable/warning/unreachable 的那些） */
  rows: ProviderHealthRow[]
  /**
   * ★ 明细行被截掉的数量（total - rows.length）。
   *   **必须披露**：明细按严重度排序且只留前 N 行 ⇒ 被切掉的几乎全是「可用」的
   *   （线上实测：徽标写「19 个上游可用」，列表里只有 3 行标着「可用」）。
   *   不披露 ⇒ 用户以为数据对不上（本仓 11-16 审计轮的 F3「硬上限必须自带披露」同型）。
   */
  hidden: number
}

/** 一条状态判据 */
export type OverviewIssue =
  | 'no_usable_provider'
  | 'provider_status_unavailable'
  | 'provider_unreachable'
  | 'provider_warning'
  | 'selfcheck_failed'
  | 'selfcheck_stale'
  | 'stats_unavailable'

/** 总览状态 */
export interface OverviewStatus {
  /** ok=一切正常 / warn=有需要注意的 / blocked=还用不了 */
  tone: 'ok' | 'warn' | 'blocked'
  /**
   * 状态点是否可信。
   * false ⇒ 上游健康读不到（无权限/接口失败）⇒ 页面**不得**宣称健康。
   */
  confident: boolean
  /** 命中的判据（按严重度排序，空数组 = 无异常） */
  issues: OverviewIssue[]
}

/** 一个统计卡 */
export interface OverviewStat {
  /** i18n key 后缀（拼成 simpleMode.overview.stat.<key>） */
  key: 'requests' | 'successRate' | 'tokens' | 'cost' | 'activeKeys' | 'models'
  /** 展示值；null ⇒ 读不到（页面渲染占位符，不是 0） */
  value: string | null
  /** 口径说明（i18n key 后缀：simpleMode.overview.stat.<key>.scope） */
  scope: string | null
  /** 语义色：给 StatCard 的状态点用 */
  tone: 'neutral' | 'success' | 'warning' | 'danger'
}

/** 接入进度的一步 */
export interface OnboardingStep {
  key: 'provider' | 'key' | 'client'
  /** 是否已完成（由真实状态推导，不让人手动打勾） */
  done: boolean
  /** 引导文案里的数字槽（如「已接 N 个上游」） */
  count: number
}

/** 看板 summary 的最小形状（只取总览要用的字段，避免绑死整个 BoardPayload） */
export interface OverviewSummaryInput {
  total_requests?: number
  total_tokens?: number
  total_cost_usd?: number
  success_rate?: number
  active_api_keys?: number
  active_models?: number
}

/** 自检状态（来自 board.operational.selfcheck） */
export interface SelfcheckInput {
  degraded?: boolean
  last_status?: string | null
  last_run_at?: string | null
  total_runs_24h?: number
}

// ─── 数字格式化（确定性：不用 toLocaleString，避免测试随宿主 locale 漂移）───

function groupDigits(n: number): string {
  const int = Math.trunc(Math.abs(n))
  const s = String(int)
  let out = ''
  for (let i = 0; i < s.length; i++) {
    if (i > 0 && (s.length - i) % 3 === 0) out += ','
    out += s[i]
  }
  return n < 0 ? `-${out}` : out
}

/** 16201 ⇒ "16,201" */
export function formatCount(n: number | null | undefined): string | null {
  if (n == null || !Number.isFinite(n)) return null
  return groupDigits(n)
}

/** 740385219 ⇒ "740.4M"（大数压缩，小数点后一位） */
export function formatCompact(n: number | null | undefined): string | null {
  if (n == null || !Number.isFinite(n)) return null
  const abs = Math.abs(n)
  const sign = n < 0 ? '-' : ''
  if (abs >= 1_000_000_000) return `${sign}${(abs / 1_000_000_000).toFixed(1)}B`
  if (abs >= 1_000_000) return `${sign}${(abs / 1_000_000).toFixed(1)}M`
  if (abs >= 1_000) return `${sign}${(abs / 1_000).toFixed(1)}K`
  return groupDigits(n)
}

/** 0.9599 ⇒ "96.0%" */
export function formatPercent(ratio: number | null | undefined): string | null {
  if (ratio == null || !Number.isFinite(ratio)) return null
  return `${(ratio * 100).toFixed(1)}%`
}

/** 2665.17 ⇒ "$2,665.17" */
export function formatUsd(v: number | null | undefined): string | null {
  if (v == null || !Number.isFinite(v)) return null
  const sign = v < 0 ? '-' : ''
  const cents = Math.round(Math.abs(v) * 100)
  const int = Math.trunc(cents / 100)
  const frac = cents % 100
  return `${sign}$${groupDigits(int)}.${frac < 10 ? '0' : ''}${frac}`
}

// ─── 上游健康 ────────────────────────────────────────────────────────────────

/** /api/providers 里本文件要用到的最小形状 */
export interface ProviderLike {
  id: number
  display_name: string
  enabled: boolean
  health_status?: 'unknown' | 'healthy' | 'warning' | 'unreachable' | 'manual_disabled' | string
  active_credential_count?: number
  healthy_credential_count?: number
  warning_credential_count?: number
  unreachable_credential_count?: number
  health_checked_at?: string | null
}

/** 严重的排前面：danger → warn → ok → muted */
const TONE_RANK: Record<ProviderHealthRow['tone'], number> = {
  danger: 0,
  warn: 1,
  ok: 2,
  muted: 3,
}

/**
 * 从 /api/providers 推导上游健康。
 *
 * ★ 口径（与 11-16 审计的「零流量供应商必须可见」一致）：
 *   `unknown`（没有凭据 / 还没探活）**不算健康**，单列一档。
 *   把「没探活」并进「健康」就是本仓反复踩的那个坑——
 *   「查不出来」渲染成 0 + 绿点 = 主动宣称健康。
 */
export function deriveProviderHealth(
  providers: ProviderLike[] | null | undefined,
  options?: { unavailable?: boolean; maxRows?: number },
): ProviderHealthSummary {
  const unavailable = options?.unavailable === true
  const maxRows = options?.maxRows ?? 12
  if (unavailable || !providers) {
    return {
      total: 0,
      usable: 0,
      warning: 0,
      unreachable: 0,
      unknown: 0,
      disabled: 0,
      unavailable: true,
      rows: [],
      hidden: 0,
    }
  }

  const rows: ProviderHealthRow[] = []
  let usable = 0
  let warning = 0
  let unreachable = 0
  let unknown = 0
  let disabled = 0

  for (const p of providers) {
    const credentialCount = p.active_credential_count ?? 0
    const healthyCount = p.healthy_credential_count ?? 0
    const row: ProviderHealthRow = {
      id: p.id,
      name: p.display_name,
      tone: 'muted',
      credentialCount,
      healthyCount,
      checkedAt: p.health_checked_at ?? null,
    }

    if (!p.enabled) {
      disabled++
      rows.push(row)
      continue
    }
    if (credentialCount === 0) {
      // 没有凭据 ⇒ 从没接通过，不是「不健康」，是「还没接」
      unknown++
      rows.push(row)
      continue
    }
    switch (p.health_status) {
      case 'healthy':
        if (healthyCount > 0) {
          usable++
          row.tone = 'ok'
        } else {
          unknown++
        }
        break
      case 'warning':
        warning++
        row.tone = 'warn'
        break
      case 'unreachable':
      case 'manual_disabled':
        unreachable++
        row.tone = 'danger'
        break
      default:
        unknown++
        break
    }
    rows.push(row)
  }

  const sorted = [...rows].sort((a, b) => {
    const d = TONE_RANK[a.tone] - TONE_RANK[b.tone]
    if (d !== 0) return d
    return b.healthyCount - a.healthyCount
  })

  const visibleRows = sorted.slice(0, maxRows)
  return {
    total: providers.length,
    usable,
    warning,
    unreachable,
    unknown,
    disabled,
    unavailable: false,
    rows: visibleRows,
    // 被截掉的那部分（页面必须披露，否则「微标写 19 、列表只有 3 个可用」看起来像数据对不上）
    hidden: providers.length - visibleRows.length,
  }
}

// ─── 总览状态 ────────────────────────────────────────────────────────────────

/** 自检「多久算陈旧」：7 天没有跑过 ⇒ 陈旧 */
const SELFCHECK_STALE_MS = 7 * 24 * 60 * 60 * 1000

export interface OverviewStatusInput {
  providerHealth: ProviderHealthSummary
  selfcheck?: SelfcheckInput | null
  /** 看板 summary 是否降级（读不到 ⇒ 不能宣称健康） */
  statsDegraded?: boolean
  /** 看板 summary 是否整体缺失 */
  statsMissing?: boolean
}

/**
 * 推导总览状态（状态条的判据）。
 *
 * ★ 严重度排序：blocked（用不了）> warn（有异常）> ok（正常）。
 * ★ `confident=false` ⇒ 上游健康读不到 ⇒ 页面必须写「读不到」，
 *   不能写「一切正常」（magpie 的绿点语义：不知道 ≠ 正常）。
 * ★ 「读不到」与「一个都没有」必须分开：
 *   - `provider_status_unavailable`：读不到上游状态（无权限 / 接口失败）
 *     ⇒ 判据是 **warn + confident=false**，**不是 blocked**
 *     （把「不知道」渲染成「用不了」同样是失真，且更糟：它会让用户去修一个没坏的组件）。
 *   - `no_usable_provider`：确实一个能用的上游都没有 ⇒ blocked。
 */
export function deriveOverviewStatus(input: OverviewStatusInput): OverviewStatus {
  const { providerHealth, selfcheck, statsDegraded, statsMissing } = input
  const issues: OverviewIssue[] = []

  if (providerHealth.unavailable) {
    issues.push('provider_status_unavailable')
  } else if (providerHealth.usable === 0) {
    issues.push('no_usable_provider')
  }

  if (!providerHealth.unavailable) {
    if (providerHealth.unreachable > 0) issues.push('provider_unreachable')
    else if (providerHealth.warning > 0) issues.push('provider_warning')
  }

  if (selfcheck && !selfcheck.degraded) {
    if (selfcheck.last_status && selfcheck.last_status !== 'success') {
      issues.push('selfcheck_failed')
    } else if (selfcheck.last_run_at) {
      const ranAt = Date.parse(selfcheck.last_run_at)
      if (Number.isFinite(ranAt) && Date.now() - ranAt > SELFCHECK_STALE_MS) {
        issues.push('selfcheck_stale')
      }
    }
  }

  if (statsDegraded || statsMissing) issues.push('stats_unavailable')

  const blocked = issues.includes('no_usable_provider')
  const tone: OverviewStatus['tone'] = blocked ? 'blocked' : issues.length > 0 ? 'warn' : 'ok'

  return {
    tone,
    confident: !providerHealth.unavailable && !(statsDegraded || statsMissing),
    issues,
  }
}

// ─── 统计卡 ──────────────────────────────────────────────────────────────────

export interface OverviewStatsInput {
  summary: OverviewSummaryInput | null
  /** 看板 summary 降级 ⇒ 所有数字都不可信 */
  degraded: boolean
  /** 密钥总数与启用数（来自 /api/keys） */
  keyTotal?: number
  keyEnabled?: number
}

/**
 * 推导统计卡。
 *
 * ★ 降级时 value 一律给 null（页面渲染占位符），**绝不**给 0。
 *   「读不到」渲染成 0 就是本仓反复修过的那类静默错账。
 */
export function deriveOverviewStats(input: OverviewStatsInput): OverviewStat[] {
  const { summary, degraded } = input
  const s = degraded ? null : summary

  const requests = s?.total_requests ?? null
  const successRate = s?.success_rate ?? null
  const tokens = s?.total_tokens ?? null
  const cost = s?.total_cost_usd ?? null
  const models = s?.active_models ?? null

  const activeKeys =
    s?.active_api_keys != null
      ? s.active_api_keys
      : input.keyEnabled != null
        ? input.keyEnabled
        : null

  const successTone: OverviewStat['tone'] =
    successRate == null
      ? 'neutral'
      : successRate >= 0.99
        ? 'success'
        : successRate >= 0.9
          ? 'warning'
          : 'danger'

  return [
    {
      key: 'requests',
      value: formatCount(requests),
      scope: requests == null ? null : 'today',
      tone: 'neutral',
    },
    {
      key: 'successRate',
      value: formatPercent(successRate),
      scope: successRate == null ? null : 'today',
      tone: successTone,
    },
    {
      key: 'tokens',
      value: formatCompact(tokens),
      scope: tokens == null ? null : 'today',
      tone: 'neutral',
    },
    {
      key: 'cost',
      value: formatUsd(cost),
      scope: cost == null ? null : 'today',
      tone: 'neutral',
    },
    {
      key: 'activeKeys',
      value:
        activeKeys == null
          ? null
          : input.keyTotal != null
            ? `${formatCount(activeKeys)} / ${formatCount(input.keyTotal)}`
            : formatCount(activeKeys),
      scope: activeKeys == null ? null : 'active',
      tone: 'neutral',
    },
    {
      key: 'models',
      value: formatCount(models),
      scope: models == null ? null : 'active',
      tone: 'neutral',
    },
  ]
}

// ─── 接入进度（引导 checklist）────────────────────────────────────────────────

export interface OnboardingInput {
  providerHealth: ProviderHealthSummary
  /** 已签发且启用的密钥数 */
  keysEnabled: number
  /** 真实被用过的密钥数（last_used_at 非空）⇒ 客户端真的接过 */
  keysInUse: number
}

/**
 * 推导接入进度三步。
 *
 * ★ magpie 的做法（app.js:1735-1743）：每一步的完成状态从**真实状态**推导，
 *   完成后引导卡整体消失（同上 :1740）。这里同款：
 *   done 由 providerHealth / keysEnabled / keysInUse 算，
 *   不接受外部传一个「已完成」标记（手动打勾会立刻与现实不一致）。
 *
 * ★ 三步的语义（与 v2 设计稿 §四「一行一个客户端接入」对齐）：
 *   ① 接一个上游 —— 有**能用的**上游（usable > 0，不把「没凭据」算进去）
 *   ② 签发网关密钥 —— 有已签发且启用的密钥
 *   ③ 接入客户端 —— 有密钥真的被请求过（last_used_at 非空）
 */
export function deriveOnboardingSteps(input: OnboardingInput): OnboardingStep[] {
  const { providerHealth, keysEnabled, keysInUse } = input
  return [
    {
      key: 'provider',
      done: !providerHealth.unavailable && providerHealth.usable > 0,
      count: providerHealth.usable,
    },
    {
      key: 'key',
      done: keysEnabled > 0,
      count: keysEnabled,
    },
    {
      key: 'client',
      done: keysInUse > 0,
      count: keysInUse,
    },
  ]
}

/** 三步是否全部完成（完成 ⇒ 引导卡消失，magpie app.js:1740 同款） */
export function onboardingComplete(steps: OnboardingStep[]): boolean {
  return steps.length > 0 && steps.every((s) => s.done)
}

// ─── 我的客户端（接入清单）：排序与分页 ──────────────────────────────────────
//
// ★ 为什么单独一段（2026-11-17 第五轮）：
//   接入清单原来把**在用密钥整表铺开**（线上 118 行在用 + 91 行折叠）⇒
//   「初级用户一眼看明白」这件事在这一块直接失败：表格比页面还长。
//   ⇒ 改成**两个分页列表**（最新 / 最多），排序与分页都是纯函数、可单测，
//     视图只负责画表与转发翻页动作。
//
// ★ 口径纪律：
//   · 行里的请求数 / 花费是**累计**口径（/api/keys 的 total_requests /
//     total_cost_usd）；**今日**口径在总览统计卡那边（board summary）。
//     两个口径**不混排**：列表行必须带「累计」字样，统计卡带「今日口径」字样。
//   · 排序**必须确定**：并列时按累计请求数、最后按 id 兜底 ⇒
//     否则同一份数据翻两页，行的位置会变（用户会以为列表在乱跳）。

/** 客户端列表的一行（纯数据；视图只做格式化） */
export interface ClientRow {
  id: number
  name: string
  /** 状态口径：ok（在用）/ idle（未使用）/ pending（待生效）/ off（已停用） */
  stateKey: 'ok' | 'idle' | 'pending' | 'off'
  /** 累计请求数（/api/keys 的 total_requests） */
  requests: number
  /** 累计花费（美元） */
  spend: number
  /** 最近一次使用时间 */
  lastUsedAt: string | null
  /** 签发时间 */
  createdAt: string | null
}

/** 排序口径：最近在用 / 最近签发 / 用得最多 */
export type ClientSortKey = 'recent-used' | 'recent-created' | 'busiest'

function timeValue(ts: string | null | undefined): number {
  if (!ts) return Number.NEGATIVE_INFINITY
  const v = Date.parse(ts)
  return Number.isNaN(v) ? Number.NEGATIVE_INFINITY : v
}

/**
 * 按时间倒序比较两个时间戳（**缺时间排最后**）。
 *
 * ★ 缺值必须**显式**判：`缺值 - 有限值` = ±Infinity，`有限值 - 缺值` = ∓Infinity，
 *   两边都缺时还是 NaN ⇒ 用「差值是否为有限数」来判会把缺值行当成并列
 *   （实测踩过：从不使用的客户端没有被排到最后，而是被 id 兜底排到了前面）。
 */
function compareTimeDesc(a: string | null, b: string | null): number {
  const av = timeValue(a)
  const bv = timeValue(b)
  if (av === bv) return 0
  if (av === Number.NEGATIVE_INFINITY) return 1
  if (bv === Number.NEGATIVE_INFINITY) return -1
  return bv - av
}

/**
 * 按指定口径排序客户端行（**不改动入参**，返回新数组）。
 *
 * ★ 并列时的次序是契约的一部分：累计请求数降序 → id 升序。
 *   少了 id 兜底，同一批 0 请求的客户端在两次渲染之间会换位置，
 *   分页就会「漏行/重行」（用户翻页时看到的行对不上）。
 */
export function sortClientRows<T extends ClientRow>(rows: T[], sort: ClientSortKey): T[] {
  const out = [...rows]
  out.sort((a, b) => {
    if (sort === 'busiest') {
      if (b.requests !== a.requests) return b.requests - a.requests
    } else if (sort === 'recent-created') {
      const d = compareTimeDesc(a.createdAt, b.createdAt)
      if (d !== 0) return d
    } else {
      const d = compareTimeDesc(a.lastUsedAt, b.lastUsedAt)
      if (d !== 0) return d
    }
    if (b.requests !== a.requests) return b.requests - a.requests
    return a.id - b.id
  })
  return out
}

/** 一页客户端行 + 翻页所需的派生状态 */
export interface ClientPage<T extends ClientRow = ClientRow> {
  rows: T[]
  /** 当前页（1 基，已钳制到合法范围） */
  page: number
  /** 总页数（至少 1） */
  pageCount: number
  /** 总条数 */
  total: number
  pageSize: number
  hasPrev: boolean
  hasNext: boolean
}

/**
 * 客户端分页（纯函数）。
 *
 * ★ **钳制**页码：列表变短（例如筛选后）时若还停在第 9 页，
 *   不钳制就会渲染出**空页**（用户以为「这一页没有数据」）。
 *   ⇒ 页码越界时落到最后一页；pageSize 非法（<=0 / NaN）时按 1 处理，
 *     绝不返回 NaN 页数。
 */
export function paginateClientRows<T extends ClientRow>(
  rows: T[],
  page: number,
  pageSize: number,
): ClientPage<T> {
  const size = Number.isFinite(pageSize) && pageSize >= 1 ? Math.floor(pageSize) : 1
  const total = rows.length
  const pageCount = Math.max(1, Math.ceil(total / size))
  const current = Number.isFinite(page) ? Math.min(Math.max(1, Math.floor(page)), pageCount) : 1
  const start = (current - 1) * size
  return {
    rows: rows.slice(start, start + size),
    page: current,
    pageCount,
    total,
    pageSize: size,
    hasPrev: current > 1,
    hasNext: current < pageCount,
  }
}
