import { req, type RequestOptions } from './client'

// nodeAudit.ts — /api/admin/audit/node-operations（节点操作审计）。
//
// ⚠️ **覆盖面实测结论（2026-10-06 读后端 admin/node_operations_audit.go 后核实，
// 这一点很容易想当然）**：这个审计面**只记供应商级**两类操作，**不记凭据级**：
//   · auditTestNow    → metadata.operation = "test-now"    （:35，provider 级探测）
//   · auditNodeToggle → metadata.operation = "enable_toggle"（:63，provider 级启停）
// 且两处都是 `go func()` 异步落库、不阻塞主流程（:30、:59）。
// ⇒ 后果：**§11 上移的四个凭据级写操作（停用/恢复/检查/强制恢复）不会出现在这里**。
//   所以本视图只能回答「谁动过这个供应商」，**不能**当凭据操作审计用。
//   别在凭据详情页挂它 —— 那会让用户以为「我刚才那次停用有记录」，实际查不到。
//
// 鉴权：admin 档（admin/handler.go:1181），**tenant_admin 可用**；租户隔离靠 RLS
// （audit_operations.go:219-223：super_admin 走 withAllTenantReadOnlyTx，
// 其余走 withTenantTx）。

/** 与后端 admin/audit_operations.go:37-53 的 AuditOperationEntry 逐字段对齐。 */
export interface NodeAuditEntry {
  request_id: string
  provider_id: number
  operation: string
  from_state?: string
  to_state?: string
  operator_id?: string
  correlation_id?: string
  idempotency_key?: string
  status?: string
  latency_ms?: number
  reason?: string
  enabled?: boolean
  source?: string
  created_at: string
  raw?: Record<string, unknown>
}

export interface NodeAuditResponse {
  entries: NodeAuditEntry[]
  count: number
  limit: number
}

/** 后端硬上限（audit_operations.go:56-57），超了会被静默 clamp 到 200。 */
export const NODE_AUDIT_MAX_LIMIT = 200

export interface NodeAuditParams {
  provider_id?: number
  operation?: string
  operator_id?: string
  /** RFC3339。后端解析失败直接 400 "since must be RFC3339"（:86-90）。 */
  since?: string
  limit?: number
}

export function fetchNodeAudit(
  params?: NodeAuditParams,
  options?: RequestOptions,
): Promise<NodeAuditResponse> {
  const qs = new URLSearchParams()
  if (params?.provider_id != null) qs.set('provider_id', String(params.provider_id))
  if (params?.operation) qs.set('operation', params.operation)
  if (params?.operator_id) qs.set('operator_id', params.operator_id)
  if (params?.since) qs.set('since', params.since)
  if (params?.limit != null) {
    // ⚠️ limit < 1 或非数字 → 后端 400 "invalid limit"（:95-99）；
    //   > 200 会被静默 clamp。前端先夹到合法区间，别让请求白跑一次拿 400。
    const n = Math.trunc(params.limit)
    if (Number.isFinite(n) && n >= 1) {
      qs.set('limit', String(Math.min(n, NODE_AUDIT_MAX_LIMIT)))
    }
  }
  const q = qs.toString()
  return req<NodeAuditResponse | NodeAuditEntry[]>(
    'GET',
    `/api/admin/audit/node-operations${q ? '?' + q : ''}`,
    undefined,
    options,
  ).then(unwrapNodeAudit)
}

/**
 * 这是本仓第**四种**响应形态（前三见 UI规范 17 §11.7）：
 *   /api/providers                     → 裸数组
 *   /api/candidate-failures/alerts     → {data, count}
 *   /api/credentials/monitor-summary   → {credentials, count, meta}
 *   本端点                            → {entries, count, limit}
 * 依旧不抽通用解包器；形状不符抛错而非返空。
 */
export function unwrapNodeAudit(resp: NodeAuditResponse | NodeAuditEntry[]): NodeAuditResponse {
  if (Array.isArray(resp)) return { entries: resp, count: resp.length, limit: resp.length }
  if (resp && typeof resp === 'object' && Array.isArray(resp.entries)) return resp
  const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
  throw new Error(`node-operations 响应形状不符：期望 {entries:[…], count, limit}，实得 ${actual}`)
}

/**
 * 操作类型 → 展示名。
 *
 * ⚠️ 取值以**后端实际写入的字符串**为准，不是文档注释：
 *   admin/audit_operations.go:14 的注释写「test_now / enable_toggle」，
 *   但真正落库的是 admin/node_operations_audit.go:35 的 **"test-now"**（连字符）。
 *   ⇒ 过滤器若照抄注释的 `test_now` 会永远查不到行。故下面按连字符版匹配，
 *   并保留下划线版作为历史兼容。
 *
 * 另：manual_disabled / force_recover 两类**不落在这个端点**（见文件头覆盖面说明），
 * 这里列出仅为万一后端将来补记时不至于显示成原始英文；不会造成假记录。
 */
export function operationLabel(op: string, t: (k: string) => string): string {
  switch (op) {
    case 'test-now':
    case 'test_now':
      return t('audit.opTestNow')
    case 'enable_toggle':
      return t('audit.opEnableToggle')
    default:
      return op || t('common.unknown')
  }
}

/** 是否属于「凭据级写操作」——移动端 §11 上移的那四个。用于 UI 显式说明审计缺口。 */
export function isCredentialLevelOp(op: string): boolean {
  return op === 'manual_disabled' || op === 'clear_manual_disabled' || op === 'force_recover'
}
