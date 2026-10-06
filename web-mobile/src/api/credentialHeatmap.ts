import { req, type RequestOptions } from './client'

// credentialHeatmap.ts — 凭据 × 模型 × 时间的热力图。
//   GET /api/credentials/heatmap
//
// 鉴权：monitorH.RegisterMonitorRoutes(mux, h.admin)（credential_monitor.go:169）
// ⇒ tenant_admin 可用。
//
// 它回答的是节点/供应商页都答不了的问题：「**这个模型在这段时间里，
// 落在各个凭据上的成功率各是多少**」。NodesView 告诉你「哪条路挂了」，
// 热力图告诉你「是哪个模型 × 哪个凭据的组合在坏」——排障时这两者经常指向
// 不同结论（例如某个模型整体 40% 失败，但分散在 3 条凭据上而不是集中在 1 条）。
//
// ⚠️ 四个会直接 400 的约束，全部必须在**发出请求前**处理：
//
// (1) `time_start` / `time_end` **必填**（:113-116）。缺一个就是
//     "time_start and time_end are required" —— 不是"用默认窗口"。
// (2) 窗口 **> 7d 直接 400**（:136-139）。
// (3) `granularity` 只能是 `1m|5m|15m|1h|1d`（granularitySeconds, :669-684），
//     非法值 400。
// (4) ★ 桶数 > 5000 也 400（:153-157）。这是最容易漏的一条：
//     7d × 1m = 10080 桶 > 5000 ⇒ **「选 7 天 + 1 分钟粒度」是一个必然失败的组合**。
//     ⇒ 粒度选项必须随窗口联动收窄，不能让用户自己撞。

export type HeatmapGranularity = '1m' | '5m' | '15m' | '1h' | '1d'

export const HEATMAP_GRANULARITIES: readonly HeatmapGranularity[] = ['1m', '5m', '15m', '1h', '1d'] as const

/** 后端硬上限（credential_monitor_heatmap.go:137-139）。 */
export const HEATMAP_MAX_WINDOW_HOURS = 7 * 24
/** 桶数硬上限（:153-157）。 */
export const HEATMAP_MAX_BUCKETS = 5000

/** 单桶秒数。与后端 granularitySeconds 一一对应。 */
const GRANULARITY_SECONDS: Record<HeatmapGranularity, number> = {
  '1m': 60,
  '5m': 300,
  '15m': 900,
  '1h': 3600,
  '1d': 86400,
}

/**
 * ★ 某个窗口下**允许**的粒度。
 *
 * 后端按「窗口 ÷ 粒度」算桶数，超过 5000 就 400。
 * 所以 UI 不能只按窗口长短短选粒度，必须按**桶数**筛。
 * 例：7d 窗口下最小可用粒度是 15m（7×24×4 = 672 桶 ✓），
 *     1m 会得到 10080 桶 ⇒ 必然 400。
 */
export function allowedGranularities(windowHours: number): HeatmapGranularity[] {
  return HEATMAP_GRANULARITIES.filter((g) => {
    const buckets = (windowHours * 3600) / GRANULARITY_SECONDS[g]
    return Math.floor(buckets) <= HEATMAP_MAX_BUCKETS
  })
}

export interface HeatmapNodeStatus {
  /**
   * 状态词表（HeatmapNodeStatus 注释，与 v_node_probe_state_compat 同源）：
   * healthy_confirmed | broken_confirmed | suspicious | probing
   * | unknown（手动下线）| unprobed（尚无状态行）
   */
  state: string
  routable: boolean
  last_direct_ok?: boolean
  last_err_code?: string
  last_attempt_at?: string
  next_retry_at?: string
  consecutive_failures: number
  consecutive_successes: number
  paused: boolean
}

export interface HeatmapBucket {
  time_bucket: string
  status: string
  total_requests: number
  success_count: number
  failed_count: number
  success_rate: number
  /** 指针 + 无 omitempty ⇒ 可能是显式 null（该桶无样本） */
  avg_latency_ms: number | null
  p95_latency_ms: number | null
  error_distribution: Record<string, number> | null
  sample_request_ids: string[] | null
}

export interface HeatmapModel {
  raw_model_name: string
  buckets: HeatmapBucket[] | null
  node_status?: HeatmapNodeStatus
}

export interface HeatmapCredential {
  credential_id: number
  label: string
  provider_name: string
  models: HeatmapModel[] | null
}

export interface HeatmapMeta {
  time_start: string
  time_end: string
  granularity: string
  bucket_count: number
  cache_hit: boolean
  generated_at: string
  expires_at: string
  duration_ms: number
}

export interface HeatmapResponse {
  meta: HeatmapMeta
  credentials: HeatmapCredential[] | null
}

export interface HeatmapParams {
  /** RFC3339，**必填** */
  time_start: string
  /** RFC3339，**必填** */
  time_end: string
  granularity?: HeatmapGranularity
  /** 逗号分隔；非法项后端静默丢弃（parseIntParam 默认 0） */
  credential_ids?: number[]
  /** 逗号分隔；后端统一转小写比较 */
  models?: string[]
  /**
   * ⚠️ 缺省 **true**。后端特意改成默认 true（credential_monitor_heatmap.go:106-111），
   * 因为此前 queryBool 缺省返回 false，会把自检流量算进服务质量。
   * ⇒ 移动端不传这个参数，跟着默认走；只有用户显式要看自检时才发 false。
   */
  exclude_self_test?: boolean
}

export function fetchCredentialHeatmap(
  params: HeatmapParams,
  options?: RequestOptions,
): Promise<HeatmapResponse> {
  const qs = new URLSearchParams()
  qs.set('time_start', params.time_start)
  qs.set('time_end', params.time_end)
  if (params?.granularity) qs.set('granularity', params.granularity)
  if (params?.credential_ids && params.credential_ids.length > 0) {
    qs.set('credential_ids', params.credential_ids.join(','))
  }
  if (params?.models && params.models.length > 0) qs.set('models', params.models.join(','))
  if (params?.exclude_self_test === false) qs.set('exclude_self_test', 'false')
  const s = qs.toString()
  return req<HeatmapResponse>('GET', `/api/credentials/heatmap?${s}`, undefined, options)
}

/**
 * 构造时间窗，并**按桶数上限选一个一定不会 400 的粒度**。
 *
 * ⚠️ 调用方若自己传粒度，非法组合仍会 400 —— 这个函数是给 UI 的默认值来源，
 *    不是替调用方兜底。理由：静默改掉用户选的粒度会让「我选的是 1m」
 *    和「图上其实是 15m」对不上，而排障时看错粒度会得出错误结论。
 *    ⇒ 改粒度必须由用户显式选，且选项本身已按窗口过滤。
 */
export function buildHeatmapWindow(
  hours: number,
  now = Date.now(),
): { time_start: string; time_end: string; hours: number } {
  const h = Math.max(1, Math.min(hours, HEATMAP_MAX_WINDOW_HOURS))
  return {
    time_end: new Date(now).toISOString(),
    time_start: new Date(now - h * 3600_000).toISOString(),
    hours: h,
  }
}

/** 当前窗口下可用的粒度里的**最细**那个（做默认值）。 */
export function finestAllowedGranularity(windowHours: number): HeatmapGranularity {
  const ok = allowedGranularities(windowHours)
  return ok[0] ?? '1d'
}

/** 状态词表 → 展示用色。未在词表内的一律当未知（muted），不当成功。 */
export function nodeStateTone(state: string | null | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  switch (state) {
    case 'healthy_confirmed':
      return 'success'
    case 'broken_confirmed':
      return 'danger'
    case 'suspicious':
    case 'probing':
      return 'warning'
    case 'unknown':
    case 'unprobed':
      return 'muted'
    default:
      return 'muted'
  }
}

/**
 * 桶的成功率。
 *
 * ⚠️ `total_requests === 0` 返回 null 而不是 0 —— 「没有样本」与
 * 「成功率 0%」是两件事，后者意味着**全部失败**。
 * 直接 `success/total` 会得到 NaN，用 `|| 0` 兜底则会把两者混为一谈。
 */
export function bucketRate(b: Pick<HeatmapBucket, 'total_requests' | 'success_count' | 'success_rate'> | null | undefined): number | null {
  if (!b) return null
  if (b.total_requests > 0 && Number.isFinite(b.success_rate)) return b.success_rate
  return null
}
