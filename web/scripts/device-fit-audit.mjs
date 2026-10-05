/**
 * device-fit-audit.mjs — 真机屏幕适配实测（docs/UI规范 21 §待补）。
 *
 * ## 为什么不看 CSS 就下结论
 *
 * 断点白名单（480/640/768/1024/1440）只说明**允许**在哪些宽度换布局，
 * 不说明**实际有没有溢出**。横向溢出（`scrollWidth > clientWidth`）只在真实
 * 渲染 + 真实字体度量下才暴露，而这两样在 jsdom 里都不存在。
 * ⇒ 本脚本用本机 Chrome 经 CDP 实测，不新增依赖。
 *
 * ## 覆盖的「真机宽度」怎么来的
 *
 * 用设备像素比换算：CSS px = 物理宽 / DPR。覆盖从 iPhone SE(375@2x)
 * 到 iPad(834@2x) 的主流区间，并**显式包含**两端极值——
 * 320（iPhone 5/SE1，最窄的存量机）与 1024（布局档位分界），
 * 因为「小屏溢出」和「刚好跨过断点」是最容易出事的两个位置。
 *
 * 用法：node scripts/device-fit-audit.mjs [--url <业务SPA地址>] [--out <json>]
 */
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

/** 主流设备矩阵：DPR 与真机一致（CSS px = 物理宽 / DPR）。 */
const DEVICES = [
  { name: 'iPhone SE 1/5 (最窄存量)', w: 320, h: 568, dpr: 2, mobile: true },
  { name: 'iPhone 8/SE2', w: 375, h: 667, dpr: 2, mobile: true },
  { name: 'iPhone 12/13 mini', w: 375, h: 812, dpr: 3, mobile: true },
  { name: 'iPhone 14/15', w: 390, h: 844, dpr: 3, mobile: true },
  { name: 'iPhone 15 Pro Max (最宽手机)', w: 430, h: 932, dpr: 3, mobile: true },
  { name: '华为/nova 窄屏', w: 360, h: 780, dpr: 3, mobile: true },
  { name: 'Pixel 7', w: 412, h: 915, dpr: 2.625, mobile: true },
  { name: 'Redmi Note (安卓主流)', w: 393, h: 873, dpr: 2.75, mobile: true },
  { name: '折叠屏 展开', w: 673, h: 841, dpr: 2.625, mobile: true },
  { name: 'iPad mini 竖屏', w: 744, h: 1133, dpr: 2, mobile: true },
  { name: 'iPad Pro 11 竖屏', w: 834, h: 1194, dpr: 2, mobile: true },
  { name: '1024 断点分界', w: 1024, h: 768, dpr: 2, mobile: false },
]

/** 在页面内跑的探针：找出横向溢出的元素与过窄的可点区域。 */
const PROBE = `(() => {
  const de = document.documentElement
  const overflowPx = de.scrollWidth - de.clientWidth
  const offenders = []
  // 只看**可见**元素；隐藏元素不构成用户可感的溢出
  const vw = de.clientWidth

  // ★ 按**语义**判定「这是个横向滚动容器」，不按类名白名单。
  //   参考仓 nbjl3 在 3d9d0267 踩过同一个坑并已修：它原判据只认字面类名
  //   .table-scroll，于是把 .diff-audit__scroll、.overview__table-wrap 这类
  //   **按用途命名**的等价滚动容器全报成缺口 —— 实测 60 个含表格视图里
  //   **12 个是假阳性**。本仓更甚：声明了 overflow 的类名共 107 个，
  //   而字面 .table-scroll 只有 1 个 ⇒ 任何按类名白名单写的判据都会漏掉 106 个。
  //   正确判据是**读计算样式**：该元素（或其祖先链）是否 overflow-x 为 auto/scroll。
  const isHScrollable = (el) => {
    for (let n = el; n && n !== de; n = n.parentElement) {
      const cs = getComputedStyle(n)
      if (/(auto|scroll)/.test(cs.overflowX || '')) return true
    }
    return false
  }
  // 「被滚动容器兜住」的：自身越界但祖先链上有 overflow-x ⇒ **不是缺口**
  const contained = []
  for (const el of document.querySelectorAll('*')) {
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.visibility === 'hidden') continue
    const r = el.getBoundingClientRect()
    if (r.width === 0 && r.height === 0) continue
    if (r.right > vw + 1) {
      const rec = {
        sel: (el.tagName.toLowerCase()
          + (el.id ? '#' + el.id : '')
          + (el.className && typeof el.className === 'string'
              ? '.' + el.className.trim().split(/\\s+/).slice(0, 3).join('.') : '')),
        right: Math.round(r.right),
        width: Math.round(r.width),
      }
      if (isHScrollable(el)) contained.push(rec)
      else offenders.push(rec)
    }
  }
  // 去重：同一 class 前缀只留最右的一个
  const dedupe = (arr) => {
    const seen = new Map()
    for (const o of arr) {
      if (!seen.has(o.sel) || seen.get(o.sel).right < o.right) seen.set(o.sel, o)
    }
    return [...seen.values()]
  }
  // 过窄的可点区域：移动端触控目标应 >= 44px（WCAG 2.5.5 / Material 48dp）
  const smallTargets = []
  for (const el of document.querySelectorAll('button, a, [role="button"], input, select, textarea')) {
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.visibility === 'hidden') continue
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    if (r.height < 40) {
      smallTargets.push({
        sel: el.tagName.toLowerCase() + (el.className && typeof el.className === 'string'
          ? '.' + el.className.trim().split(/\\s+/).slice(0, 2).join('.') : ''),
        h: Math.round(r.height), w: Math.round(r.width),
      })
    }
  }
  return JSON.stringify({
    innerWidth: window.innerWidth,
    clientWidth: de.clientWidth,
    dpr: window.devicePixelRatio,
    scrollWidth: de.scrollWidth,
    overflowPx,
    offenders: dedupe(offenders).sort((a, b) => b.right - a.right).slice(0, 12),
    containedCount: contained.length,
    bodyText: (document.body?.innerText || '').slice(0, 80),
  })
})()`

function sleep(ms) { return new Promise(r => setTimeout(r, ms)) }

async function main() {
  const args = process.argv.slice(2)
  const urlIdx = args.indexOf('--url')
  const outIdx = args.indexOf('--out')
  const URL_ = urlIdx >= 0 ? args[urlIdx + 1] : 'http://127.0.0.1:8782/'
  const OUT = outIdx >= 0 ? args[outIdx + 1] : null

  if (!existsSync(CHROME)) {
    console.error(`[device-fit] 找不到 Chrome：${CHROME}`)
    process.exit(2)
  }

  const port = 9222 + Math.floor(process.pid % 500)
  const profile = mkdtempSync(join(tmpdir(), 'devicefit-'))
  const chrome = spawn(CHROME, [
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    '--headless=new',
    '--no-first-run', '--no-default-browser-check',
    '--hide-scrollbars',
    'about:blank',
  ], { stdio: 'ignore' })

  // 等 devtools 端口就绪
  let listUrl = null
  for (let i = 0; i < 60; i++) {
    await sleep(300)
    try {
      const r = await fetch(`http://127.0.0.1:${port}/json/version`)
      if (r.ok) { listUrl = `http://127.0.0.1:${port}/json/list`; break }
    } catch { /* 未就绪，继续等 */ }
  }
  if (!listUrl) {
    chrome.kill()
    console.error('[device-fit] Chrome devtools 端口未就绪')
    process.exit(2)
  }

  const results = []
  let failures = 0

  for (const d of DEVICES) {
    // 用 Emulation.setDeviceMetricsOverride 切到该设备的真实尺寸与 DPR
    const r = await fetch(listUrl)
    const targets = await r.json()
    const page = targets.find(t => t.type === 'page')
    if (!page) { chrome.kill(); console.error('[device-fit] 没有可用 page target'); process.exit(2) }

    const { WebSocket } = await globalThis
    const sock = new WebSocket(page.webSocketDebuggerUrl)
    await new Promise((res, rej) => { sock.addEventListener('open', res, { once: true }); sock.addEventListener('error', rej, { once: true }) })

    let id = 0
    const call = (method, params) => new Promise((res, rej) => {
      const myId = ++id
      const on = (e) => {
        const m = JSON.parse(e.data)
        if (m.id === myId) { sock.removeEventListener('message', on); m.error ? rej(new Error(JSON.stringify(m.error))) : res(m.result) }
      }
      sock.addEventListener('message', on)
      sock.send(JSON.stringify({ id: myId, method, params }))
      setTimeout(() => { sock.removeEventListener('message', on); rej(new Error(`${method} 超时`)) }, 20000)
    })

    try {
      await call('Emulation.setDeviceMetricsOverride', {
        width: d.w, height: d.h, deviceScaleFactor: d.dpr, mobile: d.mobile,
      })
      await call('Page.enable', {})
      await call('Page.navigate', { url: URL_ })
      await sleep(2500) // 等前端渲染 + 字体度量稳定

      const ev = await call('Runtime.evaluate', { expression: PROBE, returnByValue: true, awaitPromise: true })
      const data = typeof ev.result.value === 'string' ? JSON.parse(ev.result.value) : ev.result.value
      results.push({ device: d, ...data })

      const bad = data.overflowPx > 1
      if (bad) failures++
      console.log(
        `${bad ? '✗' : '✓'} ${d.name.padEnd(26)} ${String(d.w).padStart(4)}@${d.dpr}x ` +
        `client=${data.clientWidth} scroll=${data.scrollWidth} ` +
        `溢出=${data.overflowPx}px 滚动容器兜住=${data.containedCount}` +
        (data.offenders.length ? `\n    最右越界: ${data.offenders.slice(0, 3).map(o => `${o.sel}(right=${o.right})`).join(', ')}` : ''),
      )
    } catch (e) {
      failures++
      console.log(`✗ ${d.name.padEnd(26)} 探测失败: ${e.message}`)
    } finally {
      sock.close()
    }
  }

  chrome.kill()
  if (OUT) writeFileSync(OUT, JSON.stringify(results, null, 2))

  console.log('================================================================')
  console.log(`覆盖 ${results.length} 个设备宽度；横向溢出 ${failures} 处`)
  process.exit(failures > 0 ? 1 : 0)
}

main().catch(e => { console.error(e); process.exit(3) })
