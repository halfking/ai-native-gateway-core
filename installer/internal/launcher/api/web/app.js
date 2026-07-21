// KX Launcher web UI — minimal vanilla JS, no framework.
// Polls /status + /plan every 3s. Token stored in localStorage.

const TOKEN_KEY = 'kxLauncherToken';
let TOKEN = localStorage.getItem(TOKEN_KEY) || '';
let currentPlanId = null;
let pollTimer = null;

function getToken() {
  if (!TOKEN) {
    TOKEN = prompt('请输入 Launcher token\n(在服务器上执行: cat /var/lib/kx-launcher/token)') || '';
    if (TOKEN) localStorage.setItem(TOKEN_KEY, TOKEN);
  }
  return TOKEN;
}

function clearToken() {
  TOKEN = '';
  localStorage.removeItem(TOKEN_KEY);
}

async function api(path, opts = {}) {
  const r = await fetch('/launcher/api' + path, {
    ...opts,
    headers: {
      'X-Launcher-Token': getToken(),
      'Content-Type': 'application/json',
      ...(opts.headers || {}),
    },
  });
  if (r.status === 401) {
    clearToken();
    setAuth('unauthorized');
    throw new Error('unauthorized');
  }
  setAuth('ok');
  return r.json();
}

function setAuth(state) {
  const el = document.getElementById('auth-status');
  if (state === 'ok') el.textContent = '✓ 认证';
  else if (state === 'unauthorized') el.textContent = '✗ token 无效';
  else el.textContent = '';
}

function toast(msg) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.remove('hidden');
  setTimeout(() => el.classList.add('hidden'), 3000);
}

const STATES = ['NOTIFIED', 'PREPARING', 'PREPARED', 'ACTIVATING', 'DRAINING', 'DONE'];

async function refresh() {
  try {
    const s = await api('/status');
    document.getElementById('active-version').textContent = s.active_version || '—';
    document.getElementById('active-addr').textContent = s.active_addr || '—';
    document.getElementById('plan-state').textContent = s.plan_state || '无';
    const dot = document.getElementById('health-dot');
    dot.className = 'dot ' + (s.active_addr ? 'healthy' : 'unknown');
  } catch (e) { /* token prompt handled in api() */ }
}

async function loadPlan() {
  try {
    const p = await api('/plan');
    const card = document.getElementById('plan-card');
    if (!p || !p.id) {
      card.classList.add('hidden');
      currentPlanId = null;
      return;
    }
    currentPlanId = p.id;
    card.classList.remove('hidden');
    document.getElementById('plan-id').textContent = p.id;
    const prog = document.getElementById('plan-progress');
    const curIdx = STATES.indexOf(p.state);
    prog.innerHTML = STATES.map((st, i) => {
      let cls = '';
      if (st === p.state) cls = 'active';
      else if (curIdx >= 0 && i < curIdx) cls = 'done';
      if (p.state === 'FAILED' && st === p.state) cls = 'failed';
      if (p.state === 'ROLLED_BACK' && st === 'DONE') cls = 'done';
      return `<span class="${cls}">${st}</span>`;
    }).join('');
    document.getElementById('plan-detail').textContent = JSON.stringify(p, null, 2);
    // Enable buttons based on state
    document.getElementById('btn-prepare').disabled = !['NOTIFIED', 'FAILED'].includes(p.state);
    document.getElementById('btn-apply').disabled = p.state !== 'PREPARED';
    const hint = document.getElementById('plan-hint');
    if (p.state === 'PREPARED') hint.textContent = '✓ 准备就绪。点 Apply 将流量切换到新版本。';
    else if (p.state === 'DONE') hint.textContent = '✓ 升级完成,流量已在新版本。';
    else if (p.state === 'FAILED') hint.textContent = '⚠ 失败:' + (p.error || 'unknown');
    else hint.textContent = '';
  } catch (e) { /* token prompt handled */ }
}

document.getElementById('btn-check').onclick = async () => {
  try {
    const r = await api('/check', { method: 'POST' });
    document.getElementById('check-result').textContent = JSON.stringify(r);
    toast('已检查');
    setTimeout(loadPlan, 600);
  } catch (e) {}
};

document.getElementById('btn-prepare').onclick = async () => {
  if (!currentPlanId) return;
  if (!confirm('开始 Prepare?\n将下载新版本、运行数据库迁移、启动新实例(不影响当前服务)。')) return;
  try {
    await api('/plan/' + currentPlanId + '/prepare', { method: 'POST' });
    toast('Prepare 已启动');
    setTimeout(loadPlan, 1500);
  } catch (e) {}
};

document.getElementById('btn-apply').onclick = async () => {
  if (!currentPlanId) return;
  if (!confirm('确认 Apply?\n流量将切换到新版本。旧版本进入 drain。')) return;
  try {
    // Two-step gate: server requires {confirm:true}
    const r = await api('/plan/' + currentPlanId + '/apply', {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    });
    toast(r.status === 'applied' ? '切换完成' : JSON.stringify(r));
    setTimeout(loadPlan, 1500);
  } catch (e) {}
};

document.getElementById('btn-rollback').onclick = async () => {
  if (!currentPlanId) return;
  if (!confirm('确认 Rollback?\n流量将切回上一个活跃实例。')) return;
  try {
    await api('/plan/' + currentPlanId + '/rollback', { method: 'POST' });
    toast('已回滚');
    setTimeout(loadPlan, 1500);
  } catch (e) {}
};

// Boot
(async () => {
  await refresh();
  await loadPlan();
  pollTimer = setInterval(async () => {
    await refresh();
    await loadPlan();
  }, 3000);
})();
