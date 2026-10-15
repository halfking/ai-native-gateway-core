// dataHorizon.ts — 拉取「流量数据地平线」告示（2026-10-15 r1033 重写）。
//
// # 这个 API 回答什么
//
// 网关仍在接收请求时，session_turns（流量统计的系统记录）是否在记录新数据。
// 统计链（看板分钟表/看板 fallback/积分估算）全部由 session_turns 供数后，
// 这是唯一需要一条横幅让人看见的平台级失效。
//
// # 为什么单独一个端点而不是往每个响应里塞字段
//
// admin 的响应体是各端点自己的结构；往里加字段要改 20+ 处 struct，
// 而那 20+ 处就是 20+ 个可能漏改的地方，且漏改的那一个**没有任何门会报**。
// 一个独立端点 + 一个全局横幅，读起来也更清楚。
//
// # 这个 API 的形状是被后端契约钉死的，不要单方面改
//
// 后端 `admin/data_horizon.go` 的约定：
//
//	健康（含网关空闲） ⇒ { data_horizon: null }
//	断流               ⇒ { data_horizon: { stale: true, unknown: false, ... } }
//	**读不到**         ⇒ { data_horizon: { stale: false, unknown: true, ... } }
//
// 第三种是 §9.79.8 纪律的延续：判据查 DB，查不到就不能替数据背书。
//
// ⚠ 因此 `stale: false` **有合法实例**（就是 unknown 那一档），
// 但 `stale: false && unknown: false` 不会出现 —— 后端把那种情形返回成 null。
// ⇒ 前端判据是「键为 null ⇒ 健康」，**不是**「读 stale 字段」。
// 后者会把 unknown 误当成断流。
import { req } from './_core'

/** 与后端 DataHorizonNotice 一一对应。 */
export interface DataHorizonNotice {
  /**
   * true = **确证断流**：网关近期有请求而 session_turns 未记录。
   * false 时必须看 `unknown`：false + unknown:true 是「读点没给出答案」，
   * 不是「数据新鲜」。false + unknown:false 不会由后端产出（那种情形返回 null）。
   */
  stale: boolean
  /**
   * true = session_turns 读取失败。
   * ⇒ **不能**被读成「数据是新的」，**也不能**被读成「已断流」。
   */
  unknown: boolean
  /** 被检查的数据源族，如 "session_turns_hot"。 */
  source: string
  /** 一句话说明「看到这些数字意味着什么」。 */
  effect: string
  /** 最要紧的一句：这类断流没有错误信号。 */
  silence: string
  /** 排查方向（给运维）。 */
  hint: string
}

export interface DataHorizonResponse {
  data_horizon: DataHorizonNotice | null
  '//': string
}

/**
 * 取当前流量数据地平线告示。
 *
 * 失败时**抛**而不是静默返回「健康」：一个取不到告示的页面，
 * 如果按「没取到 = 没断流」处理，就等于在断流期间把滞后数字当正常展示。
 * 静默降级成「健康」是这里唯一真正危险的错误方向。
 */
export async function getDataHorizon(): Promise<DataHorizonResponse> {
  return req<DataHorizonResponse>('GET', '/api/admin/data-horizon')
}
