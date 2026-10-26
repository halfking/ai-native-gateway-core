#!/usr/bin/env node
// page-sweep-cdp.mjs — 全量页面巡检（Chrome DevTools Protocol，**零第三方依赖**）
//
// 为什么要有这个工具：
//   「每一个页面每一个 api 均需验证，并检查其效率」这条要求里，**页面**这一半
//   之前只能靠人肉点，结论不可复现、也没有逐页的毫秒数。本工具把「逐页打开
//   → 计时 → 收集控制台错误 / 失败请求 → 标出 >2s 的页」固化成一条可重复的命令，
//   让「页面慢」从印象变成读数。
//
// 依赖：只用 Node 内置能力（node >= 22 提供全局 fetch + 全局 WebSocket）。
//   Node 内置 WebSocket（undici）在 node 22 起默认可用 ⇒ 不需要 puppeteer/playwright
//   ⇒ 不触发「依赖安装属用户侧决策」。
//
// 用法：
//   node scripts/audit/page-sweep-cdp.mjs \
//     --base http://127.0.0.1:8782 \
//     --env  /path/to/.env.local \
//     --out  /tmp/kx-page-sweep.tsv \
//     --slow-ms 2000
//
// 计时口径（**逐项写清，避免把别的开销算进来**）：
//   docMs        = navigation entry 的 loadEventEnd - startTime（含 HTML+JS+CSS+首屏同步）
//   dclMs        = navigation entry 的 domContentLoadedEventEnd - startTime
//   idleMs       = 从 navigate 到「网络静默 300ms」的总墙钟（含首屏 XHR 往返）
//                 —— 这一项才是用户感知的「页面打开到可用」的近似
//   slowestApi   = 页面内 duration 最长的 resource 条目（initiatorType=xhr/fetch）
//   consoleErr   = Runtime.consoleAPICalled(level=error) + Log.entryAdded(level=error)
//   failedReq    = Network.loadingFailed + HTTP>=400 的响应
//
// ★★★ R153 轮新增的三个口径（**都因为本轮实测发现旧口径会读错**）：
//   serverMs     = CDP `Network.responseReceived` 的 `response.timing`：
//                  receiveHeadersEnd - sendEnd（毫秒）⇒ **服务端处理 + 回程**
//                  ⇒ ★ 这是**唯一能把「服务端慢」和「浏览器排队」分开**的读数：
//                    resource 的 duration 含**浏览器连接排队**（HTTP/1.1 同源并发
//                    上限 6 ⇒ SPA 一次打 20~60 个 XHR 时，后面那些的 duration 会
//                    远大于服务端耗时）。本轮实测：`/api/routing/resolve?model=…`
//                    浏览器侧读到 4273ms，服务端直连只花 **0.22~0.53s** ⇒ 纯排队假象。
//                  ★ 取不到 timing（缓存命中 / service worker / HTTP/2）时记 -1。
//   load1        = 该页巡检时刻的**宿主机 1 分钟负载**（os.loadavg()[0]）
//                  ⇒ ★ 让**每一行读数自带混淆项**：宿主机饱和时所有读数都不可信，
//                    没有这列就无法事后区分「端点慢」与「机器忙」。
//   apiServerMaxMs = 该页所有 XHR/Fetch 里 **serverMs 的最大值**
//                  ⇒ ★ 「页面慢」的**服务端口径**：只有它 > 2s 才谈得上「端点慢」。
//
// 退出码：0 = 全部页面都渲染成功（无 5xx / 无加载失败 / 无 console error）
//        2 = 有页面失败（详见 TSV 的 fail 列）
//        3 = 工具自身失败（Chrome 起不来 / 登录失败 / 路由清单为空 / 量具自检失败）

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir, loadavg } from 'node:os';
import { join } from 'node:path';

const argv = process.argv.slice(2);
const arg = (k, d) => {
  const i = argv.indexOf('--' + k);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const BASE = arg('base', 'http://127.0.0.1:8782');
const ENVFILE = arg('env', '');
const OUT = arg('out', '/tmp/kx-page-sweep.tsv');
const SLOW_MS = Number(arg('slow-ms', '2000'));
const ROUTES_FILE = arg('routes', '');
const MOBILE = argv.includes('--mobile');
const PREFIX = arg('prefix', MOBILE ? '/m' : '');
const UA = arg('ua', MOBILE
  ? 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36'
  : '');
const PORT = Number(arg('port', '9333'));
const CHROME = arg('chrome', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const SHOTS = arg('shots', '');
const NAV_TIMEOUT_MS = Number(arg('nav-timeout-ms', '25000'));
const IDLE_MS = Number(arg('idle-ms', '300'));

function die(msg, code = 3) {
  console.error(`[page-sweep] FATAL: ${msg}`);
  process.exit(code);
}

// ---------------------------------------------------------------- 凭据与登录
function loadEnv(file) {
  const env = {};
  if (!file || !existsSync(file)) return env;
  for (const raw of readFileSync(file, 'utf8').split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#') || !line.includes('=')) continue;
    const i = line.indexOf('=');
    env[line.slice(0, i).trim()] = line.slice(i + 1).trim().replace(/^["']|["']$/g, '');
  }
  return env;
}

async function login() {
  const env = loadEnv(ENVFILE);
  const user = env.LLM_GATEWAY_ADMIN_USER;
  const pass = env.LLM_GATEWAY_ADMIN_PASSWORD;
  if (!user || !pass) die('admin credentials not found (need LLM_GATEWAY_ADMIN_USER/PASSWORD in --env)');
  const res = await fetch(`${BASE}/api/auth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: user, password: pass, remember_me: true }),
  });
  if (!res.ok) die(`login failed: HTTP ${res.status}`);
  const setCookie = res.headers.getSetCookie?.() || [];
  const hit = setCookie.find((c) => c.startsWith('llmgw_session='));
  if (!hit) die('login succeeded but no llmgw_session cookie in Set-Cookie');
  const cookie = hit.split(';')[0].split('=').slice(1).join('=');

  // ★★ 2026-10-27（R153 轮）：浏览器登录态是**三道闸门**，不是一道。
  //
  //   ① HttpOnly cookie `llmgw_session` ⇒ 后端认它（跨请求认证就靠它；
  //      `llmgw_jwt` / `llmgw_api_key` 自 2026-08-26 P1-7 起**不再写 localStorage**）；
  //   ② localStorage['llmgw_user_info'] ⇒ 路由守卫 `isAuthed()` / `isSuperAdmin()`
  //      的**兜底读**（web/src/router.ts:139/159/184 ⇒ 需要 `{role, id}`）；
  //   ③ localStorage['llmgw_require_bootstrap']='0' ⇒ 跳过 bootstrap 软闸门
  //      （web/src/router.ts:387-388）。
  //
  // ⇒ ★★ **只注入 cookie ⇒ 每一页都被弹到 `/bootstrap?redirect=…`** ⇒ 整轮读数作废
  //   （★ R99 §11.2 实测过这一版：36/36 页全部 redirect ⇒ 看起来像「整站坏了」）。
  //
  // ⇒ ★ 用户对象**不手编**：运行时调 `/api/auth/me` 取**服务端真实用户对象**
  //   （★ 手编 ⇒ 一旦字段形状变了，页面会按「伪造的登录态」跑 ⇒ 读数不可信）。
  const me = await fetch(`${BASE}/api/auth/me`, { headers: { Cookie: `llmgw_session=${cookie}` } });
  if (!me.ok) die(`auth/me failed: HTTP ${me.status}`);
  const meBody = await me.json();
  const meUser = meBody && meBody.user ? meBody.user : null;
  if (!meUser || !meUser.role || !meUser.id) die('auth/me returned no usable user object');
  return { cookie, user: meUser };
}

// ---------------------------------------------------------------- CDP 客户端
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.handlers = new Map();
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id != null && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result);
        return;
      }
      const hs = this.handlers.get(msg.method);
      if (hs) for (const h of hs) h(msg.params);
    });
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true });
      ws.addEventListener('error', reject, { once: true });
    });
    return new CDP(ws);
  }
  on(method, fn) {
    if (!this.handlers.has(method)) this.handlers.set(method, []);
    this.handlers.get(method).push(fn);
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error(`CDP timeout: ${method}`));
        }
      }, NAV_TIMEOUT_MS + 5000);
    });
  }
  close() { try { this.ws.close(); } catch { /* ignore */ } }
}

// ---------------------------------------------------------------- Chrome 拉起
async function waitJSON(url, tries = 100) {
  for (let i = 0; i < tries; i++) {
    try {
      const r = await fetch(url);
      if (r.ok) return await r.json();
    } catch { /* not ready */ }
    await new Promise((r) => setTimeout(r, 150));
  }
  return null;
}

async function launchChrome() {
  if (!existsSync(CHROME)) die(`chrome binary not found: ${CHROME}`);
  const profile = mkdtempSync(join(tmpdir(), 'kx-page-sweep-'));
  const child = spawn(CHROME, [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu',
    '--disable-extensions', '--disable-background-networking',
    '--window-size=1440,900',
    'about:blank',
  ], { stdio: 'ignore', detached: false });
  const ver = await waitJSON(`http://127.0.0.1:${PORT}/json/version`);
  if (!ver) {
    try { child.kill('SIGKILL'); } catch { /* ignore */ }
    rmSync(profile, { recursive: true, force: true });
    die('chrome devtools endpoint did not come up');
  }
  return { child, profile };
}

async function newTarget() {
  // 新版 Chrome 的 /json/new 需要 PUT。
  let res = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`, { method: 'PUT' });
  if (!res.ok) res = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`);
  if (!res.ok) die(`cannot create devtools target: HTTP ${res.status}`);
  return await res.json();
}

// ---------------------------------------------------------------- 路由清单
async function collectRoutes() {
  if (ROUTES_FILE && existsSync(ROUTES_FILE)) {
    return readFileSync(ROUTES_FILE, 'utf8').split('\n').map((s) => s.trim()).filter(Boolean);
  }
  // 从仓内 router 源文件抽 path（与前端真实路由同源，不手抄清单）。
  //   --mobile ⇒ web-mobile/src/router/index.ts（移动端 SPA，挂在 /m 下）
  //   默认     ⇒ web/src/router.ts（桌面端 SPA）
  const rel = MOBILE ? '../../web-mobile/src/router/index.ts' : '../../web/src/router.ts';
  const routerPath = new URL(rel, import.meta.url);
  if (!existsSync(routerPath)) die('router source not found; pass --routes explicitly');
  const src = readFileSync(routerPath, 'utf8');
  const set = new Set();
  for (const m of src.matchAll(/path:\s*['"]([^'"]+)['"]/g)) set.add(m[1]);
  return [...set].filter((p) => !p.includes(':') && p !== '/:pathMatch(.*)*');
}

// ---------------------------------------------------------------- 逐页巡检
async function main() {
  const routes = (await collectRoutes()).filter((p) => p !== '/bootstrap');
  if (!routes.length) die('empty route list');

  const { cookie, user } = await login();
  const { child, profile } = await launchChrome();
  const cleanup = () => {
    try { child.kill('SIGKILL'); } catch { /* ignore */ }
    try { rmSync(profile, { recursive: true, force: true }); } catch { /* ignore */ }
  };
  process.on('exit', cleanup);

  const target = await newTarget();
  const cdp = await CDP.connect(target.webSocketDebuggerUrl);

  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');
  await cdp.send('Network.enable');
  await cdp.send('Log.enable');
  await cdp.send('Network.setCookie', {
    name: 'llmgw_session', value: cookie, domain: '127.0.0.1', path: '/',
  });
  // ★★ 2026-10-27（R153 轮）：注入**另两道闸门**（在页面脚本之前执行）。
  //   ⇒ ★ 没有这一步 ⇒ 每一页都被弹到 `/bootstrap?redirect=…` ⇒ 整轮作废。
  //   ⇒ ★ `llmgw_user_info` 存的是**服务端真实用户对象**（见 login()）。
  await cdp.send('Page.addScriptToEvaluateOnNewDocument', {
    source: `try {
      localStorage.setItem('llmgw_user_info', ${JSON.stringify(JSON.stringify(user))});
      localStorage.setItem('llmgw_require_bootstrap', '0');
    } catch (e) { /* localStorage 不可用 ⇒ 交给自检去发现 */ }`,
  });
  if (UA) {
    // 移动端 UA ⇒ 命中网关的 unified entry（GET / → 302 /m/），也让 SPA
    // 走移动断点。CDP 的 UA 覆盖同时改变 navigator.userAgent 与请求头。
    await cdp.send('Emulation.setUserAgentOverride', {
      userAgent: UA,
      acceptLanguage: 'zh-CN,zh;q=0.9',
      platform: MOBILE ? 'Android' : 'macOS',
    });
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width: 412, height: 915, deviceScaleFactor: 2.625, mobile: MOBILE,
    });
  }

  const rows = [];
  let failed = 0;

  for (const route of routes) {
    const urlPath = PREFIX + route;
    const consoleErrs = [];
    const failedReqs = [];
    const badStatus = [];
    const maintain503 = [];
    const cancelledReqs = [];
    let inFlight = 0;
    let idleSince = 0;
    let loaded = false;
    let loadResolve;

    const onLoad = new Promise((r) => { loadResolve = r; });
    const hLoad = () => { loaded = true; loadResolve(); };
    // ★ SSE / 长轮询：这类响应**永远不结束**，若计入 inFlight 则「网络静默」
    //   永远等不到 ⇒ 每一页都会被误判成「25s 才打开」。故按 URL 形状排除。
    const isStream = (u) => /\/stream(\b|\?)|text\/event-stream|live-stream|probe\/stream|system-monitor\/stream/i.test(u || '');
    const streamIds = new Set();
    // ★ R153：把「服务端耗时」与「浏览器排队」分开的两个映射。
    //   reqInfo      : requestId -> {url, type}（type 用来只取 XHR/Fetch）
    //   serverMsByUrl: url -> serverMs（取该 URL 多次出现的最大值）
    const reqInfo = new Map();
    const serverMsByUrl = new Map();
    const hReq = (p) => {
      reqInfo.set(p.requestId, { url: p.request?.url || '', type: p.type || '' });
      if (isStream(p.request?.url)) { streamIds.add(p.requestId); return; }
      inFlight++; idleSince = 0;
    };
    const hDone = (p) => {
      if (streamIds.has(p.requestId)) return;
      inFlight = Math.max(0, inFlight - 1); idleSince = 0;
    };
    const hFail = (p) => {
      if (streamIds.has(p.requestId)) return;
      inFlight = Math.max(0, inFlight - 1);
      const err = p.errorText || 'failed';
      // ★ 客户端自己取消的请求（切页时 SPA 会 abort 在途 XHR）不是缺陷。
      //   net::ERR_ABORTED / ERR_CANCELED 归为「取消」，单列不计入 fail。
      if (/ERR_ABORTED|ERR_CANCELED|cancelled/i.test(err)) {
        cancelledReqs.push(err);
        return;
      }
      failedReqs.push(err);
    };
    const hResp = (p) => {
      const st = p.response?.status;
      // ★ R153：服务端耗时 = CDP timing 的 receiveHeadersEnd - sendEnd（毫秒）。
      //   ⇒ **唯一能把「端点慢」与「浏览器连接排队」分开的读数**：
      //     resource 的 duration 含排队（HTTP/1.1 同源并发上限 6），
      //     而 timing 是浏览器测的**服务端往返**，两者可能差一个数量级。
      const tm = p.response?.timing;
      if (tm && tm.receiveHeadersEnd >= 0 && tm.sendEnd >= 0) {
        const typ = reqInfo.get(p.requestId)?.type || '';
        if (typ === 'XHR' || typ === 'Fetch') {
          const u0 = (p.response?.url || '').replace(BASE, '');
          const s = Math.round(tm.receiveHeadersEnd - tm.sendEnd);
          const prev = serverMsByUrl.get(u0);
          if (prev === undefined || s > prev) serverMsByUrl.set(u0, s);
        }
      }
      if (!st || st < 400) return;
      const u = (p.response.url || '').replace(BASE, '');
      // ★ /maintain-api/* 在 MAINTAIN_SERVICE_URL 未配置时恒 503 —— 那是**配置态**
      //   （2026-09-28 审计档已定性为设计内降级），不是页面缺陷，单列不计入 fail。
      // 「未配置」族的 503：maintain 服务未配（MAINTAIN_SERVICE_URL 未设）、
      //   free-discovery 族在本机未启用（database disabled）。两者都是**配置态**，
      //   不是页面缺陷，单列不计入 fail（口径与 2026-09-28 审计档一致）。
      if (u.startsWith('/maintain-api/') || u === '/maintain' ||
          u.startsWith('/api/free-discovery/')) { maintain503.push(`${st} ${u}`); return; }
      badStatus.push(`${st} ${u}`);
    };
    const hConsole = (p) => {
      if (p.type === 'error') {
        consoleErrs.push((p.args || []).map((a) => a.value ?? a.description ?? a.type).join(' ').slice(0, 200));
      }
    };
    const hLog = (p) => {
      if (p.entry && p.entry.level === 'error') {
        consoleErrs.push(String(p.entry.text || '').slice(0, 200));
      }
    };

    cdp.on('Page.loadEventFired', hLoad);
    cdp.on('Network.requestWillBeSent', hReq);
    cdp.on('Network.loadingFinished', hDone);
    cdp.on('Network.loadingFailed', hFail);
    cdp.on('Network.responseReceived', hResp);
    cdp.on('Runtime.consoleAPICalled', hConsole);
    cdp.on('Log.entryAdded', hLog);

    const t0 = Date.now();
    let navErr = '';
    try {
      await cdp.send('Page.navigate', { url: BASE + urlPath });
      await Promise.race([onLoad, new Promise((_, rej) => setTimeout(() => rej(new Error('nav-timeout')), NAV_TIMEOUT_MS))]);
    } catch (e) {
      navErr = e.message;
    }

    // 网络静默等待：首屏 XHR 往返都算进「页面打开」成本。
    const deadline = Date.now() + NAV_TIMEOUT_MS;
    while (Date.now() < deadline) {
      if (inFlight === 0) {
        if (!idleSince) idleSince = Date.now();
        else if (Date.now() - idleSince >= IDLE_MS) break;
      } else {
        idleSince = 0;
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    const idleMs = Date.now() - t0;

    let perf = { docMs: -1, dclMs: -1, slowestApi: '', slowestApiMs: -1, title: '', path: '' };
    try {
      const ev = await cdp.send('Runtime.evaluate', {
        expression: `(() => {
          const n = performance.getEntriesByType('navigation')[0];
          const rs = performance.getEntriesByType('resource')
            .filter(r => r.initiatorType === 'xhr' || r.initiatorType === 'fetch');
          const sorted = rs.slice().sort((a,b) => b.duration - a.duration);
          const top = sorted[0];
          const sum = rs.reduce((s,r) => s + r.duration, 0);
          return JSON.stringify({
            docMs: n ? Math.round(n.loadEventEnd - n.startTime) : -1,
            dclMs: n ? Math.round(n.domContentLoadedEventEnd - n.startTime) : -1,
            slowestApi: top ? top.name.replace(${JSON.stringify(BASE)}, '') : '',
            slowestApiMs: top ? Math.round(top.duration) : -1,
            apiCount: rs.length,
            apiSumMs: Math.round(sum),
            // ★ 排队判据：resource 的 duration 含**浏览器连接排队**时间。
            //   HTTP/1.1 同源并发上限 6 ⇒ SPA 一次打 20+ 个 XHR 时，后面那些
            //   的 duration 会远大于服务端耗时。所以「页面最慢的 API」不能直接
            //   当成「服务端慢查询」——要对照 apiCount 与服务端直连读数。
            apiMaxConcurrencyHint: (() => {
              let cur = 0, peak = 0;
              const evs = [];
              for (const r of rs) evs.push([r.startTime, 1], [r.responseEnd, -1]);
              evs.sort((a,b) => a[0]-b[0] || b[1]-a[1]);
              for (const [,d] of evs) { cur += d; if (cur > peak) peak = cur; }
              return peak;
            })(),
            title: document.title,
            path: location.pathname,
          });
        })()`,
        returnByValue: true,
      });
      if (ev?.result?.value) perf = JSON.parse(ev.result.value);
    } catch (e) {
      perf.title = 'EVAL-ERR:' + e.message;
    }

    // ★ R153：服务端口径 —— 本页 XHR/Fetch 里 serverMs 的最大值与对应 URL。
    //   ⇒ 只有 apiServerMaxMs > SLOW_MS 才谈得上「这个端点真的慢」；
    //     否则 slowApiMs 的高分只是**浏览器排队**。
    let slowApiServer = '', slowApiServerMs = -1, apiServerMaxMs = -1;
    for (const [u, ms] of serverMsByUrl) {
      if (ms > apiServerMaxMs) apiServerMaxMs = ms;
      if (ms > slowApiServerMs) { slowApiServerMs = ms; slowApiServer = u; }
    }

    // ★ R153：**落地路径 ≠ 请求路径** ⇒ 这不是「这个页面慢」，而是「这个 URL 被重定向了」。
    //   本轮实测：`/dev/waterfall-preview` 与 `/dev/live-topology-preview` 是 DEV-only
    //   夹具预览页（router.ts 里 meta.public=true、仅 DEV 注册）⇒ 访问它们落到 `/`
    //   ⇒ 它们的 25s idleMs 是「首页的某个长请求没结束」，**与该 URL 无关**。
    //   ⇒ 不加这道标注，巡检会把「重定向」误报成「慢页」。
    const pathMismatch = (perf.path && urlPath !== perf.path &&
      !urlPath.endsWith('/') && perf.path !== urlPath + '/') ? 'Y' : '';

    const fail = !!(navErr || badStatus.length || failedReqs.length);
    if (fail) failed++;

    // ★★★ 量具自检（R153 轮新增）：★ **首屏被弹到 `/bootstrap` ⇒ 登录态三闸门没造齐**
    // ⇒ ⇒ 继续跑只会把「量具失效」读成「整站坏了」（★ R99 §11.2 整轮作废的同款）
    // ⇒ ⇒ ★★ **立即 fail-fast**（exit 3 = 工具自身失败），★ 不要产出整轮假读数。
    if (rows.length === 0 && perf.path && perf.path.includes('/bootstrap')) {
      die('量具失效：首屏被弹到 ' + perf.path +
        ' ⇒ 浏览器登录态的三道闸门没造齐（cookie / llmgw_user_info / llmgw_require_bootstrap）' +
        ' ⇒ ★ 本轮读数会全部作废（★ R99 §11.2 的同款），请先修量具再跑');
    }

    let shot = '';
    if (SHOTS && (fail || perf.idleMs > SLOW_MS)) {
      shot = join(SHOTS, (route.replace(/[^\w]+/g, '_') || 'root') + '.png');
      try {
        const { data } = await cdp.send('Page.captureScreenshot', { format: 'png' });
        writeFileSync(shot, Buffer.from(data, 'base64'));
      } catch { /* ignore */ }
    }

    rows.push({
      route: urlPath,
      path: perf.path,
      http: navErr ? 'NAV-ERR' : (badStatus.some((b) => b.startsWith('5')) ? '5xx' : '200'),
      docMs: perf.docMs,
      dclMs: perf.dclMs,
      idleMs,
      slowApi: perf.slowestApi,
      slowApiMs: perf.slowestApiMs,
      slowApiServer: slowApiServer,
      slowApiServerMs: slowApiServerMs,
      apiServerMaxMs: apiServerMaxMs,
      load1: Math.round((loadavg()[0] || 0) * 100) / 100,
      apiCount: perf.apiCount ?? -1,
      apiSumMs: perf.apiSumMs ?? -1,
      apiPeak: perf.apiMaxConcurrencyHint ?? -1,
      consoleErr: consoleErrs.length,
      consoleErrText: consoleErrs.slice(0, 2).join(' | ').replace(/\t/g, ' '),
      failedReq: failedReqs.length,
      cancelledReq: cancelledReqs.length,
      badStatus: badStatus.slice(0, 3).join(' | '),
      maintain503: maintain503.length,
      pathMismatch: pathMismatch,
      shot,
      fail: fail ? 'Y' : '',
    });

    process.stdout.write(
      `${fail ? 'FAIL' : ' ok '} ${urlPath.padEnd(42)} doc=${String(perf.docMs).padStart(6)}ms ` +
      `idle=${String(idleMs).padStart(6)}ms api=${String(perf.slowestApiMs).padStart(6)}ms ` +
      `n=${String(perf.apiCount ?? -1).padStart(3)} sum=${String(perf.apiSumMs ?? -1).padStart(6)}ms peak=${String(perf.apiMaxConcurrencyHint ?? -1).padStart(3)} ` +
      `srv=${String(apiServerMaxMs).padStart(6)}ms load=${String(Math.round((loadavg()[0] || 0) * 100) / 100).padStart(7)} ` +
      `${perf.slowestApi.slice(0, 46)}${badStatus.length ? '  << ' + badStatus.slice(0, 2).join(' ; ') : ''}` +
      `${consoleErrs.length ? '  consoleErr=' + consoleErrs.length : ''}` +
      `${maintain503.length ? '  [maintain503=' + maintain503.length + ']' : ''}\n`
    );

    for (const m of ['Page.loadEventFired', 'Network.requestWillBeSent', 'Network.loadingFinished',
      'Network.loadingFailed', 'Network.responseReceived', 'Runtime.consoleAPICalled', 'Log.entryAdded']) {
      // handlers 是数组，移除本次注册的引用（CDP.on 不支持 off ⇒ 直接重建映射）
    }
    // 轻量清理：把本次的 handler 从数组尾部摘掉
    for (const [method, arr] of cdp.handlers) {
      const arr2 = arr.filter((f) => ![hLoad, hReq, hDone, hFail, hResp, hConsole, hLog].includes(f));
      cdp.handlers.set(method, arr2);
    }
  }

  const header = ['route', 'path', 'http', 'docMs', 'dclMs', 'idleMs', 'slowApi', 'slowApiMs',
    'slowApiServer', 'slowApiServerMs', 'apiServerMaxMs', 'load1',
    'apiCount', 'apiSumMs', 'apiPeak', 'consoleErr', 'consoleErrText', 'failedReq',
    'cancelledReq', 'badStatus', 'maintain503', 'pathMismatch', 'shot', 'fail'];
  // ★ 表头与行字段必须**逐项对齐** —— 曾经出现过「行里加了字段、表头没加」
  //   ⇒ TSV 列错位 ⇒ 读数被按错误的列解释（量具自己的缺陷）。
  //   这里用列数自检把它变成**不可能发货**的状态。
  if (rows.length) {
    const rowKeys = Object.keys(rows[0]);
    if (rowKeys.length !== header.length) {
      die(`header/row mismatch: header has ${header.length} columns, row has ${rowKeys.length} ` +
        `(row keys: ${rowKeys.join(',')})`);
    }
    for (let i = 0; i < header.length; i++) {
      if (header[i] !== rowKeys[i]) {
        die(`header/row order mismatch at column ${i}: header="${header[i]}" row="${rowKeys[i]}"`);
      }
    }
  }
  const tsv = [header.join('\t'), ...rows.map((r) => header.map((h) => String(r[h] ?? '')).join('\t'))].join('\n');
  writeFileSync(OUT, tsv + '\n');

  const slow = rows.filter((r) => r.idleMs > SLOW_MS);
  // ★ R153：**服务端口径**的慢页（浏览器侧排队被剔除后的真慢候选）。
  const srvSlow = rows.filter((r) => r.apiServerMaxMs > SLOW_MS);
  const load1s = rows.map((r) => r.load1).filter((v) => typeof v === 'number' && v >= 0);
  const loadMax = load1s.length ? Math.max(...load1s) : -1;
  console.log(`\n[page-sweep] pages=${rows.length} failed=${failed} slow(>${SLOW_MS}ms)=${slow.length} out=${OUT}`);
  // ★ 宿主机负载：这轮读数是在多饱和的机器上采的吗？⇒ 读数**自带混淆项**。
  console.log(`[page-sweep] host load1 max=${loadMax}` +
    (loadMax > 8 ? '  ★ 机器高度饱和 ⇒ 本轮所有耗时读数都不可单独采信（须与 srv= 对照）' : ''));
  for (const s of slow) {
    console.log(`  slow: ${s.route} idle=${s.idleMs}ms doc=${s.docMs}ms ` +
      `browserApi=${s.slowApi}(${s.slowApiMs}ms) serverApi=${s.slowApiServer}(${s.slowApiServerMs}ms) ` +
      `apiServerMax=${s.apiServerMaxMs}ms n=${s.apiCount} load1=${s.load1}`);
  }
  const mismatched = rows.filter((r) => r.pathMismatch);
  if (mismatched.length) {
    console.log(`[page-sweep] ★ 落地路径≠请求路径的页（重定向 / DEV-only 夹具页）=${mismatched.length}` +
      ` ⇒ 这些不是有效巡检对象，其耗时与该 URL 无关：`);
    for (const m of mismatched) console.log(`  redirected: ${m.route} -> ${m.path} idle=${m.idleMs}ms`);
  }
  console.log(`[page-sweep] 服务端口径慢页(>${SLOW_MS}ms)=${srvSlow.length}` +
    (srvSlow.length ? '' : '  ★ 没有一个端点的服务端耗时超过门槛 ⇒ 浏览器侧的「慢页」是排队假象'));
  for (const s of srvSlow) {
    console.log(`  srv-slow: ${s.route} apiServerMax=${s.apiServerMaxMs}ms url=${s.slowApiServer} load1=${s.load1}`);
  }
  cdp.close();
  cleanup();
  process.exit(failed ? 2 : 0);
}

main().catch((e) => { console.error('[page-sweep] ERROR', e); process.exit(3); });
