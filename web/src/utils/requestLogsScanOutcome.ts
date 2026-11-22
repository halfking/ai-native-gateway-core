// requestLogsScanOutcome.ts —— 「服务端扫描故障」与「页码越界」的判定（2026-11-23 审计轮，P1-1）。
//
// ★ 为什么要有这一层（判据必须与后端**逐项对齐**）：
//
//   后端真正的扫描故障判据是**三项**（admin/logs.go:1133）：
//
//     if len(items) == 0 && count > 0 && scanErrCount > 0 { → 500 }
//
//     · admin/logs.go:1093  scanErrCount：本次页查询里**行扫描失败**的条数
//     · admin/logs.go:1133  判据：items 空 **且** count>0 **且** scanErrCount>0
//     · admin/logs.go:1140  命中 ⇒ 500 "data scanning failed: found N records …"
//
//   第三项 scanErrCount **只在服务端可见**（它不在 200 响应体里）
//   ⇒ 前端在 200 上最多只能看到前两项 ⇒ 把前两项当扫描故障**必然误报**：
//
//   · **页码越界**（page 超出最后一页）：后端不做钳制
//     （admin/logs.go:644-658 只夹 page>=1 / pageSize，offset=(page-1)*pageSize
//     原样进 SQL）⇒ OFFSET 越过末页 ⇒ 0 行，但 count 仍是**命中总数**
//     ⇒ 后端回 **200 + items=[]**（此时 scanErrCount=0 ⇒ 后端自己也**没有**
//     把它当故障）。
//   · 前端旧判据（items 空 && count>0 ⇒ 「服务端数据格式问题」）把它说成故障
//     ⇒ 用户看到红色告警「请联系管理员检查后端日志」，真相只是「你在第 7 页，
//     而这个筛选只有 3 页」。
//
// ★ 逐项对齐后的口径（本模块就是这一口径的唯一实现）：
//   · 服务端**明确拒绝**（500 + "data scanning failed"）⇒ **真扫描故障** ⇒ 报错；
//   · 200 + items 空 + count>0 ⇒ **页码越界**（不是扫描故障）⇒ 跳回最后一页；
//   · 其余 ⇒ 正常。
//
// ★ 为什么放 utils 而不是留在视图里：视图组件（Vue/jsdom）测不动，
//   而本模块是**纯函数** ⇒ 可以被 vitest 直接钉住两种情形各自的判定结果
//   （同族做法见 utils/usageTrendDeepLink.ts）。

/** 后端扫描故障 500 的消息标记（admin/logs.go:1140 的固定前缀）。 */
const SCAN_FAILURE_MARKER = 'data scanning failed'

/** 从后端消息里取「命中记录数」（admin/logs.go:1140 的 "found %d records"）。 */
const SCAN_FAILURE_COUNT_RE = /found (\d+) records/i

export interface LogsListPageState {
  /** 本页返回的行数 */
  itemsLength: number
  /** 后端 COUNT 的命中总数（不受分页影响） */
  count: number
  /** 当前页码（1 起） */
  page: number
  /** 每页行数 */
  pageSize: number
}

/**
 * 页码越界 ⇒ 返回应跳回的页码；不是越界 ⇒ null。
 *
 * ★ 判据与后端一致：后端**不钳制**页码（admin/logs.go:1058 offset=(page-1)*pageSize），
 *   所以「本页 0 行但 count>0」的第一种成因就是 page 越过了最后一页。
 *   ⇒ 这里把页码拉回最后一页（ceil(count/pageSize)），由调用方重取该页。
 */
export function resolveLogsPageOverflow(state: LogsListPageState): number | null {
  const { itemsLength, count, page, pageSize } = state
  if (itemsLength > 0) return null
  if (count <= 0) return null
  if (pageSize <= 0) return null
  if (page < 1) return null
  const lastPage = Math.max(1, Math.ceil(count / pageSize))
  if (page <= lastPage) return null
  return lastPage
}

/** 取错误对象上的 HTTP 状态码（ApiError.status / err.status / err.response.status）。 */
function statusOfError(err: unknown): number {
  if (!err || typeof err !== 'object') return 0
  const e = err as Record<string, unknown>
  if (typeof e.status === 'number' && e.status >= 100 && e.status < 600) return e.status
  const resp = e.response
  if (resp && typeof resp === 'object') {
    const s = (resp as Record<string, unknown>).status
    if (typeof s === 'number' && s >= 100 && s < 600) return s
  }
  return 0
}

/** 取错误对象上的原始 detail（ApiError.detail / err.detail / err.message / 字符串）。 */
function detailOfError(err: unknown): string {
  if (typeof err === 'string') return err
  if (!err || typeof err !== 'object') return ''
  const e = err as Record<string, unknown>
  if (typeof e.detail === 'string' && e.detail) return e.detail
  if (typeof e.message === 'string' && e.message) return e.message
  return ''
}

/**
 * 服务端**明确**报告的扫描故障（真故障）⇒ true。
 *
 * ★ 与页码越界的区别：页码越界是后端回 **200**（它没把这次查询当故障），
 *   真扫描故障是后端回 **500** 且消息里带 "data scanning failed"。
 *   ⇒ 判据只看**服务端怎么说**，不猜。
 */
export function isBackendScanFailure(err: unknown): boolean {
  if (statusOfError(err) !== 500) return false
  return detailOfError(err).toLowerCase().includes(SCAN_FAILURE_MARKER)
}

/**
 * 从服务端的扫描故障消息里取「命中记录数」（给 i18n 文案的 {count} 占位符用）。
 * 取不到 ⇒ null（调用方退回到通用错误文案，**不编数字**）。
 */
export function scanFailureCount(err: unknown): number | null {
  const m = SCAN_FAILURE_COUNT_RE.exec(detailOfError(err))
  if (!m) return null
  const n = Number(m[1])
  return Number.isFinite(n) ? n : null
}
