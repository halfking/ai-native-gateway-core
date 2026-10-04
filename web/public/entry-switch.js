/* entry-switch.js — 统一入口的客户端腿（2026-10-04）。
 *
 * 网关在 "/" 上按 User-Agent 做服务端 302 → /m/，但 iPadOS 13+ 的 Safari
 * 报桌面级 UA（Macintosh + 触屏），服务端永远看不见触屏能力。这里用
 * pointer:coarse + 小屏补上这个缺口：触屏且短边 ≤ 930px 的设备在裸入口
 * （仅 "/"，深链不动）被送到移动端。
 *
 * 与服务端同一套克制语义：
 *   - 只切裸入口 location.pathname === '/'；
 *   - ?desktop=1 显式留下（与网关 MOBILE_WEB_ENTRY_REDIRECT/?desktop=1 对齐）；
 *   - 不写 cookie/sessionStorage —— 刷新重新评估，方向始终由当前设备形态决定。
 */
;(function () {
  try {
    if (location.pathname !== '/') return
    if (/[?&]desktop=1\b/.test(location.search)) return
    if (!window.matchMedia || !matchMedia('(pointer: coarse)').matches) return
    if (Math.min(screen.width, screen.height) > 930) return
    location.replace('/m/' + location.search)
  } catch (e) {
    /* 任何探测失败都留在 PC 端：入口切换是增强，不是门槛。 */
  }
})()
