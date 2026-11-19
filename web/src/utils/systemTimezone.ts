/**
 * systemTimezone.ts — 系统时区全局状态（2026-11-20 时区设置轮）。
 *
 * 后端平台设置 system.timezone（默认 Asia/Shanghai）是全站「日切/日期串
 * 语义」的真源：后端 SQL 边界、前端日切函数（kxDatePresets / boardTimeRange）
 * 与显示格式化（datetime.ts）都按它解释。本模块启动时拉取
 * GET /api/admin/system-timezone 并缓存；拿不到时回退浏览器本地时区
 * （与旧行为兼容——后端不可达时查询边界本来也发不出去）。
 *
 * 关键语义：IANA 名 → IANA 合法性在浏览器侧无法验证（无完整 tz 数据库），
 * 信任后端返回（后端写入面已校验 IANA 可加载）；未加载前的回退值取
 * Intl.DateTimeFormat().resolvedOptions().timeZone（浏览器本地时区）。
 */

export interface SystemTimezoneState {
  /** IANA 名，如 'Asia/Shanghai'；'' = 尚未加载。 */
  timezone: string
  /** 后端现值是否等于默认东8区（供 UI 提示）。 */
  isDefault: boolean
  loaded: boolean
}

const state: SystemTimezoneState = {
  timezone: '',
  isDefault: false,
  loaded: false,
}

const listeners = new Set<() => void>()

/** 浏览器本地 IANA 名（回退值）。 */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/**
 * 当前生效系统时区。未加载完成时回退浏览器本地时区——保持与既有行为
 * （显示本地时间）兼容，且日切函数不需要判空。
 */
export function systemTimezone(): string {
  return state.timezone || browserTimezone()
}

export function systemTimezoneState(): Readonly<SystemTimezoneState> {
  return state
}

/** 时区变更订阅（设置页改时区后各视图刷新显示）。返回退订函数。 */
export function onSystemTimezoneChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function applyTimezone(name: string, isDefault: boolean) {
  const changed = state.timezone !== name
  state.timezone = name
  state.isDefault = isDefault
  state.loaded = true
  if (changed) {
    listeners.forEach((fn) => {
      try {
        fn()
      } catch {
        /* 单个订阅者异常不拖垮其它 */
      }
    })
  }
}

/** GET /api/admin/system-timezone → 写入全局状态。失败静默（保留回退值）。 */
export async function fetchSystemTimezone(): Promise<void> {
  try {
    const res = await fetch('/api/admin/system-timezone', { credentials: 'include' })
    if (!res.ok) return
    const data = (await res.json()) as { timezone?: string; default?: string }
    if (typeof data.timezone === 'string' && data.timezone) {
      applyTimezone(data.timezone, data.timezone === (data.default ?? 'Asia/Shanghai'))
    }
  } catch {
    /* 后端不可达：保持回退值 */
  }
}

/** 测试与设置页写后即时更新用。 */
export function setSystemTimezoneForTest(name: string) {
  applyTimezone(name, false)
}

/** 测试复位：清空状态回到「未加载」。 */
export function resetSystemTimezoneForTest() {
  state.timezone = ''
  state.isDefault = false
  state.loaded = false
}
