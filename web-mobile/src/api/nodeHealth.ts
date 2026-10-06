import { req, type RequestOptions } from './client'

// nodeHealth.ts — 单凭据的健康事件时间线。
//   GET /api/admin/node-health/{credential_id}/timeline
//
// 鉴权：`admin(h.handleNodeHealthTimeline)`（admin/handler.go:1028）= AdminMiddleware
// ⇒ tenant_admin 可用。
//
// 它和热力图是同一条线的两个视角：热力图看「一段时间的密度」，
// 时间线看「这一路最近到底发生了什么、什么时候恢复的」。
// 真源是 `node_probe_runs`（探测完成后落库的审计行），不返回 body/headers。
//
// ⚠️★★ **相邻两个端点对「超窗」的处理正好相反**，别互相照抄：
//
//   credential_heatmap.go:136-139  窗口 > 7d ⇒ **400**（硬失败）
//   node_health.go:64-66           since > 7d ⇒ **静默 clamp 到 7d**（无提示）
//
//   所以 `since` 传 "30d" 不会报错，只会**悄悄少给你 23 天**，
//   而界面若无其事地显示「30 天」，用户会以为看全了。
//   ⇒ 这里把上限显式夹住，并让 UI 如实显示实际生效的范围。
//
// ⚠️ 另一个坑：`since` 的格式是 **Go duration**（"24h" / "90m"）或 `Nd` 后缀，
//   **不是** RFC3339 时间戳（与热力图的 time_start/time_end 完全不同）。
//   解析失败或 <=0 ⇒ 400（node_health.go:69-73）。

/** 后端默认 / 上限（node_health.go:20-22）。 */
export const NODE_HEALTH_DEFAULT_SINCE_HOURS = 24
export const NODE_HEALTH_MAX_SINCE_HOURS = 7 * 24
/** 单次返回上限（node_health.go:22），SQL 里 LIMIT 200。 */
export const NODE_HEALTH_EVENT_CAP = 200

export interface NodeRecoveryEvent {
  /** ★ 字符串，不是 number —— 后端 `strconv.FormatInt` 后再序列化的。 */
  credential_id: string
  event_type: string
  occurred_at: string
  duration_ms?: number
  reason_code?: string
  note?: string
}

export interface NodeRecoveryTimelineResponse {
  /** ★ 同样是字符串。 */
  credential_id: string
  /** 后端目前恒为 "complete"；保留字段是因为它可能区分「没查到」与「查到但为空」。 */
  observation_status: string
  /** 后端显式初始化为 `[]`（node_health.go:167-169），0 条时是 `[]` 不是 null。 */
  events: NodeRecoveryEvent[] | null
}

export interface NodeHealthTimelineParams {
  /**
   * Go duration 字符串（"24h"）或 `Nd`（"7d"）。
   * 超过 7d 会被后端**静默 clamp**，所以这里先夹到上限再发。
   */
  since?: string
}

/**
 * 把小时数转成后端认的 `since` 串。
 *
 * ★ 24 的倍数用 `Nd`、其余用 Go duration —— 两者后端都认（node_health.go:58-68），
 *   但 `Nd` 走的是「天数」分支、会被 maxSince clamp；`Nh` 走 ParseDuration 分支、
 *   同样 clamp。统一在**前端**夹一次，界面显示的才和实际取到的一致。
 */
export function buildSince(hours: number): string {
  // ★ 非法输入必须**回落到默认 24h**，而不是被 Math.max(1, …) 夹成 1h。
  //   一个乱传进来的 -5 会让「看最近 1 小时」显示成「看最近 1 天」——
  //   界面标签与实际取数对不上，而这正是排障时最不能有的那种不一致。
  const raw = Math.trunc(hours)
  const h =
    !Number.isFinite(raw) || raw <= 0
      ? NODE_HEALTH_DEFAULT_SINCE_HOURS
      : Math.min(raw, NODE_HEALTH_MAX_SINCE_HOURS)
  if (h % 24 === 0) return `${h / 24}d`
  return `${h}h`
}

export function fetchNodeHealthTimeline(
  credentialId: number,
  params?: NodeHealthTimelineParams,
  options?: RequestOptions,
): Promise<NodeRecoveryTimelineResponse> {
  // 后端对非正数 / 含非数字的 credential_id 返回 400 "invalid credential_id"（:146-149）
  const id = String(Math.trunc(credentialId ?? 0))
  const qs = new URLSearchParams()
  if (params?.since) qs.set('since', params.since)
  const s = qs.toString()
  return req<NodeRecoveryTimelineResponse>('GET', `/api/admin/node-health/${id}/timeline${s ? '?' + s : ''}`, undefined, options)
}

/** 事件是否代表「恢复正常」。用于配色，不用于下结论。 */
export function isRecoveryEvent(e: NodeRecoveryEvent): boolean {
  return e.event_type === 'recovered' || e.event_type === 'recovery' || e.event_type === 'healthy'
}

export function eventTone(e: NodeRecoveryEvent): 'success' | 'warning' | 'danger' | 'muted' {
  if (isRecoveryEvent(e)) return 'success'
  if (e.reason_code) return 'danger'
  return 'warning'
}

/**
 * 把响应里的 `credential_id`（字符串）转回数字。
 *
 * ★ 返回 null 而不是 0：0 是「无凭据」的合法语义之外的值，
 *   拿它去拼 URL 会得到 400。null 让调用方显式处理「没有 id」而不是悄悄发一个错的。
 */
export function credentialIdOf(r: Pick<NodeRecoveryTimelineResponse, 'credential_id'> | null | undefined): number | null {
  if (!r) return null
  const raw = typeof r.credential_id === 'string' ? r.credential_id.trim() : String(r.credential_id ?? '').trim()
  if (raw === '') return null
  const n = Number(raw)
  return Number.isFinite(n) && n > 0 ? n : null
}
