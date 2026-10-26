import { req } from './_core'

// opsOverview.ts — 批 158e（2026-10-17）
// P4 opsOverview 孤儿接前端：组合 /api/admin/ops/overview 12 路 payload 的综合视图
// （后端 buildOpsOverviewPayload 并行 12 个 query helper）。
//
// 端点：GET /api/admin/ops/overview（superAdmin 中间件，批 158e 从 admin 降级）
// 表无 tenant_id 字段（gateway_instances / licenses / fault_events /
// upgrade_logs / download_events / offline_activation_requests /
// runtime_metrics / instance_heartbeats）—— 平台聚合表，tenant_admin 调 = 403
//
// 与批 158a/b/c/d 同形态：API helper + View + router + 测试。

export interface CenterStats {
  total_instances: number
  online_instances: number
  offline_instances: number
  degraded_instances: number
}

export interface RegionItem {
  region: string
  total_instances: number
  online_instances: number
  offline_instances: number
  degraded_instances: number
  last_heartbeat?: string
}

export interface RegionStats {
  byRegion: Record<string, RegionItem>
  total: number
}

export interface DeploymentNode {
  node_id: string
  region?: string
  version?: string
  status?: string
  started_at?: string
  last_heartbeat?: string
}

export interface DataPlaneTable {
  name: string
  count: number
}

export interface FaultStats {
  total?: number
  resolved?: number
  pending?: number
  by_severity?: Record<string, number>
}

export interface FaultEvent {
  id: string | number
  rule_id?: string
  severity?: string
  message?: string
  status?: string
  triggered_at?: string
  resolved_at?: string
}

export interface UpgradeLog {
  id: string | number
  version_from?: string
  version_to?: string
  status?: string
  started_at?: string
  completed_at?: string
}

export interface DownloadStats {
  total_downloads?: number
  by_channel?: Record<string, number>
  last_24h?: number
}

export interface RuntimeMetricsSummary {
  uptime_seconds?: number
  memory_rss_bytes?: number
  goroutines?: number
  cpu_percent?: number
  gc_pause_p99_ms?: number
}

export interface OpsOverviewPayload {
  generated_at: string
  center_stats?: CenterStats
  region_stats?: RegionStats
  deployment_nodes?: DeploymentNode[]
  data_plane_tables?: DataPlaneTable[]
  license_total?: number
  offline_requests?: unknown[]
  fault_stats?: FaultStats
  recent_faults?: FaultEvent[]
  recent_upgrades?: UpgradeLog[]
  download_stats?: DownloadStats
  runtime_metrics_summary?: RuntimeMetricsSummary
  refreshedAt: string
}

/**
 * 拉取运维总览 12 路综合 payload。
 * 端点：GET /api/admin/ops/overview
 * 后端缓存 15s TTL（ops_overview.go:5 const opsOverviewCacheTTL = 15 * time.Second）
 */
export async function getOpsOverview(): Promise<OpsOverviewPayload> {
  const data = await req<Omit<OpsOverviewPayload, 'refreshedAt'>>('GET', '/api/admin/ops/overview')
  return { ...data, refreshedAt: new Date().toISOString() }
}
