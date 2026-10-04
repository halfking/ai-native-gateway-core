import { req, type RequestOptions } from './client'

export interface HealthzInfo {
  status: string
  version?: string
  git_sha?: string
  build_seq?: number | string
  build_date?: string
  ready?: boolean
}

export interface VersionInfo {
  version: string
  git_sha: string
  build_seq: number | string
  build_date: string
  module?: string
  runtime_role?: string
}

/** 公开端点：状态条（无需登录）。 */
export function fetchHealthz(options?: RequestOptions): Promise<HealthzInfo> {
  return req<HealthzInfo>('GET', '/healthz', undefined, options)
}

export function fetchVersion(options?: RequestOptions): Promise<VersionInfo> {
  return req<VersionInfo>('GET', '/version', undefined, options)
}
