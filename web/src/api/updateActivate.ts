/** APIs for the merged「更新与激活」page (versions + modules + downloads + license status). */

import { MAINTAIN_API_BASE } from '../config/edition'
import {
  checkForUpgrade,
  getUpgradeStatus,
  type UpgradeCheckResponse,
  type UpgradeStatus,
} from './customer'

export type { UpgradeStatus, UpgradeCheckResponse }

export type ModuleCatalogItem = {
  module_id: string
  display_name?: string
  plugin_id?: string
  version?: string
  channel?: string
  licensed?: boolean
  installed?: boolean
  description?: string
  platform?: string
  arch?: string
  activated_at?: string
  opened_at?: string
}

export type ModuleCatalogResponse = {
  items: ModuleCatalogItem[]
  purchase_enabled?: boolean
}

/** License status returned by maintain-api /license/status?instance_id=. */
export type LicenseState = 'none' | 'active' | 'grace' | 'expired' | 'revoked'

export type LicenseStatus = {
  state: LicenseState | string
  license_key?: string
  customer_name?: string
  expires_at?: string
  subscription_tier?: string
  device_name?: string
  last_heartbeat?: string
}

/** Catalog of downloadable releases returned by /downloads/catalog. */
export type CatalogItem = {
  platform: string
  arch: string
  label: string
  artifact_name: string
  sha256?: string
  size_bytes?: number
}

export type Release = {
  version: string
  build_seq: number
  channel: string
  release_date: string
  items: CatalogItem[]
  install_doc_url?: string
  git_repo_url?: string
  git_branch?: string
  docs_url?: string
  contact_email?: string
}

export type CatalogResponse = {
  versions: Release[]
  supporters: number
  git_repo_url?: string
  contact_email?: string
  docs_url?: string
}

export type DownloadTicket = {
  request_id: string
  url: string
  expires_at: string
  file_name: string
}

export type TicketRequest = {
  version: string
  platform: string
  arch: string
}

export type DownloadEventRequest = {
  request_id: string
  result: 'started' | 'completed' | 'failed'
  duration_ms?: number
}

export type UpgradeReportRequest = {
  instance_id: string
  from_version: string
  to_version: string
  status: 'started' | 'completed' | 'failed'
  error?: string
}

async function maintainGet<T>(path: string): Promise<T> {
  const response = await fetch(`${MAINTAIN_API_BASE}${path}`, {
    method: 'GET',
    credentials: 'same-origin',
    cache: 'no-store',
  })
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const msg =
      (body && typeof body === 'object' && (body.message || body.error)) ||
      `请求失败（${response.status}）`
    throw new Error(String(msg))
  }
  return body as T
}

async function maintainPost<T>(path: string, payload: unknown): Promise<T> {
  const response = await fetch(`${MAINTAIN_API_BASE}${path}`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(payload),
    cache: 'no-store',
  })
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const msg =
      (body && typeof body === 'object' && (body.message || body.error)) ||
      `请求失败（${response.status}）`
    throw new Error(String(msg))
  }
  return body as T
}

export const updateActivateApi = {
  upgradeStatus: () => getUpgradeStatus(),
  upgradeCheck: () => checkForUpgrade(),
  modulesCatalog: () => maintainGet<ModuleCatalogResponse>('/modules/catalog'),
  licenseStatus: (instanceId: string) =>
    maintainGet<LicenseStatus>(`/license/status?instance_id=${encodeURIComponent(instanceId)}`),
  downloadsCatalog: () => maintainGet<CatalogResponse>('/downloads/catalog'),
  downloadTicket: (payload: TicketRequest) =>
    maintainPost<DownloadTicket>('/downloads/ticket', payload),
  downloadEvent: (payload: DownloadEventRequest) =>
    maintainPost<unknown>('/downloads/events', payload).catch(() => undefined),
  upgradeReport: (payload: UpgradeReportRequest) =>
    maintainPost<{ status: string }>('/upgrade/report', payload),
}