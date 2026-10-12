import { req, type RequestOptions } from './client'

/**
 * routingBlocked.ts — 供应商级路由诊断（UI规范 17 §2 desktopOnly 让位轮，2026-10-08）。
 *
 * `GET /api/admin/diagnostics/routing-blocked?provider_id=X`
 * 注册 admin/handler.go:1464 = **`h.superAdmin`**（tenant_admin 直接 403）。
 * handler admin/diagnostics_routing.go:60。
 *
 * 存在的理由（后端注释原话）：*"credentials look healthy but routing can't find
 * them"* —— 凭据看起来健康，但路由池里找不到它。**这是移动端「凭据节点检查」
 * 与「路由检查」两条诉求的交汇点**：monitor-summary 说这个凭据 `status=ok`，
 * 而这里说它的每个 (credential, model) 绑定到底 `is_routable` 与为什么不是。
 *
 * ⚠️ 后端 :56-57 明写 *"this is the authoritative routing view"*：判据来自视图
 *   `v_routable_credential_models`，不是从 credentials 表反推的。
 */

/**
 * ★★★ 每条 (credential, raw_model) 绑定。逐字对齐 `routingBlockedBinding`。
 *
 * ★★ `unavailable_reason` 带 `omitempty` 且类型是 `*string` ⇒
 *   **键不存在 ⇔ SQL 里是 NULL**，而 `"unavailable_reason": ""` 表示
 *   **后端确实存了空串**。两者语义不同：后端 `:130-133` 只在 `!= nil` 时
 *   才把 breakdown 的键替换成 `"unknown"`，空串会原样落进 `block_reason_breakdown`
 *   ⇒ `block_reason_breakdown` 里可能有一个**空字符串键**。
 */
export interface RoutingBlockedBinding {
  credential_id: number
  credential_label: string
  raw_model_name: string
  is_routable: boolean
  /** 键可能**不存在**（NULL 时被 omitempty 掉），也可能存在但值为空串。 */
  unavailable_reason?: string
}

/**
 * ★ 逐字对齐 `routingBlockedCredential`。
 *
 * ★★★ 五个状态字段在**凭据状态查询失败时全是零值**（`:173-196` best-effort：
 *   查询出错只 `slog.Warn` 不改响应）⇒ `status`/`availability_state`/
 *   `health_status`/`lifecycle_status` 全为 `""`，且
 *   ★ `manual_disabled` 变成 **`false`** —— 一个被手动停用的凭据会显示成「未停用」。
 *   用 `routingBlockedStateUnavailable()` 识别这种整段缺失，别把它读成「状态正常」。
 */
export interface RoutingBlockedCredential {
  credential_id: number
  /**
   * ★ 是**拼接值**：`name || ':' || COALESCE(provider_name, 'unknown')`（`:168`）。
   * 状态查询失败时回退成 binding 侧的原始 `credential_label`（`:214-217`），
   * 所以**这个字段的格式随降级路径变化**，不能拿去当唯一标识。
   */
  credential_label: string
  status: string
  availability_state: string
  health_status: string
  manual_disabled: boolean
  lifecycle_status: string
  bindings_total: number
  bindings_routable: number
  bindings_blocked: number
  bindings: RoutingBlockedBinding[]
}

/**
 * ★★★ 逐字对齐 `routingBlockedDiagnostic`。
 *
 * ★★ `truncated` 带 `omitempty` ⇒ **false 时键不存在**。不能写
 *   `resp.truncated === false` 当「没截断」的判据，那是恒假的坑。
 */
export interface RoutingBlockedDiagnostic {
  provider_id: number
  provider_name: string
  /**
   * ★★★ `bindings_total` 被**钳到 500**（`:144-147`），但 `bindings_routable`
   *   **没有钳**（它统计的是 LIMIT 501 命中的全部行）⇒ 截断时
   *   `bindings_total` 可能小于 `bindings_routable`，且
   *   `bindings_blocked = bindings_total - bindings_routable` 可能为**负数**。
   *   用 `routingBlockedTotalsDisagree()` / `routingBlockedBlockedNegative()` 检出。
   */
  bindings_total: number
  bindings_routable: number
  bindings_blocked: number
  /** 空 map 序列化成 `{}`（不是 null）；但可能含一个**空字符串键**。 */
  block_reason_breakdown: Record<string, number>
  credentials: RoutingBlockedCredential[]
  /** 键可能不存在（omitempty）。 */
  truncated?: boolean
}

/** 后端 `:90` 的 `maxBindings` 常量。上限 500，但 SQL 取 501 行用于判定截断。 */
export const ROUTING_BLOCKED_MAX_BINDINGS = 500

/** 顶层 7 个恒存在的键（`truncated` 带 omitempty，不在内）。 */
export const ROUTING_BLOCKED_REQUIRED_KEYS = [
  'provider_id',
  'provider_name',
  'bindings_total',
  'bindings_routable',
  'bindings_blocked',
  'block_reason_breakdown',
  'credentials',
] as const

/** 凭据项 11 个恒存在的键。 */
export const ROUTING_BLOCKED_CRED_REQUIRED_KEYS = [
  'credential_id',
  'credential_label',
  'status',
  'availability_state',
  'health_status',
  'manual_disabled',
  'lifecycle_status',
  'bindings_total',
  'bindings_routable',
  'bindings_blocked',
  'bindings',
] as const

/** 绑定项 4 个恒存在的键（`unavailable_reason` 带 omitempty，不在内）。 */
export const ROUTING_BLOCKED_BINDING_REQUIRED_KEYS = [
  'credential_id',
  'credential_label',
  'raw_model_name',
  'is_routable',
] as const

export function fetchRoutingBlockedDiagnostic(
  providerId: number,
  options?: RequestOptions,
): Promise<RoutingBlockedDiagnostic> {
  // ★ 后端 `:65-70`：provider_id 缺失 / Atoi 失败 / <=0 三种都返同一个 400
  //   `missing or invalid provider_id query parameter` ⇒ 前端先挡一道，
  //   否则用户点进去只会看到一句对他没意义的英文报错。
  if (!Number.isInteger(providerId) || providerId <= 0) {
    return Promise.reject(new Error(`missing or invalid provider_id query parameter: ${providerId}`))
  }
  const qs = new URLSearchParams({ provider_id: String(providerId) })
  return req<unknown>(
    'GET',
    `/api/admin/diagnostics/routing-blocked?${qs}`,
    undefined,
    options,
  ).then(unwrapRoutingBlocked)
}

/**
 * 逐字段校验。**不抽通用解包器**：这个端点的三层层级（诊断 → 凭据 → 绑定）
 * 各有自己的必填键清单，混在一个通用函数里会丢层。
 */
export function unwrapRoutingBlocked(resp: unknown): RoutingBlockedDiagnostic {
  if (!resp || typeof resp !== 'object' || Array.isArray(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`路由阻塞诊断 响应形状不符：期望对象，实得 ${actual}`)
  }
  const d = resp as Record<string, unknown>
  const missing = ROUTING_BLOCKED_REQUIRED_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`路由阻塞诊断 响应形状不符：缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
  if (
    typeof d.provider_id !== 'number' ||
    typeof d.provider_name !== 'string' ||
    typeof d.bindings_total !== 'number' ||
    typeof d.bindings_routable !== 'number' ||
    typeof d.bindings_blocked !== 'number' ||
    !d.block_reason_breakdown ||
    typeof d.block_reason_breakdown !== 'object' ||
    Array.isArray(d.block_reason_breakdown) ||
    !Array.isArray(d.credentials)
  ) {
    throw new Error('路由阻塞诊断 响应形状不符：顶层键类型不对')
  }
  // 第二层：逐个凭据
  d.credentials.forEach((c, i) => {
    if (!c || typeof c !== 'object' || Array.isArray(c)) {
      throw new Error(`路由阻塞诊断 credentials[${i}] 不是对象`)
    }
    const cm = c as Record<string, unknown>
    const cMissing = ROUTING_BLOCKED_CRED_REQUIRED_KEYS.filter((k) => !(k in cm))
    if (cMissing.length > 0) {
      throw new Error(`路由阻塞诊断 credentials[${i}] 缺 ${cMissing.length} 个键（${cMissing.join(', ')}）`)
    }
    if (!Array.isArray(cm.bindings)) {
      throw new Error(`路由阻塞诊断 credentials[${i}].bindings 不是数组`)
    }
    // 第三层：逐条绑定
    cm.bindings.forEach((b, j) => {
      if (!b || typeof b !== 'object' || Array.isArray(b)) {
        throw new Error(`路由阻塞诊断 credentials[${i}].bindings[${j}] 不是对象`)
      }
      const bm = b as Record<string, unknown>
      const bMissing = ROUTING_BLOCKED_BINDING_REQUIRED_KEYS.filter((k) => !(k in bm))
      if (bMissing.length > 0) {
        throw new Error(
          `路由阻塞诊断 credentials[${i}].bindings[${j}] 缺 ${bMissing.length} 个键（${bMissing.join(', ')}）`,
        )
      }
    })
  })
  return d as unknown as RoutingBlockedDiagnostic
}

// ── 语义判据：把后端那些「数字对不上」「状态整段缺失」的情况显式化 ─────────

/**
 * ★★★ 顶层计数**自相矛盾** —— 命中后端 500 行的钳位 bug。
 *
 * 后端 `:144-147` 把 `total` 钳到 500，但 `routable` 是遍历 LIMIT 501 命中的
 * 全部行累加的，**没有钳**；`:243` 又用**钳后**的 total 算
 * `BindingsBlocked = total - routable`。
 *
 * ⚠️ 陷阱：`routable + blocked === total` 这个恒等式**恒成立**（blocked 就是
 * 用钳后的 total 减出来的），**它检测不出任何异常** —— 我第一版就把它当判据，
 * 结果全部全绿。真正的异常是**子集大于全集**：`routable > total`。
 *
 * 命中时典型读数：`total=500, routable=501, blocked=-1`。
 */
export function routingBlockedTotalsDisagree(d: RoutingBlockedDiagnostic): boolean {
  return d.bindings_routable > d.bindings_total || d.bindings_blocked < 0
}

/** ★★★ `bindings_blocked` 为**负数** —— 上游那个钳位不一致的直接后果。 */
export function routingBlockedBlockedNegative(d: RoutingBlockedDiagnostic): boolean {
  return d.bindings_blocked < 0
}

/** ★ 命中后端 500 行的截断路径（`truncated` 键可能不存在，视为 false）。 */
export function routingBlockedTruncated(d: RoutingBlockedDiagnostic): boolean {
  return d.truncated === true
}

/**
 * ★★★★★★ 凭据状态**整段缺失** ⇒ 后端那次状态查询失败（`:173-196` best-effort）。
 *
 * 此时五个状态字段全是零值，其中 ★ `manual_disabled: false` 是**危险的错值**：
 * 一个真被手动停用的凭据会显示成「未停用」。
 * ⇒ UI 必须显示「状态未知」，不能显示「正常 / 未停用」。
 */
export function routingBlockedStateUnavailable(d: RoutingBlockedDiagnostic): boolean {
  if (d.credentials.length === 0) return false
  return d.credentials.every((c) => c.status === '' && c.availability_state === '' && c.health_status === '')
}

/**
 * ★ 逐条判断：这条凭据的状态与「是否手动停用」**不可信**。
 *
 * （早期签名带一个 `diag` 参数，但判据只需要凭据自身的零值形态 ——
 *   留着不用的参数会让调用方以为它在看全局状态，实际没看。）
 */
export function routingBlockedManualDisabledUnreliable(c: RoutingBlockedCredential): boolean {
  return c.status === '' && c.availability_state === '' && c.health_status === '' && c.manual_disabled === false
}

/**
 * ★ 被阻塞但**拿不到原因** —— 分两种，语义不同：
 *   · `unavailable_reason` **键不存在** ⇔ SQL 是 NULL（后端 :131 换成 `"unknown"`）
 *   · `unavailable_reason === ''`        ⇔ 后端确实存了空串
 *   UI 不能把两者画成同一种「原因未知」。
 */
export function routingBlockedReasonAbsent(b: RoutingBlockedBinding): boolean {
  return !b.is_routable && b.unavailable_reason === undefined
}

export function routingBlockedReasonEmpty(b: RoutingBlockedBinding): boolean {
  return !b.is_routable && b.unavailable_reason === ''
}

/** ★ breakdown 里可能有**空字符串键**（后端只把 NULL 换成 `"unknown"`）。 */
export function routingBlockedBreakdownEmptyKey(d: RoutingBlockedDiagnostic): boolean {
  return Object.prototype.hasOwnProperty.call(d.block_reason_breakdown, '')
}

/**
 * ★ 顶层 `bindings_total` 与逐凭据求和**对不上**。
 *
 * 后端两条路径的基数不同：顶层数的是**扫描行数**（被钳到 500），
 * 逐凭据的 `bindings_total = len(keys)` 加起来是**未钳的真实行数**。
 * ⇒ 截断时两者必然不等，UI 不能拿其中一个校验另一个。
 */
export function routingBlockedSumMismatch(d: RoutingBlockedDiagnostic): boolean {
  return d.credentials.reduce((n, c) => n + c.bindings_total, 0) !== d.bindings_total
}

/** ★ 逐凭据内部自相矛盾（routable + blocked ≠ total）。 */
export function routingBlockedCredInternalMismatch(c: RoutingBlockedCredential): boolean {
  return c.bindings_total !== c.bindings_routable + c.bindings_blocked
}