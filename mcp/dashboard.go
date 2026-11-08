package mcp

// dashboardHTML 是 MCP 网关的运营看板页面（自包含，无外部依赖）。
//
// 设计取舍：刻意不引入前端构建链 —— 看板要能随网关二进制直接分发，在
// 内网/边缘环境里「打开就能看」，不依赖 npm registry 与 CDN。
//
// 认证：页面从 URL 查询参数 `token` 读取管理令牌（也支持页内输入后刷新）；
// 令牌只保存在内存（不落 localStorage，避免在共享机器上留痕）。
const dashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MCP 网关运营看板</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", sans-serif;
         margin: 0; padding: 24px; background: #f6f7f9; color: #1c1e21; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .sub { color: #6b7280; font-size: 13px; margin-bottom: 20px; }
  .cards { display: flex; flex-wrap: wrap; gap: 12px; margin-bottom: 20px; }
  .card { background: #fff; border: 1px solid #e5e7eb; border-radius: 8px; padding: 12px 16px; min-width: 150px; }
  .card .k { font-size: 12px; color: #6b7280; }
  .card .v { font-size: 22px; font-weight: 600; margin-top: 4px; }
  section { background: #fff; border: 1px solid #e5e7eb; border-radius: 8px; padding: 16px; margin-bottom: 16px; }
  section h2 { font-size: 15px; margin: 0 0 10px; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #f0f0f0; }
  th { color: #6b7280; font-weight: 500; }
  input { padding: 6px 8px; border: 1px solid #d1d5db; border-radius: 6px; min-width: 280px; }
  button { padding: 6px 12px; border-radius: 6px; border: 1px solid #d1d5db; background: #fff; cursor: pointer; }
  .err { color: #b91c1c; font-size: 13px; }
  .ok { color: #15803d; }
  code { background: #f3f4f6; padding: 1px 5px; border-radius: 4px; }
</style>
</head>
<body>
<h1>MCP 网关运营看板</h1>
<div class="sub">llm-gateway-go 内置 MCP 服务网关 · 数据面 <code>POST /mcp</code> · 控制面 <code>/mcp/control</code></div>

<div class="cards">
  <div class="card"><div class="k">适配器总数</div><div class="v" id="adapters-total">-</div></div>
  <div class="card"><div class="k">健康适配器</div><div class="v" id="adapters-healthy">-</div></div>
  <div class="card"><div class="k">审计事件数</div><div class="v" id="audit-total">-</div></div>
  <div class="card"><div class="k">放行调用</div><div class="v" id="audit-allowed">-</div></div>
  <div class="card"><div class="k">拒绝调用</div><div class="v" id="audit-denied">-</div></div>
  <div class="card"><div class="k">运行时长（秒）</div><div class="v" id="uptime">-</div></div>
</div>

<section>
  <h2>管理令牌</h2>
  <input id="token" type="password" placeholder="X-Admin-Token / Bearer">
  <button id="refresh">刷新</button>
  <span class="err" id="err"></span>
  <span class="err" id="err-detail"></span>
</section>

<section>
  <h2>上游适配器</h2>
  <table>
    <thead><tr><th>名称</th><th>端点</th><th>能力域</th><th>启用</th><th>熔断</th></tr></thead>
    <tbody id="adapters"><tr><td colspan="5">加载中…</td></tr></tbody>
  </table>
</section>

<section>
  <h2>Top 工具（按调用量）</h2>
  <table>
    <thead><tr><th>工具</th><th>调用次数</th></tr></thead>
    <tbody id="top-tools"><tr><td colspan="2">加载中…</td></tr></tbody>
  </table>
</section>

<section>
  <h2>最近审计事件</h2>
  <table>
    <thead><tr><th>时间</th><th>类别</th><th>结果</th><th>工具</th><th>主体</th><th>原因</th></tr></thead>
    <tbody id="events"><tr><td colspan="6">加载中…</td></tr></tbody>
  </table>
</section>

<script>
(function () {
  var tokenInput = document.getElementById('token');
  var errBox = document.getElementById('err');
  var params = new URLSearchParams(window.location.search);
  if (params.get('token')) { tokenInput.value = params.get('token'); }

  function token() { return tokenInput.value.trim(); }
  function esc(s) {
    return String(s === undefined || s === null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }
  function set(id, v) { document.getElementById(id).textContent = v; }
  // 错误文案与详情分成两段：错误文案是**稳定字符串**（供自动化断言），
  // 详情（HTTP 状态等）单独一段，避免整串拼接导致文本匹配失败。
  function fail(prefix, err) {
    document.getElementById('err').textContent = prefix;
    document.getElementById('err-detail').textContent = err && err.message ? err.message : '';
  }

  function api(path) {
    return fetch(path, { headers: token() ? { 'X-Admin-Token': token() } : {} })
      .then(function (r) {
        if (!r.ok) { throw new Error('HTTP ' + r.status); }
        return r.json();
      });
  }

  function load() {
    errBox.textContent = '';
    document.getElementById('err-detail').textContent = '';
    api('/mcp/control/health').then(function (h) {
      set('adapters-total', h.adapters_total);
      set('adapters-healthy', h.adapters_healthy);
      set('uptime', h.uptime_seconds);
      var rows = (h.adapters || []).map(function (a) {
        return '<tr><td>' + esc(a.name) + '</td><td>' + esc(a.endpoint) + '</td><td>' +
          esc(a.capability) + '</td><td>' + (a.enabled ? '是' : '否') + '</td><td>' +
          esc(a.circuit) + '</td></tr>';
      }).join('');
      document.getElementById('adapters').innerHTML = rows ||
        '<tr><td colspan="5">暂无适配器</td></tr>';
    }).catch(function (e) {
      fail('健康信息加载失败', e);
      document.getElementById('adapters').innerHTML =
        '<tr><td colspan="5">加载失败</td></tr>';
    });

    api('/mcp/control/audit?limit=200').then(function (a) {
      var stats = a.stats || {};
      set('audit-total', stats.total || 0);
      set('audit-allowed', (stats.by_outcome || {}).allowed || 0);
      set('audit-denied', (stats.by_outcome || {}).denied || 0);
      var tools = (stats.by_tool || {});
      var names = Object.keys(tools).sort(function (x, y) { return tools[y] - tools[x]; }).slice(0, 10);
      document.getElementById('top-tools').innerHTML = names.map(function (n) {
        return '<tr><td>' + esc(n) + '</td><td>' + tools[n] + '</td></tr>';
      }).join('') || '<tr><td colspan="2">暂无数据</td></tr>';
      var evs = a.events || [];
      document.getElementById('events').innerHTML = evs.map(function (e) {
        return '<tr><td>' + esc(e.at) + '</td><td>' + esc(e.kind) + '</td><td>' +
          esc(e.outcome) + '</td><td>' + esc(e.tool) + '</td><td>' +
          esc(e.user_id || e.key_id) + '</td><td>' + esc(e.reason) + '</td></tr>';
      }).join('') || '<tr><td colspan="6">暂无事件</td></tr>';
    }).catch(function (e) {
      if (!document.getElementById('err').textContent) { fail('审计加载失败', e); }
      document.getElementById('events').innerHTML =
        '<tr><td colspan="6">加载失败</td></tr>';
    });
  }

  document.getElementById('refresh').addEventListener('click', load);
  load();
})();
</script>
</body>
</html>
`
