// entry-switch.js — web-mobile 首帧脚本（<head> 内同步执行，CSP 外部文件）。
//
// 两个职责：
//  1. 主题首帧初始化（与桌面端 /theme-init.js 同一约定，防 FOUC）：
//     优先级 URL ?theme=light|dark > localStorage['llmgw_theme'] > 系统偏好。
//  2. 统一入口自动切换（UI 规范 17 §R11）：本 SPA 挂 /m，是 compact 主战场；
//     large（≥1280px，01 §2 桌面红线档）访问 /m 的**根入口**时自动回桌面端 /。
//     判定用 window class（宽度），不猜 User-Agent。覆盖顺序：
//       ?ui=mobile 显式停留 > sessionStorage['llmgw_ui_mode'] 用户选择 > 自动判定。
//     只在根入口（/m 或 /m/）触发，深链（/m/nodes 等）与桌面 deliberate 访问不弹跳。
(function () {
  try {
    // ── 主题 ───────────────────────────────────────────────────────────
    var sp = new URLSearchParams(window.location.search)
    var t = sp.get('theme')
    if (t !== 'light' && t !== 'dark') {
      try { t = localStorage.getItem('llmgw_theme') } catch (e) { t = null }
    }
    if (t !== 'light' && t !== 'dark') {
      t = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
    }
    document.documentElement.setAttribute('data-theme', t)
    document.documentElement.style.colorScheme = t

    // ── 统一入口：large 根入口 → 桌面端 ────────────────────────────────
    var ui = sp.get('ui')
    if (ui === 'mobile') {
      try { sessionStorage.setItem('llmgw_ui_mode', 'mobile') } catch (e) {}
    } else if (ui === 'desktop') {
      try { sessionStorage.setItem('llmgw_ui_mode', 'desktop') } catch (e) {}
    }
    var forced = null
    try { forced = sessionStorage.getItem('llmgw_ui_mode') } catch (e) {}
    var isRootEntry =
      window.location.pathname === '/m' || window.location.pathname === '/m/'
    var isLarge = window.matchMedia('(min-width: 1280px)').matches
    if (isRootEntry && forced !== 'mobile' && isLarge) {
      // replace() 不留历史，桌面端返回键不会弹回 /m 形成环。
      var qs = window.location.search
        .replace(/[?&]ui=mobile/g, '')
        .replace(/[?&]ui=desktop/g, '')
      var target = '/' + (qs && qs !== '?' ? qs : '')
      window.location.replace(target)
    }
  } catch (e) {
    // 首帧脚本永不阻塞应用渲染。
  }
})()
