import { req } from './_core'

// system.ts — 状态条（版本/构建），公开端点。

export interface SystemVersion {
  version?: string
  build?: string
  git_sha?: string
  commit?: string
  [k: string]: unknown
}

export function fetchHealth(signal?: AbortSignal) {
  return req<{ status?: string }>('GET', '/healthz', undefined, signal)
}

export function fetchSystemVersion(signal?: AbortSignal) {
  return req<SystemVersion>('GET', '/api/system/version', undefined, signal)
}
