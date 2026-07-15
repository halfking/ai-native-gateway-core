import { getSetting } from '../api/settings'

const STORAGE_KEY = 'dashboard.refresh.settings.v1'
const DEFAULT_REFRESH_MS = 10_000

type CachedRefreshSettings = {
  foldUnit: string
  foldInterval: number
  cachedAt: number
}

function readCached(): CachedRefreshSettings | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as CachedRefreshSettings
    if (!parsed || typeof parsed.foldInterval !== 'number') return null
    return parsed
  } catch {
    return null
  }
}

function writeCached(settings: CachedRefreshSettings) {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(settings))
  } catch {
    // ignore quota / private mode
  }
}

function intervalFromSettings(settings: CachedRefreshSettings): number {
  if (settings.foldUnit === 'minute') {
    return Math.max(1, settings.foldInterval) * 60_000
  }
  return Math.max(1, settings.foldInterval) * 1000
}

export async function resolveDashboardRefreshMs(): Promise<number> {
  const cached = readCached()
  if (cached) {
    return intervalFromSettings(cached)
  }
  try {
    const foldUnit = await getSetting('dashboard.stats.fold_unit')
    const intervalSetting = await getSetting('dashboard.stats.fold_interval')
    const foldInterval = Number(intervalSetting.value)
    const safeInterval = Math.max(1, Number.isFinite(foldInterval) ? foldInterval : 1)
    writeCached({
      foldUnit: foldUnit.value,
      foldInterval: safeInterval,
      cachedAt: Date.now(),
    })
    if (foldUnit.value === 'minute') {
      return safeInterval * 60_000
    }
    return safeInterval * 1000
  } catch {
    return DEFAULT_REFRESH_MS
  }
}
