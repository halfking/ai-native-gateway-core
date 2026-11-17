/**
 * PI 错误分析（/admin/pi-analysis）运行时可用性探测。
 *
 * ★ 2026-11-18 审计轮 P2：菜单项「PI 分析」（/admin/pi-analysis）此前
 * **无条件展示**，而它背后的 `/api/admin/pi-analysis/*` 只有在
 * 「有 DB（piErrorCollectorGlobal/piSQLDBGlobal 非 nil）」且
 * 「admin 鉴权材料齐备（pool + secret）」时才注册
 * （见 cmd/gateway/main.go 与 admin/pi_analysis_handler.go 的
 * fail-closed 拒绝注册）⇒ 没有 DB / 没配 secret 的部署上，
 * 点了菜单项就是一个 404 空页 ⇒ 菜单在**宣传一个不存在的功能**。
 *
 * 为什么不改成「后端无条件注册」：
 *   RegisterRoutes 拒绝注册是**有意的 fail-closed**（没有 pool/secret
 *   就无法鉴权 ⇒ 注册裸 handler 等于把内部错误队列敞在公网上）。
 *   ⇒ 这个 404 不能在后端消掉，只能由前端如实反映。
 *
 * 口径（与 config/edition.ts 的 maintain 探测同形，但**乐观默认**）：
 *   - unknown（未探测）：**照常展示**（避免探测未完成时菜单闪烁/计数漂移）；
 *   - available：展示；
 *   - unavailable：隐藏菜单项。
 *
 * 注意：本模块**不 import vue** —— appNav.ts 会被 scripts/export-menu-config.mjs
 * 用 esbuild 打包成构建产物 menu-config.json，那条链路不能拖进 vue。
 * 与 edition.ts 一样用「模块级单例 + listener」把变化通知出去。
 */

type TriState = 'unknown' | 'available' | 'unavailable'

const PI_ANALYSIS_PROBE_TTL_MS = 5 * 60_000

let piAnalysisState: TriState = 'unknown'
let piAnalysisProbedAt = 0
let probePromise: Promise<boolean> | null = null
const listeners = new Set<() => void>()

function notify() {
  for (const cb of listeners) {
    try {
      cb()
    } catch {
      /* listener 异常不得影响其它订阅者 */
    }
  }
}

/**
 * PI 分析菜单项当前是否应当展示。
 *
 * unknown（还没探测）⇒ true（照常展示）：探测是异步的，
 * 若默认隐藏，菜单会先消失再出现，且未探测期间的计数断言会漂移。
 */
export function showPIAnalysis(): boolean {
  return piAnalysisState !== 'unavailable'
}

/** 订阅可用性变化；返回取消订阅函数。 */
export function onPIAnalysisAvailabilityChange(cb: () => void): () => void {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

/**
 * 探测 PI 分析 API 是否可用。
 *
 * 判据：GET /api/admin/pi-analysis/stats 返回 200 ⇒ 可用；
 * 404（路由未注册：无 DB / 无鉴权材料）/ 401 / 403 / 5xx ⇒ 不可用。
 * 5xx 也算不可用：那种状态下页面本身也是坏的，展示它没有意义。
 *
 * 结果缓存 TTL 5 分钟；force=true 时强制重探。
 */
export async function probePIAnalysisAvailable(force = false): Promise<boolean> {
  const now = Date.now()
  if (!force && piAnalysisState !== 'unknown' && now - piAnalysisProbedAt < PI_ANALYSIS_PROBE_TTL_MS) {
    return piAnalysisState === 'available'
  }
  if (!force && probePromise) return probePromise

  const current = (async () => {
    try {
      const res = await fetch('/api/admin/pi-analysis/stats', {
        method: 'GET',
        credentials: 'same-origin',
        cache: 'no-store',
      })
      piAnalysisState = res.ok ? 'available' : 'unavailable'
    } catch {
      piAnalysisState = 'unavailable'
    } finally {
      piAnalysisProbedAt = Date.now()
    }
    notify()
    return piAnalysisState === 'available'
  })()
  probePromise = current
  try {
    return await current
  } finally {
    if (probePromise === current) probePromise = null
  }
}

/** 仅供测试：复位单例状态。 */
export function _resetPIAnalysisForTests() {
  piAnalysisState = 'unknown'
  piAnalysisProbedAt = 0
  probePromise = null
  listeners.clear()
}
