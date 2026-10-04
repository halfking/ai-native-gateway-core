/* entry-switch.js — 统一入口的客户端腿（2026-10-04）。
 *
 * 网关在 "/" 与 "/index.html" 上按 User-Agent 做服务端 302 → /m/，但
 * iPadOS 13+ 的 Safari 报桌面级 UA（Macintosh + 触屏），服务端永远看不见
 * 触屏能力。这里用 pointer:coarse + 小屏补上这个缺口：触屏且短边 ≤ 930px
 * 的设备在裸入口被送到移动端。
 *
 * 与服务端（cmd/gateway/mobile_static.go）同一套克制语义：
 *   - 只切裸入口 location.pathname 为 '/' 或 '/index.html'；
 *   - ?desktop 参数存在即留下（不论取值，与服务端 URL.Query().Has("desktop")
 *     对齐——两条腿在同一份查询串上必须做同一个决定）；
 *   - 深链不动：PC 路由深链保持 PC 表面；
 *   - 不写 cookie/sessionStorage —— 刷新重新评估，方向始终由当前设备形态决定。
 */
;(function () {
  try {
    var path = location.pathname
    if (path !== '/' && path !== '/index.html') return
    if (/[?&]desktop(?:=|&|$)/.test(location.search)) return
    if (!window.matchMedia || !matchMedia('(pointer: coarse)').matches) return
    if (Math.min(screen.width, screen.height) > 930) return
    location.replace('/m/' + location.search)
  } catch (e) {
    /* 任何探测失败都留在 PC 端：入口切换是增强，不是门槛。 */
  }
})()
