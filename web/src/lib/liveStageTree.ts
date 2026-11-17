// liveStageTree.ts — /live 阶段树（live2）的纯函数层（2026-11-16）
//
// 只做推导，不碰 DOM、不碰 Vue：阶段状态、最终匹配模型、缓存命中率口径、
// 失败定位，全部在这里算出来，组件只渲染。判据必须与被测逻辑同层（仓内教训）。
//
// ── 三条硬纪律（写死在这里，组件不许自己再推导一遍） ──────────────────
// 1. **未测量 ≠ 未到达**：
//    · `unmeasured` = 这一段**没有实时信号**（能力未接线 / 未命中 / 信号未到）；
//    · `pending`    = 这一段**还没跑到**，或**失败后没走到**。
//    合并成一个灰态 ⇒ 评审者会把「没数据」读成「系统没做这一步」。
// 2. **事件到达顺序由 seq 保证**：同一 request_id 内 seq 单调递增，
//    store 按 seq 排序（不是到达顺序）。本层只认 seq 序。
// 3. **口径必须说清**：缓存命中率的分母随上游协议变；口径不可判定显示「—」
//    而不是 0%；算出 >100% 报「口径异常」而不是显示一个假百分比。
//
// ★ **动作明细的真形状**（2026-11-16 对生产发射点逐条核过，2026-11-17 线上再核）：
//   Go 侧 `liveactions.ActionEvent` 的 per-action 明细装在 `Detail map[string]string`
//   （json tag `detail`）里，**值一律是字符串**。
//   ★★ 但**线上 SSE 把 detail 摊平到顶层**（`admin/live_stream_lifecycle.go` 的
//      `flattenActionEvent`：先 `delete(m,"detail")`，再把 detail 的键**拷到顶层**）
//      ⇒ 2026-11-17 线上抓包实测：动作帧里**根本没有 `detail` 对象**，
//      `mutations` / `decision` / `score` / `candidates` / `raw_model` / `ttfb_ms`
//      全部是**顶层平铺字段**。
//      而 REST 回退（`/api/admin/request-actions`）返回的是**嵌套 detail**。
//   ⇒ **只读 `action.detail` ⇒ 生产线上每个阶段的取值都是空**（阶段节点只剩名字，
//      审批拒绝判不出失败、注入命中判不出告警、最终模型退化成帧里的原始名）。
//      ⇒ 本层统一走 `actionDetail(action)`：**两种形状都吃**，嵌套优先、顶层补齐。

import type { ActionEvent, LiveRequest } from '../composables/liveStreamStore'
import {
  ALL_STAGES,
  RECV_STAGES,
  SEND_STAGES,
  type CacheConvention,
  type CacheRate,
  type ErrorSource,
  type ModelSource,
  type ResolvedModel,
  type StageBand,
  type StageKey,
  type StageNode,
  type StageState,
} from '../types/liveStageTree'

/**
 * 动作明细的**统一读取口**（两种线形状都吃）。
 *
 * ① 嵌套 `detail`（REST 回退形状）优先；
 * ② 顶层平铺字段（SSE 线上形状）补齐 detail 里没有的键。
 *
 * ★ 返回值一律是字符串（Go 侧 detail 本来就是 map[string]string；顶层字段
 *   可能是数字/布尔 ⇒ 这里统一 String()，避免调用方再做类型体操）。
 */
export function actionDetail(action: ActionEvent | undefined | null): Record<string, string> {
  if (!action) return {}
  const raw = action as unknown as Record<string, unknown>
  const out: Record<string, string> = {}
  const nested = raw.detail
  if (nested && typeof nested === 'object' && !Array.isArray(nested)) {
    for (const [k, v] of Object.entries(nested as Record<string, unknown>)) {
      if (v === null || v === undefined) continue
      out[k] = String(v)
    }
  }
  for (const [k, v] of Object.entries(raw)) {
    if (k === 'detail' || k in out) continue
    if (v === null || v === undefined || typeof v === 'object') continue
    out[k] = String(v)
  }
  return out
}

/** 是否是后端的**空闲标记**（idle_marker 心跳，不是真实请求）。 */
export function isIdleMarker(request: LiveRequest): boolean {
  if (request.type === 'idle_marker') return true
  // 兜底：有些回放路径只带 status='idle'（后端 idle_marker 的终态字段）。
  return (request.status ?? '') === ('idle' as LiveRequest['status'])
}


/**
 * 动作 → 阶段映射。未列出的动作（state_change / node_switch 等）不参与阶段点亮。
 *
 * ★ 映射是「这个动作证明哪一段被走过」的**证据映射**，不是「动作发生在哪一段」
 * 的时间映射（两者恰好一致，因为 seq 单调）。
 */
const ACTION_STAGE: Readonly<Record<string, StageKey>> = {
  arrive: 'auth',
  sanitize_applied: 'sanitize',
  approval_decided: 'approval',
  injection_scored: 'injection',
  route_resolved: 'route',
  model_enqueued: 'route',
  // ★ tools 还原**不是独立一列**：它发生在「压缩之后、出站之前」⇒
  //   是压缩节点的徽标（「tools 回填 n」），主档 §5.2 附注已写死这条。
  tools_restored: 'compress',
  credential_selected: 'provider',
  node_enqueued: 'provider',
  node_selected: 'provider',
  upstream_request: 'provider',
  first_byte: 'first_byte',
  reply: 'reply',
}

/** 阶段在 ALL_STAGES 里的下标（用于「失败下游 ⇒ 未到达」）。 */
const STAGE_INDEX: Readonly<Record<StageKey, number>> = ALL_STAGES.reduce(
  (acc, s, i) => {
    acc[s.key] = i
    return acc
  },
  {} as Record<StageKey, number>,
)

function num(v: string | undefined): number | null {
  if (v === undefined || v === null || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) ? n : null
}

/** 动作按 seq 排序后的视图（store 已排序，这里再排一次以防调用方乱传）。 */
function sortedActions(actions: ActionEvent[] | undefined): ActionEvent[] {
  if (!actions || actions.length === 0) return []
  return actions.slice().sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0))
}

/** 取最后一个指定动作（seq 序）。 */
function lastAction(ordered: ActionEvent[], name: string): ActionEvent | undefined {
  for (let i = ordered.length - 1; i >= 0; i--) {
    if (ordered[i].action === name) return ordered[i]
  }
  return undefined
}

/** 该请求是否还在途（未终态）。 */
export function isInFlight(request: LiveRequest): boolean {
  const s = request.status ?? ''
  return !s || s === 'in_progress'
}

/**
 * 失败发生在哪一段。
 *
 * 判据（按证据强度从强到弱）：
 *  1. 帧上 failure_stage=upstream 且已转发 ⇒ 供应商/上游段；
 *  2. no_route 动作 ⇒ 路由段（没有可用路由）；
 *  3. 审批动作 decision=block ⇒ 审批段（请求被策略拒绝）；
 *  4. reply 动作 status=failure ⇒ 回复段；
 *  5. 已转发但无 reply ⇒ 供应商段（上游超时/断流）；
 *  6. 已解析路由但未转发 ⇒ 路由/排队段；
 *  7. 其它 ⇒ 鉴权段（没进到路由就挂了）。
 *
 * ★ 只有**失败态**才定位（in_progress / success 不算失败）。
 */
export function failureStageOf(request: LiveRequest, actions: ActionEvent[]): StageKey | null {
  const status = request.status ?? ''
  if (status !== 'failure' && status !== 'rate_limited') return null
  const ordered = sortedActions(actions)
  const has = (a: string) => ordered.some(x => x.action === a)
  if (request.failure_stage === 'upstream' && has('upstream_request')) return 'provider'
  if (has('no_route')) return 'route'
  const approval = ordered.find(x => x.action === 'approval_decided')
  if (actionDetail(approval).decision === 'block') return 'approval'
  const reply = ordered.find(x => x.action === 'reply')
  const replyStatus = actionDetail(reply).status
  if (reply && replyStatus && replyStatus !== 'success') return 'reply'
  if (has('upstream_request')) return 'provider'
  if (has('route_resolved') || has('model_enqueued')) return 'route'
  return 'auth'
}

/**
 * 缓存命中率（**带口径**）。
 *
 * ★ 口径判据必须与生产逐字对齐（domains/streaming/usage.go 的
 *   `CacheTokenConventionForProtocol`）：
 *   · `anthropic-messages` ⇒ **并列口径**：input_tokens 不含 cache_read
 *     ⇒ 分母 = prompt + cache_read；
 *   · `openai-*` ⇒ **包含口径**：prompt_tokens 已含 cached_tokens
 *     ⇒ 分母 = prompt（211 实测 openai-completions 的 cache>prompt 违例 0.0%）；
 *   · 其它 / 空 / 未知 ⇒ **不猜**（生产对未知协议同样返回 unset）
 *     ⇒ 口径不可判定，显示「—」而不是 0%。
 *
 * 护栏：包含口径下算出 >100% ⇒ 说明两个口径混用了（2026-10-06 真库事故同族：
 * 1628 行负成本 / cache 是 prompt 的 25 倍）⇒ 报「口径异常」，不显示假百分比。
 */
export function cacheRateOf(request: LiveRequest): CacheRate | null {
  const read = request.cache_read_tokens ?? 0
  if (!read) return null // 上游没报 cache_read ⇒ 无命中率（**不是 0%**）
  const proto = (request.upstream_protocol ?? '').trim().toLowerCase()
  const prompt = request.prompt_tokens ?? 0
  if (proto === 'anthropic-messages') {
    const denom = Math.max(1, prompt + read)
    return {
      pct: Math.round((read / denom) * 100),
      convention: 'parallel_anthropic',
      why: 'parallel',
      anomaly: false,
    }
  }
  if (proto.startsWith('openai')) {
    const denom = Math.max(1, prompt)
    const pct = Math.round((read / denom) * 100)
    if (pct > 100) {
      return {
        pct: null,
        convention: 'subset_openai',
        why: 'anomaly',
        anomaly: true,
      }
    }
    return { pct, convention: 'subset_openai', why: 'subset', anomaly: false }
  }
  return { pct: null, convention: 'unknown', why: 'unknownProtocol', anomaly: false }
}

/**
 * 模型解析结果（客户端要什么 → 最终用了什么）。
 *
 * ★ 生产口径（domains/streaming/handler.go）：
 *   auto 请求的 `client_model` 在 handler 侧**已被改写成选中的模型**
 *   （preAutoModel 只进 requestjourney、不上帧）⇒
 *   「客户端要的是 auto」只能由 `is_auto_request` 表达，
 *   **不能靠 client_model=="auto" 反推**。
 *
 * ★ 最终匹配模型的取值链（2026-11-16 对生产发射点逐条核过）：
 *   1. `node_selected` 动作的 `Model` = `qr.resolvedModel()`
 *      —— **路由/auto 解析后的标准模型名**，是「最终匹配模型」的第一手证据；
 *   2. 请求帧的 `canonical_name` —— 持久化的标准模型名（终态才有）；
 *   3. `upstream_request` 动作 detail 的 `raw_model`
 *      —— 实际下发上游的模型串（可能是供应商侧原始名）；
 *   4. 请求帧的 `model` —— 最后兜底。
 *
 *   ⚠️ **不能用 `route_resolved` 动作的 `Model`**：那一路的 `Model` 填的是
 *   `clientModel`（**客户端请求的模型名**），不是解析后的模型 ——
 *   拿它当「最终匹配模型」会把 auto 请求显示成「auto 选了 auto」。
 */
export function resolveModelOf(request: LiveRequest, actions: ActionEvent[]): ResolvedModel {
  const ordered = sortedActions(actions)
  const routeAction = lastAction(ordered, 'route_resolved')
  const nodeAction = lastAction(ordered, 'node_selected')
  const upstreamAction = lastAction(ordered, 'upstream_request')
  const switchAction = lastAction(ordered, 'model_switch')

  const routeDetail = actionDetail(routeAction)
  const upstreamDetail = actionDetail(upstreamAction)

  let final = ''
  let source: ModelSource = 'none'
  const nodeModel = (nodeAction?.model ?? '').trim()
  if (nodeModel) {
    final = nodeModel
    source = 'action'
  } else {
    const canonical = (request.canonical_name ?? '').trim()
    if (canonical) {
      final = canonical
      source = 'canonical'
    } else {
      const raw = (upstreamDetail.raw_model ?? '').trim()
      if (raw) {
        final = raw
        source = 'upstream_raw'
      } else {
        const frameModel = (request.model ?? '').trim()
        if (frameModel) {
          final = frameModel
          source = 'frame'
        }
      }
    }
  }

  const isAuto = !!request.is_auto_request
  // auto 请求的 client_model 已被改写 ⇒ 不能拿它当「客户端要的名字」。
  const requested = isAuto ? '' : (request.client_model ?? '').trim() || final
  const switchDetail = actionDetail(switchAction)
  const switched =
    switchDetail.from_model && switchDetail.to_model
      ? { from: switchDetail.from_model, to: switchDetail.to_model }
      : null

  return {
    requested,
    isAuto,
    taskType: (routeDetail.auto_task_type ?? '').trim() || (request.task_type ?? '').trim(),
    autoConfidence: num(routeDetail.auto_confidence),
    final,
    upstream: (upstreamDetail.raw_model ?? '').trim(),
    source,
    reason: isAuto ? 'auto' : routeDetail.candidates ? 'routed' : 'direct',
    switched: switched && switched.from && switched.to ? switched : null,
    candidates: num(routeDetail.candidates),
  }
}

/** 单段的状态与取值（由该段的**全部**动作推导；第一个动作＝到达信号）。 */
function stageFromActions(
  key: StageKey,
  list: ActionEvent[],
  ctx: { request: LiveRequest; model: ResolvedModel },
): StageNode {
  const first = list[0]
  const d = actionDetail(first)
  const durMs = num(d.dur_ms)
  switch (key) {
    case 'auth': {
      // arrive 动作证明请求**已通过鉴权**（它在 AuthMiddleware 之内侧发射）。
      return {
        key,
        state: 'done',
        value: d.client_protocol ?? '',
        detail: d.method ?? '',
        durMs: null,
      }
    }
    case 'sanitize':
      return { key, state: 'done', value: d.mutations ?? '', detail: '', durMs }
    case 'approval': {
      const decision = d.decision ?? ''
      // block ⇒ 请求被拒（失败在审批段）；need_approval ⇒ 挂起等人工（进行中）；
      // pass ⇒ 放行。
      const state: StageState =
        decision === 'block' ? 'failed' : decision === 'need_approval' ? 'active' : 'done'
      return { key, state, value: decision, detail: d.approval_id ?? '', durMs }
    }
    case 'injection': {
      const decision = d.decision ?? ''
      // v1 是 observe-only：命中也**不拦截** ⇒ 命中记「告警」不记「失败」。
      const state: StageState = decision === 'warn' || decision === 'block' ? 'warned' : 'done'
      return { key, state, value: d.score ?? '', detail: decision, durMs }
    }
    case 'route': {
      const rr = list.find(a => a.action === 'route_resolved') ?? first
      const rd = actionDetail(rr)
      const autoTask = (rd.auto_task_type ?? '').trim()
      const cand = (rd.candidates ?? '').trim()
      // ★ 设计口径（原型 bandInner 注释：「路由节点的 v = **最终匹配模型**（canonical）
      //   —— 路由才是「决定用哪个模型」的那一步」）：
      //   路由这一格回答「选了哪个模型」；候选数另走字段（candidates），
      //   在 tooltip 与摘要行的「N 候选」chip 上出现，不挤占主值。
      return {
        key,
        state: 'done',
        value: ctx.model.final,
        detail: autoTask ? `auto:${autoTask}` : '',
        durMs: num(rd.dur_ms),
        candidates: cand === '' ? null : num(cand),
      }
    }
    case 'restore': {
      // ★ 这一段是**响应侧的脱敏占位符回填**（`SanitizeRestoreInterceptor`
      //   挂在响应链上 ⇒ 必然在首字节之后 ⇒ 属接收轨）。
      //   本轮**没有为它打点** ⇒ 有脱敏时如实显示「未测量」，不假装跑过；
      //   输入侧没有脱敏 ⇒ 压根没有占位符要回填 ⇒ 「不适用」。
      //   （推导在 deriveStages 里做，因为这一段**没有对应动作**。）
      return { key, state: 'unmeasured', value: '', detail: '', durMs: null }
    }
    case 'provider': {
      // ★ 供应商/令牌池这一格回答「落到哪家供应商、哪个池」（设计：节点值＝
      //   供应商名 + 凭据徽标）；「用了哪个模型」是**路由**那一格的职责，
      //   这里放到 detail（model=…）里，供 tooltip 核对，不与路由格重复。
      const ns = lastAction(list, 'node_selected')
      const up = lastAction(list, 'upstream_request')
      const cs = list.find(a => a.action === 'credential_selected')
      const model = (ns?.model ?? '').trim() || (actionDetail(up).raw_model ?? '').trim()
      const tier = (actionDetail(cs).tier ?? '').trim()
      const attempt =
        (actionDetail(ns).attempt ?? actionDetail(up).attempt ?? '').trim()
      const detail = [
        model ? `model=${model}` : '',
        tier ? `tier:${tier}` : '',
        attempt ? `attempt:${attempt}` : '',
      ]
        .filter(Boolean)
        .join(' · ')
      return { key, state: 'done', value: providerLabelOf(ctx.request), detail, durMs: null }
    }
    case 'first_byte':
      return { key, state: 'done', value: d.ttfb_ms ?? '', detail: '', durMs: null }
    case 'reply': {
      const st = d.status ?? ''
      return {
        key,
        state: st && st !== 'success' ? 'failed' : 'done',
        value: st,
        detail: '',
        durMs: num(d.latency_ms),
      }
    }
    default:
      return { key, state: 'done', value: '', detail: '', durMs: null }
  }
}

/** 压缩段（**帧派生**：压缩结论只在终态帧上，无对应动作）。 */
function compressStage(request: LiveRequest, inFlight: boolean, reached: boolean): StageNode {
  const base: StageNode = { key: 'compress', state: 'pending', value: '', detail: '', durMs: null }
  // 会话压缩器块的入口条件含 `gwSessionID != ""` ⇒ 没有会话 ⇒ 这一段不适用。
  const sessionId = (request.gw_session_id ?? '').trim()
  if (!sessionId) {
    return { ...base, state: 'skipped' }
  }
  if (inFlight) {
    // 在途请求的压缩结论还没落终态帧 ⇒ 状态可判、值未测量。
    return { ...base, state: reached ? 'done' : 'pending' }
  }
  // 终态：compressed=true ⇒ 压缩层改写过出站体；
  // 缺省 ⇒ 压缩层**没有改写**出站体（strategy 只在非空时写入）。
  const band = (request.token_band ?? '').trim()
  return {
    key: 'compress',
    state: 'done',
    value: request.compressed === true ? 'yes' : 'no',
    detail: band,
    durMs: null,
  }
}

/** 计量段（**帧派生**：token/成本只在终态帧上，无对应动作）。 */
function meterStage(request: LiveRequest, inFlight: boolean): StageNode {
  const base: StageNode = { key: 'meter', state: 'pending', value: '', detail: '', durMs: null }
  if (inFlight) return base
  if (request.total_tokens != null) {
    return {
      key: 'meter',
      state: 'done',
      value: String(request.total_tokens),
      detail: request.cost_usd != null ? String(request.cost_usd) : '',
      durMs: null,
    }
  }
  // 终态但没上报 token ⇒ 未测量（不是 0）。
  return { ...base, state: 'unmeasured' }
}

/**
 * 推导一条请求带的全部阶段状态。
 *
 * 判定顺序（对每一段）：
 *  1. 失败段之后 ⇒ `pending`（未到达）；
 *  2. 有动作信号 ⇒ 动作决定的状态 + 取值 + 耗时；
 *  3. 压缩/计量两段没有动作，改由**请求帧**推导（它们是终态快照字段）；
 *  4. 在途请求：第一个「越过但无信号」的段 ⇒ `active`（请求正停在这里）；
 *  5. 请求已经越过这一段（后面有信号）但这一段没信号 ⇒ `unmeasured`（未测量）；
 *  6. 请求还没越过这一段 ⇒ `pending`（未到达）。
 */
export function deriveStages(
  request: LiveRequest,
  actions: ActionEvent[],
  failedStage: StageKey | null,
  model?: ResolvedModel,
): StageNode[] {
  const ordered = sortedActions(actions)
  const inFlight = isInFlight(request)
  // ★ 路由/供应商两格的取值依赖「最终匹配模型」的解析结果 ⇒ 由调用方传入，
  //   避免同一份数据解析两遍、也避免两处口径漂移。
  const resolvedModel: ResolvedModel = model ?? resolveModelOf(request, ordered)

  // 每段的动作（seq 序）。
  const byStage = new Map<StageKey, ActionEvent[]>()
  for (const a of ordered) {
    const key = a.action ? ACTION_STAGE[a.action] : undefined
    if (!key) continue
    const list = byStage.get(key)
    if (list) list.push(a)
    else byStage.set(key, [a])
  }

  // 「请求已经越过到哪一段」= 有信号的最靠后阶段下标。
  let reached = -1
  for (const s of ALL_STAGES) {
    if (byStage.has(s.key)) reached = Math.max(reached, STAGE_INDEX[s.key])
  }
  // 压缩/计量虽无动作，但终态帧能证明它们跑过（压缩在路由后、计量在回复前）。
  if (!byStage.has('compress') && !inFlight && request.compressed !== undefined) {
    reached = Math.max(reached, STAGE_INDEX.compress)
  }
  if (!byStage.has('meter') && !inFlight && request.total_tokens != null) {
    reached = Math.max(reached, STAGE_INDEX.meter)
  }
  // 终态成功 ⇒ 整条管线都走过（哪怕动作被淘汰了）。
  if (!inFlight && request.status === 'success') {
    reached = Math.max(reached, ALL_STAGES.length - 1)
  }
  const failedIndex = failedStage ? STAGE_INDEX[failedStage] : -1
  if (failedIndex >= 0) reached = Math.max(reached, failedIndex)

  // 在途请求的「当前所在段」= 第一个越过但无信号的段。
  let activeIndex = -1
  if (inFlight) {
    for (let i = 0; i < ALL_STAGES.length; i++) {
      if (i > reached && !byStage.has(ALL_STAGES[i].key)) {
        activeIndex = i
        break
      }
    }
  }

  return ALL_STAGES.map((def, idx) => {
    // ① 失败段之后 ⇒ 未到达（失败节点自己红，下游不是「也失败」）。
    if (failedIndex >= 0 && idx > failedIndex) {
      return { key: def.key, state: 'pending', value: '', detail: '', durMs: null }
    }
    // ② 压缩段：**帧派生**（压缩结论只在终态帧上）+ tools 还原徽标（动作）。
    //    ★ 必须排在「有动作信号」之前 —— 它有动作（tools_restored），
    //      但压缩本身的结论来自帧，不能被动作分支盖掉。
    if (def.key === 'compress') {
      const node = compressStage(request, inFlight, reached >= idx)
      const tools = (byStage.get('compress') ?? []).find(a => a.action === 'tools_restored')
      if (!tools) return node
      const td = actionDetail(tools)
      const toolsCount = (td.tools_count ?? '').trim()
      const restored = (td.restored ?? '').trim()
      const badge = [
        node.detail,
        toolsCount ? `tools:${toolsCount}` : '',
        restored === '1' ? 'restored' : '',
      ]
        .filter(Boolean)
        .join(' · ')
      return { ...node, state: node.state === 'skipped' ? 'done' : node.state, detail: badge }
    }
    // ③ 还原段（响应侧脱敏占位符回填）：**本轮未打点** ⇒ 按输入侧脱敏结论
    //    推导「适用性」，有脱敏时如实显示「未测量」。
    if (def.key === 'restore') {
      // 在途请求正停在响应侧这一段（首字节已到、回填进行中）⇒ 进行中。
      if (idx === activeIndex) {
        return { key: def.key, state: 'active', value: '', detail: '', durMs: null }
      }
      if (reached < idx) {
        return { key: def.key, state: 'pending', value: '', detail: '', durMs: null }
      }
      const sanitize = (byStage.get('sanitize') ?? []).find(a => a.action === 'sanitize_applied')
      const mutations = (actionDetail(sanitize).mutations ?? '').trim()
      // 输入侧没有脱敏 ⇒ 压根没有占位符要回填 ⇒ 不适用。
      if (sanitize && (mutations === '' || mutations === '0')) {
        return { key: def.key, state: 'skipped', value: '', detail: '', durMs: null }
      }
      return { key: def.key, state: 'unmeasured', value: '', detail: '', durMs: null }
    }
    // ④ 有动作信号。
    const list = byStage.get(def.key)
    if (list && list.length > 0) {
      const s = stageFromActions(def.key, list, { request, model: resolvedModel })
      // ★ 失败段**自己**必须显示为失败：有信号不代表这一段成功（例：转发动作
      //   存在，但请求在上游报错了 ⇒ 这一段就是失败点）。
      //   例外：信号本身已是「有告警」⇒ 保留告警（注入检测 observe-only 命中）。
      if (failedIndex >= 0 && idx === failedIndex && s.state !== 'warned') {
        return { ...s, state: 'failed' }
      }
      return s
    }
    // ⑤ 失败点本身没有信号（例：鉴权段被拒）⇒ 失败。
    if (failedIndex >= 0 && idx === failedIndex) {
      return { key: def.key, state: 'failed', value: '', detail: '', durMs: null }
    }
    // ⑥ 计量段：终态帧字段。
    if (def.key === 'meter') {
      return meterStage(request, inFlight)
    }
    // ⑦ 在途请求正停在这一段。
    if (idx === activeIndex) {
      return { key: def.key, state: 'active', value: '', detail: '', durMs: null }
    }
    // ⑧ 请求已越过这一段但没有信号 ⇒ 未测量（不是「未到达」）。
    if (reached >= idx) {
      return { key: def.key, state: 'unmeasured', value: '', detail: '', durMs: null }
    }
    // ⑨ 还没跑到。
    return { key: def.key, state: 'pending', value: '', detail: '', durMs: null }
  })
}

/** 客户端显示名（agent_name → agent_type → 协议 → 未知）。 */
export function clientNameOf(request: LiveRequest): string {
  return (
    (request.agent_name ?? '').trim() ||
    (request.agent_type ?? '').trim() ||
    (request.client_profile ?? '').trim() ||
    (request.client_protocol ?? '').trim() ||
    '—'
  )
}

/** 客户端类型（web/cli/api/bot/内部…；无则用 agent_name）。 */
export function clientTypeOf(request: LiveRequest): string {
  return (
    (request.agent_type ?? '').trim() ||
    (request.agent_name ?? '').trim() ||
    (request.client_profile ?? '').trim() ||
    '—'
  )
}

/** 供应商 / 凭据显示名。 */
export function providerLabelOf(request: LiveRequest): string {
  const cred = (request.credential_label ?? '').trim()
  const provider = (request.provider_code ?? '').trim()
  if (provider && cred) return `${provider} · ${cred}`
  return provider || cred || '—'
}

/**
 * 错误来源（owner「在最右边显示错误源头及类型」的「源头」那一半）。
 *
 * 判据（按证据强度）：
 *  1. 帧上 `failure_stage=upstream` ⇒ **供应商**；
 *  2. error_kind 属于上游族（超时 / 限流 / 配额 / 断流）⇒ **供应商**；
 *  3. 其它有 error_kind ⇒ **网关**（策略拒绝、鉴权失败、限流、路由不到…）；
 *  4. 有失败但说不清 ⇒ **其它**（不硬塞进网关/供应商）。
 */
export function errorSourceOf(
  request: LiveRequest,
  errorKind: string,
): ErrorSource {
  if (!errorKind) return ''
  if ((request.failure_stage ?? '').toLowerCase() === 'upstream') return 'provider'
  if (UPSTREAM_ERROR_KINDS.has(errorKind)) return 'provider'
  if ((request.failure_stage ?? '').toLowerCase() === 'gateway') return 'gateway'
  return 'gateway'
}

/** 归到「供应商侧」的错误码族（上游超时/限流/配额/断流/5xx）。 */
const UPSTREAM_ERROR_KINDS = new Set([
  'upstream_timeout',
  'upstream_rate_limited',
  'upstream_quota_permanent',
  'upstream_error',
  'upstream_connection_error',
  'upstream_status_5xx',
  'eof_without_done',
  'probe_timeout',
])

/**
 * 推导一条请求带的全部内容（阶段 + 模型 + 指标）。
 */
export function deriveStageBand(
  request: LiveRequest,
  actions: ActionEvent[],
): StageBand {
  const ordered = sortedActions(actions)
  const failedStage = failureStageOf(request, ordered)
  // ★ 模型先解析：路由格显示「最终匹配模型」、供应商格显示「供应商 · 凭据」，
  //   两格都依赖它 ⇒ 解析一次、两处共用（避免口径漂移）。
  const model = resolveModelOf(request, ordered)
  const stages = deriveStages(request, ordered, failedStage, model)
  const cache = cacheRateOf(request)
  const ttfbAction = ordered.find(a => a.action === 'first_byte')
  const errorKind = (request.error_kind ?? '').trim()

  // 排队时长：模型入队 → 选中节点（两个动作的时间戳之差）。
  const enqueued = ordered.find(a => a.action === 'model_enqueued')
  const selected = lastAction(ordered, 'node_selected')
  const enqueuedAt = Date.parse(enqueued?.ts ?? '')
  const selectedAt = Date.parse(selected?.ts ?? '')
  const queueMs =
    Number.isFinite(enqueuedAt) && Number.isFinite(selectedAt) && selectedAt >= enqueuedAt
      ? selectedAt - enqueuedAt
      : null

  return {
    requestId: request.request_id ?? '',
    clientName: clientNameOf(request),
    clientType: clientTypeOf(request),
    clientProtocol: (request.client_protocol ?? '').trim(),
    stages,
    model,
    cache,
    chunks: null,
    ttfbMs: num(actionDetail(ttfbAction).ttfb_ms),
    latencyMs: request.latency_ms ?? null,
    totalTokens: request.total_tokens ?? null,
    costUsd: request.cost_usd ?? null,
    providerCode: (request.provider_code ?? '').trim(),
    providerLabel: providerLabelOf(request),
    credentialLabel: (request.credential_label ?? '').trim(),
    credentialId: typeof request.credential_id === 'number' ? request.credential_id : null,
    rawModel: model.upstream,
    errorKind,
    errorSource: errorSourceOf(request, errorKind),
    failedStage,
    attempts: ordered.filter(a => a.action === 'upstream_request').length,
    queueMs,
    promptTokens: request.prompt_tokens ?? null,
    completionTokens: request.completion_tokens ?? null,
    cacheReadTokens: request.cache_read_tokens ?? null,
    sessionId: (request.gw_session_id ?? '').trim(),
    isProbe: !!request.is_probe,
    inFlight: isInFlight(request),
    tsMs: Date.parse(request.ts ?? '') || 0,
  }
}

/** 发送轨 / 接收轨的阶段切分（组件按轨渲染）。 */
export function splitTracks(stages: StageNode[]): { send: StageNode[]; recv: StageNode[] } {
  const byKey = new Map(stages.map(s => [s.key, s]))
  return {
    send: SEND_STAGES.map(d => byKey.get(d.key)!).filter(Boolean),
    recv: RECV_STAGES.map(d => byKey.get(d.key)!).filter(Boolean),
  }
}

export type { CacheConvention, CacheRate, ModelSource, ResolvedModel, StageBand, StageKey, StageNode, StageState }
