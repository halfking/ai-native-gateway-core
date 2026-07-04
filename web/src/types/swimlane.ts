// swimlane.ts — 泳道数据结构定义
// 2026-07-05: 实时请求流泳道系统的核心类型定义
// 2026-07-05 v2: 完善字符截断、空闲块、状态枚举

export type GroupByDimension = 'vendor' | 'provider' | 'model'

// 请求状态枚举
export type RequestStatus = 
  | 'success' 
  | 'in_progress' 
  | 'failure_5xx'
  | 'failure_4xx'
  | 'failure_timeout'
  | 'failure_not_found'
  | 'failure_other'
  | 'idle'              // 空闲块（系统生成）

// 状态描述映射（显示在状态图例中）
export const STATUS_DESCRIPTIONS: Record<RequestStatus, string> = {
  'success': '请求成功',
  'in_progress': '正在处理中',
  'failure_5xx': '服务端错误(5xx)',
  'failure_4xx': '客户端错误(4xx)',
  'failure_timeout': '请求超时',
  'failure_not_found': '资源未找到',
  'failure_other': '其它错误',
  'idle': '系统空闲（心跳）',
}

// 状态缩写（用于色块内显示）
export const STATUS_SHORT_LABELS: Record<RequestStatus, string> = {
  'success': '成功',
  'in_progress': '进行中',
  'failure_5xx': '5xx',
  'failure_4xx': '4xx',
  'failure_timeout': '超时',
  'failure_not_found': '未找到',
  'failure_other': '失败',
  'idle': '空闲',
}

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

// 原厂配色 - 使用我们常用的前6个原厂
export const VENDOR_COLORS: Record<string, string> = {
  'openai': '#10a37f',       // OpenAI - 青绿色
  'anthropic': '#d97757',    // Anthropic - 橙褐色
  'google': '#4285f4',       // Google - 蓝色
  'deepseek': '#6366f1',     // DeepSeek - 靛蓝色
  'minimax': '#ec4899',      // MiniMax - 粉红色
  'zhipu': '#8b5cf6',        // 智谱AI - 紫色
  '__others__': '#6b7280',   // 其它 - 灰色
  '__unknown__': '#4b5563',  // 未知 - 深灰色
  '__idle__': '#374151',     // 空闲 - 更深的灰色
}

// 状态边框色
export const STATUS_BORDER_COLORS: Record<string, string> = {
  'success': '#22c55e',           // 成功 - 绿色
  'in_progress': '#3b82f6',       // 进行中 - 蓝色
  'failure_5xx': '#ef4444',       // 5xx错误 - 红色
  'failure_4xx': '#f59e0b',       // 4xx错误 - 橙色
  'failure_timeout': '#dc2626',   // 超时 - 深红色
  'failure_not_found': '#a855f7', // 未找到 - 紫色
  'failure_other': '#9ca3af',     // 其它错误 - 灰色
  'idle': '#4b5563',              // 空闲 - 深灰色
  '__default__': '#6b7280',
}

// 从status和error_kind计算边框颜色key
export function getStatusBorderKey(status: string, errorKind?: string): string {
  if (status === 'success') return 'success'
  if (status === 'in_progress') return 'in_progress'
  if (status === 'idle') return 'idle'
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
  if (!m) return '__unknown__'
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

// 智能截断文本（考虑字符完整性、emoji、中文字符）
export function truncateText(text: string, maxLength: number): string {
  if (!text) return ''
  if (text.length <= maxLength) return text
  
  // 计算安全截断点（避免在emoji代理对中间截断）
  let truncated = text.slice(0, maxLength)
  
  // 检查最后一个字符是否为高代理项（emoji的前半部分）
  const lastCharCode = truncated.charCodeAt(truncated.length - 1)
  if (lastCharCode >= 0xD800 && lastCharCode <= 0xDBFF) {
    // 是高代理项，移除它避免半个emoji
    truncated = truncated.slice(0, -1)
  }
  
  // 检查是否真的被截断了
  if (truncated.length < text.length) {
    return truncated + '…'
  }
  
  return truncated
}

// 格式化延迟时间（xxxs 或 xxxms）
export function formatLatency(ms: number | undefined | null): string {
  if (ms == null || ms < 0) return ''
  if (ms >= 1000) {
    // 大于1秒，显示为xxxs
    const seconds = ms / 1000
    if (seconds >= 100) {
      return `${Math.round(seconds)}s`
    }
    return `${seconds.toFixed(1)}s`
  }
  // 小于1秒，显示为xxxms
  return `${Math.round(ms)}ms`
}

// 创建空闲块
export function createIdleTile(): RequestTile {
  return {
    request_id: `idle-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
    timestamp: new Date().toISOString(),
    model: '空闲',
    vendor: '__idle__',
    provider: '系统心跳',
    status: 'idle',
  }
}

// 判断是否为空闲块
export function isIdleTile(tile: RequestTile): boolean {
  return tile.status === 'idle' || tile.request_id.startsWith('idle-')
}