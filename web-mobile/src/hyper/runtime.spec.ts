import type { Router } from 'vue-router'
import { describe, expect, it, vi } from 'vitest'
import type { OverlayHandle } from './types'
import { HyperRuntime } from './runtime'

describe('HyperRuntime nested overlay history', () => {
  it('does not treat a child close pop as a user Back that closes the parent', () => {
    const runtime = new HyperRuntime()
    const router = {
      afterEach: () => () => {},
      currentRoute: { value: { name: 'overview' } },
      replace: vi.fn(),
    } as unknown as Router
    runtime.install(router, {
      appName: 'Gateway',
      getAccountId: () => 'test-account',
      translate: (key) => key,
      getFallback: () => '/overview',
    })
    const parent: OverlayHandle = {
      id: 'account-sheet',
      presentation: 'sheet',
      title: () => 'Account',
      close: vi.fn(),
    }
    const child: OverlayHandle = {
      id: 'logout-confirm',
      presentation: 'modal',
      title: () => 'Sign out?',
      close: vi.fn(),
    }
    const originalState = window.history.state
    const originalUrl = window.location.href
    const back = vi.spyOn(window.history, 'back').mockImplementation(() => {})

    try {
      window.history.replaceState(null, '', originalUrl)
      runtime.presentOverlay(parent)
      runtime.presentOverlay(child)
      runtime.dismissOverlay(child.id)

      expect(back).toHaveBeenCalledTimes(1)
      expect(runtime.topOverlay.value?.id).toBe(parent.id)

      // Model WebKit returning an unmarked state for a same-URL programmatic pop.
      window.dispatchEvent(new PopStateEvent('popstate', { state: null }))

      expect(runtime.topOverlay.value?.id).toBe(parent.id)
      expect(parent.close).not.toHaveBeenCalled()
    } finally {
      const internal = runtime as unknown as { detach: Array<() => void> }
      internal.detach.forEach((detach) => detach())
      window.history.replaceState(originalState, '', originalUrl)
      back.mockRestore()
    }
  })

  it('keeps parent overlays open for same-route history changes, but closes them on navigation', () => {
    const runtime = new HyperRuntime()
    type RouteHook = Parameters<Router['afterEach']>[0]
    let routeHook: RouteHook | undefined
    const router = {
      afterEach: (hook: RouteHook) => {
        routeHook = hook
        return () => {}
      },
      currentRoute: { value: { name: 'settings', fullPath: '/settings' } },
      replace: vi.fn(),
    } as unknown as Router
    runtime.install(router, {
      appName: 'Gateway',
      getAccountId: () => 'test-account',
      translate: (key) => key,
      getFallback: () => '/settings',
    })
    const parent: OverlayHandle = {
      id: 'account-sheet',
      presentation: 'sheet',
      title: () => 'Account',
      close: vi.fn(),
    }
    const child: OverlayHandle = {
      id: 'logout-confirm',
      presentation: 'modal',
      title: () => 'Sign out?',
      close: vi.fn(),
    }
    const originalState = window.history.state
    const originalUrl = window.location.href
    const back = vi.spyOn(window.history, 'back').mockImplementation(() => {})

    try {
      window.history.replaceState(null, '', originalUrl)
      runtime.presentOverlay(parent)
      runtime.presentOverlay(child)
      runtime.dismissOverlay(child.id)

      const sameRoute = { fullPath: '/settings' } as Parameters<RouteHook>[0]
      const sameRouteFrom = { fullPath: '/settings' } as Parameters<RouteHook>[1]
      routeHook?.(sameRoute, sameRouteFrom)

      expect(runtime.topOverlay.value?.id).toBe(parent.id)
      expect(parent.close).not.toHaveBeenCalled()

      const nextRoute = { fullPath: '/nodes' } as Parameters<RouteHook>[0]
      routeHook?.(nextRoute, sameRoute)

      expect(runtime.topOverlay.value).toBeNull()
      expect(parent.close).toHaveBeenCalledTimes(1)
    } finally {
      const internal = runtime as unknown as { detach: Array<() => void> }
      internal.detach.forEach((detach) => detach())
      window.history.replaceState(originalState, '', originalUrl)
      back.mockRestore()
    }
  })
})
