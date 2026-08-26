// useSessionSummaryJump.ts — 详情抽屉「会话总结」按钮 → 请求日志页 session 预填跳转。
//
// 2026-08-06: 三个父视图（DashboardViewV2 / DashboardViewLegacy / TenantDashboardView）
// 此前都各自复制粘贴了 `openSessionSummary(sessionId)` 7 行函数：短路校验 + 关抽屉 +
// router.push(预填 query)。本 composable 集中存放这条跳转逻辑，避免重复实现。
//
// 行为契约：
//  1. sessionId 为空 / 空白 / null 时**静默 no-op**（避免点击 emit 时把空串跳到 URL）
//  2. 调用方可在 `onBeforeJump` 钩子里先关抽屉、清理状态等副作用（同步顺序）
//  3. 跳到 `/request-logs?gw_session_id=...`，由 RequestLogsView 的 onMounted 预填
//
// 未来扩展点（保留）：
//  - 多语言键共享：`requests.list.trace.drawerSummaryButton`（已对接 tooltip）
//  - 其它 to={path, query:{gw_xxx_id}} 的跳转可以共用本 composable，新加 `taskId` 字段

import { useRouter } from 'vue-router'

export interface SessionSummaryJumpOptions {
  /**
   * 跳转前的同步副作用钩子。用于关闭抽屉、清理临时状态等。
   * 错误会被吞掉（详见实现），不要在这里面 throw 业务错误。
   */
  onBeforeJump?: () => void
}

export function useSessionSummaryJump(options: SessionSummaryJumpOptions = {}) {
  const router = useRouter()

  /**
   * 跳到 RequestLogsView 并预填 gw_session_id。
   * 空 / 空白 sessionId 静默 no-op。
   */
  function jumpToSessionSummary(sessionId: string | null | undefined) {
    const trimmed = (sessionId ?? '').toString().trim()
    if (!trimmed) return

    try {
      options.onBeforeJump?.()
    } catch {
      // 钩子里出错不能阻塞跳转；与"业务弹窗已关"等价视作最佳努力。
    }

    void router.push({
      path: '/request-logs',
      query: { gw_session_id: trimmed },
    })
  }

  return { jumpToSessionSummary }
}
