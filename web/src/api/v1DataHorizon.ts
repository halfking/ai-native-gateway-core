// v1DataHorizon.ts — 拉取「v1 读源族是否已停更」的告示（审计 §9.73.7，③-4 裁决「接受冻结 + UI 标注」）。
//
// # 为什么单独一个端点而不是往每个响应里塞字段
//
// admin 的响应体是各端点自己的结构；往里加字段要改 20+ 处 struct，
// 而那 20+ 处就是 20+ 个可能漏改的地方，且漏改的那一个**没有任何门会报**。
// 一个独立端点 + 一个全局横幅，读起来也更清楚：
// 「这个平台的 v1 流量数据当前停更了」是**平台级事实**，不是一个端点的属性。
//
// # 这个 API 的形状是被后端契约钉死的，不要单方面改
//
// 后端 `admin/v1_freeze_notice.go` 的约定：
//
//	未冻结     ⇒ { v1_data_horizon: null }
//	已冻结     ⇒ { v1_data_horizon: { frozen: true, unknown: false, ... } }
//	**读不到** ⇒ { v1_data_horizon: { frozen: false, unknown: true, ... } }
//
// 第三种是 §9.79.8 补的：写门读点 `GetPlatformBool` 有三个回落点，
// **全部**返回 fallback(=true) —— 「读到 true」既可能是 DB 里的真值，
// 也可能是**根本没读到**。后端用 `EffectiveValue` 的 `source == "default"`
// 把两者分开，并把后者报成 `unknown`。
//
// ⚠ 因此 `frozen: false` **现在是有合法实例的**（就是 unknown 那一档），
// 但 `frozen: false && unknown: false` 不会出现 —— 后端把那种情形返回成 `null`。
// ⇒ 前端判据是「键为 null ⇒ 未冻结」，**不是**「读 frozen 字段」。
// 后者会把 unknown 误当成已冻结。
import { req } from './_core'

/** 与后端 V1FreezeNotice 一一对应。 */
export interface V1DataHorizonNotice {
  /**
   * true = **明确读到**停写。
   * false 时必须看 `unknown`：false + unknown:true 是「读点没给出答案」，
   * 不是「没停写」。false + unknown:false 不会由后端产出（那种情形返回 null）。
   */
  frozen: boolean
  /**
   * true = 该 gate 键在配置里没有显式取值，读到的值来自默认值回落。
   * ⇒ **不能**被读成「数据是新的」，**也不能**被读成「数据已停更」。
   */
  unknown: boolean
  /** 被冻结的读源族，如 "request_logs"。 */
  source: string
  /** 控制它的 settings 键，运维要知道改哪里。 */
  gate_key: string
  /** 一句话说明「看到这些数字意味着什么」。 */
  effect: string
  /** 最要紧的一句：这类读点没有错误信号。 */
  silence: string
  /** 受影响的读点档位。 */
  affects: string[]
}

export interface V1DataHorizonResponse {
  v1_data_horizon: V1DataHorizonNotice | null
  '//': string
}

/**
 * 取当前 v1 数据地平线告示。
 *
 * 失败时**抛**而不是静默返回「未冻结」：一个取不到告示的页面，
 * 如果按「没取到 = 没停更」处理，就等于在停写期间把冻结当正常展示。
 * 静默降级成「未冻结」是这里唯一真正危险的错误方向。
 */
export async function getV1DataHorizon(): Promise<V1DataHorizonResponse> {
  return req<V1DataHorizonResponse>('GET', '/api/admin/v1-data-horizon')
}
