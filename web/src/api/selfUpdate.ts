// selfUpdate.ts — 自更新管理端 API（自动检索最新版本 → 提醒 → 安装升级）。
//
// 后端契约（autoupdate.AdminAPI，路由前缀 /api/admin/releases，全部需 super admin）：
//   GET  /check-status     最近一次检索结论快照
//   POST /check            立刻检索一次；远端检索失败时回 502 {error, detail, result}
//   GET  /install-status   当前/最近一次安装任务快照
//   GET  /notifications    「有新版待处理」的提醒列表
//   POST /:version/install         安装制品（下载 → 校验 → 替换 → 迁移 → 重启）
//   POST /:version/source-build    本机从源码编译出二进制再安装（不动用户工作树）
//
// ★★ 为什么 check() 不把失败直接抛出去
//
//   远端检索失败时后端回 **502**，body 同时带 `error` / `detail` / `result`：
//     {error: "version check failed", detail: "<真实原因>", result: {...}}
//   而 `req()` 的 ApiError 只保留 status + message，message 取的是 body.error
//   ⇒ **detail（真实原因，例如「远端源未配置 / 网络不可达」）会丢**。
//
//   ⇒ 这里把失败收敛成 `{ok:false, error, detail, result}`，
//     并补读一次 /check-status（后端会把失败结论记进 lastResult，其 error 字段
//     就是真实原因）⇒ 页面能显示「状态未知 + 原因」，
//     而不是把「没查成」读成「已是最新」。
//
//   ★ 这条纪律的来源是本仓已有血案：
//     web/src/views/lifecycle/upgradeLatestVsUnknown.test.ts 记录了旧实现在
//     检查失败时说「已是最新」的假话，导致用户据此跳过补丁。

import { req, ApiError } from './_core'

/** 一条主机配置变更指令（升级配置合同：新增/强制更新，原值保值）。 */
export type EnvConfigUpdate = {
  key: string
  value: string
  forced: boolean
  note?: string
}

/** 远端发布版本（检索结论里的最新版本）。 */
export type RemoteRelease = {
  version: string
  build_seq: number
  channel: string
  mandatory: boolean
  manifest_url?: string
  sha256?: string
  size_mb?: number
  release_notes?: string
  binary_url?: string
  binary_sha256?: string
  /** 该版本要求应用的主机配置变更（maintain manifest 下发；旧源无此字段）。 */
  config_updates?: EnvConfigUpdate[]
}

/** 一次版本检索的结论。 */
export type CheckResult = {
  current_version: string
  current_build_seq: number
  latest?: RemoteRelease
  up_to_date: boolean
  checked_at: string
  /** true 表示本次检索为该版本生成了提醒。 */
  notified: boolean
  /** 非空表示检索失败（此时 latest 不可信）。 */
  error?: string
}

/** GET /check-status 的返回。 */
export type CheckStatusResponse = {
  /** false = 本节点未配置远端版本源（自动检索/提醒未启用）。 */
  configured: boolean
  /** configured=true 但还没查过时为 false。 */
  checked?: boolean
  interval_secs?: number
  current_version?: string
  result?: CheckResult
}

/** 安装模式：下载制品 / 本机源码编译。 */
export type InstallMode = 'artifact' | 'source'

/** 安装任务状态。 */
export type InstallJobStatus =
  | 'pending'
  | 'downloading'
  | 'ready_to_restart'
  | 'upgrading'
  | 'success'
  | 'failed'
  | 'rolled_back'

/** 一次安装任务的运行时状态。 */
export type InstallJob = {
  id: string
  version: string
  mode: InstallMode
  status: InstallJobStatus | string
  old_version?: string
  backup_path?: string
  error?: string
  steps?: string[]
  started_at: string
  completed_at?: string
  /** true = 安装已完成并已安排服务重启。 */
  restart_scheduled?: boolean
}

/** GET /install-status 的返回。 */
export type InstallStatusResponse = {
  configured: boolean
  running: boolean
  job?: InstallJob
}

/** 一条升级提醒。 */
export type ReleaseNotification = {
  release_id: number | null
  instance_id: string
  status: string
  version: string
  started_at: string
  completed_at?: string
  error?: string
  retry_count: number
}

/** GET /notifications 的返回。 */
export type NotificationListResponse = {
  items: ReleaseNotification[]
  total: number
  history_total: number
}

/**
 * 公开端点：本节点当前运行的版本。
 *
 * ★ 为什么需要它：admin 族的 check-status 在「未配置远端版本源」时只回
 *   {configured:false}，**不带任何版本号** ⇒ 状态卡的「当前版本 / 构建序号」
 *   会显示成占位符（而节点明明在跑某个版本）。
 *   ⇒ 这里补读公开端点 /api/system/upgrade/status 作为兜底。
 */
export type NodeUpgradeStatus = {
  current_version: string
  current_build_seq: number
  channel?: string
  latest_version?: string
  latest_build_seq?: number
  has_update: boolean
  update_mandatory?: boolean
  release_title?: string
  release_notes?: string
  min_version?: string
  is_compatible?: boolean
  published_at?: string
}

/** 安装请求体（全部可选）。 */
export type InstallRequestBody = {
  mode?: InstallMode
  restart?: boolean
  binary_url?: string
  sha256?: string
}

/** 源码编译请求体。 */
export type SourceBuildRequestBody = {
  restart?: boolean
  source?: {
    repo_dir?: string
    ref?: string
    package?: string
    output?: string
    version?: string
    build_seq?: number
  }
}

/**
 * 一次「立即检查」的结论。
 *
 * ★ 刻意做成**二值**而不是抛异常：调用方要能区分
 *   「查到了，没有新版」与「没查成」—— 这两件事在界面上必须长得不一样。
 */
export type CheckOutcome =
  | { ok: true; result: CheckResult }
  | { ok: false; error: string; detail: string; result?: CheckResult }

const BASE = '/api/admin/releases'

/** 把任意拒绝值压成一句可展示的文本。 */
function errorText(err: unknown): string {
  if (err instanceof ApiError) return err.message || String(err.status)
  if (err instanceof Error) return err.message
  return String(err)
}

/** 最近一次检索结论快照。 */
export async function getCheckStatus(): Promise<CheckStatusResponse> {
  return req<CheckStatusResponse>('GET', `${BASE}/check-status`)
}

/**
 * 立刻检索一次最新版本。
 *
 * 失败**不抛异常**：返回 `{ok:false, error, detail}`，
 * 其中 detail 是真实原因（远端源未配置 / 网络不可达 / …）。
 */
export async function runCheck(): Promise<CheckOutcome> {
  try {
    const result = await req<CheckResult>('POST', `${BASE}/check`)
    return { ok: true, result }
  } catch (err) {
    const error = errorText(err)
    let detail = ''
    let result: CheckResult | undefined
    // 后端把失败结论记进了 lastResult ⇒ /check-status 能取回真实原因。
    // 取不到就退化成 error 本身（**绝不**把失败说成「已是最新」）。
    try {
      const status = await getCheckStatus()
      result = status.result
      if (result?.error) detail = result.error
    } catch {
      /* 读不到原因就只用 error */
    }
    if (!detail) detail = error
    return { ok: false, error, detail, result }
  }
}

/** 当前/最近一次安装任务快照。 */
export async function getInstallStatus(): Promise<InstallStatusResponse> {
  return req<InstallStatusResponse>('GET', `${BASE}/install-status`)
}

/**
 * 本节点当前运行的版本（公开端点，兜底用）。
 *
 * 失败不抛：调用方把它当「取不到就显示占位符」处理。
 */
export async function getNodeUpgradeStatus(): Promise<NodeUpgradeStatus> {
  return req<NodeUpgradeStatus>('GET', '/api/system/upgrade/status')
}

/** 升级提醒列表。 */
export async function getNotifications(): Promise<NotificationListResponse> {
  return req<NotificationListResponse>('GET', `${BASE}/notifications`)
}

/**
 * 安装指定版本（下载制品 → 校验 → 替换 → 迁移 → 重启）。
 *
 * 已有安装任务在跑时后端回 409 ⇒ 调用方需要自己处理（提示「已有任务在跑」）。
 */
export async function installVersion(
  version: string,
  body?: InstallRequestBody,
): Promise<InstallJob> {
  const res = await req<{ job: InstallJob }>(
    'POST',
    `${BASE}/${encodeURIComponent(version)}/install`,
    body ?? { mode: 'artifact', restart: true },
  )
  return res.job
}

/** 本机从源码编译出二进制再安装。 */
export async function sourceBuildVersion(
  version: string,
  body?: SourceBuildRequestBody,
): Promise<InstallJob> {
  const res = await req<{ job: InstallJob }>(
    'POST',
    `${BASE}/${encodeURIComponent(version)}/source-build`,
    body ?? { restart: true },
  )
  return res.job
}
