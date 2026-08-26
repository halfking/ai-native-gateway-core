// useSessionSummaryJump.test.ts — 单测覆盖空值短路 / 钩子执行顺序 / router.push 调用。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { useSessionSummaryJump } from './useSessionSummaryJump'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/request-logs', name: 'request-logs', component: { template: '<div />' } },
    ],
  })
}

function mountComposable(opts?: Parameters<typeof useSessionSummaryJump>[0]) {
  let api: ReturnType<typeof useSessionSummaryJump> | null = null
  const harness = defineComponent({
    setup() {
      api = useSessionSummaryJump(opts)
      return () => h('div')
    },
  })
  const router = makeRouter()
  const wrapper = mount(harness, { global: { plugins: [router] } })
  return {
    api: api!,
    router,
    wrapper,
  }
}

type RouterPushArgs = Parameters<ReturnType<typeof makeRouter>['push']>

function spyOnRouterPush(router: ReturnType<typeof makeRouter>) {
  const spy = vi.fn<RouterPushArgs>()
  const realPush = router.push.bind(router)
  router.push = ((...args: RouterPushArgs) => {
    spy(...args)
    return realPush(...args)
  }) as typeof router.push
  return spy
}

describe('useSessionSummaryJump', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('pushes /request-logs?gw_session_id= when sessionId is non-empty', async () => {
    const { api, router } = mountComposable()
    const pushSpy = spyOnRouterPush(router)

    api.jumpToSessionSummary('gw_abc_123')
    await nextTick()
    expect(pushSpy).toHaveBeenCalledWith({
      path: '/request-logs',
      query: { gw_session_id: 'gw_abc_123', open_summary: '1' },
    })
  })

  it('trims whitespace before push', async () => {
    const { api, router } = mountComposable()
    const pushSpy = spyOnRouterPush(router)

    api.jumpToSessionSummary('   gw_xyz_456  ')
    await nextTick()
    expect(pushSpy).toHaveBeenCalledWith({
      path: '/request-logs',
      query: { gw_session_id: 'gw_xyz_456', open_summary: '1' },
    })
  })

  it('is a silent no-op for empty / null / undefined sessionId', async () => {
    const { api, router } = mountComposable()
    const pushSpy = spyOnRouterPush(router)

    api.jumpToSessionSummary('')
    api.jumpToSessionSummary('   ')
    api.jumpToSessionSummary(null as unknown as string)
    api.jumpToSessionSummary(undefined as unknown as string)
    await nextTick()
    expect(pushSpy).not.toHaveBeenCalled()
  })

  it('invokes onBeforeJump before the push', async () => {
    const order: string[] = []
    const { api, router } = mountComposable({
      onBeforeJump: () => order.push('hook'),
    })
    const pushSpy = spyOnRouterPush(router)
    // 用 wrapper 跟踪顺序而不是 spy；spy 仅确认 push 被调用
    void pushSpy
    const realPush = router.push.bind(router)
    router.push = ((...args: RouterPushArgs) => {
      order.push('push')
      return realPush(...args)
    }) as typeof router.push

    api.jumpToSessionSummary('gw_seq')
    await nextTick()
    expect(order).toEqual(['hook', 'push'])
  })

  it('swallows errors thrown by onBeforeJump but still pushes', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { api, router } = mountComposable({
      onBeforeJump: () => {
        throw new Error('drawer close failure')
      },
    })
    const pushSpy = spyOnRouterPush(router)

    // 不应 throw；router.push 仍要执行
    expect(() => api.jumpToSessionSummary('gw_still_works')).not.toThrow()
    await nextTick()
    expect(pushSpy).toHaveBeenCalled()
    consoleError.mockRestore()
  })
})
