import { req } from './_core'
import { storageConfigGet, attachmentFilesystemStats, logConfigGet, type StorageConfig, type LogConfig } from './tuning'

// storageAndLogConfig.ts — 批 204（2026-10-17）
// 🟡 P0 storageAndLogConfig 孤儿接前端：附件存储 + 日志配置只读摘要
//
// 17 跳 evidence（line 1062）原判"否（by-design）/ 0 工作量" — 子视图
//   StorageConfig.vue / FilesystemMaintenance.vue / LogManagement.vue 全部
//   在 DataLifecycleView 父视图下作为 tab 切换使用，无独立深链。
//
// 本批按用户拍板"全部 8 孤儿"——建 1 个轻量只读摘要详细页，让用户能
//   不经过 DataLifecycleView 直接深链查看三段配置。

export interface AttachmentFilesystemStats {
  attachment_dir: string
  total_files: number
  total_size_bytes: number
  total_size_human: string
  oldest_file_time: string | null
  disk_total_bytes: number
  disk_used_bytes: number
  disk_avail_bytes: number
  disk_usage_percent: number
  disk_warning_level: 'safe' | 'warning' | 'danger'
}

export interface StorageAndLogSnapshot {
  storage: StorageConfig
  filesystem: AttachmentFilesystemStats
  log: LogConfig
  refreshedAt: string
}

/**
 * 一次性拉取 3 段配置：storage + filesystem + log
 * 端点：GET /api/admin/storage/config + /attachments/filesystem/stats + /log/config
 * 注：本函数是组合 helper（3 端点并发），与批 158b getUsageEnhancedData 同形态
 */
export async function getStorageAndLogSnapshot(): Promise<StorageAndLogSnapshot> {
  const [storage, filesystem, log] = await Promise.all([
    storageConfigGet(),
    attachmentFilesystemStats(),
    logConfigGet(),
  ])
  return { storage, filesystem, log, refreshedAt: new Date().toISOString() }
}
