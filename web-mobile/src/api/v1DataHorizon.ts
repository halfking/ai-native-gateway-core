import { req, type RequestOptions } from './client'

/**
 * v1DataHorizon.ts — 「v1 读源族是否已停更」的全局告示（2026-10-08，第八十六批）。
 *
 * GET /api/admin/v1-data-horizon
 *
 * - **注册**：`admin/handler.go:1088`
 *   `mux.HandleFunc("/api/admin/v1-data-horizon", admin(h.handleV1DataHorizon))`
 *   ⇒ ★ **admin 档**（tenant_admin 可用）⇒ 抽屉席**不设** `requiresRole`。
 *   ⇒ 不在 `maintain_proxy.go` 的 `maintainCompatPrefixes` ⇒ 本进程提供。
 * - **实现**：`admin/v1_freeze_notice.go`（决策纯函数 `v1GateState` 在 `:147-155`）。
 *
 * ══════════════════════════════════════════════════════════════════════════
 * ## ★★★★★ 本族最要紧的十一件事
 * ══════════════════════════════════════════════════════════════════════════
 *
 * (1) ★★★★★ **主键恒在，值有「对象」与「裸 `null`」两种形态** —— 不是主键消失。
 *      `:258-261`（有告示）与 `:265-268`（无告示）都写**两键**：
 *      ```go
 *      {"v1_data_horizon": notice, "//": "frozen=true 表示…"}
 *      ```
 *      ⇒ ★★★ 与批 84 的降级信封（**主键整个消失**）**正好相反**。
 *      ⇒ ⇒ 判据必须是「**值为 null ⇒ 未停更**」，**不能**是「键存在 ⇒ 有告示」。
 *
 * (2) ★★★★★ **后端刻意不产出「有对象但 frozen=false 且 unknown=false」**。
 *      `V1FreezeNotice` 类型注释（`:75-77`）与 `handleV1DataHorizon`（`:264`）都写明：
 *      那种情形被返回成 `null`。
 *      ⇒ ★★★ 于是「有对象 ⇒ `frozen` 与 `unknown` **恰好一个为 true**」是**可自验的强不变量**：
 *        - frozen 分支（`:217-224`）：`Frozen: true`，`Unknown` 未设 ⇒ false
 *        - unknown 分支（`:207-215`）：`Frozen: false`，`Unknown: true`
 *
 * (3) ★★★★★ **`frozen: false` 现在是有合法实例的**（就是 unknown 那一档）。
 *      ⇒ ★★★ 判「已停更」**不能**只读 `frozen`；桌面客户端注释明写这一点。
 *
 * (4) ★★★★★ **三态判定是一个三行纯函数**（`:147-155`），可逐行镜像：
 *      ```go
 *      if source == "" || source == "default" { return unavailable }
 *      if logsWriteEnabled { return live }
 *      return frozen
 *      ```
 *      ⇒ ★★★ **头号陷阱**：`logsWriteEnabled === true` **但** `source` 回落成 `default`
 *        ⇒ 结果是 **unavailable 而不是 live** —— 这正是整个文件存在的理由
 *        （`settings` 的 `GetPlatformBool` 有三个回落点，全部返回 fallback=true）。
 *
 * (5) ★★★★ **空串与 `"default"` 同义**（`:143-146`）。
 *      `EffectiveValue` 出错时返回 `("", err)`，调用方把 err 与空串一起折叠成 default
 *      ⇒ 若把空串当「显式」，**读失败就会显示成「数据是新的」**。
 *
 * (6) ★★★★ **响应头 `X-LLM-Gateway-V1-Data-Frozen` 是「三值 + 缺失」**。
 *      `applyV1FreezeNotice`（`:236-248`）：
 *      - live ⇒ **头根本不设置**（`:238-240` 直接 return）
 *      - frozen ⇒ 头 `"1"`
 *      - unknown ⇒ 头 `"unknown"`（**与 frozen 用不同值**，抓包必须能区分）
 *      ⇒ ★★★ 这是**第十二种 nil 编码：头缺失**。
 *      ⇒ ★★ 头与 body 是**两套独立表达**，客户端可以用它交叉验证。
 *
 * (7) ★★★★ **`source` 这个名字在本响应里指两件不同的事**（命名陷阱）：
 *      - `notice.source` = **硬编码字面量** `"request_logs"`（被冻结的读源族，`:210/:219`）
 *      - gate 的 source = `{db, env, default}`（`spec.go:283`），**不出现**在响应里
 *      ⇒ ★★ 客户端不要把 `notice.source` 当成「配置来源」。
 *
 * (8) ★★★ `gate_key` 恒为 `settings.KeyRequestLogsWriteEnabled`
 *      = 字面量 **`storage.request_logs_write_enabled`**（`settings/key_request_logs_write_enabled.go:19`）。
 *      ⇒ 恒定常量，本模块导出但**不提供校验取值的判据**（恒真）。
 *
 * (9) ★★★ `affects` 三值 `{silently_frozen, silently_degraded_content, silently_empty}`，
 *      **两个分支的字面量完全相同**（`:214` 与 `:223`）。
 *
 * (10) ★★ `effect` / `silence` 是**给人读的文案**，**两个分支各一套**
 *      ⇒ ★★ 客户端**不要**用文案判状态，要用 `frozen`/`unknown`；
 *        文案只用于**展示**（frozen 那套讲「停写」，unknown 那套讲「读不到」）。
 *
 * (11) ★★ `V1FreezeNotice` 七键**无 omitempty** ⇒ **七键恒在**。
 *
 * ★★ **本模块明确声明的校验边界**：解包器校验**两键信封恒在** + 主键是
 *   「七键对象」或「`null`」+ 各键类型；**不校验** `effect`/`silence` 的文案内容
 *   （那是展示文案，且两套字面量都是后端硬编码）。
 */

/** `:71` 携带告示的响应头名。见 (6)。 */
export const V1_FREEZE_HEADER = 'X-LLM-Gateway-V1-Data-Frozen'

/** `settings/key_request_logs_write_enabled.go:19`。见 (8)。 */
export const V1_HORIZON_GATE_KEY = 'storage.request_logs_write_enabled'

/** `:210` / `:219` 硬编码的「被冻结的读源族」。见 (7)。 */
export const V1_HORIZON_READ_SOURCE = 'request_logs'

/** `spec.go:283` `EffectiveValue` 的 source 域。见 (4)(5)。 */
export const V1_GATE_SOURCES = ['db', 'env', 'default'] as const

/** `:121-130` 的三态。 */
export const V1_GATE_STATES = ['live', 'frozen', 'unavailable'] as const

/** `:134` 回落来源标签。 */
export const V1_GATE_SOURCE_DEFAULT = 'default'

/** `:214` / `:223` 两处**完全相同**的三值。见 (9)。 */
export const V1_HORIZON_AFFECTS = ['silently_frozen', 'silently_degraded_content', 'silently_empty'] as const

/** `V1FreezeNotice` 的七个恒在键（无 omitempty）。见 (11)。 */
export const V1_HORIZON_NOTICE_KEYS = [
  'frozen',
  'unknown',
  'source',
  'gate_key',
  'effect',
  'silence',
  'affects',
] as const

/** 响应恒在的两个键。见 (1)。 */
export const V1_HORIZON_ENVELOPE_KEYS = ['v1_data_horizon', '//'] as const

/** 主键名。 */
export const V1_HORIZON_KEY = 'v1_data_horizon'

// ── 类型 ─────────────────────────────────────────────────────────────────────

export type V1GateState = (typeof V1_GATE_STATES)[number]

export interface V1DataHorizonNotice {
  /** ★ true = **明确读到**停更；false 时**必须**看 `unknown`。见 (2)(3)。 */
  frozen: boolean
  /** ★ true = 读点没给出可信答案（配置键没有显式取值，值来自回落）。见 (4)。 */
  unknown: boolean
  /** ★ 恒为 `"request_logs"`；**不是**配置来源。见 (7)。 */
  source: string
  gate_key: string
  /** ★ 展示文案；两个分支各一套，不要用它判状态。见 (10)。 */
  effect: string
  /** ★ 展示文案；两个分支各一套。见 (10)。 */
  silence: string
  affects: string[]
}

export interface V1DataHorizonResponse {
  /** ★ 恒在；值是「七键对象」或**裸 `null`**。见 (1)。 */
  v1_data_horizon: V1DataHorizonNotice | null
  '//': string
}

// ── fetch ───────────────────────────────────────────────────────────────────

/** GET `/api/admin/v1-data-horizon`（**admin 档**）。 */
export function fetchV1DataHorizon(options?: RequestOptions): Promise<V1DataHorizonResponse> {
  return req<unknown>('GET', '/api/admin/v1-data-horizon', undefined, options).then(unwrapV1DataHorizon)
}

// ═══════════════════════════════════════════════════════════════════════════
// 解包
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★★★★ 见 (1)：**主键恒在**，值是七键对象**或裸 `null`** —— 两种都要接受。
 * 注意这与「降级信封的主键消失」是**相反**的形状。
 */
export function unwrapV1DataHorizon(resp: unknown): V1DataHorizonResponse {
  const d = requireObject(resp, 'v1 数据地平线')
  requireKeys(d, V1_HORIZON_ENVELOPE_KEYS, 'v1 数据地平线')
  if (typeof d['//'] !== 'string') throw new Error('v1 数据地平线 的 // 不是字符串')
  const notice = d[V1_HORIZON_KEY]
  // ★ 主键在但值为 null 是**合法**形态（未停更），不是错误。
  if (notice !== null) requireNotice(notice, 'v1 数据地平线 的 v1_data_horizon')
  return d as unknown as V1DataHorizonResponse
}

function requireNotice(v: unknown, where: string): void {
  const o = requireObject(v, where)
  requireKeys(o, V1_HORIZON_NOTICE_KEYS, where)
  for (const k of ['frozen', 'unknown'] as const) {
    if (typeof o[k] !== 'boolean') throw new Error(`${where} 的 ${k} 不是布尔`)
  }
  for (const k of ['source', 'gate_key', 'effect', 'silence'] as const) {
    if (typeof o[k] !== 'string') throw new Error(`${where} 的 ${k} 不是字符串`)
  }
  const arr = o['affects']
  if (!Array.isArray(arr)) throw new Error(`${where} 的 affects 不是数组`)
  for (let i = 0; i < arr.length; i++) {
    if (typeof arr[i] !== 'string') throw new Error(`${where} 的 affects[${i}] 不是字符串`)
  }
}

function requireObject(v: unknown, where: string): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) {
    const actual = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  return v as Record<string, unknown>
}
function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
}

// ═══════════════════════════════════════════════════════════════════════════
// 语义判据
// ═══════════════════════════════════════════════════════════════════════════

// ── (4)(5) 三态判定：逐行镜像 `v1GateState`（`:147-155`） ──

/**
 * ★★★★★ 见 (4)：**逐行镜像**后端那个三行纯函数 `v1GateState`。
 *
 * ```go
 * if source == "" || source == "default" { return unavailable }
 * if logsWriteEnabled { return live }
 * return frozen
 * ```
 *
 * ★★★ 头号陷阱：`logsWriteEnabled === true` **但** `source` 是回落值
 * ⇒ 结果是 **unavailable 而不是 live**。
 * ★★ 见 (5)：**空串与 `"default"` 同义**，两者都判 unavailable。
 */
export function deriveV1GateState(logsWriteEnabled: boolean, source: string): V1GateState {
  if (source === '' || source === V1_GATE_SOURCE_DEFAULT) return 'unavailable'
  if (logsWriteEnabled) return 'live'
  return 'frozen'
}

// ── (1)(2)(3) 三态的响应侧判定 ──

/**
 * ★★★★★ 见 (1)：**值为 `null` ⇒ 未停更**（唯一正确的判据）。
 *
 * ★ 保留 `=== null` 而不是 `!body.v1_data_horizon`：主键的可达集合是
 *   `{七键对象, null}`，对象**永远是真值** ⇒ 两种写法在可达集合上**可证等价**。
 *   与 `*float64` 那种「0 是 falsy」的情况不同，这里没有可区分的中间格。
 */
export function v1HorizonIsNotFrozen(body: V1DataHorizonResponse): boolean {
  return body.v1_data_horizon === null
}

/** ★★★★ 见 (3)：已停更**不能**只读 `frozen`，必须两个字段都判。 */
export function v1HorizonIsFrozen(body: V1DataHorizonResponse): boolean {
  const n = body.v1_data_horizon
  return n !== null && n.frozen === true && n.unknown === false
}

/** ★★★★ 见 (3)：unknown 是**第三种形态**，既不是 live 也不是 frozen。 */
export function v1HorizonIsUnknown(body: V1DataHorizonResponse): boolean {
  const n = body.v1_data_horizon
  return n !== null && n.unknown === true && n.frozen === false
}

/**
 * ★★★★★ 见 (2)：**有对象 ⇒ `frozen` 与 `unknown` 恰好一个为 true**。
 *
 * ★ 后端两分支各自只设一个标志（`:207-215` 设 unknown、`:217-224` 设 frozen），
 *   且 `:264` 明确「不要返回一个 frozen=false 的对象」
 *   ⇒ **三值判据**（存在/frozen/unknown 三个互斥取值）都成立时必有一条为真。
 * ⇒ ★ 这条判据不一致 ⇒ 响应被截断、形状拆错，或后端版本变了。
 */
export function v1HorizonExactlyOneFlag(body: V1DataHorizonResponse): boolean {
  const n = body.v1_data_horizon
  if (n === null) return true
  return n.frozen !== n.unknown
}

/** ★★★ 见 (2)：`frozen === false` 只可能出现在 unknown 那一档。 */
export function v1HorizonFrozenFalseOnlyWhenUnknown(body: V1DataHorizonResponse): boolean {
  const n = body.v1_data_horizon
  if (n === null || n.frozen) return true
  return n.unknown === true
}

// ── (6) 响应头：第四种表达 ──

/**
 * ★★★★ 见 (6)：解析 `X-LLM-Gateway-V1-Data-Frozen` 头。
 *
 * - `'1'` ⇒ 明确读到停更
 * - `'unknown'` ⇒ 读点没给出答案（**与 1 用不同值**，抓包必须能区分）
 * - `null` ⇒ **头不存在** ⇒ live（`applyV1FreezeNotice` 在 `!ok` 时根本不设置）
 *
 * ⇒ ★★★ 这是「头缺失」这种编码；body 与头是**两套独立表达**，可交叉验证。
 */
export function parseV1FreezeHeader(get: (name: string) => string | null): '1' | 'unknown' | null {
  const raw = get(V1_FREEZE_HEADER)
  if (raw === null) return null
  if (raw === 'unknown') return 'unknown'
  return '1'
}

/**
 * ★★★★ 见 (6)：按状态推出响应头**应该**是什么 —— 直接镜像
 * `applyV1FreezeNotice`（`:236-248`），用于把实际头与 body 交叉对账。
 */
export function v1FreezeHeaderValueFor(state: V1GateState): '1' | 'unknown' | null {
  if (state === 'live') return null
  if (state === 'unavailable') return 'unknown'
  return '1'
}

/** ★★★ 见 (6)：头与 body 是否一致（两套独立表达的交叉校验）。 */
export function v1FreezeHeaderMatchesBody(
  header: '1' | 'unknown' | null,
  body: V1DataHorizonResponse,
): boolean {
  if (body.v1_data_horizon === null) return header === null
  return header === (body.v1_data_horizon.frozen ? '1' : 'unknown')
}

// ── (7)(9) 取值域 ──

/**
 * ★★★ 见 (7)：`notice.source` **恒为** `"request_logs"` —— 但本模块
 * **不提供校验它取值的判据**：那是硬编码常量，校验它是恒真判据。
 * ⇒ 只在文档里标注（`V1_HORIZON_READ_SOURCE`），不写 `expect(...).toBe(...)` 的判据。
 */
export function v1HorizonAffectsAreKnown(notice: V1DataHorizonNotice): boolean {
  return notice.affects.every((a) => (V1_HORIZON_AFFECTS as readonly string[]).includes(a))
}

/** ★★★ 见 (9)：`affects` 恒是那三值（两分支字面量完全相同）。 */
export function v1HorizonAffectsCountIs( notice: V1DataHorizonNotice, n: number): boolean {
  return notice.affects.length === n
}

/** ★★ 见 (5)：gate 的 source 是回落值（空串或 `default`）⇒ unavailable。 */
export function v1GateSourceIsFallback(source: string): boolean {
  return source === '' || source === V1_GATE_SOURCE_DEFAULT
}

/** ★★★ 见 (4)(5)：非回落 source（`db`/`env`）才能落到 live/frozen。 */
export function v1GateSourceIsExplicit(source: string): boolean {
  return (V1_GATE_SOURCES as readonly string[]).includes(source) && !v1GateSourceIsFallback(source)
}
