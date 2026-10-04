// entry-switch.js — 桌面端 web/ 首帧脚本（<head> 内同步执行，CSP 外部文件，
// 与 theme-init.js 同款先例）。
//
// 职责：统一入口自动切换（UI 规范 17 §R11 / 01 §2）——compact（<600px，
// 竖屏手机）访问桌面根入口时自动进移动端 /m。判定用 window class（宽度），
// 不猜 User-Agent。覆盖顺序：
//   ?ui=desktop 显式停留 > sessionStorage['llmgw_ui_mode'] 用户选择 > 自动判定。
// 只在根入口（/ 或空路径）触发；深链与 deliberate 移动访问不弹跳。
// 移动端侧的反向切换（large 根入口 → /）由 /m/entry-switch.js 承担。
(function () {
  try {
    var sp = new URLSearchParams(window.location.search)
    var ui = sp.get('ui')
    if (ui === 'desktop') {
      try { sessionStorage.setItem('llmgw_ui_mode', 'desktop') } catch (e) {}
    } else if (ui === 'mobile') {
      try { sessionStorage.setItem('llmgw_ui_mode', 'mobile') } catch (e) {}
    }
    var forced = null
    try { forced = sessionStorage.getItem('llmgw_ui_mode') } catch (e) {}
    // 根入口判定：pathname 为 '/' 或空。桌面 SPA 的一切深链（/login、
    // /dashboard 等）不弹跳——深链可能来自分享/收藏，强行改写目标更糟。
    var isRootEntry = window.location.pathname === '/' || window.location.pathname === ''
    // compact：<600px（01 §2 断点 SSOT；600–1279 的 medium/expanded 归移动端
    // SPA 自己的宽栅格或桌面端皆可，不强制弹跳）。
    var isCompact = window.matchMedia('(max-width: 599.98px)').matches
    if (isRootEntry && forced !== 'desktop' && isCompact) {
      // replace() 不留历史：移动端返回键不会弹回桌面根形成环。
      var qs = window.location.search
        .replace(/[?&]ui=mobile/g, '')
        .replace(/[?&]ui=desktop/g, '')
      var target = '/m' + (qs && qs !== '?' ? qs : '')
      window.location.replace(target)
    }
  } catch (e) {
    // 首帧脚本永不阻塞桌面 SPA 渲染。
  }
})()
