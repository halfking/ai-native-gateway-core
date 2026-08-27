// theme-init.js — 在 <head> 内同步执行，初始化 data-theme
// 必须比 React/Vue 渲染早，避免主题切换闪烁（FOUC）。
// 优先级：URL ?theme=light|dark > localStorage['llmgw_theme'] > 系统偏好
// URL 永远赢；命中后写回 localStorage，方便后续刷新保持。
//
// 2026-08-25：从 index.html 拆出来作为外部脚本，避免被 CSP 拦截
// （CSP script-src 'self' 'unsafe-eval' 不允许 inline script）。
(function () {
  try {
    var sp = new URLSearchParams(window.location.search);
    var t = sp.get('theme');
    if (t !== 'light' && t !== 'dark') {
      try { t = localStorage.getItem('llmgw_theme'); } catch (e) {}
    }
    if (t !== 'light' && t !== 'dark') {
      t = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    }
    document.documentElement.setAttribute('data-theme', t);
    document.documentElement.style.colorScheme = t;
    try { localStorage.setItem('llmgw_theme', t); } catch (e) {}
  } catch (e) {}
})();
