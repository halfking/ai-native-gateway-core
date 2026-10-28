/**
 * router/authHydration.ts —— 路由守卫「等 auth 水合」的判据（纯逻辑，可单测）。
 *
 * ★ 2026-10-28（R176 实测修正）的来历：
 *   守卫原来写的是 `store.authHydrated && store.userInfo && store.userInfo.id`，
 *   把「探测已 settle」误写成「拿到了用户」。**访客永远没有 userInfo**
 *   ⇒ 一旦走进这个分支，唯一出口就是 10 秒的 AUTH_HYDRATE_MAX_WAIT_MS
 *   ⇒ **每个访客页面都白等满 10 秒**才渲染正文
 *   （实测 2026-10-28：/forbidden 10131ms、/bootstrap 10108ms、
 *     /customer/update-activate 10116ms；而这些页面的 FCP 只要 100ms、DOMContentLoaded 90ms）。
 *   登录态不受影响（登录后 userInfo 在位 ⇒ 立即放行）⇒ 这也解释了
 *   「为什么只有访客面慢、登录面全绿」。
 *
 *   守卫真正要等的是「**auth 探测（/api/auth/me）已经 settle**」（否则 cookie 登录的用户
 *   会在水合完成前被弹回首页 —— 见 router.ts 的注释）。
 *   App.vue 的 onMounted 在 finally 里 markAuthHydrated()，且 setUserInfo(user)
 *   排在它**之前** ⇒ authHydrated=true 时已登录者的 userInfo 必然已在位，
 *   isAuthed()（含 localStorage 兜底）可以直接用 ⇒ **不需要再要求 userInfo.id**。
 */

/** 守卫要等的东西的最小快照。 */
export interface AuthHydrationSnapshot {
  /** /api/auth/me 探测是否已经 settle（App.vue 的 markAuthHydrated）。 */
  authHydrated: boolean
  /** 当前用户信息（可能为 null —— 访客就是 null）。保留在快照里只为让判据自解释。 */
  userInfo?: { id?: unknown } | null
}

/**
 * 「等 auth 水合」的判据：**只看探测是否 settle**。
 *
 * ★ 旧判据（`authHydrated && userInfo && userInfo.id`）把访客判成「永远不就绪」。
 */
export function authHydrationSatisfied(snapshot: AuthHydrationSnapshot): boolean {
  return snapshot.authHydrated === true
}

/** 死锁兜底上限：探测始终不 settle 时（接口挂掉 / 竞态）最多等这么久。 */
export const AUTH_HYDRATE_MAX_WAIT_MS = 10_000

/** 轮询间隔。 */
export const AUTH_HYDRATE_POLL_MS = 30

export interface WaitForAuthHydrationOptions {
  /** 读当前快照。 */
  snapshot: () => AuthHydrationSnapshot
  /** 上限（默认 AUTH_HYDRATE_MAX_WAIT_MS）。 */
  timeoutMs?: number
  /** 轮询间隔（默认 AUTH_HYDRATE_POLL_MS）。 */
  pollMs?: number
  /** 时钟（默认 Date.now）。 */
  now?: () => number
  /** 计时器（默认 setTimeout）。测试可注入假计时器。 */
  schedule?: (fn: () => void, ms: number) => unknown
}

/**
 * 等 auth 水合完成（带死锁兜底）。
 *
 * ★ 访客路径现在**在探测 settle 的那一刻就放行**（一次轮询、不再是 10 秒），
 *   这就是「访客页面 10 秒」那个缺陷的修法。
 */
export function waitForAuthHydration(opts: WaitForAuthHydrationOptions): Promise<void> {
  const timeoutMs = opts.timeoutMs ?? AUTH_HYDRATE_MAX_WAIT_MS
  const pollMs = opts.pollMs ?? AUTH_HYDRATE_POLL_MS
  const now = opts.now ?? (() => Date.now())
  const schedule = opts.schedule ?? ((fn: () => void, ms: number) => setTimeout(fn, ms))
  const startedAt = now()
  return new Promise<void>((resolve) => {
    const check = () => {
      if (authHydrationSatisfied(opts.snapshot())) {
        resolve()
      } else if (now() - startedAt >= timeoutMs) {
        resolve()
      } else {
        schedule(check, pollMs)
      }
    }
    check()
  })
}
