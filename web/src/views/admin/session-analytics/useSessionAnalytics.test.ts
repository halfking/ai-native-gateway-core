import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import { ApiError } from '../../../api/_core'

/**
 * 会话分析加载状态机的契约测试。
 *
 * ★ 这个三态（loading / stale / error）必须互斥且口径稳定：
 *   · 服务端 503 + code=analytics_view_stale 是「聚合视图正在刷新」，
 *     **不是**故障 —— 渲染成通用错误会把「再等一轮」说成「坏了」。
 *   · 陈旧态与错误态同时出现会让用户既刷新又重试，两个动作都无意义。
 */

describe('useSessionAnalyticsLoader', () => {
  it('成功后返回数据并复位 loading', async () => {
    const { loading, error, stale, run } = useSessionAnalyticsLoader()
    const res = await run(async () => ({ ok: true }), 'sessionAnalytics.error.overviewFailed')
    expect(res).toEqual({ ok: true })
    expect(loading.value).toBe(false)
    expect(error.value).toBe('')
    expect(stale.value).toBe(false)
  })

  it('陈旧错误只点亮 stale，不写 error', async () => {
    const { loading, error, stale, run } = useSessionAnalyticsLoader()
    const res = await run(async () => {
      throw new ApiError(503, 'stale', 'analytics_view_stale')
    }, 'sessionAnalytics.error.overviewFailed')
    expect(res).toBeNull()
    expect(stale.value).toBe(true)
    expect(error.value).toBe('')
    expect(loading.value).toBe(false)
  })

  it('普通错误写 error 且不点亮 stale', async () => {
    const { error, stale, run } = useSessionAnalyticsLoader()
    await run(async () => {
      throw new ApiError(500, 'boom', '')
    }, 'sessionAnalytics.error.overviewFailed')
    expect(stale.value).toBe(false)
    expect(error.value).not.toBe('')
  })

  it('503 但不是陈旧码 ⇒ 走错误分支（不把一切 503 都当成陈旧）', async () => {
    const { error, stale, run } = useSessionAnalyticsLoader()
    await run(async () => {
      throw new ApiError(503, 'maintenance', 'maintenance_mode')
    }, 'sessionAnalytics.error.overviewFailed')
    expect(stale.value).toBe(false)
    expect(error.value).not.toBe('')
  })

  it('每次调用前清空上一轮的错误与陈旧标记', async () => {
    const { error, stale, run } = useSessionAnalyticsLoader()
    await run(async () => {
      throw new ApiError(500, 'boom', '')
    }, 'sessionAnalytics.error.overviewFailed')
    expect(error.value).not.toBe('')
    const res = await run(async () => ({ ok: 1 }), 'sessionAnalytics.error.overviewFailed')
    expect(res).toEqual({ ok: 1 })
    expect(error.value).toBe('')
    expect(stale.value).toBe(false)
  })

  it('失败时 loading 也必须复位（否则按钮永久禁用）', async () => {
    const { loading, run } = useSessionAnalyticsLoader()
    await run(async () => {
      throw new Error('network')
    }, 'sessionAnalytics.error.overviewFailed')
    expect(loading.value).toBe(false)
  })

  it('错误文案必须来自指定的键（键缺失时仍有兜底文案而不是空串）', async () => {
    const { error, run } = useSessionAnalyticsLoader()
    await run(async () => {
      throw new Error('network down')
    }, 'sessionAnalytics.error.overviewFailed')
    expect(error.value.length).toBeGreaterThan(0)
  })

  it('多次并发调用各自复位各自的 loading（最后一次调用结束即完成）', async () => {
    const { loading, run } = useSessionAnalyticsLoader()
    const slow = ref(false)
    const p1 = run(async () => {
      await new Promise((r) => setTimeout(r, 30))
      slow.value = false
      return 1
    }, 'sessionAnalytics.error.overviewFailed')
    const p2 = run(async () => 2, 'sessionAnalytics.error.overviewFailed')
    await Promise.all([p1, p2])
    expect(loading.value).toBe(false)
  })
})
