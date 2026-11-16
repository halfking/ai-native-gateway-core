#!/usr/bin/env node
// export-concept-png.mjs — 把本地 HTML 设计稿导出成逐节 PNG，并做一遍自动布局体检
//
// 为什么要有这个工具：
//   设计稿是给人看的，但「看」这个动作没法被自动复查。上一轮就出现过真实缺陷：
//   一个没写尺寸的 `<svg>` 用 SVG 的默认盒子 300×150 把按钮撑爆，页面上压出一个空框；
//   另一处 `<g id>` 雪碧图没有 viewBox，24 单位坐标的图标在 15px 盒子里被裁掉。
//   两者都不会触发任何构建或测试报错 ⇒ 只能靠**渲染出来看**发现。
//   ⇒ 这个工具把「导出成图」+「逐节体检」固化成一条可重复的命令。
//
// 依赖：只用 Node 内置能力（node >= 22 提供全局 fetch + 全局 WebSocket），
//   与 scripts/audit/page-sweep-cdp.mjs 同款做法 ⇒ 不需要 puppeteer/playwright。
//
// 用法：
//   node scripts/audit/export-concept-png.mjs \
//     --html docs/03-design/02-feature-design/simple-mode-concept.html \
//     --out  docs/03-design/02-feature-design/assets/simple-mode-concept \
//     --themes dark,light \
//     --scale 2
//
// 体检口径（逐项写清，避免把别的开销算进来）：
//   clipped     元素 scrollWidth > clientWidth 且自身 overflow 不可滚动 ⇒ 文字被切
//   tinyIcon    <svg> 渲染尺寸 < 6px ⇒ 图标小到看不见
//   hugeIcon    <svg> 渲染尺寸 > 64px ⇒ 图标被撑爆（默认盒子 300×150 是典型值）
//   danglingUse <use href="#x"> 的 x 在文档里不存在 ⇒ 图标画不出来
//   hOverflow   元素右边界超出文档宽度 ⇒ 横向滚动条
//   microText   计算字号 < 10px ⇒ 文字读不出来
//
// 退出码：0 = 全部体检项干净；2 = 有问题（见报告）；3 = 工具自身失败

import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync, readFileSync, existsSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const argv = process.argv.slice(2);
const arg = (k, d) => {
  const i = argv.indexOf('--' + k);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const HTML = arg('html', 'docs/03-design/02-feature-design/simple-mode-concept.html');
const OUT = arg('out', 'docs/03-design/02-feature-design/assets/simple-mode-concept');
const THEMES = arg('themes', 'dark,light').split(',').map((s) => s.trim()).filter(Boolean);
const SCALE = Number(arg('scale', '2'));
const WIDTH = Number(arg('width', '1440'));
const CHROME = arg('chrome', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const PORT = Number(arg('port', '9344'));
const TIMEOUT_MS = Number(arg('timeout-ms', '30000'));

function die(msg, code = 3) {
  console.error(`[export-concept] FATAL: ${msg}`);
  process.exit(code);
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
        const { resolve: rs, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        msg.error ? reject(new Error(msg.error.message)) : rs(msg.result);
        return;
      }
      const hs = this.handlers.get(msg.method);
      if (hs) for (const h of hs) h(msg.params);
    });
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((rs, reject) => {
      ws.addEventListener('open', rs, { once: true });
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
    return new Promise((rs, reject) => {
      this.pending.set(id, { resolve: rs, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error(`CDP timeout: ${method}`));
        }
      }, TIMEOUT_MS);
    });
  }
  close() { try { this.ws.close(); } catch { /* ignore */ } }
}

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
  const profile = mkdtempSync(join(tmpdir(), 'kx-export-concept-'));
  const child = spawn(CHROME, [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu',
    '--disable-extensions', '--disable-background-networking',
    '--hide-scrollbars',
    `--window-size=${WIDTH},900`,
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
  let res = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`, { method: 'PUT' });
  if (!res.ok) res = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`);
  if (!res.ok) die(`cannot create devtools target: HTTP ${res.status}`);
  return await res.json();
}

// ---------------------------------------------------------------- 页面内取值
const evalIn = async (cdp, expression) => {
  const ev = await cdp.send('Runtime.evaluate', {
    expression, returnByValue: true, awaitPromise: true,
  });
  if (ev?.exceptionDetails) throw new Error(ev.exceptionDetails.text || 'eval failed');
  return ev?.result?.value;
};

// 章节清单：id → 文件名与中文名
// 默认是 v1 概念稿的章节；换稿子用 --sections / --sections-file 传入。
const DEFAULT_SECTIONS = [
  { id: 'doc-head', file: '00-cover', label: '封面与导读' },
  { id: 's1', file: '01-full-page', label: '整页视图' },
  { id: 's2', file: '02-menu-overview', label: '菜单全貌' },
  { id: 's3', file: '03-entry-mapping', label: '条目归属' },
  { id: 's4', file: '04-expert-menus', label: '专家面菜单' },
  { id: 's5', file: '05-menu-rules', label: '菜单规则' },
  { id: 'foot', file: '06-footer', label: '页脚' },
];

// --sections      : 直接给一段 JSON 数组
// --sections-file : 给一个 JSON 文件路径
function loadSections() {
  const inline = arg('sections', null);
  const file = arg('sections-file', null);
  let raw = null;
  if (inline) raw = inline;
  else if (file) {
    if (!existsSync(file)) die('章节清单文件不存在：' + file);
    raw = readFileSync(file, 'utf8');
  }
  if (!raw) return DEFAULT_SECTIONS;
  let list;
  try { list = JSON.parse(raw); } catch (e) { die('章节清单不是合法 JSON：' + e.message); }
  if (!Array.isArray(list) || list.length === 0) die('章节清单必须是非空数组');
  for (const s of list) {
    if (!s || !s.id || !s.file) die('章节清单每一项都要有 id 与 file：' + JSON.stringify(s));
  }
  return list;
}
const SECTIONS = loadSections();

// 体检脚本（在页面里跑，返回 JSON 字符串）
const AUDIT_JS = `(() => {
  const out = { clipped: [], tinyIcon: [], hugeIcon: [], danglingUse: [], hOverflow: [], microText: [] };
  const docW = document.documentElement.scrollWidth;
  const label = (el) => {
    const t = (el.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 60);
    return el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') +
      (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\\s+/).join('.') : '') +
      (t ? ' :: ' + t : '');
  };
  const all = document.querySelectorAll('body *');
  for (const el of all) {
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) continue;
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') continue;

    // ① 文字被切：自身不可滚动却装不下内容
    const canScroll = /auto|scroll/.test(cs.overflowX);
    if (!canScroll && el.scrollWidth > el.clientWidth + 1 && el.clientWidth > 0) {
      const hasText = [...el.childNodes].some(n => n.nodeType === 3 && n.textContent.trim());
      if (hasText) out.clipped.push({ el: label(el), scrollW: el.scrollWidth, clientW: el.clientWidth });
    }
    // ②③ 图标尺寸异常
    if (el.tagName.toLowerCase() === 'svg') {
      const w = r.width, h = r.height;
      if (w < 6 || h < 6) out.tinyIcon.push({ el: label(el), w: Math.round(w), h: Math.round(h) });
      if (w > 64 || h > 64) out.hugeIcon.push({ el: label(el), w: Math.round(w), h: Math.round(h) });
    }
    // ④ <use> 指向不存在的定义
    if (el.tagName.toLowerCase() === 'use') {
      const href = el.getAttribute('href') || el.getAttribute('xlink:href') || '';
      if (href.startsWith('#') && !document.getElementById(href.slice(1))) {
        out.danglingUse.push({ el: label(el), href });
      }
    }
    // ⑤ 横向溢出
    if (r.right > docW + 1 && cs.position !== 'fixed') {
      out.hOverflow.push({ el: label(el), right: Math.round(r.right), docW });
    }
    // ⑥ 字号过小
    const fs = parseFloat(cs.fontSize);
    if (fs && fs < 10 && el.childNodes.length) {
      const hasText = [...el.childNodes].some(n => n.nodeType === 3 && n.textContent.trim());
      if (hasText) out.microText.push({ el: label(el), fs });
    }
  }
  for (const k of Object.keys(out)) out[k] = out[k].slice(0, 40);
  return JSON.stringify(out);
})()`;

async function main() {
  const htmlPath = resolve(HTML);
  if (!existsSync(htmlPath)) die(`html not found: ${htmlPath}`);
  mkdirSync(resolve(OUT), { recursive: true });

  // ── 台账门：页面里带 data-ledger-row 标记 ⇒ 它的行数必须与 appNav.ts 真源一致。
  //   为什么放在导出流程里：台账手抄过一次就漂过一次（v1 稿写 61 条，真源已到 63 条）。
  //   渲染出来的那张表看着「对」，只有拿真源数行数才知道它少了两条 ⇒ 门必须自动跑。
  //   工具见 scripts/audit/gen-simple-mode-ledger.mjs（同一套真源口径）。
  const htmlSrc = readFileSync(htmlPath, 'utf8');
  if (htmlSrc.includes('data-ledger-row')) {
    const ledgerTool = resolve(dirname(fileURLToPath(import.meta.url)), 'gen-simple-mode-ledger.mjs');
    if (!existsSync(ledgerTool)) die('台账校验工具不存在：' + ledgerTool);
    const r = spawnSync(process.execPath, [ledgerTool, '--check', htmlPath], { encoding: 'utf8' });
    const out = ((r.stdout || '') + (r.stderr || '')).trim();
    if (out) console.log('[export-concept] 台账门：' + out.split('\n').pop());
    if (r.status !== 0) die('台账与真源对不齐，先修台账再导出（退出码 ' + r.status + '）', 2);

    // 台账第一列必须是**中文名**，不能是 i18n key。
    //   生成器只解析 zh-CN/nav.ts，个别条目用别的词条文件（如 sanitizeRules.title
    //   在 zh-CN/sanitizeRules.ts）⇒ 会原样落一个 key 进表里。行数对得上、内容却是
    //   key，渲染出来一眼就露馅 ⇒ 这条也要卡。
    //   注意别误伤：官方中文名里就有英文词（Agent Registry、VibeCoding）⇒
    //   只拦「key 形状」（小驼峰开头 + 点分），不拦「不含中文」。
    const KEY_LIKE = /^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)+$/;
    const names = [...htmlSrc.matchAll(/<tr data-ledger-row><td>([^<]*)<\/td>/g)].map((m) => m[1]);
    const rawKeys = names.filter((n) => KEY_LIKE.test(n) || n.startsWith('nav.'));
    if (rawKeys.length) {
      die('台账里有未解析成中文名的条目（行数对但名字是 key）：' + rawKeys.join('、'));
    }
    console.log(`[export-concept] 台账门：${names.length} 行，条目名全部解析成显示名 ✓`);
  }

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
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: WIDTH, height: 900, deviceScaleFactor: 1, mobile: false,
  });

  const loaded = new Promise((rs) => cdp.on('Page.loadEventFired', rs));
  await cdp.send('Page.navigate', { url: 'file://' + htmlPath });
  await Promise.race([loaded, new Promise((_, rej) => setTimeout(() => rej(new Error('nav-timeout')), TIMEOUT_MS))]);
  // 等字体与布局稳定
  await new Promise((r) => setTimeout(r, 900));

  const report = { html: HTML, width: WIDTH, scale: SCALE, themes: {}, generatedAt: new Date().toISOString() };

  for (const theme of THEMES) {
    await evalIn(cdp, `(() => {
      document.documentElement.setAttribute('data-theme', '${theme}');
      const ic = document.getElementById('theme-icon');
      if (ic) ic.setAttribute('href', '${theme}' === 'dark' ? '#i-sun' : '#i-moon');
      return 1;
    })()`);
    await new Promise((r) => setTimeout(r, 350));

    const themeDir = join(resolve(OUT), theme);
    mkdirSync(themeDir, { recursive: true });

    const files = [];

    // 整页长图：一眼看完整个文档（逐节图用于细看）
    const whole = await evalIn(cdp, `(() => {
      const de = document.documentElement;
      return JSON.stringify({ w: Math.round(de.scrollWidth), h: Math.round(de.scrollHeight) });
    })()`);
    const wb = JSON.parse(whole);
    if (wb.w > 4 && wb.h > 4) {
      const shot = await cdp.send('Page.captureScreenshot', {
        format: 'png',
        captureBeyondViewport: true,
        clip: { x: 0, y: 0, width: wb.w, height: wb.h, scale: 1 },
      });
      const file = join(themeDir, 'whole-document.png');
      writeFileSync(file, Buffer.from(shot.data, 'base64'));
      files.push({ file: `${theme}/whole-document.png`, label: '整页长图', w: wb.w, h: wb.h });
      console.log(`[export-concept] ${theme}/whole-document.png  ${wb.w}x${wb.h} @1x  整页长图`);
    }

    for (const sec of SECTIONS) {
      // ★★ `captureBeyondViewport` 会让 Chrome 重绘整页，**绝对定位的浮层会按文档
      //   坐标漂移** ⇒ 上一节的浮层残影会落进下一节的裁切区（导出伪影，不是页面缺陷）。
      //   ⇒ 截图前把**不在本节内**的浮层设为 visibility:hidden（不动布局，只是不画）。
      await evalIn(cdp, `(() => {
        document.querySelectorAll('.dropdown').forEach((d) => {
          const owner = d.closest('section[id]');
          d.style.visibility = (owner && owner.id === '${sec.id}') ? '' : 'hidden';
        });
        return 1;
      })()`);

      const box = await evalIn(cdp, `(() => {
        const el = document.getElementById('${sec.id}');
        if (!el) return null;
        const r = el.getBoundingClientRect();
        const sy = window.scrollY, sx = window.scrollX;
        return JSON.stringify({
          x: Math.max(0, Math.round(r.left + sx)),
          y: Math.max(0, Math.round(r.top + sy)),
          width: Math.round(r.width),
          height: Math.round(r.height),
        });
      })()`);
      if (!box) { console.warn(`[export-concept] 跳过缺失锚点 ${sec.id}`); continue; }
      const b = JSON.parse(box);
      if (b.width < 4 || b.height < 4) continue;

      // 留白：**只向下**留。
      //   向上留会把上一节的尾巴截进本节（看着像残影）；向下留只是给最后一行
      //   文字留出与下一节的间距，两种都不改页面本身。
      const PAD = Number(arg('pad', '24'));
      const docH = await evalIn(cdp, 'document.documentElement.scrollHeight');
      const clip = {
        x: b.x,
        y: b.y,
        width: b.width,
        height: Math.min(docH, b.y + b.height + PAD) - b.y,
        scale: SCALE,
      };

      const shot = await cdp.send('Page.captureScreenshot', {
        format: 'png',
        captureBeyondViewport: true,
        clip,
      });
      const file = join(themeDir, sec.file + '.png');
      writeFileSync(file, Buffer.from(shot.data, 'base64'));
      files.push({ file: `${theme}/${sec.file}.png`, label: sec.label, w: b.width, h: b.height });
      console.log(`[export-concept] ${theme}/${sec.file}.png  ${b.width}x${b.height} @${SCALE}x  ${sec.label}`);
    }

    const audit = JSON.parse(await evalIn(cdp, AUDIT_JS));
    report.themes[theme] = { files, audit };
  }

  const reportFile = join(resolve(OUT), 'layout-audit.json');
  writeFileSync(reportFile, JSON.stringify(report, null, 2));

  let issues = 0;
  for (const [theme, t] of Object.entries(report.themes)) {
    const a = t.audit;
    const counts = Object.fromEntries(Object.entries(a).map(([k, v]) => [k, v.length]));
    const n = Object.values(counts).reduce((x, y) => x + y, 0);
    issues += n;
    console.log(`\n[export-concept] 体检 ${theme}: ${JSON.stringify(counts)}`);
    for (const [k, v] of Object.entries(a)) {
      for (const item of v.slice(0, 12)) console.log(`   ${k}: ${JSON.stringify(item)}`);
    }
  }
  console.log(`\n[export-concept] 输出目录 ${OUT}`);
  console.log(`[export-concept] 报告 ${reportFile}`);
  cdp.close();
  process.exit(issues > 0 ? 2 : 0);
}

main().catch((e) => {
  console.error(`[export-concept] FATAL: ${e.stack || e.message}`);
  process.exit(3);
});
