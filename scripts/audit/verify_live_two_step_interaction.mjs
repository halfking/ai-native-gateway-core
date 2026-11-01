#!/usr/bin/env node
// verify_live_two_step_interaction.mjs — /live 两步交互 runtime 验证（CDP，零第三方依赖）
//
// ★ 2026-10-31 浮层塌缩轮固化（LESSONS §593）。验的是**部署站点的用户可见行为**：
//   L1/R1  点左/右栏请求行 ⇒ 摘要浮层（RequestZoomPanel .zoom）出现、本页不跳转、不开新窗；
//          ★★★ 且浮层**几何可读**（高 ≥ GEOM_MIN_PX，头部在滚入视野后可见）——
//          ★★★ 几何断言是本轮的核心教训：1280×800 下浮层曾塌成 16px 一条缝，
//          ★★★ 行为断言全绿而用户实际"看不见摘要"（连报 5 轮的根因）。
//   L1b/R1b（2026-11-01 盲区轮）：先滚列表、点【中部行】⇒ 浮层**不做任何滚动
//          补偿**即在视野内（visibleRatio ≥ 0.5）—— 旧几何断言先 scrollIntoView
//          再量，视口外的浮层也判绿；用户要的是「点完不滚就可见」。
//          配套 App 侧 LiveTopologyView 的浮层 scrollIntoView({block:'nearest'}) 保底。
//   L2/R2  浮层「查看全景」⇒ 新窗 /request-detail/:id（受信点击，见坑 b）；
//   L3/R3  行尾 ↗ ⇒ 新窗 /request-detail/:id（10-18 owner 要求的有意直达入口）；
//   C0     全程 console 无错误。
//   默认视口 1440x900（≥1440 三栏健康基线）+ 1280x800（两栏塌缩受害视口）。
//
// 用法（登录三通道，任选其一）：
//   A) 管理员凭据：--env /path/.env（含 LLM_GATEWAY_ADMIN_USER/PASSWORD）
//   B) 现成会话：  --token-file /path/token（文件内容 = llmgw_session cookie 值）
//   C) 铸签：      --mint-secret-file /path/secret [--mint-user-id 41 --mint-tenant default
//                     --mint-user admin --mint-role super_admin]
//                  ★ 铸签与 admin/jwt.go 的 SignToken **同构**（HS256 /
//                    iss=llm-gateway / aud=llm-gateway-api / user_id·tenant_id·
//                    username·role claims）—— 后端改签发格式必须同步本文件。
//                    铸签后先打 /api/auth/me 验签再进浏览器阶段。
//   选项：--base https://llm.itestu.cn  --viewports 1440x900,1280x800
//         --chrome /path/to/chrome  --port 9333  --shot /tmp/shots（截图留档）
//
// ⚠ 两个实测得来的坑（改脚本前先读，都是 2026-10-31 现场踩过并修掉的）：
//   a) 实时拓扑初始帧很大（245 实测 ≈3.5MB / 200 条，t+29s 才到）——必须轮询
//      等条目出现（默认上限 150s），固定 sleep 只会看到空面板，那是量具问题。
//   b) `window.open` 的弹窗拦截只挡**非受信**点击：synthetic el.click() 会把
//      「开新窗」断言全部打成假红 ⇒ 开新窗类断言必须用 CDP
//      Input.dispatchMouseEvent（受信输入）。新 target 刚创建时 url 为空串 ⇒
//      点击后轮询 /json/list；初始 about:blank 标签会污染「未开新窗」计数 ⇒
//      快照对比口径 + 过滤。
//
// 退出码：0 = 全部视口全过；2 = 有断言失败；3 = 量具自身失败（Chrome/登录/无条目）。

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHmac } from 'node:crypto';

// ---------------------------------------------------------------- 参数
const argv = process.argv.slice(2);
const arg = (k, d) => {
  const i = argv.indexOf('--' + k);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const hasFlag = (k) => argv.includes('--' + k);
const BASE = (arg('base', 'https://llm.itestu.cn')).replace(/\/$/, '');
const ENVFILE = arg('env', '');
const TOKEN_FILE = arg('token-file', '');
const MINT_SECRET_FILE = arg('mint-secret-file', '');
const MINT_USER_ID = Number(arg('mint-user-id', '41'));
const MINT_TENANT = arg('mint-tenant', 'default');
const MINT_USER = arg('mint-user', 'admin');
const MINT_ROLE = arg('mint-role', 'super_admin');
const VIEWPORTS = (arg('viewports', '1440x900,1280x800'))
  .split(',').map((s) => s.trim()).filter(Boolean)
  .map((s) => { const [w, h] = s.split('x').map(Number); return { w, h, label: s }; });
const CHROME = arg('chrome', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const PORT = Number(arg('port', '9333'));
const SHOT_DIR = arg('shot', '');
const ROW_WAIT_MS = Number(arg('row-wait-ms', '150000'));
/** 摘要浮层的最低可读高度（px）。1280 实测修复后 216~296 / 修复前 16~23。 */
const GEOM_MIN_PX = Number(arg('geom-min-px', '200'));

function die(msg, code = 3) {
  console.error(`[two-step] FATAL: ${msg}`);
  process.exit(code);
}

let failures = 0;
function check(name, ok, detail = '') {
  const mark = ok ? '✓' : '✗';
  if (!ok) failures++;
  console.log(`  ${mark} ${name}${detail ? `  — ${detail}` : ''}`);
  return ok;
}

// ---------------------------------------------------------------- 登录三通道
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

/** 与 admin/jwt.go SignToken 同构的 HS256 铸签（坑：改后端签发格式须同步这里）。 */
function mintToken() {
  if (!MINT_SECRET_FILE || !existsSync(MINT_SECRET_FILE)) {
    die('--mint-secret-file 需要一个含 LLM_GATEWAY_SECRET_KEY（或 JWT_SECRET）原文的文件');
  }
  const secret = readFileSync(MINT_SECRET_FILE, 'utf8').trim();
  const b64u = (buf) => Buffer.from(buf).toString('base64url');
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: 'HS256', typ: 'JWT' };
  const claims = {
    user_id: MINT_USER_ID,
    tenant_id: MINT_TENANT,
    username: MINT_USER,
    role: MINT_ROLE,
    iss: 'llm-gateway',
    aud: ['llm-gateway-api'],
    exp: now + 3600,
    iat: now,
  };
  const unsigned = `${b64u(JSON.stringify(header))}.${b64u(JSON.stringify(claims))}`;
  const sig = createHmac('sha256', secret).update(unsigned).digest('base64url');
  return `${unsigned}.${sig}`;
}

async function resolveSession() {
  let cookie = '';
  if (TOKEN_FILE) {
    cookie = readFileSync(TOKEN_FILE, 'utf8').trim();
  } else if (MINT_SECRET_FILE) {
    cookie = mintToken();
  } else if (ENVFILE) {
    const env = loadEnv(ENVFILE);
    const user = env.LLM_GATEWAY_ADMIN_USER;
    const pass = env.LLM_GATEWAY_ADMIN_PASSWORD;
    if (!user || !pass) die('--env 文件里需要 LLM_GATEWAY_ADMIN_USER / LLM_GATEWAY_ADMIN_PASSWORD');
    const res = await fetch(`${BASE}/api/auth/token`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: user, password: pass, remember_me: true }),
    });
    if (!res.ok) die(`login failed: HTTP ${res.status}`);
    const setCookie = res.headers.getSetCookie?.() || [];
    const hit = setCookie.find((c) => c.startsWith('llmgw_session='));
    if (!hit) die('login succeeded but no llmgw_session cookie in Set-Cookie');
    cookie = hit.split(';')[0].split('=').slice(1).join('=');
  } else {
    die('需要一种登录通道：--env FILE / --token-file FILE / --mint-secret-file FILE');
  }

  // 统一验签（铸签路径的假 token 在这里被拦下，不进浏览器阶段）。
  const me = await fetch(`${BASE}/api/auth/me`, { headers: { Cookie: `llmgw_session=${cookie}` } });
  if (!me.ok) die(`auth/me failed: HTTP ${me.status}（token 无效或过期）`);
  const body = await me.json();
  const user = body?.user ?? null;
  if (!user?.role || !user?.id) die('auth/me returned no usable user object');
  console.log(`[two-step] 登录态就绪：${user.username} / ${user.role} / id=${user.id}`);
  return { cookie, user };
}

// ---------------------------------------------------------------- CDP 骨架
class CDP {
  constructor(ws) {
    this.ws = ws; this.id = 0; this.pending = new Map(); this.handlers = new Map();
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(msg.error.message)); else resolve(msg.result);
      } else if (msg.method) {
        for (const fn of this.handlers.get(msg.method) || []) fn(msg.params);
      }
    });
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('ws error')); });
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
        if (this.pending.has(id)) { this.pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }
      }, 210000);
    });
  }
  close() { try { this.ws.close(); } catch { /* ignore */ } }
}

async function waitJSON(url, tries = 100) {
  for (let i = 0; i < tries; i++) {
    try { const r = await fetch(url); if (r.ok) return await r.json(); } catch { /* not ready */ }
    await new Promise((r) => setTimeout(r, 150));
  }
  return null;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------- 单视口跑批
async function runViewport({ w, h, label }, session) {
  console.log(`\n═══ 视口 ${label} ═══`);
  const profile = mkdtempSync(join(tmpdir(), `two-step-${w}-`));
  const child = spawn(CHROME, [
    '--headless=new', `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--disable-extensions',
    '--disable-background-networking', `--window-size=${w},${h}`, 'about:blank',
  ], { stdio: 'ignore' });
  const cleanup = () => {
    try { child.kill('SIGKILL'); } catch { /* ignore */ }
    try { rmSync(profile, { recursive: true, force: true }); } catch { /* ignore */ }
  };
  process.on('exit', cleanup);
  try {
    const ver = await waitJSON(`http://127.0.0.1:${PORT}/json/version`);
    if (!ver) return die('chrome devtools endpoint did not come up');
    let created = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`, { method: 'PUT' });
    if (!created.ok) created = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`);
    if (!created.ok) return die('cannot create devtools target');
    const target = await created.json();
    const cdp = await CDP.connect(target.webSocketDebuggerUrl);
    await cdp.send('Page.enable'); await cdp.send('Runtime.enable'); await cdp.send('Network.enable');

    const consoleErrors = [];
    cdp.on('Runtime.consoleAPICalled', (p) => {
      if (p.type === 'error') consoleErrors.push((p.args?.[0]?.value ?? '').toString().slice(0, 200));
    });
    cdp.on('Log.entryAdded', (p) => {
      if (p.entry?.level === 'error') consoleErrors.push(`${p.entry.source}: ${p.entry.text}`.slice(0, 200));
    });

    await cdp.send('Network.setCookie', { name: 'llmgw_session', value: session.cookie, domain: new URL(BASE).hostname, path: '/', secure: BASE.startsWith('https') });
    // 登录态三道闸门（只注入 cookie ⇒ 全被弹到 /bootstrap，R99 §11.2 实测）。
    await cdp.send('Page.addScriptToEvaluateOnNewDocument', {
      source: `try {
        localStorage.setItem('llmgw_user_info', ${JSON.stringify(JSON.stringify(session.user))});
        localStorage.setItem('llmgw_require_bootstrap', '0');
      } catch (e) {}`,
    });

    const evalJs = async (expression) => {
      const r = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (r.exceptionDetails) throw new Error(`eval: ${r.exceptionDetails.exception?.description ?? r.exceptionDetails.text}`);
      return r.result?.value;
    };

    // 探针流量可见（?probe=1）：窗口内可见流量多为 probe，不靠点开关（与首帧竞态）。
    /** 轮询等左栏条目出现（初始帧很大，t+29s 才到是正常的 —— 固定 sleep 只会
     *  看到空面板，那是量具问题；L3/R3 新载页面后也用它）。 */
    const waitRows = async () => {
      const t = Date.now();
      let n = 0;
      while (Date.now() - t < ROW_WAIT_MS) {
        n = await evalJs(`document.querySelectorAll('.cp__item').length`);
        if (n > 0) break;
        await sleep(2000);
      }
      return n;
    };
    /** 新载 /live（探针流量可见）并等条目就绪 —— 用于脱离深滚动态。 */
    const openFreshLive = async () => {
      await cdp.send('Page.navigate', { url: `${BASE}/live?probe=1` });
      await sleep(2000);
      return waitRows();
    };
    await cdp.send('Page.navigate', { url: `${BASE}/live?probe=1` });
    await sleep(4000);
    const rows = await waitRows();
    check(`L0 [${label}] 左栏出现请求条目`, rows > 0, `rows=${rows}`);
    if (!rows) { cdp.close(); return 1; }
    const rightRows = await evalJs(`document.querySelectorAll('.pp__main').length`);

    const countZoom = () => evalJs(`document.querySelectorAll('.zoom').length`);
    const currentPath = () => evalJs('location.pathname + location.search');
    const pageUrls = async () => {
      const res = await fetch(`http://127.0.0.1:${PORT}/json/list`);
      const arr = await res.json();
      return arr.filter((t) => t.type === 'page').map((t) => t.url).filter((u) => u && u !== 'about:blank' && !u.startsWith('http://127.0.0.1'));
    };
    /** 除当前 /live 外的页面 URL（等最多 3s 让新窗完成导航）。 */
    const openedTargetsAfter = async () => {
      for (let i = 0; i < 12; i++) {
        const others = (await pageUrls()).filter((u) => !u.includes('/live'));
        if (others.length) return others;
        await sleep(250);
      }
      return [];
    };
    const closeDetailTabs = async () => {
      const res = await fetch(`http://127.0.0.1:${PORT}/json/list`);
      const arr = await res.json();
      for (const t of arr) {
        if (t.type === 'page' && t.url.includes('/request-detail/')) {
          await fetch(`http://127.0.0.1:${PORT}/json/close/${t.id}`).catch(() => {});
        }
      }
      await sleep(400);
    };
    /** 受信点击（Input.dispatchMouseEvent ⇒ isTrusted=true ⇒ window.open 不被拦）。
     *  ★ 2026-11-01 L3 盲区轮两条实测纪律：
     *  a) 滚动用 block:'nearest' —— 'center' 会把【页面级滚动器】也卷进
     *     「多滚动容器连环滚动」状态，该状态下合成器侧输入命中与 DOM 侧
     *     elementFromPoint 持续错位（实测点击脱靶、L3 假红，跨帧不复原）；
     *  b) 派发前用 elementFromPoint 复核命中目标子树，不命中则等 300ms
     *     重试（最多 3 次）—— DOM 侧复核虽拦不住合成器侧错位，但能拦住
     *     「实时 prepend 把行推走」这类时序竞态。 */
    const trustedClick = async (selector) => {
      let lastBox = null;
      for (let attempt = 0; attempt < 3; attempt++) {
        const box = await evalJs(`(() => {
          const el = document.querySelector(${JSON.stringify(selector)});
          if (!el) return null;
          el.scrollIntoView({ block: 'nearest' });
          const r = el.getBoundingClientRect();
          const x = r.x + r.width / 2, y = r.y + r.height / 2;
          const hit = document.elementFromPoint(x, y);
          return { x, y, ok: !!hit && (hit === el || el.contains(hit) || hit.contains(el)) };
        })()`);
        if (!box) throw new Error(`no element for ${selector}`);
        lastBox = box;
        if (box.ok) break;
        await sleep(300);
      }
      if (!lastBox.ok) throw new Error(`hit-test never lands on ${selector} (compositor/layout mismatch?)`);
      for (const type of ['mousePressed', 'mouseReleased']) {
        await cdp.send('Input.dispatchMouseEvent', { type, x: lastBox.x, y: lastBox.y, button: 'left', clickCount: 1 });
      }
    };
    /** 浮层几何（★ 不做任何滚动补偿 —— 2026-11-01 盲区轮）。
     *  旧版这里先 head.scrollIntoView() 再量 ⇒ 浮层哪怕渲染在视口外 2000px
     *  也判绿，而用户的体感是「点了什么都没弹出」（连报 6 轮的体感来源）。
     *  用户要的是【点完不滚就可见】，断言就量【点完不滚的几何】。 */
    const zoomGeometry = () => evalJs(`(() => {
      const z = document.querySelector('.zoom');
      if (!z) return null;
      const r = z.getBoundingClientRect();
      const visible = Math.max(0, Math.min(r.bottom, innerHeight) - Math.max(r.top, 0));
      const head = z.querySelector('.zoom__head');
      const hr = head?.getBoundingClientRect();
      return { w: Math.round(r.width), h: Math.round(r.height),
               visibleRatio: r.height > 0 ? +(visible / r.height).toFixed(2) : 0,
               headVisible: !!hr && hr.width > 0 && hr.bottom > 0 && hr.top < innerHeight,
               rows: z.querySelectorAll('.zoom__row').length };
    })()`);
    /** 按「定位表达式」点击（L1b/R1b 用：目标不是唯一 selector 而是第 N 行）。
     *  滚动与复核纪律同 trustedClick（block:'nearest' + 命中复核重试）。 */
    const trustedClickLoc = async (locateExpr, label) => {
      let lastBox = null;
      for (let attempt = 0; attempt < 3; attempt++) {
        const box = await evalJs(`(() => {
          const el = (${locateExpr});
          if (!el) return null;
          el.scrollIntoView({ block: 'nearest' });
          const r = el.getBoundingClientRect();
          const x = r.x + r.width / 2, y = r.y + r.height / 2;
          const hit = document.elementFromPoint(x, y);
          return { x, y, ok: !!hit && (hit === el || el.contains(hit) || hit.contains(el)) };
        })()`);
        if (!box) throw new Error(`no element for ${label}`);
        lastBox = box;
        if (box.ok) break;
        await sleep(300);
      }
      if (!lastBox.ok) throw new Error(`hit-test never lands on ${label}`);
      for (const type of ['mousePressed', 'mouseReleased']) {
        await cdp.send('Input.dispatchMouseEvent', { type, x: lastBox.x, y: lastBox.y, button: 'left', clickCount: 1 });
      }
    };
    const shot = async (name) => {
      if (!SHOT_DIR) return;
      const data = await cdp.send('Page.captureScreenshot', { format: 'png' });
      mkdirSync(SHOT_DIR, { recursive: true });
      writeFileSync(join(SHOT_DIR, `${label}-${name}.png`), Buffer.from(data.data, 'base64'));
    };

    // ════ 左栏 ════
    await trustedClick('.cp__item');
    await sleep(1200);
    check(`L1 [${label}] 点左栏行 → 摘要浮层出现`, (await countZoom()) === 1, `zoom=${await countZoom()}`);
    check(`L1 [${label}] 本页未跳转（仍在 /live）`, (await currentPath()).startsWith('/live'), await currentPath());
    {
      const before = (await pageUrls()).sort(); await sleep(600);
      const after = (await pageUrls()).sort();
      check(`L1 [${label}] 未开新窗`, before.length === after.length && before.every((u, i) => u === after[i]),
        `after=${JSON.stringify(after)}`);
    }
    {
      const g = await zoomGeometry();
      check(`L1 [${label}] 浮层几何可读（h ≥ ${GEOM_MIN_PX} 且不滚动可见）`, !!g && g.h >= GEOM_MIN_PX && g.visibleRatio >= 0.5 && g.rows > 0,
        g ? JSON.stringify(g) : 'no zoom');
      await shot('zoom-left');
    }
    // L1b（2026-11-01 盲区轮）：用户是先滚列表再点行的 —— 先关掉 L1 的浮层，
    // 点【中部行】，断言（a）浮层重新出现且不滚动即在视野内（App 侧
    // scrollIntoView 保底）；（b）浮层内容 = 该行的请求 ID（点击真换绑了目标，
    // 不是旧浮层假绿）。
    {
      await evalJs(`document.querySelector('.zoom .zoom__close')?.click()`);
      await sleep(400);
      const midExpr = `(() => { const items=[...document.querySelectorAll('.cp__item')]; return items[Math.min(24, items.length-1)]; })()`;
      const midId = await evalJs(`(() => { const el=(${midExpr}); const row=el.closest('.cp__row'); const btn=row?.querySelector('[data-testid^="client-open-page-"]'); return btn ? btn.dataset.testid.slice('client-open-page-'.length) : ''; })()`);
      await evalJs(`(${midExpr})?.scrollIntoView({block:'center'})`);
      await sleep(300);
      await trustedClickLoc(midExpr, 'cp item #24');
      await sleep(1200);
      const g = await zoomGeometry();
      const zoomText = await evalJs(`document.querySelector('.zoom')?.textContent ?? ''`);
      check(`L1b [${label}] 滚动后点中部行(#24) → 浮层不滚动即在视野内`, !!g && g.h >= GEOM_MIN_PX && g.visibleRatio >= 0.5 && g.rows > 0,
        g ? JSON.stringify(g) : 'no zoom');
      check(`L1b [${label}] 浮层内容 = 被点行（id=${midId.slice(0, 8)}…）`, !!midId && zoomText.includes(midId),
        `zoomText ${zoomText.includes(midId) ? 'contains' : 'MISSES'} mid id`);
      await shot('zoom-left-mid');
    }
    await trustedClick('.zoom [data-testid="zoom-open-page"]');
    check(`L2 [${label}] 浮层「查看全景」→ 新窗 /request-detail/:id`,
      (await openedTargetsAfter()).some((u) => u.includes('/request-detail/')));
    await closeDetailTabs();
    await evalJs(`document.querySelector('.zoom .zoom__close')?.click()`);
    await sleep(400);

    // L3（2026-11-01 盲区轮改版）：L1b 的深滚动态会触发 headless 命中测试
    // 与布局矩形的持续错位（DOM elementFromPoint 与 gBCR 同帧互斥、滚动复位
    // 不复原，实测稳定复现）⇒ ↗ 直达入口改为**新载页面**后点第 1 行行尾 ↗
    // （fresh 态命中正常，v2 探针实证），验的是同一个产品行为。
    if (!(await openFreshLive())) {
      check(`L3 [${label}] 行尾 ↗ → 新窗 /request-detail/:id（有意直达入口）`, false, 'reload 后无条目');
    } else {
      await trustedClick('[data-testid^="client-open-page-"]');
      check(`L3 [${label}] 行尾 ↗ → 新窗 /request-detail/:id（有意直达入口）`,
        (await openedTargetsAfter()).some((u) => u.includes('/request-detail/')));
    }
    await closeDetailTabs();

    // ════ 右栏 ════
    if (rightRows > 0) {
      await trustedClick('.pp__main');
      await sleep(1200);
      check(`R1 [${label}] 点右栏行 → 摘要浮层出现`, (await countZoom()) === 1, `zoom=${await countZoom()}`);
      check(`R1 [${label}] 本页未跳转（仍在 /live）`, (await currentPath()).startsWith('/live'), await currentPath());
      {
        const before = (await pageUrls()).sort(); await sleep(600);
        const after = (await pageUrls()).sort();
        check(`R1 [${label}] 未开新窗`, before.length === after.length && before.every((u, i) => u === after[i]),
          `after=${JSON.stringify(after)}`);
      }
      {
        const g = await zoomGeometry();
        check(`R1 [${label}] 浮层几何可读（h ≥ ${GEOM_MIN_PX} 且不滚动可见）`, !!g && g.h >= GEOM_MIN_PX && g.visibleRatio >= 0.5 && g.rows > 0,
          g ? JSON.stringify(g) : 'no zoom');
        await shot('zoom-right');
      }
      // R1b：与 L1b 同口径（先关浮层 → 点右栏中部行 → 内容换绑断言）。
      {
        await evalJs(`document.querySelector('.zoom .zoom__close')?.click()`);
        await sleep(400);
        const midExpr = `(() => { const items=[...document.querySelectorAll('.pp__main')]; return items[Math.min(24, items.length-1)]; })()`;
        const midId = await evalJs(`(() => { const el=(${midExpr}); const row=el.closest('.pp__row'); const btn=row?.querySelector('[data-testid^="provider-open-page-"]'); return btn ? btn.dataset.testid.slice('provider-open-page-'.length) : ''; })()`);
        await evalJs(`(${midExpr})?.scrollIntoView({block:'center'})`);
        await sleep(300);
        await trustedClickLoc(midExpr, 'pp main #24');
        await sleep(1200);
        const g = await zoomGeometry();
        const zoomText = await evalJs(`document.querySelector('.zoom')?.textContent ?? ''`);
        check(`R1b [${label}] 滚动后点中部行(#24) → 浮层不滚动即在视野内`, !!g && g.h >= GEOM_MIN_PX && g.visibleRatio >= 0.5 && g.rows > 0,
          g ? JSON.stringify(g) : 'no zoom');
        check(`R1b [${label}] 浮层内容 = 被点行（id=${midId.slice(0, 8)}…）`, !!midId && zoomText.includes(midId),
          `zoomText ${zoomText.includes(midId) ? 'contains' : 'MISSES'} mid id`);
        await shot('zoom-right-mid');
      }
      await trustedClick('.zoom [data-testid="zoom-open-page"]');
      check(`R2 [${label}] 浮层「查看全景」→ 新窗 /request-detail/:id`,
        (await openedTargetsAfter()).some((u) => u.includes('/request-detail/')));
      await closeDetailTabs();
      await evalJs(`document.querySelector('.zoom .zoom__close')?.click()`);
      await sleep(400);

      // R3：与 L3 同口径（新载页面脱离深滚动态后点行尾 ↗）。
      if (!(await openFreshLive())) {
        check(`R3 [${label}] 行尾 ↗ → 新窗 /request-detail/:id（有意直达入口）`, false, 'reload 后无条目');
      } else {
        // 右栏条目独立轮询（waitRows 只等左栏；新载页面右栏到达可能稍晚）。
        let rr = await evalJs(`document.querySelectorAll('.pp__main').length`);
        for (let i = 0; i < 20 && !rr; i++) { await sleep(2000); rr = await evalJs(`document.querySelectorAll('.pp__main').length`); }
        if (!rr) {
          check(`R3 [${label}] 行尾 ↗ → 新窗 /request-detail/:id（有意直达入口）`, false, 'reload 后右栏无条目');
        } else {
          await trustedClick('[data-testid^="provider-open-page-"]');
          check(`R3 [${label}] 行尾 ↗ → 新窗 /request-detail/:id（有意直达入口）`,
            (await openedTargetsAfter()).some((u) => u.includes('/request-detail/')));
        }
      }
      await closeDetailTabs();
    } else {
      check(`R1-R3 [${label}] 右栏无条目（该窗口无供应商响应流量，跳过）`, false, 'skipped');
    }

    check(`C0 [${label}] 全程 console 无错误`, consoleErrors.length === 0,
      consoleErrors.slice(0, 3).join(' | ') || '0 errors');
    cdp.close();
    return 0;
  } finally {
    cleanup();
    process.removeListener('exit', cleanup);
  }
}

// ---------------------------------------------------------------- 主流程
async function main() {
  if (!existsSync(CHROME)) die(`chrome binary not found: ${CHROME}（--chrome 指定）`);
  const session = await resolveSession();
  for (const vp of VIEWPORTS) {
    await runViewport(vp, session);
  }
  console.log(failures ? `\nRESULT: FAIL (${failures})` : '\nRESULT: ALL PASS');
  process.exit(failures ? 2 : 0);
}

main().catch((e) => die(e?.stack || e?.message || String(e)));
