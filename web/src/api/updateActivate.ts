/** APIs for the merged「更新与激活」page (versions + modules catalog). */

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

export const updateActivateApi = {
  upgradeStatus: () => getUpgradeStatus(),
  upgradeCheck: () => checkForUpgrade(),
  modulesCatalog: () => maintainGet<ModuleCatalogResponse>('/modules/catalog'),
}
