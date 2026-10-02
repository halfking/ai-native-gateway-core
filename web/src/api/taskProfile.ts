// taskProfile.ts — taskprofile 模块前端 API 客户端（2026-09-18）。
//
// 后端: taskprofile/handler.go — /api/admin/task-profile 前缀。
// 用途: 标注工作台提交人工标注时，同步把"人工确认/改判的任务类型"作为
// 逐请求修正写入 task_type_corrections，形成分类反馈闭环。

import { req, headers, BASE, ApiError } from './_core'

export interface TaskProfileInfo {
  task_type: string
  description: string
  preferred_tier: string
  fallback_tiers: string[]
  min_confidence: number
}

export interface TaskProfileCorrectionStat {
  task_type: string
  total: number
  agrees: number
  corrected: number
  correction_rate: number
}

export interface TaskProfileSuggestion {
  task_type: string
  tier: string
  fallback_tiers: string[]
  min_confidence: number
  tier_source: string
}

export interface TaskProfileView {
  registry_version: string
  schema_version: number
  task_types: string[]
  profiles: (TaskProfileInfo & {
    correction_stats?: TaskProfileCorrectionStat
    suggestion: TaskProfileSuggestion
  })[]
}

export interface CreateTaskTypeCorrectionRequest {
  request_id: string
  human_task_type: string
  annotator: string
  reason: string
}

export interface TaskTypeCorrection {
  id: number
  request_id: string
  auto_task_type: string
  human_task_type: string
  agrees: boolean
  classifier_confidence?: number | null
  profile?: string | null
  annotator: string
  reason: string
  created_at: string
}

export interface CorrectionStatsResponse {
  since: string
  stats: Record<string, TaskProfileCorrectionStat>
  // 每个被修正任务类型的当前分层建议（handler.go handleCorrectionStats）。
  suggestions?: Record<string, TaskProfileSuggestion>
  recent: TaskTypeCorrection[]
}

export interface AppliedTierConfig {
  task_type: string
  preferred_tier: string
  min_confidence: number
  tier_source: string
}

/** 记录一条人工任务类型修正（request_id 冲突返回 409）。 */
export function createTaskTypeCorrection(data: CreateTaskTypeCorrectionRequest): Promise<TaskTypeCorrection> {
  // 后端信封为 {success, correction}（taskprofile/handler.go handleCreateCorrection）；
  // 显式声明响应形状再取 correction，避免把整封信封当 correction 强转。
  return req<{ correction: TaskTypeCorrection }>(
    'POST',
    '/api/admin/task-profile/corrections',
    data
  ).then((r) => r.correction)
}

/** 任务档案总览（注册表 + 修正统计 + 分层建议合并视图）。 */
export function getTaskProfile(): Promise<TaskProfileView> {
  return req<TaskProfileView>('GET', '/api/admin/task-profile')
}

/** 修正统计（sinceDays 天窗口 + recent 明细）。 */
export function getTaskTypeCorrectionStats(sinceDays = 30): Promise<CorrectionStatsResponse> {
  return req<CorrectionStatsResponse>(
    'GET',
    `/api/admin/task-profile/corrections/stats?since_days=${sinceDays}`
  )
}

/**
 * 把当前修正驱动的分层建议显式写入 task_type_tier_config
 * （autoroute.TierSelector 的运行时数据源）。空列表 = 仅修正驱动的升档类型。
 */
export function applyTierConfig(taskTypes?: string[]): Promise<{ applied: AppliedTierConfig[] }> {
  return req('POST', '/api/admin/task-profile/apply-tier-config', { task_types: taskTypes ?? [] })
}

/** 重新加载 overlay 档案文件（TASKPROFILE_OVERLAY；未配置则复位内嵌默认）。 */
export function reloadTaskProfile(): Promise<{ registry_version: string }> {
  return req('POST', '/api/admin/task-profile/reload', {})
}

// ── Export / Import CSV（2026-09-18 round 2） ───────────────────────

export interface CorrectionImportSummary {
  total_rows: number
  imported: number
  skipped: number
  row_errors?: { line: number; message: string }[]
}

export interface CorrectionsExportResult {
  filename: string
  content: string
}

/**
 * GET /api/admin/task-profile/corrections/export?since_days=30。
 * 直接拿到 CSV 文本（后端已设 Content-Disposition）；上层负责落盘。
 */
export async function exportCorrections(sinceDays = 30): Promise<CorrectionsExportResult> {
  const path = `/api/admin/task-profile/corrections/export?since_days=${sinceDays}`
  const r = await fetch(BASE + path, {
    method: 'GET',
    headers: headers('GET', false),
    credentials: 'same-origin',
  })
  if (!r.ok) {
    let detail = r.statusText
    try {
      const j = await r.json()
      detail = j?.error?.message ?? j?.error ?? j?.detail ?? detail
    } catch { /* ignore */ }
    throw new ApiError(r.status, detail || 'export failed')
  }
  // 后端 Content-Disposition: attachment; filename="task-type-corrections-YYYYMMDD.csv"
  const dispo = r.headers.get('Content-Disposition') ?? ''
  const m = /filename="([^"]+)"/.exec(dispo)
  const filename = m?.[1] ?? `task-type-corrections-${new Date().toISOString().slice(0, 10)}.csv`
  const content = await r.text()
  return { filename, content }
}

/**
 * POST /api/admin/task-profile/corrections/import，body 必须是 raw CSV text。
 * 后端要求 Content-Type: text/csv，不能走 req()（它写 application/json）。
 */
export async function importCorrections(csvText: string): Promise<CorrectionImportSummary> {
  const r = await fetch(BASE + '/api/admin/task-profile/corrections/import', {
    method: 'POST',
    headers: {
      ...headers('POST', true),
      'Content-Type': 'text/csv; charset=utf-8',
    },
    credentials: 'same-origin',
    body: csvText,
  })
  if (!r.ok) {
    let detail = r.statusText
    try {
      const j = await r.json()
      detail = j?.error?.message ?? j?.error ?? j?.detail ?? detail
    } catch { /* ignore */ }
    throw new ApiError(r.status, detail || 'import failed')
  }
  const j = (await r.json()) as { success: boolean; summary: CorrectionImportSummary }
  return j.summary
}
