// taskProfile.ts — taskprofile 模块前端 API 客户端（2026-09-18）。
//
// 后端: taskprofile/handler.go — /api/admin/task-profile 前缀。
// 用途: 标注工作台提交人工标注时，同步把"人工确认/改判的任务类型"作为
// 逐请求修正写入 task_type_corrections，形成分类反馈闭环。

import { req } from './_core'

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
  // R43: 原写法把响应泛型写成 TaskTypeCorrection 再 as 强转，vue-tsc TS2352。
  return req<{ correction: TaskTypeCorrection }>('POST', '/api/admin/task-profile/corrections', data).then(
    (r) => r.correction
  )
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
