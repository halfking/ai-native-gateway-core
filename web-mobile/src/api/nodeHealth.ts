import { req, type RequestOptions } from './client'
import {
  unwrapNodeRecoveryTimeline,
  nodeTimelineCredentialIdInvalid,
  type NodeRecoveryEvent,
  type NodeRecoveryTimelineResponse,
} from './nodeHealthTimeline'

// nodeHealth.ts — 单凭据的健康事件时间线（视图层适配器，2026-10-08 第一百零二批重写）。
//   GET /api/admin/node-health/{credential_id}/timeline
//
// 鉴权：`admin(h.handleNodeHealthTimeline)`（admin/handler.go:1028）= AdminMiddleware
// ⇒ tenant_admin 可用。
//
// ⚠️⚠️⚠️⚠️ **本文件曾经是另一个模块的「劣质副本」，现已改成它的薄适配层。**
//
// 第七十三批写过一个 `nodeHealthTimeline.ts`（453 行、30+ 导出、**27 KB 判据**），
// 它从 `admin/node_health.go` 逐行推出契约；**而本文件是更早写的一个 115 行薄版**，
// 只对同一个端点做了**裸类型强转、零校验、零判据**。
// 视图一直引的是**本文件**，第七十三批那个模块因此成了**孤儿**（`orphanLedger.spec.ts` 记着）。
//
// ⇒ ★★★★ **孤儿那个才是对的，被用的那个是错的**，而且错在两处**用户可见**的地方：
//
//   (1) ★★★★★ **`isRecoveryEvent` 漏判 `reconnected`。**
//       `admin/node_health.go:81-88` 的 `mapProbeRunToEvent` 只产生三个值：
//       ```go
//       eventType := "failed"
//       if row.Success {
//           eventType = "recovered"
//           if row.TriggerKind == "credential_recovery" { eventType = "reconnected" }
//       }
//       ```
//       ⇒ `reconnected` 是 `Success` 的**子分支**，它是**成功**事件，
//         而且**只**在「强制恢复」触发时出现（`trigger_kind = 'credential_recovery'`）。
//       ⇒ ★★★ 而旧实现写的是 `recovered | recovery | healthy`：
//         `recovery` / `healthy` **后端根本不产生**，`reconnected` **却没列进去**。
//       ⇒ ⇒ ★★★★ 后果：**一次成功的强制恢复被渲染成 warning / danger 徽章。**
//         `NodeHealthView.vue:128` 直接拿 `eventTone(e)` 决定徽章颜色 ⇒ **线上活的缺陷**。
//       ⇒ 为什么没被发现：本文件**一个判据文件都没有**，而那个 27 KB 的判据在孤儿模块上。
//
//   (2) ★★★ **`events` 被声明成 `NodeRecoveryEvent[] | null`。**
//       后端 `admin/node_health.go:167-169` 显式 `if events == nil { events = []nodeRecoveryEvent{} }`
//       ⇒ **恒为数组，0 条是 `[]` 不是 `null`** ⇒ 旧的 `resp.events ?? []` 是对不存在情况的防御。
//
// ⇒ ⇒ ★★★ **本批的处理不是「把孤儿接上」，也不是「删掉其中一份」**，
//   而是**让薄的那份去调厚的那个**：下面 `fetchNodeHealthTimeline` 已改成
//   `req<unknown>` + `unwrapNodeRecoveryTimeline`，与 `nodeHealthTimeline.ts` 共用同一套校验。
//   ⇒ 孤儿因此变成被引用者，棘轮清单同步删一行；两份互相矛盾的契约也合并成一份。
//
// ⚠️ 保留在本文件里的只有**视图层的便利函数**（`buildSince` / `credentialIdOf` /
//   `eventTone` / 三个常量），因为 `NodeHealthView.vue` 按这些名字引，且
//   `nodeHealthTimeline.ts` 没有 1:1 的对应物（`eventTone` 尤其没有）。

/** 后端默认 / 上限（node_health.go:20-22）。 */
export const NODE_HEALTH_DEFAULT_SINCE_HOURS = 24
export const NODE_HEALTH_MAX_SINCE_HOURS = 7 * 24
/** 单次返回上限（node_health.go:22），SQL 里 LIMIT 200。 */
export const NODE_HEALTH_EVENT_CAP = 200

export type { NodeRecoveryEvent, NodeRecoveryTimelineResponse }

export interface NodeHealthTimelineParams {
  /**
   * Go duration 字符串（"24h"）或 `Nd`（"7d"）。
   * 超过 7d 会被后端**静默 clamp**（`node_health.go:64-66`），所以这里先夹到上限再发。
   * ⚠️ 与热力图相反：热力图超 7d 是 **400**（`credential_heatmap.go:136-139`），
   *   这里是无提示 clamp ⇒ 前端必须自己夹，且界面要显示**实际生效**的范围。
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
  const id = Math.trunc(credentialId ?? 0)
  // ★ 复用厚的那个模块的 id 校验：非法 id 在本地就被拦下，
  //   不必发出去换一个 400 "invalid credential_id"（`admin/node_health.go:146-149`）。
  // ⚠️★ `nodeTimelineCredentialIdInvalid(id: unknown)` 的入参判的是 `typeof id !== 'number'`
  //   ⇒ 必须传**数字**。第一版这里先 `String(...)` 再传，于是**每次取数都被本地误拒**，
  //   是判据 `★ 完整响应被解出` 当场抓到的。
  const invalid = nodeTimelineCredentialIdInvalid(id)
  if (invalid !== null) return Promise.reject(new Error(invalid))
  const qs = new URLSearchParams()
  if (params?.since) qs.set('since', params.since)
  const s = qs.toString()
  // ★★ `req<unknown>` + `unwrap…` —— 旧的 `req<NodeRecoveryTimelineResponse>` 是裸强转，
  //   后端少一个键或多一个键都不会被发现。
  return req<unknown>(
    'GET',
    `/api/admin/node-health/${id}/timeline${s ? '?' + s : ''}`,
    undefined,
    options,
  ).then((r) => unwrapNodeRecoveryTimeline(r))
}

/**
 * ★★★★★ 事件是否代表「成功」——
 *
 * **只认 `recovered` 与 `reconnected` 两个值**（`admin/node_health.go:84-88`）。
 * ⚠️ 旧实现写的是 `recovered | recovery | healthy`：`recovery` / `healthy` 后端不产生，
 * 而真正会出现的 `reconnected` 恰恰漏掉 ⇒ **成功的强制恢复被判成失败**。
 *
 * ★ 用于配色，不用于下结论（值域校验由 `unwrapNodeRecoveryTimeline` 负责）。
 */
export function isRecoveryEvent(e: NodeRecoveryEvent): boolean {
  return e.event_type === 'recovered' || e.event_type === 'reconnected'
}

export function eventTone(e: NodeRecoveryEvent): 'success' | 'warning' | 'danger' | 'muted' {
  // ★★★ 顺序要紧：成功判定必须在前面。`reconnected` 通常**没有** `reason_code`
  //   （`mapProbeRunToEvent:104-107` 只在有 err code 时才写），
  //   所以旧实现里它一路落到 `return 'warning'`。
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