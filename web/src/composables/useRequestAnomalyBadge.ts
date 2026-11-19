// useRequestAnomalyBadge.ts — 导航栏"格式异常监控"徽标（2026-09-21）。
//
// 2026-11-20 徽标口径修正轮（owner 报障：页面数据与菜单徽标数字不匹配）：
// 菜单「格式异常监控」页有两个数据源——响应格式异常（response_format_anomalies
// PG 表）与请求错误（reqprobe），但徽标此前只读 request-anomalies/count（请求
// 侧），页面主体（响应格式异常 tab）完全不在统计里 ⇒ 数字对不上。
// 修正：徽标 = 两个数据源未解决数之和（/format-anomalies/count + /request-anomalies/count）。
//
// 轮询 60s。仅 super_admin 拉取（该页面本身 super-only；普通用户不产生请求）。
// 模块级单例：多个组件共享同一定时器与状态；单侧失败静默保留该侧旧值
// （徽标是观测面，不阻断任何流程）——另一侧照常刷新。
import { computed, ref } from 'vue'
import {
  getFormatAnomalyCounts,
  type FormatAnomalyCounts,
} from '../api/format-anomalies'
import { getRequestAnomalyCounts, type RequestAnomalyCounts } from '../api/request-anomalies'
import { isSuperAdmin } from '../store'

const POLL_INTERVAL_MS = 60_000

const formatCounts = ref<FormatAnomalyCounts>({ unresolved: 0, new_today: 0 })
const counts = ref<RequestAnomalyCounts>({ unresolved: 0, new_today: 0 })

/** 合计：两个数据源未解决数之和（菜单徽标渲染这个）。 */
const totalUnresolved = computed(() => formatCounts.value.unresolved + counts.value.unresolved)
const totalNewToday = computed(() => formatCounts.value.new_today + counts.value.new_today)

let timer: ReturnType<typeof setInterval> | null = null
let inFlight = false

async function refresh() {
  if (inFlight || !isSuperAdmin()) return
  inFlight = true
  try {
    // 双源并行；Promise.allSettled ⇒ 一侧 4xx/网络抖动不影响另一侧刷新。
    const [fmtRes, reqRes] = await Promise.allSettled([
      getFormatAnomalyCounts(),
      getRequestAnomalyCounts(),
    ])
    if (fmtRes.status === 'fulfilled') formatCounts.value = fmtRes.value
    if (reqRes.status === 'fulfilled') counts.value = reqRes.value
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
 *
 * - counts：请求错误侧（reqprobe）原始计数；
 * - formatCounts：响应格式异常侧原始计数；
 * - totalUnresolved / totalNewToday：两源合计（菜单徽标用）。
 */
export function useRequestAnomalyBadge() {
  start()
  return { counts, formatCounts, totalUnresolved, totalNewToday, refresh }
}

/** 页面内解决（单条/批量）后立即刷新徽标。 */
export function refreshRequestAnomalyBadge() {
  void refresh()
}

if (typeof window !== 'undefined') {
  window.addEventListener('beforeunload', stop)
}
