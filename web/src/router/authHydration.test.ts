// router/authHydration.test.ts —— 路由守卫「等 auth 水合」的判据（2026-10-28 R176 回归）。
//
// ★ 这条测试守护的是一个**实测到的 10 秒性能缺陷**：
//   守卫原判据 `authHydrated && userInfo && userInfo.id` 把「访客」判成
//   「永远不就绪」⇒ 每个访客页面都白等满 10 秒才渲染正文
//   （实测 /forbidden 10131ms、/bootstrap 10108ms、/customer/update-activate 10116ms，
//     而这些页面 DOMContentLoaded 90ms / FCP 100ms ⇒ 慢的不是渲染，是等）。
//
// 判据来源：docs/UI规范/18 §3/§4 的同款纪律 —— 决策逻辑必须是**可单测的纯函数**。
import { describe, expect, it } from 'vitest'
import {
  AUTH_HYDRATE_MAX_WAIT_MS,
  AUTH_HYDRATE_POLL_MS,
  authHydrationSatisfied,
  waitForAuthHydration,
} from './authHydration'

describe('authHydrationSatisfied（判据）', () => {
  it('探测已 settle ⇒ 就绪（不管有没有 userInfo）', () => {
    expect(authHydrationSatisfied({ authHydrated: true, userInfo: null })).toBe(true)
    expect(authHydrationSatisfied({ authHydrated: true, userInfo: { id: 1 } })).toBe(true)
  })

  it('★ 访客（authHydrated=true 但 userInfo=null）必须判就绪', () => {
    // 旧判据在这里返回 false ⇒ 访客每页等 10 秒。
    expect(authHydrationSatisfied({ authHydrated: true, userInfo: null })).toBe(true)
  })

  it('探测未 settle ⇒ 未就绪', () => {
    expect(authHydrationSatisfied({ authHydrated: false, userInfo: null })).toBe(false)
    expect(authHydrationSatisfied({ authHydrated: false, userInfo: { id: 1 } })).toBe(false)
  })
})

describe('waitForAuthHydration（等待循环）', () => {
  it('★ 访客路径：探测一 settle 就放行，不等 10 秒', async () => {
    let polls = 0
    const p = waitForAuthHydration({
      snapshot: () => ({ authHydrated: true, userInfo: null }),
      schedule: (fn) => {
        polls++
        fn()
      },
    })
    await p
    expect(polls).toBe(0) // 一次都不轮询 ⇒ 没有 30ms/10s 的等待
  })

  it('探测未 settle ⇒ 按 pollMs 轮询直到 settle', async () => {
    let hydrated = false
    let polls = 0
    const p = waitForAuthHydration({
      snapshot: () => ({ authHydrated: hydrated, userInfo: null }),
      schedule: (fn) => {
        polls++
        hydrated = true
        fn()
      },
    })
    await p
    expect(polls).toBe(1)
  })

  it('★ 探测永不 settle ⇒ 超时兜底放行（不死锁）', async () => {
    let now = 0
    const p = waitForAuthHydration({
      snapshot: () => ({ authHydrated: false, userInfo: null }),
      timeoutMs: 1_000,
      pollMs: 100,
      now: () => now,
      schedule: (fn) => {
        now += 100
        fn()
      },
    })
    await p
    expect(now).toBeGreaterThanOrEqual(1_000)
  })

  it('常量符合死锁兜底设计', () => {
    expect(AUTH_HYDRATE_MAX_WAIT_MS).toBe(10_000)
    expect(AUTH_HYDRATE_POLL_MS).toBe(30)
  })
})
