// liveStageTree.ts — /live 阶段树（live2）的数据模型（2026-11-16）
//
// 这是**类型与纯常量**层，不含副作用；阶段状态推导、最终模型解析、缓存口径
// 等纯函数在 `src/lib/liveStageTree.ts`（便于单测直接驱动 —— 判据必须与被测
// 逻辑同层，这是本仓反复吃过亏的教训）。
//
// ── 阶段表的口径纪律 ────────────────────────────────────────────────
// 1. **阶段顺序 = 代码真实管线顺序**（2026-11-16 对生产中间件链与发射点逐条
//    核过，与 owner 初始列举顺序有**三处**不同，界面跟代码走）：
//      发送轨：鉴权/限流 → **脱敏** → **审批** → 注入检测 → 路由
//                → **压缩（会话压缩）** → 供应商/令牌池
//      接收轨：首字节 → **还原（脱敏占位符回填）** → 计量 → 回复
//    ★ 三处差异的证据（写死在这里，免得下次又被列举顺序带偏）：
//      ① 脱敏在**审批之前** —— 脱敏是中间件
//         (`sanitize.NewSanitizeInputMiddleware` → `ChatHandler.ServeHTTP`
//          包住 `serveHTTPInner`)，而审批/注入检测/路由都在
//         `serveHTTPInner` **内部** ⇒ 脱敏必然先跑。
//      ② 压缩在**路由之后** —— `logCtx.OutboundStrategy` 只在会话压缩器
//         `sessionCompressor.Prepare` 之后写入（handler.go:4559），而
//         Prepare 明确「Runs AFTER candidate resolution」。
//      ③ 还原（脱敏占位符回填）在**响应侧** —— `SanitizeRestoreInterceptor`
//         挂在响应链上（`InterceptNonStream` / `InterceptStreamChunk`），
//         ⇒ 必然在首字节之后 ⇒ 属接收轨。
//      ★ 审批在注入检测**之前**同样成立（session-audit 门 3451 < armor
//        打分 3850），两者都在 `serveHTTPInner` 内。
//    ★ **tools 还原不是一列**：它在「压缩之后、出站之前」⇒ 是压缩节点的
//      徽标（「tools 回填 n」），不单列一列（主档 §5.2 附注）。
// 2. **状态七态**，其中两个必须分开（不可合并成一个「没发生」）：
//      · `unmeasured`（未测量）= 这一段**没有实时信号**（能力未接线/未命中/
//        信号未到）；
//      · `pending`（未到达）  = 这一段**还没跑到**。
//    ★ 「未测量」≠「未到达」：合并成一个灰态，评审者会把「没数据」读成
//    「系统没做这一步」—— 那是两种完全相反的结论。
// 3. **失败下游一律 `pending`（未到达）**，不写「失败」—— 失败节点自己红，
//    下游是「没走到」，不是「也失败了」。

/** 单段阶段状态。 */
export type StageState =
  | 'pending'    // 未到达（还没跑到这一段）
  | 'active'     // 进行中（请求正停在这一段）
  | 'done'       // 已完成
  | 'skipped'    // 已跳过（这一段不适用）
  | 'failed'     // 在这一段失败
  | 'warned'     // 完成但有告警（如 observe-only 命中）
  | 'unmeasured' // 未测量（越过了但这一段没有实时信号）

/** 阶段键。 */
export type StageKey =
  | 'auth'
  | 'sanitize'
  | 'approval'
  | 'injection'
  | 'route'
  | 'compress'
  | 'restore'
  | 'provider'
  | 'first_byte'
  | 'meter'
  | 'reply'

/** 阶段定义（静态表）。 */
export interface StageDef {
  key: StageKey
  /** 阶段名（i18n key，liveStageTree.stage.<key>）。 */
  labelKey: string
  /** 列位（发送轨 0..7 / 接收轨 0..2）。 */
  col: number
  /** 所属轨。 */
  track: 'send' | 'recv'
}

/** 发送轨阶段表（自左向右，7 列）。 */
export const SEND_STAGES: readonly StageDef[] = [
  { key: 'auth', labelKey: 'liveStageTree.stage.auth', col: 0, track: 'send' },
  { key: 'sanitize', labelKey: 'liveStageTree.stage.sanitize', col: 1, track: 'send' },
  { key: 'approval', labelKey: 'liveStageTree.stage.approval', col: 2, track: 'send' },
  { key: 'injection', labelKey: 'liveStageTree.stage.injection', col: 3, track: 'send' },
  { key: 'route', labelKey: 'liveStageTree.stage.route', col: 4, track: 'send' },
  { key: 'compress', labelKey: 'liveStageTree.stage.compress', col: 5, track: 'send' },
  { key: 'provider', labelKey: 'liveStageTree.stage.provider', col: 6, track: 'send' },
] as const

/** 接收轨阶段表（自左向右，4 列；渲染时**从右往左**排）。 */
export const RECV_STAGES: readonly StageDef[] = [
  { key: 'first_byte', labelKey: 'liveStageTree.stage.firstByte', col: 0, track: 'recv' },
  { key: 'restore', labelKey: 'liveStageTree.stage.restore', col: 1, track: 'recv' },
  { key: 'meter', labelKey: 'liveStageTree.stage.meter', col: 2, track: 'recv' },
  { key: 'reply', labelKey: 'liveStageTree.stage.reply', col: 3, track: 'recv' },
] as const

/** 全部阶段（发送轨 + 接收轨），按管线顺序。 */
export const ALL_STAGES: readonly StageDef[] = [...SEND_STAGES, ...RECV_STAGES] as const

/** 阶段网格列数（客户端列 1 + 发送轨 8 列）。 */
export const STAGE_GRID_COLUMNS = SEND_STAGES.length + 1

/** 单段阶段状态（推导结果）。 */
export interface StageNode {
  key: StageKey
  state: StageState
  /** 主值（模型名 / 命中数 / 耗时…）。空串 = 该段没有可显示的值。 */
  value: string
  /** 补充说明（tooltip 二级文案）。 */
  detail: string
  /** 该段耗时（毫秒）。未测量 = null（禁止用 0 冒充）。 */
  durMs: number | null
}

/** 最终匹配模型的来源（用于如实说明「这个值是从哪来的」）。 */
export type ModelSource =
  | 'action'        // node_selected 动作（= qr.resolvedModel()，路由决定）
  | 'canonical'     // 请求帧的 canonical_name（持久化的标准模型名）
  | 'upstream_raw'  // upstream_request 动作的 raw_model（实际下发上游的模型串）
  | 'frame'         // 请求帧的 model
  | 'none'          // 没有任何来源 ⇒ 未测量

/** 模型解析结果（auto / 别名 / 直达 + 最终匹配模型）。 */
export interface ResolvedModel {
  /** 客户端原始请求模型名。auto 请求在生产上已被改写成选中模型 ⇒ 这里存改写后的。 */
  requested: string
  /** 「客户端要的是 auto」由这个标志表达（不能靠 requested=="auto" 反推）。 */
  isAuto: boolean
  /** auto 分类的任务类型（chat/reasoning/code/…）。 */
  taskType: string
  /** auto 决策置信度（0..1）。未上报 = null。 */
  autoConfidence: number | null
  /** 最终匹配模型（标准模型名）。空串 = 未测量。 */
  final: string
  /** 上游实际模型串（upstream_request 的 raw_model）。可能与 final 不同。 */
  upstream: string
  /** final 的来源（如实标注，不假装是「路由决定」）。 */
  source: ModelSource
  /** 解析方式说明（auto 选型 / 别名解析 / 直达）。 */
  reason: string
  /** 发生过模型切换时的切换链（from → to）。 */
  switched: { from: string; to: string } | null
  /** 候选数（route_resolved 动作带）。未上报 = null。 */
  candidates: number | null
}

/** 缓存命中率结果（带口径）。 */
export interface CacheRate {
  /** 命中率百分比（0..100）。不可判定 = null。 */
  pct: number | null
  /** 口径标识：subset_openai（cache⊆prompt）/ parallel_anthropic（cache∥prompt）/ unknown。 */
  convention: CacheConvention
  /** 口径说明（面向用户的一句话）。 */
  why: string
  /** 口径异常（观测到 >100% ⇒ 两口径混用）。 */
  anomaly: boolean
}

/** 缓存口径标识。 */
export type CacheConvention = 'subset_openai' | 'parallel_anthropic' | 'unknown'

/** 单条请求带的全部推导结果（纯函数产物，组件只渲染不改写）。 */
export interface StageBand {
  /** 请求 id。 */
  requestId: string
  /** 客户端显示名。 */
  clientName: string
  /** 客户端类型（web/cli/api/bot/内部…）。 */
  clientType: string
  /** 客户端协议（openai / anthropic / …）。 */
  clientProtocol: string
  /** 阶段状态（按 ALL_STAGES 顺序）。 */
  stages: StageNode[]
  /** 模型解析结果。 */
  model: ResolvedModel
  /** 缓存命中率（未上报 cache_read ⇒ null）。 */
  cache: CacheRate | null
  /** 响应帧数（未上报 = null）。 */
  chunks: number | null
  /** 首字节耗时（毫秒）。未上报 = null。 */
  ttfbMs: number | null
  /** 总耗时（毫秒）。 */
  latencyMs: number | null
  /** token 合计。未上报 = null。 */
  totalTokens: number | null
  /** 成本（USD）。未上报 = null。 */
  costUsd: number | null
  /** 供应商 code。 */
  providerCode: string
  /** 供应商 / 凭据合并显示名（provider · credential）。 */
  providerLabel: string
  /** 凭据标签（凭据级显示名）。 */
  credentialLabel: string
  /** 凭据 id（可信度关联键的第一段）。 */
  credentialId: number | null
  /** 上游实际模型串（upstream_request 的 raw_model；可信度关联键的第二段）。 */
  rawModel: string
  /** 失败原因码（error_kind）。 */
  errorKind: string
  /** 失败发生在哪一段（无失败 = null）。 */
  failedStage: StageKey | null
  /** 是否探测请求。 */
  isProbe: boolean
  /** 是否在途（未终态）。 */
  inFlight: boolean
  /** 客户端侧时间戳（毫秒）。 */
  tsMs: number
}
