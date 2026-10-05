// entry-switch.js — web-mobile 首帧脚本（<head> 内同步执行，外部文件）。
//
// 为什么是外部文件而不是内联 <script>：网关的 CSP 是
// `script-src 'self' 'unsafe-eval'`（middleware/security_headers_mw.go），
// **不含 'unsafe-inline'**，而 /m 挂载在安全头中间件之内
// （cmd/gateway/main.go：中间件入链在前，newMobileGatewayHandler 在后）。
// 内联首帧脚本会被浏览器直接拦掉，移动端主题首帧防 FOUC 静默失效。
// 桌面端 web/index.html 早已为同一个理由把 theme-init.js 拆成外部文件，
// 本文件是移动端的对齐。移植自 feat/web-mobile-hyper。
//
// 两个职责：
//  1. 主题首帧落位（防 FOUC，UI规范 14 §3）。语义必须与
//     src/stores/theme.ts 逐项一致：同一个 storage key
//     （llmgw_mobile_theme）、同一个落位方式（html.dark class）、
//     同一组 theme-color 取值。store 只负责用户显式切换，首帧由本脚本负责。
//  2. 统一入口反向切换：large（≥1280px）访问 /m 根入口时回桌面端 /。
//     正向腿（compact 访问 / 回 /m）在桌面侧 web/public/entry-switch.js
//     与网关服务端 UA 302 上，两侧合起来才是「一个入口自动选表面」。
//
// 断点 1280 是 UI规范 01 §2 的 large 档，与
// src/composables/useWindowClass.ts 的 BREAKPOINT_LARGE_PX 双镜像
// （该文件是 CSS token 的 TS 镜像，本文件是它的 JS 镜像）。
// 改断点必须三处同改。
//
// 与桌面侧一致的克制语义：
//   - 只切根入口（/m 或 /m/），深链（/m/nodes 等）不动；
//   - ?mobile 存在即留在移动端（不论取值）；
//   - ?desktop 存在即回桌面端（与网关侧 URL.Query().Has("desktop")
//     同一份查询串上做同一个决定）；
//   - 不写 cookie / sessionStorage —— 刷新重新评估，方向始终由当前
//     设备形态决定。桌面侧 entry-switch.js 的这条约定同样适用这里。
(function () {
  try {
    // ── 1. 主题首帧 ──────────────────────────────────────────────────
    var stored = null
    try {
      stored = localStorage.getItem('llmgw_mobile_theme')
    } catch (e) {
      stored = null
    }
    var dark =
      stored === 'dark' ||
      (!stored && window.matchMedia('(prefers-color-scheme: dark)').matches)
    if (dark) {
      document.documentElement.classList.add('dark')
      var meta = document.querySelector('meta[name="theme-color"]')
      if (meta) meta.setAttribute('content', '#0f141c')
    }

    // ── 2. 反向入口切换：large 根入口 → 桌面端 ────────────────────────
    var search = window.location.search || ''
    var path = window.location.pathname
    var isRootEntry = path === '/m' || path === '/m/' || path === '/m/index.html'
    // ?mobile 优先：用户显式选择留在移动端。
    if (/(^|[?&])mobile(?:=|&|$)/.test(search)) return
    if (!isRootEntry) return
    // ?desktop 显式要求桌面端（与网关侧 Has("desktop") 同一语义）。
    if (/(^|[?&])desktop(?:=|&|$)/.test(search)) {
      window.location.replace('/')
      return
    }
    var isLarge = window.matchMedia('(min-width: 1280px)').matches
    if (!isLarge) return
    var qs = search.replace(/[?&]mobile(?:=[^&]*)?/g, '').replace(/[?&]desktop(?:=[^&]*)?/g, '')
    if (qs && qs !== '?') qs = qs.replace(/^\?/, '?').replace(/^&&/, '?')
    // replace() 不留历史：移动端返回键不会弹回 /m 形成环。
    window.location.replace('/' + qs)
  } catch (e) {
    // 首帧脚本永不阻塞应用渲染。任何探测失败都留在移动端。
  }
})()
