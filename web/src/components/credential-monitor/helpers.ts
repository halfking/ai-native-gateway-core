/**
 * credential-monitor 子组件共享的展示辅助函数（2026-09-13 P3-5 第二批，
 * 自 CredentialMonitorView.vue 拆出，逻辑保持不变）。
 */
import { credentialDisplayState } from '../../utils/credentialStatus'
import { formatCompactDateTime } from '../../utils/datetime'
import type { CredentialMonitorMeta, CredentialMonitorSummary } from '../../api'

export function effectiveCredentialState(c: CredentialMonitorSummary) {
  return c.effective_state || credentialDisplayState(c)
}

export function effectiveCredentialReason(c: CredentialMonitorSummary) {
  return c.effective_reason || c.state_reason_detail || c.state_reason_code || undefined
}

export function statusBadge(state: string) {
  if (state === 'ready') return 'badge-green'
  if (['degraded', 'cooling', 'rate_limited'].includes(state)) return 'badge-amber'
  if (['unreachable', 'auth_failed', 'suspended'].includes(state)) return 'badge-red'
  return 'badge-gray'
}

export function healthBadge(h: string) {
  if (h === 'healthy') return 'badge-green'
  if (h === 'warning') return 'badge-amber'
  if (h === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

export function rateClass(rate: number | null | undefined) {
  if (rate == null) return 'rate-none'
  if (rate >= 0.9) return 'rate-good'
  if (rate >= 0.5) return 'rate-warn'
  return 'rate-bad'
}

export function rateText(rate: number | null | undefined) {
  if (rate == null) return '—'
  return (rate * 100).toFixed(1) + '%'
}

// 🆕 2026-06-23: 延迟 P95 色阶 (用于模型可用性表).
//   <500ms 绿 / 500-1500ms 琥珀 / >1500ms 红 / null 不染色.
// 阈值参考 credentialhealth 默认配置和 llm-gateway-go 实测分布.
export function p95Class(ms: number | null | undefined) {
  if (ms == null) return ''
  if (ms < 500) return 'p95-good'
  if (ms < 1500) return 'p95-warn'
  return 'p95-bad'
}

// 审计 R3#10：紧凑时间戳统一走 utils/datetime.ts。
export function formatTs(ts: string) {
  // '2026-06-23T10:00:00Z' -> '06-23 10:00'
  return formatCompactDateTime(ts)
}

export function formatRefreshAt(d: Date | null): string {
  return formatCompactDateTime(d, { withSeconds: true, empty: '尚未刷新' })
}

export function formatCacheMeta(meta: CredentialMonitorMeta | null): string {
  if (!meta) return '缓存状态未知'
  return `${meta.cache_hit ? '缓存命中' : '实时生成'} · 服务端 ${meta.server_duration_ms}ms · 过期 ${formatTs(meta.expires_at)}`
}
