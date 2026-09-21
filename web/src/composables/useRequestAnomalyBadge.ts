// useRequestAnomalyBadge.ts — 导航栏"格式异常监控"徽标（2026-09-21）。
//
// 轮询 /api/admin/request-anomalies/count（60s），把未解决的请求侧异常数
// 暴露给 AppTopbar 下拉项与 AppNavDrawer。仅 super_admin 拉取（该页面
// 本身 super-only；普通用户不产生请求）。模块级单例：多个组件共享同一
// 定时器与状态；请求失败静默保留上次值（徽标是观测面，不阻断任何流程）。
import { ref } from 'vue'
import { getRequestAnomalyCounts, type RequestAnomalyCounts } from '../api/request-anomalies'
import { isSuperAdmin } from '../store'

const POLL_INTERVAL_MS = 60_000

const counts = ref<RequestAnomalyCounts>({ unresolved: 0, new_today: 0 })
let timer: ReturnType<typeof setInterval> | null = null
let inFlight = false

async function refresh() {
  if (inFlight || !isSuperAdmin()) return
  inFlight = true
  try {
    counts.value = await getRequestAnomalyCounts()
  } catch {
    // 静默：未登录会话 / 网络抖动保留旧值。
  } finally {
    inFlight = false
  }
}

function start() {
  if (timer != null) return
  void refresh()
  timer = setInterval(refresh, POLL_INTERVAL_MS)
}

function stop() {
  if (timer != null) {
    clearInterval(timer)
    timer = null
  }
}

/**
 * 返回共享的徽标计数。首次调用即启动轮询；登录身份变化由 refresh 内的
 * isSuperAdmin 守卫处理（普通用户拉到 401 后保持静默）。
 */
export function useRequestAnomalyBadge() {
  start()
  return { counts, refresh }
}

/** 页面内解决（单条/批量）后立即刷新徽标。 */
export function refreshRequestAnomalyBadge() {
  void refresh()
}

if (typeof window !== 'undefined') {
  window.addEventListener('beforeunload', stop)
}
