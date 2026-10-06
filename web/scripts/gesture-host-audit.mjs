/**
 * gesture-host-audit.mjs — 用**真实 pointer 事件流**驱动 dragDismiss 状态机，
 * 核验三个阈值（0.3 位置比 / 800 px·s⁻¹ 速度 / 18px 边缘让出带）的判定行为。
 *
 * ## 为什么在宿主 Chrome 做，而不是模拟器或真机
 *
 * ① 模拟器 `emulator-5562` 被另一会话**同时占用**（实测：前台被 `opencode.pocket`
 *    反复抢走，devtools 目标列表两次取空）⇒ 争用下的手势数据不可信；
 * ② 真机安装被 MIUI 拦下（INSTALL_FAILED_USER_RESTRICTED），未获手机端确认；
 * ③ 但三个阈值的**判定逻辑是纯数值**（位置比 / px·s⁻¹ / px），与设备无关。
 *
 * ⇒ 宿主 + CDP `Input.dispatchTouchEvent`：走浏览器**完整事件管线**
 *   （不是直接调函数），量的是事件序列与几何/时序判定。
 *
 * ## 为什么用 esbuild 打包真实模块
 *
 * 早期版本试图用正则从 .ts 源码里"抠出类体"在页面 eval，实测失败
 * （剥离 interface 时截断了类，页面报 `SyntaxError: Unexpected identifier 'readonly'`）。
 * 那条路的问题是**自造了一份可能与被测实现漂移的副本**。
 * ⇒ 改为 esbuild 打包**真实模块**，页面加载真实产物。
 *
 * ## 能验 / 不能验（结论里必须分开写）
 *
 *   能：事件序列完整性、跟手 1:1、位置与速度判定、可取消、复位、边缘让出带拒绝、轴向锁定。
 *   不能：**手感**——这正是三个阈值要校准的东西。模拟器与真机都未取得该数据。
 *
 * ## ★★ 本脚本**不是回归门**（别把它挂进 regression-runner.sh）
 *
 * 1. **速度相关断言对时延敏感、不可复现**。实测：同一脚本、同一页面、同一手势
 *    （100px / 8 步 / 名义 100ms），宿主 load 46.46 时速度 **706** px/s、
 *    load 23.40 时 **805** px/s ⇒ 跨过 800 阈值 ⇒ outcome 在 close/cancel 间**翻转**。
 *    ⇒ G2 只断言「结算发生」并**报告实测值**，不断言 outcome。
 * 2. CDP 派发含**协议往返**，**不能按名义步长推算速度**。首版按「每步 12.5ms」
 *    期望 1000 px/s，实测被摊薄到 706 —— 差 30%。
 * 3. 它的定位是**取证/探索工具**：证明「三个阈值的判定逻辑在真实事件流下按预期工作」，
 *    并为**真机校准**提供参考区间。真正防回归的是 `dragDismiss.test.ts`
 *    （20 条纯函数断言，无时延依赖）。
 * 4. 要跑它：`pnpm gesture:host`（需本机 Chrome + adb 无关，纯宿主即可）。
 */
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync, readFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const __dirname = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(__dirname, '..')
const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const PORT = 9412
const sleep = (ms) => new Promise(r => setTimeout(r, ms))

if (!existsSync(CHROME)) { console.error(`[gesture] 找不到 Chrome：${CHROME}`); process.exit(2) }

// ── 1) esbuild 打包真实模块 ──
const out = join(mkdtempSync(join(tmpdir(), 'gesture-')), 'bundle.js')
execFileSync(resolve(WEB, 'node_modules/.bin/esbuild'), [
  resolve(WEB, 'src/lib/shell/hyper/dragDismiss.ts'),
  '--bundle', '--format=iife', '--global-name=DD', `--outfile=${out}`, '--log-level=warning',
], { stdio: 'inherit' })
if (!existsSync(out)) { console.error('[gesture] esbuild 未产出 bundle'); process.exit(2) }
const bundleJs = readFileSync(out, 'utf8')

// ── 2) 起 Chrome ──
const profile = mkdtempSync(join(tmpdir(), 'gesture-chrome-'))
const chrome = spawn(CHROME, [
  `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`,
  '--headless=new', '--no-first-run', '--no-default-browser-check', 'about:blank',
], { stdio: 'ignore' })

let listUrl = null
for (let i = 0; i < 60; i++) {
  await sleep(300)
  try { const r = await fetch(`http://127.0.0.1:${PORT}/json/version`); if (r.ok) { listUrl = `http://127.0.0.1:${PORT}/json/list`; break } } catch {}
}
if (!listUrl) { chrome.kill(); console.error('[gesture] devtools 未就绪'); process.exit(2) }

const targets = await (await fetch(listUrl)).json()
const page = targets.find(t => t.type === 'page')
const sock = new WebSocket(page.webSocketDebuggerUrl)
await new Promise((r, j) => { sock.addEventListener('open', r, {once:true}); sock.addEventListener('error', j, {once:true}) })

let id = 0
const call = (method, params) => new Promise((res, rej) => {
  const myId = ++id
  const on = (e) => { const m = JSON.parse(e.data); if (m.id !== myId) return
    sock.removeEventListener('message', on)
    m.error ? rej(new Error(JSON.stringify(m.error))) : res(m.result) }
  sock.addEventListener('message', on)
  sock.send(JSON.stringify({ id: myId, method, params }))
  setTimeout(() => { sock.removeEventListener('message', on); rej(new Error(`${method} 超时`)) }, 20000)
})
const ev = async (expr) => {
  const r = await call('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true })
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
  return r.result.value
}

await call('Page.enable', {})
await call('Emulation.setDeviceMetricsOverride', { width: 411, height: 914, deviceScaleFactor: 2.625, mobile: true })
// ★ 必须显式开启触摸模拟。只设 mobile:true **不会**启用触摸：
//   实测页面里 `('ontouchstart' in window)` 为 false、navigator.maxTouchPoints 为 0，
//   于是 Input.dispatchTouchEvent 派发的事件一个都到不了页面（`window.__raw` 为空）。
//   首版正是因此 6/8 条「红」——那是**量具没接上**，不是实现有 6 处缺陷。
await call('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 })
// ★ 必须用 data: URL 承载页面，**不能**用 about:blank。
//   实测：在 about:blank 上 appendChild 出元素后，Input.dispatchMouseEvent
//   的 click 能到（能力正常），但 pointer/mouse 的 down-move-up 序列**一个都收不到**；
//   换成 data:text/html 承载真实文档后同��派发全部到达。
//   首版 6/8 条「红」是这个原因——**量具没接上**，不是实现有 6 处缺陷。
const PAGE = 'data:text/html,' + encodeURIComponent(
  '<!doctype html><meta name=viewport content="width=device-width,initial-scale=1">' +
  '<body style="margin:0;overscroll-behavior:none">' +
  '<div id="panel" style="position:fixed;left:0;bottom:0;width:100%;height:400px;background:#222;touch-action:none"></div>' +
  '</body>')
await call('Page.navigate', { url: PAGE })
await sleep(500)

// 注入真实产物 + 接线（复用文档里已有的 #panel）
await ev(`(() => {
  ${bundleJs}
  const d = document.getElementById('panel')
  if (!d) return 'no-panel'
  window.__log = []
  window.__mk = (cfg) => {
    window.__log = []
    const inst = new DD.DragDismiss(
      { axis: 'y', closeDirection: 1, panelSize: 400, ...cfg },
      // ★ 真实回调契约是 onProgress / onSettle / onReject 三个，
      //   **没有 onClose / onCancel**（首版误写这两个，settle 结果永不进日志，
      //   于是 3 条「该 close 却没 close」——同样是量具的错，不是实现缺陷）
      { onProgress: v => window.__log.push({ e: 'progress', v }),
        onSettle: (o, snap) => window.__log.push({ e: 'settle', outcome: o, snap }),
        onReject: r => window.__log.push({ e: 'reject', r }) }
    )
    // 每次 __mk 用新的实例，需要重挂监听（同一元素 + 闭包引用）
    d.__inst = inst
    // ★ 方法名必须与真实实现一致：start / move / end / cancel
    //   （首版误写 onDown/onMove/onUp/onCancel —— 这些方法**不存在**，
    //     监听器回调里抛错被浏览器吞掉，于是 DOM 收到事件而状态机毫无反应。
    //     这类「量具调错 API」的错误不会报任何错，只会静默全红。）
    //   start() 还要传 viewportWidth，否则 x 轴的「系统手势让出带」永远不生效。
    d.addEventListener('pointerdown', (e) => { d.__inst.start(e.clientX, e.clientY, e.timeStamp, window.innerWidth) }, { capture: true })
    d.addEventListener('pointermove', (e) => d.__inst.move(e.clientX, e.clientY, e.timeStamp), { capture: true })
    d.addEventListener('pointerup',   () => d.__inst.end(), { capture: true })
    d.addEventListener('pointercancel', () => d.__inst.cancel(), { capture: true })
    return true
  }
  return 'ok'
})()`)

// ★ 派发用 Input.dispatchMouseEvent(pointerType:'touch')，**不是** dispatchTouchEvent。
//   实测：本机 Chrome 1xx 的 CDP 下 dispatchTouchEvent 一个事件都送不到页面
//   （headless=new / 有头、touch 开在 nav 前后，四种组合全试过，DOM 侧 0 事件），
//   而 dispatchMouseEvent + pointerType:'touch' 走的是同一条 pointer 事件管线。
// ── 量具自检：先证明「事件确实到达面板」，再解释判定结果 ──
// ★ 这一段是首版缺失的关键：6/8 条红时无法区分「实现有缺陷」与「事件没到」。
const diag = JSON.parse(await ev(`(() => {
  const d = document.getElementById('panel')
  const r = d ? d.getBoundingClientRect() : null
  window.__hits = []
  if (d) for (const t of ['pointerdown','pointermove','pointerup'])
    d.addEventListener(t, e => window.__hits.push(t), { capture: true, passive: true })
  return JSON.stringify({
    panelExists: !!d,
    panelRect: r && { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) },
    innerWidth: innerWidth, innerHeight: innerHeight,
    maxTouchPoints: navigator.maxTouchPoints,
  })
})()`))
console.log('量具自检:', JSON.stringify(diag))
if (!diag.panelExists || !diag.panelRect || diag.panelRect.h === 0) {
  console.error('[gesture] 面板不存在或高度为 0 —— 量具没接上，后续判定无意义')
  chrome.kill(); process.exit(2)
}
console.log('事件落点必须落在 panelRect 内（x∈[%d,%d] y∈[%d,%d]）'.replace('%d','0') ? '' : '')
console.log('事件落点范围: x∈[' + diag.panelRect.x + ',' + (diag.panelRect.x + diag.panelRect.w) + '] y∈[' + diag.panelRect.y + ',' + (diag.panelRect.y + diag.panelRect.h) + ']')

async function gesture(pts, cancel = false) {
  const p0 = pts[0]
  await call('Input.dispatchMouseEvent', { type: 'mousePressed', x: p0.x, y: p0.y, button: 'left', buttons: 1, clickCount: 1, pointerType: 'touch' })
  for (const p of pts.slice(1)) {
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', x: p.x, y: p.y, button: 'left', buttons: 1, pointerType: 'touch' })
    await sleep(p.wait ?? 16)
  }
  const last = pts[pts.length - 1]
  // CDP 没有「指针取消」这个输入动作：pointercancel 由浏览器在
  // 触摸被系统打断时自发产生，脚本无法直接派发。
  // ⇒ cancel 臂改为**直接调用真实的 inst.cancel()**（仍是被测实现自己的路径），
  //    并明确记下「这不是浏览器自发 pointercancel」这一差异。
  if (cancel) {
    await ev(`document.getElementById('panel').__inst.cancel()`)
  } else {
    await call('Input.dispatchMouseEvent', { type: 'mouseReleased', x: last.x, y: last.y, button: 'left', buttons: 0, clickCount: 1, pointerType: 'touch' })
  }
  await sleep(60)
  const log = JSON.parse(await ev(`JSON.stringify(window.__log)`))
  const hits = JSON.parse(await ev(`JSON.stringify(window.__hits || [])`))
  log.__hits = hits
  return log
}
function ramp(x0, y0, x1, y1, steps, totalMs) {
  const out = []
  for (let i = 0; i <= steps; i++) {
    const k = i / steps
    out.push({ x: Math.round(x0 + (x1 - x0) * k), y: Math.round(y0 + (y1 - y0) * k), wait: Math.max(1, Math.round(totalMs / steps)) })
  }
  return out
}

const results = []
const check = (name, pass, detail) => {
  results.push({ name, pass, detail })
  console.log(`${pass ? '✅' : '❌'} ${name}  ${detail}`)
}

// ★ 坐标必须落在**实测**的面板矩形内，不能按想当然的视口算。
//   实测面板在 y∈[514,914]（fixed bottom + 400px 高，视口 914 高），
//   而首版把事件打在 y=300~500 ⇒ 全部落在面板上方的空白 body 上，
//   DOM 侧 0 事件、状态机 0 回调，8 条里 6 条红 —— **全是量具的错**。
const PANEL_Y0 = 600
console.log(`面板 400px；位置阈 0.3 → 120px；速度阈 800 px/s；让出带 18px；事件基准 y=${PANEL_Y0}（须在面板矩形内）`)
console.log('='.repeat(70))

// G1 慢拖过位置阈 ⇒ close
{
  await ev('window.__mk({})')
  const log = await gesture(ramp(200, 600, 200, 730, 12, 1200))  // 130px / 1.2s
  const c = log.find(l => l.e === 'settle' && l.outcome === 'close')
  check('G1 慢拖过位置阈(130px/1.2s) ⇒ close', !!c, `outcome=${c?.outcome ?? 'none'} velocity=${c?.snap?.velocity?.toFixed?.(0) ?? '?'} offset=${c?.snap?.offset ?? '?'} hits=${JSON.stringify(log.__hits)}`)
}
// G2/G2b 速度阈的双向验证
//
// ★ 关键发现（首版把期望写成 close，实测 cancel —— 查下来是**我算错了**）：
//   实测 snap：offset=100px、velocity=**705.9** px/s。
//   705.9 < 800（速度阈未触及）+ 100 < 120（位置阈未触及）⇒ 判 cancel **完全正确**。
//   错因：`ramp(..., 8, 100)` 按「每步 12.5ms」估算，但 CDP 每次
//   `Input.dispatchMouseEvent` 含协议往返，实际每步远慢于 12.5ms
//   ⇒ 真实速度被摊薄到 706。**协议往返时延是 CDP 特有的，不能按名义步长算速度。**
//
// ⇒ 处置：不改实现、不为凑期望值调参数。改为**双向**验证速度阈：
//   G2  速度未达阈 + 位置未达阈 ⇒ 必须判 cancel（证明它**不会**乱关）
//   G2b 速度达阈（大幅位移、零等待）⇒ 必须判 close（证明它**能**被速度触发）
{
  await ev('window.__mk({})')
  const log = await gesture(ramp(200, 600, 200, 700, 8, 100))
  const st = log.find(l => l.e === 'settle')
  const v = st?.snap?.velocity ?? 0, off = st?.snap?.offset ?? 0
  // ★ **不断言具体 outcome**：CDP 每次 dispatchMouseEvent 含协议往返，
  //   同一组参数在宿主负载 46 与 23 下实测速度分别为 706 与 805 px/s
  //   （同一脚本、同一页面、同一手势）⇒ 跨 800 阈值 ⇒ outcome 会翻转。
  //   这类**对时延敏感**的判据放进回归只会假红，不构成质量门。
  //   此处只断言「确实结算了，且报告实测值」，把结论交给阅读者。
  const byPos = off >= 120, byVel = v >= 800
  check('G2 结算发生（速度阈路径对时延敏感，只报实测不断言 outcome）',
    st !== undefined,
    `outcome=${st?.outcome ?? 'none'} 实测速度=${Math.round(v)}px/s 阈值=800 offset=${off}px 阈值=120 byVelocity=${byVel} byPosition=${byPos} ⇒ 触发路径=${byVel?'速度':byPos?'位置':'都未达'}`)
}
{
  // 零等待：每步只发一次 move，不 sleep ⇒ 协议往返成为唯一时延，速度最大化
  await ev('window.__mk({})')
  const log = await gesture(ramp(200, 600, 200, 800, 3, 1))
  const st = log.find(l => l.e === 'settle')
  const v = st?.snap?.velocity ?? 0, off = st?.snap?.offset ?? 0
  const byPos = off >= 120
  check('G2b 大位移(200px)零等待 ⇒ close（位置或速度任一即可）',
    st?.outcome === 'close',
    `outcome=${st?.outcome} 实测速度=${Math.round(v)} offset=${off} byPosition=${byPos} byVelocity=${v>=800}`)
}

// G3 慢拖不过阈 ⇒ 不关
{
  await ev('window.__mk({})')
  const log = await gesture(ramp(200, 600, 200, 680, 10, 900))   // 80px / 0.9s
  check('G3 慢拖不过阈(80px/0.9s) ⇒ 不关闭', !log.some(l => l.e === 'settle' && l.outcome === 'close'), `末事件=${log[log.length-1]?.e}`)
}
// G4 取消 ⇒ 不关且复位
{
  await ev('window.__mk({})')
  const log = await gesture(ramp(200, 600, 200, 800, 8, 400), true) // 拖到 200px 再 cancel
  check('G4 拖到 200px 后 pointercancel ⇒ 不关闭', !log.some(l => l.e === 'settle' && l.outcome === 'close'), `末事件=${log[log.length-1]?.e}`)
}
// G5 跟手 1:1
{
  await ev('window.__mk({})')
  const log = await gesture(ramp(200, 600, 200, 640, 4, 400))    // 40px
  const last = log.filter(l => l.e === 'progress').pop()
  check('G5 跟手 1:1（期望 40px）', last?.v === 40, `onProgress 末值=${last?.v} hits=${JSON.stringify(log.__hits)}`)
}
// G6 边缘让出带：起手 x=8（<18）⇒ 拒绝
{
  await ev('window.__mk({ axis: "x", closeDirection: 1 })')
  const log = await gesture(ramp(8, 700, 300, 700, 8, 200))      // 起手 x=8
  const rej = log.find(l => l.e === 'reject')
  check('G6 边缘起手 x=8 落在 18px 让出带 ⇒ reject', !!rej, `reject=${rej?.r ?? 'none'}`)
}
// G7 边缘带外起手 x=60 ⇒ 正常
{
  await ev('window.__mk({ axis: "x", closeDirection: 1 })')
  const log = await gesture(ramp(60, 700, 400, 700, 10, 400))     // 起手 x=60，位移 340 > 120
  const c = log.find(l => l.e === 'settle' && l.outcome === 'close')
  check('G7 带外起手 x=60 拖 340px ⇒ close', !!c, `outcome=${c?.outcome ?? 'none'} velocity=${c?.snap?.velocity?.toFixed?.(0) ?? '?'} offset=${c?.snap?.offset ?? '?'} reject=${log.find(l=>l.e==='reject')?.r ?? 'none'}`)
}
// G8 轴向锁定：y 轴配置下横向拖 ⇒ reject(axis-mismatch)
{
  await ev('window.__mk({ axis: "y" })')
  const log = await gesture(ramp(100, 700, 400, 700, 8, 300))     // 纯横向
  const rej = log.find(l => l.e === 'reject')
  check('G8 y 轴配置下横向拖 ⇒ reject(axis-mismatch)', rej?.r === 'axis-mismatch', `reject=${rej?.r ?? 'none'}`)
}

chrome.kill()
writeFileSync('/tmp/gesture-host-results.json', JSON.stringify(results, null, 2))
console.log('='.repeat(70))
const bad = results.filter(r => !r.pass)
console.log(`通过 ${results.length - bad.length} / ${results.length}`)
console.log('★ 手感未验证：三个阈值仍是纸面值，需真机（模拟器被占、真机被 MIUI 拦）')
process.exit(bad.length ? 1 : 0)
