// swimlane.ts — 泳道数据结构定义
// 2026-07-05: 实时请求流泳道系统的核心类型定义

export type GroupByDimension = 'vendor' | 'provider' | 'model'

// 请求色块数据
export interface RequestTile {
  request_id: string
  timestamp: string
  model: string
  vendor: string          // 原厂 (openai, anthropic, google, etc.)
  provider: string        // 供应商 (openai-official, claude-aws, etc.)
  status: string          // success, in_progress, failure
  error_kind?: string     // 5xx, 4xx, timeout, not_found, other
  latency_ms?: number
  cost_usd?: number
  prompt_tokens?: number
  completion_tokens?: number
}

// 维度统计项
export interface DimensionStat {
  key: string            // vendor/provider/model 的具体值
  requestCount: number
  successCount: number
  failureCount: number
  lastSeen: string       // ISO timestamp
}

// 三维度统计数据
export interface DimensionStats {
  vendor: DimensionStat[]
  provider: DimensionStat[]
  model: DimensionStat[]
}

// 泳道数据
export interface SwimLane {
  id: string             // 泳道唯一ID
  name: string           // 显示名称
  dimension: GroupByDimension
  requests: RequestTile[] // 请求列表（最多30条）
  stats: {
    total: number
    success: number
    failure: number
  }
  isOthers: boolean      // 是否是"其它"泳道
}

// 原厂配色
export const VENDOR_COLORS: Record<string, string> = {
  'openai': '#10a37f',
  'anthropic': '#d97757',
  'google': '#4285f4',
  'deepseek': '#6366f1',
  'minimax': '#ec4899',
  'zhipu': '#8b5cf6',
  'alibaba': '#ff6a00',
  'baidu': '#2932e1',
  '__others__': '#6b7280',
  '__unknown__': '#4b5563',
}

// 状态边框色
export const STATUS_BORDER_COLORS: Record<string, string> = {
  'success': '#22c55e',
  'in_progress': '#3b82f6',
  'failure_5xx': '#ef4444',
  'failure_4xx': '#f59e0b',
  'failure_timeout': '#dc2626',
  'failure_not_found': '#a855f7',
  'failure_other': '#9ca3af',
  '__default__': '#6b7280',
}

// 从status和error_kind计算边框颜色key
export function getStatusBorderKey(status: string, errorKind?: string): string {
  if (status === 'success') return 'success'
  if (status === 'in_progress') return 'in_progress'
  if (status === 'failure' && errorKind) {
    if (errorKind.includes('5xx')) return 'failure_5xx'
    if (errorKind.includes('4xx')) return 'failure_4xx'
    if (errorKind.includes('timeout')) return 'failure_timeout'
    if (errorKind.includes('not_found') || errorKind.includes('disc')) return 'failure_not_found'
  }
  return status === 'failure' ? 'failure_other' : '__default__'
}

// 从模型名推断原厂
export function inferVendor(model: string): string {
  const m = model.toLowerCase()
  if (m.includes('gpt') || m.includes('o1') || m.includes('o3')) return 'openai'
  if (m.includes('claude')) return 'anthropic'
  if (m.includes('gemini') || m.includes('palm')) return 'google'
  if (m.includes('deepseek')) return 'deepseek'
  if (m.includes('abab')) return 'minimax'
  if (m.includes('glm') || m.includes('chatglm')) return 'zhipu'
  if (m.includes('qwen')) return 'alibaba'
  if (m.includes('ernie')) return 'baidu'
  return '__unknown__'
}

// 计算动态字体大小
export function calculateFontSize(text: string, maxWidth: number): number {
  const charCount = text.length
  if (maxWidth >= 100) {
    if (charCount <= 10) return 11
    if (charCount <= 15) return 10
    if (charCount <= 20) return 9
    return 8
  }
  // 80px宽度
  if (charCount <= 8) return 11
  if (charCount <= 12) return 10
  if (charCount <= 16) return 9
  return 8
}

// 截断文本（考虑字符完整性）
export function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text
  // 简单截断，避免从emoji或中文字符中间截断
  let truncated = text.slice(0, maxLength)
  // 如果最后一个字符是高代理项（emoji前半部分），移除它
  const lastCharCode = truncated.charCodeAt(truncated.length - 1)
  if (lastCharCode >= 0xd800 && lastCharCode <= 0xdbff) {
    truncated = truncated.slice(0, -1)
  }
  return truncated
}
