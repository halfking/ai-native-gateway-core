import { req } from './_core'

// dataInit.ts — 「数据初始化」面板的 API 客户端（2026-11-16）
//
// 对应后端 admin/data_lifecycle_data_init.go 的三个端点：
//   GET  /api/admin/data-lifecycle/data-init/catalog   各类数据统计
//   POST /api/admin/data-lifecycle/data-init/preview   重置预览（签发确认令牌）
//   POST /api/admin/data-lifecycle/data-init/execute   执行重置（必须带令牌）
//
// ★ 三个端点都是 superAdmin 档 ⇒ 前端不做角色判断，后端 403 就是 403。
// ★ 请求体里只有**类别 key**，没有表名 ⇒ 表清单由后端注册表决定。

export type DataInitLevel = 'operational' | 'configuration'

export interface DataInitTableStat {
  name: string
  exists: boolean
  rows: number
  bytes: number
  bytes_human: string
  oldest_at?: string
  newest_at?: string
  /** 不会被删除的行数（受保护行）。 */
  protected_rows: number
  /** 会被删除的行数。 */
  target_rows: number
}

export interface DataInitCategoryStat {
  key: string
  label_key: string
  level: DataInitLevel
  tables: DataInitTableStat[]
  rows: number
  bytes: number
  bytes_human: string
  oldest_at?: string
  newest_at?: string
  protected_rows: number
  target_rows: number
  /** i18n key：说明哪些行会被保留。 */
  keep_note_key?: string
  /** 本环境里不存在的表（老部署没迁移到位）。 */
  missing_tables?: string[]
}

export interface DataInitCatalogResponse {
  generated_at: string
  categories: DataInitCategoryStat[]
}

export interface DataInitPreviewCategory {
  key: string
  label_key: string
  level: DataInitLevel
  tables: DataInitTableStat[]
  rows: number
  protected_rows: number
  target_rows: number
  keep_note_key?: string
}

export interface DataInitPreviewResponse {
  categories: DataInitPreviewCategory[]
  total_rows: number
  total_protected_rows: number
  total_target_rows: number
  /** 预览令牌：执行时必须原样回传（5 分钟有效）。 */
  confirm_token: string
  token_expires_at: string
  /** i18n key 列表：副作用告警。 */
  warnings?: string[]
}

export interface DataInitExecuteResult {
  key: string
  rows_deleted: number
}

export interface DataInitExecuteResponse {
  actor: string
  total_rows_deleted: number
  results: DataInitExecuteResult[]
  executed_at: string
}

export function dataInitCatalog() {
  return req<DataInitCatalogResponse>('GET', '/api/admin/data-lifecycle/data-init/catalog')
}

export function dataInitPreview(categories: string[]) {
  return req<DataInitPreviewResponse>('POST', '/api/admin/data-lifecycle/data-init/preview', {
    categories,
  })
}

export function dataInitExecute(categories: string[], confirmToken: string) {
  return req<DataInitExecuteResponse>('POST', '/api/admin/data-lifecycle/data-init/execute', {
    categories,
    confirm_token: confirmToken,
  })
}
