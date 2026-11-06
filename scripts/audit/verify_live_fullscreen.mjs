#!/usr/bin/env node
// verify_fullscreen.mjs — /live 三栏整体全屏 runtime 验证（CDP，零第三方依赖）
//
// 判据（对应 LiveTopologyView.fullscreen.test.ts 的浏览器侧闭环）：
//   FS1 [data-testid=fullscreen-enter] 存在且可见（未登录也常驻）
//   FS2 受信点击（CDP Input）→ document.fullscreenElement === .live-topology
//   FS3 全屏态：根元素带 is-fullscreen；exit 按钮出现；三栏（left/stage/right）
//       均在全屏层内可见（高度 ≥ 200px）——「作为一个整体」的结构断言
//   FS4 全屏层铺满视口（shell rect ≈ window inner 尺寸，误差 ≤ 2px）
//   FS5 ESC 退出 → 状态复位（enter 按钮回归、类摘除）
//   FS6 再入全屏 → 点退出按钮 → 复位
//   C0 全程 console 无错误
// 退出码：0 全过；2 断言红；3 量具失败。

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const argv = process.argv.slice(2);
const arg = (k, d) => { const i = argv.indexOf('--' + k); return i >= 0 && argv[i + 1] ? argv[i + 1] : d; };
const BASE = (arg('base', 'http://localhost:4173')).replace(/\/$/, '');
const CHROME = arg('chrome', '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const PORT = Number(arg('port', '9335'));
const SHOT = arg('shot', '/tmp/live_shots');
const HEADFUL = argv.includes('--headful');

class CDP {
  constructor(ws) {
    this.ws = ws; this.id = 0; this.pending = new Map(); this.handlers = new Map();
    ws.onmessage = (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(msg.error.message)); else resolve(msg.result);
      } else if (msg.method) {
        for (const fn of this.handlers.get(msg.method) || []) fn(msg.params);
      }
    };
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('ws error')); });
    return new CDP(ws);
  }
  on(m, fn) { if (!this.handlers.has(m)) this.handlers.set(m, []); this.handlers.get(m).push(fn); }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => { if (this.pending.has(id)) { this.pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); } }, 20000);
    });
  }
  close() { try { this.ws.close(); } catch { } }
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitJSON(url, tries = 100) {
  for (let i = 0; i < tries; i++) { try { const r = await fetch(url); if (r.ok) return await r.json(); } catch { } await sleep(150); }
  return null;
}

let fails = 0;
const ok = (name, cond, detail = '') => {
  console.log(`  ${cond ? '✓' : '✗'} ${name}${detail ? '  — ' + detail : ''}`);
  if (!cond) fails++;
};
const die = (m) => { console.error(`[fullscreen] FATAL: ${m}`); process.exit(3); };

const profile = mkdtempSync(join(tmpdir(), 'fs-verify-'));
const child = spawn(CHROME, [
  ...(HEADFUL ? [] : ['--headless=new']),
  `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`,
  '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--disable-extensions',
  '--autoplay-policy=no-user-gesture-required',
  '--window-size=1440,900', 'about:blank',
], { stdio: 'ignore' });
const cleanup = () => { try { child.kill('SIGKILL'); } catch { } try { rmSync(profile, { recursive: true, force: true }); } catch { } };
process.on('exit', cleanup);

try {
  mkdirSync(SHOT, { recursive: true });
  const ver = await waitJSON(`http://127.0.0.1:${PORT}/json/version`);
  if (!ver) die('chrome devtools endpoint did not come up');
  let created = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`, { method: 'PUT' });
  if (!created.ok) created = await fetch(`http://127.0.0.1:${PORT}/json/new?url=about:blank`);
  if (!created.ok) die('cannot create devtools target');
  const target = await created.json();
  const cdp = await CDP.connect(target.webSocketDebuggerUrl);
  await cdp.send('Page.enable'); await cdp.send('Runtime.enable');

  const consoleErrors = [];
  cdp.on('Runtime.consoleAPICalled', (p) => { if (p.type === 'error') consoleErrors.push((p.args?.[0]?.value ?? '').toString().slice(0, 200)); });
  cdp.on('Log.entryAdded', (p) => { if (p.entry?.level === 'error') consoleErrors.push(`${p.entry.source}: ${p.entry.text}`.slice(0, 200)); });

  const evalJs = async (expression) => {
    const r = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(`eval: ${r.exceptionDetails.exception?.description ?? r.exceptionDetails.text}`);
    return r.result?.value;
  };

  // 钉视口 1440x900（headless 内窗默认 800x600 ⇒ 触发 <1024 窄屏布局，三栏断言必假红）
  await cdp.send('Emulation.setDeviceMetricsOverride', { width:1440, height:900, deviceScaleFactor:1, mobile:false });
  await cdp.send('Page.navigate', { url: `${BASE}/live` });
  await sleep(2500);

  // FS1: enter 按钮存在且可见
  const fs1 = await evalJs(`(() => {
    const b = document.querySelector('[data-testid="fullscreen-enter"]');
    if (!b) return { exists:false };
    const r = b.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width/2, r.top + r.height/2);
    return { exists:true, visible: r.width > 0 && r.height > 0, clickable: hit === b || b.contains(hit), x: r.left + r.width/2, y: r.top + r.height/2, text: b.textContent.trim() };
  })()`);
  ok('FS1 全屏按钮存在、可见、可点（未登录常驻）', !!(fs1.exists && fs1.visible && fs1.clickable), JSON.stringify({ text: fs1.text, x: Math.round(fs1.x), y: Math.round(fs1.y) }));
  if (!fs1.exists) die('fullscreen-enter 不存在 — 页面未含本轮改动?');

  // 受信点击进入全屏
  for (const type of ['mousePressed', 'mouseReleased']) {
    await cdp.send('Input.dispatchMouseEvent', { type, x: fs1.x, y: fs1.y, button: 'left', clickCount: 1 });
  }
  let entered = false;
  for (let i = 0; i < 20 && !entered; i++) { await sleep(150); entered = await evalJs(`document.fullscreenElement === document.querySelector('.live-topology')`); }
  ok('FS2 受信点击 → .live-topology 进入原生全屏', entered);

  const fs34 = await evalJs(`(() => {
    const el = document.querySelector('.live-topology');
    const rect = el.getBoundingClientRect();
    const panels = ['.live-topology__left', '.live-topology__stage', '.live-topology__right'].map((s) => {
      const p = document.querySelector(s); if (!p) return { s, h: -1 };
      const r = p.getBoundingClientRect();
      return { s, h: Math.round(r.height), w: Math.round(r.width), visible: r.width > 0 && r.height > 200 };
    });
    return {
      isFs: document.fullscreenElement === el,
      cls: el.classList.contains('is-fullscreen'),
      exitBtn: !!document.querySelector('[data-testid="fullscreen-exit"]'),
      exitText: document.querySelector('[data-testid="fullscreen-exit"]')?.textContent.trim() ?? '',
      rect: { w: Math.round(rect.width), h: Math.round(rect.height) },
      vp: { w: window.innerWidth, h: window.innerHeight },
      panels,
    };
  })()`);
  ok('FS3 全屏态类/退出按钮/三栏整体可见', fs34.cls && fs34.exitBtn && fs34.panels.every((p) => p.visible),
    `cls=${fs34.cls} exit="${fs34.exitText}" panels=${JSON.stringify(fs34.panels.map((p) => `${p.s.split('__')[1]}:${p.h}px`))}`);
  const fillW = Math.abs(fs34.rect.w - fs34.vp.w) <= 2, fillH = Math.abs(fs34.rect.h - fs34.vp.h) <= 2;
  ok('FS4 全屏层铺满视口', fillW && fillH, `shell=${fs34.rect.w}x${fs34.rect.h} vp=${fs34.vp.w}x${fs34.vp.h}`);

  // 截图留档（全屏态）
  try {
    const shot = await cdp.send('Page.captureScreenshot', { format: 'png' });
    writeFileSync(join(SHOT, 'fullscreen-1440x900.png'), Buffer.from(shot.data, 'base64'));
  } catch { /* 留档失败不判红 */ }

  // FS5: ESC 退出
  await cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27, nativeVirtualKeyCode: 27 });
  let exited = false;
  for (let i = 0; i < 20 && !exited; i++) { await sleep(150); exited = await evalJs(`!document.fullscreenElement`); }
  const fs5 = await evalJs(`(() => ({
    exited: !document.fullscreenElement,
    clsGone: !document.querySelector('.live-topology')?.classList.contains('is-fullscreen'),
    enterBack: !!document.querySelector('[data-testid="fullscreen-enter"]'),
  }))()`);
  ok('FS5 ESC → 退出全屏并复位', fs5.exited && fs5.clsGone && fs5.enterBack, JSON.stringify(fs5));

  // FS6: 再入 + 点退出按钮
  const btn2 = await evalJs(`(() => { const b = document.querySelector('[data-testid="fullscreen-enter"]'); if (!b) return null; const r = b.getBoundingClientRect(); return { x: r.left + r.width/2, y: r.top + r.height/2 }; })()`);
  if (!btn2) die('enter 按钮消失（FS5 后未复位）');
  for (const type of ['mousePressed', 'mouseReleased']) {
    await cdp.send('Input.dispatchMouseEvent', { type, x: btn2.x, y: btn2.y, button: 'left', clickCount: 1 });
  }
  let reentered = false;
  for (let i = 0; i < 20 && !reentered; i++) { await sleep(150); reentered = await evalJs(`!!document.fullscreenElement`); }
  const btn3 = await evalJs(`(() => {
    const b = document.querySelector('[data-testid="fullscreen-exit"]');
    if (!b) return { missing:true };
    const r = b.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width/2, r.top + r.height/2);
    return { x: r.left + r.width/2, y: r.top + r.height/2, hitOk: hit === b || b.contains(hit), hitTag: hit ? hit.tagName : null, text: b.textContent.trim() };
  })()`);
  let exitByBtn = false;
  let fs6dbg = { btn3 };
  if (reentered && btn3 && !btn3.missing && btn3.hitOk) {
    for (const type of ['mousePressed', 'mouseReleased']) {
      await cdp.send('Input.dispatchMouseEvent', { type, x: btn3.x, y: btn3.y, button: 'left', clickCount: 1 });
    }
    for (let i = 0; i < 20 && !exitByBtn; i++) { await sleep(150); exitByBtn = await evalJs(`!document.fullscreenElement`); }
    if (!exitByBtn) {
      fs6dbg.after = await evalJs(`(() => {
        const b = document.querySelector('[data-testid="fullscreen-exit"]') || document.querySelector('[data-testid="fullscreen-enter"]');
        return { fsEl: !!document.fullscreenElement, btnText: b?.textContent.trim(), btnDisabled: b?.disabled };
      })()`);
    }
  }
  ok('FS6 再入全屏 → 点「退出全屏」按钮 → 复位', reentered && exitByBtn, `reentered=${reentered} exitByBtn=${exitByBtn} dbg=${JSON.stringify(fs6dbg)}`);

  ok('C0 全程 console 无错误', consoleErrors.length === 0, `${consoleErrors.length} errors${consoleErrors[0] ? ' e.g. ' + consoleErrors[0] : ''}`);

  cdp.close();
  console.log(`\nRESULT: ${fails ? 'FAIL (' + fails + ')' : 'PASS'}`);
  process.exit(fails ? 2 : 0);
} catch (e) {
  die(e?.stack ?? String(e));
}
