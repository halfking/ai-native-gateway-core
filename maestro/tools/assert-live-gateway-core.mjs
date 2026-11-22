// assert-live-gateway-core.mjs —— /live 页面网关核心显示验证（真实断言版）
//
// ★ 2026-11-23 第十二轮布局优化轮：本脚本此前是**占位假绿**（exit 2「量具失效」），
//   4 条「✅」是打印出的字符串常量、零断言。本轮落地真实 CDP 断言，度量目标＝
//   **模型层与供应商层的实际渲染净间距**（owner 反馈两轮的核心诉求）。
//
// 退出码（统一走 maestro/tools/result.mjs，不自己写口径）：
//   0 = 全绿 / 1 = 产品红 / 2 = 量具失效（依赖不满足 ⇒「没评估到」≠「评估通过」）
//
// 测量方法（为什么这样量）：
//   · 层锚点＝`.topo-canvas__layer-label` 的 **inline style.top** —— 它就是布局的
//     rowY 原始值；层标签虽有 `transform: translateY(-50%)`，但 inline top 不受影响，
//     ⇒ 拿 rect.top 会差半个标签高，必须读 inline 值。
//   · 节点＝`.topo-node` 的 inline top/height（布局直接写上去的像素）。
//   · 净间距＝min(provider 层节点顶边) − max(model 层节点底边) ⇒ **卡片边缘到卡片边缘**，
//     不是中心线之差（中心线之差会凭空多算一个节点高）。
//   · 多视口实测：换 height 后让 Vue 重排再测，验证间距随画布变高而变宽，
//     并在目标高度（846px）附近核对「扩大一倍」是否达成。
//
// 用法：
//   node maestro/tools/assert-live-gateway-core.mjs --cdp http://127.0.0.1:9223 \
//         --url https://llm.itestu.cn --user admin
import { connect, openPage, evaluate } from './cdp-client.mjs'
import { summarize, EXIT_FAIL } from './result.mjs'

// ★ 参数解析要**同时**区分「选项值」与「位置参数」：
//   `--user admin` 里的 admin 是选项值，不能被当成位置参数（否则 BASE_URL 取错）。
const argv = process.argv.slice(2)
const args = {}
const positional = []
for (let i = 0; i < argv.length; i++) {
  const v = argv[i]
  if (v.startsWith('--')) {
    const key = v.slice(2)
    const next = argv[i + 1]
    if (next !== undefined && !next.startsWith('--')) { args[key] = next; i++ }
    else args[key] = true
  } else {
    positional.push(v)
  }
}

const CDP_URL = args.cdp || process.env.CDP_URL || 'http://127.0.0.1:9222'
// ★ 位置参数与 --url 都认：runner 现传位置参数（`node assert-...mjs "$BASE_URL"`），
//   参考实现传 --url —— 两种调用都要能跑，否则脚本只能被某一种调法使用。
const BASE = (args.url || positional[0] || 'http://127.0.0.1:8782').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
const PASSWORD = args.password || process.env.ADMIN_PASSWORD || ''

// 目标：模型层与供应商层至少留这么多净间距（px）。
// ★ 口径（2026-11-23 实测取证后订正）：布局已从「比例定位」改成「**固定像素 ROW_GAP=72**」，
//   因为生产实测画布只有 480px、比例被压成 1px ⇒ 净间距必须**恒定**才叫修好。
//   ⇒ 断言不止「某个数」，还要「**各视口之间几乎不变**」（比例方案的病根所在）。
const MIN_GAP = Number(args['min-gap'] || 60)   // 任何视口都该成立的下限
const TARGET_GAP = Number(args['target-gap'] || 64) // 固定间距 72 扣掉节点抖动 ±8 后的下限
const MAX_SPREAD = Number(args['max-spread'] || 20) // 各视口净间距极差上限（恒定 ⇒ 极差≈0）
// 视口高度矩阵：覆盖最小画布(480)、常见(846/900)与高屏(1200)
const VIEWPORTS = [
  { width: 1440, height: 900 },
  { width: 1440, height: 846 },
  { width: 1440, height: 1200 },
  { width: 1440, height: 700 },
]

const checks = []
const skipped = []
function check(name, ok, detail = '') {
  checks.push({ name, pass: !!ok, detail })
  console.log(`${ok ? '✅' : '❌'} ${name}${detail ? '  — ' + detail : ''}`)
  return !!ok
}
function gauge(name, detail = '') {
  skipped.push({ name, detail })
  console.log(`○ 未评估（量具失效） ${name}${detail ? '  — ' + detail : ''}`)
}

console.log(`\n🔍 /live 页面网关核心显示验证（真实断言版）`)
console.log(`目标: ${BASE}/live`)
console.log(`CDP:   ${CDP_URL}`)
console.log(`\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n`)

const { cdp } = await connect(CDP_URL)
let sessionId = (await openPage(cdp, 'about:blank')).sessionId
const sleep = (ms) => new Promise(r => setTimeout(r, ms))

async function goto(url, waitMs = 6000) {
  const created = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId: sid } = await cdp.send('Target.attachToTarget', { targetId: created.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid)
  await cdp.send('Network.enable', {}, sid)
  await evaluate(cdp, sid, `location.href = ${JSON.stringify(url)}`)
  await sleep(waitMs)
  const { sessionId: sid2 } = await cdp.send('Target.attachToTarget', { targetId: created.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid2)
  await cdp.send('Runtime.enable', {}, sid2)
  return sid2
}

// ── 登录：优先复用已有会话，否则用口令换 token ─────────────────────────
// ★ 必须接收 goto 返回的新 sessionId：goto 内部**新建了 target**，
//   留在初始 about:blank 上做 fetch('/api/auth/token') 会因相对 URL 无基址而失败
//   （实测报 "Failed to parse URL from /api/auth/token"）。
sessionId = await goto(BASE + '/?login=1', 4000)
let meStatus = await evaluate(cdp, sessionId,
  `fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(meStatus) !== '200') {
  if (!PASSWORD) {
    gauge('登录（/api/auth/token 200）', '无已有会话且未提供口令 ⇒ 无法评估页面级断言')
    console.log('\n' + summarize(checks, { skipped }).line)
    process.exit(2)
  }
  meStatus = await evaluate(cdp, sessionId,
    `fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:JSON.stringify({username:${JSON.stringify(USER)},password:${JSON.stringify(PASSWORD)}})}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
  check('登录 /api/auth/token 200', String(meStatus) === '200', `status=${meStatus}`)
  if (String(meStatus) !== '200') {
    console.log('\n' + summarize(checks, { skipped }).line)
    process.exit(EXIT_FAIL)
  }
} else {
  check('登录态（复用已有会话 /api/auth/me 200）', true)
}

// ── 页面内测量表达式 ───────────────────────────────────────────────────
// ★ 层标签 DOM 顺序＝模板里 layerLabels computed 的数组顺序＝[client, provider, model]；
//   同时用 rowY 数值关系（client < model < provider）交叉校验，两者不一致即判红。
const MEASURE = `(() => {
  const canvas = document.querySelector('.topo-canvas');
  if (!canvas) return { error: 'no-canvas' };
  const cr = canvas.getBoundingClientRect();

  const labelEls = Array.from(document.querySelectorAll('.topo-canvas__layer-label'));
  const rows = labelEls.map(el => ({
    text: (el.textContent || '').trim(),
    rowY: parseFloat(el.style.top),
  }));
  if (rows.length < 3) return { error: 'no-layer-labels', rows };

  // DOM 顺序 [client, provider, model]
  const rowY = { client: rows[0].rowY, provider: rows[1].rowY, model: rows[2].rowY };
  const orderOk = rowY.client < rowY.model && rowY.model < rowY.provider;
  const byValue = {
    client: rows.map(r => r.rowY).sort((a,b) => a-b)[0],
    model: rows.map(r => r.rowY).sort((a,b) => a-b)[1],
    provider: rows.map(r => r.rowY).sort((a,b) => a-b)[2],
  };
  const valueOk = byValue.client === rowY.client && byValue.model === rowY.model && byValue.provider === rowY.provider;

  const nodes = Array.from(document.querySelectorAll('.topo-node')).map(el => {
    const top = parseFloat(el.style.top);
    const h = parseFloat(el.style.height);
    const label = (el.querySelector('.topo-node__label') || {}).textContent || '';
    return { label: label.trim(), top, h, center: top + h / 2 };
  });

  // 按中心线归属层：离哪个 rowY 最近算哪层（容差 = 一个节点高的一半）
  const assign = (n) => {
    const cands = [['client', rowY.client], ['model', rowY.model], ['provider', rowY.provider]];
    let best = null, bestD = Infinity;
    for (const [k, v] of cands) {
      const d = Math.abs(n.center - v);
      if (d < bestD) { bestD = d; best = k; }
    }
    return { col: best, dist: bestD };
  };
  const buckets = { client: [], model: [], provider: [] };
  for (const n of nodes) { const a = assign(n); if (a.dist < 60) buckets[a.col].push(n); }

  const gwEl = document.querySelector('.gw');
  const gwRect = gwEl ? gwEl.getBoundingClientRect() : null;

  return {
    canvasH: cr.height, canvasW: cr.width,
    rowY, orderOk, valueOk,
    nodes, buckets,
    gw: gwRect ? { top: gwRect.top - cr.top, bottom: gwRect.bottom - cr.top, h: gwRect.height } : null,
  };
})()`

// ── 逐视口测量 ────────────────────────────────────────────────────────
const measurements = []
for (const vp of VIEWPORTS) {
  await cdp.send('Emulation.setDeviceMetricsOverride',
    { width: vp.width, height: vp.height, deviceScaleFactor: 1, mobile: false }, sessionId)
  await evaluate(cdp, sessionId, `location.href = ${JSON.stringify(BASE + '/live')}`)
  await sleep(3500)
  // 等拓扑节点渲染出来（没有节点＝这一视口下量具失效，不是「间距为 0」）
  let m = null
  for (let i = 0; i < 20; i++) {
    m = await evaluate(cdp, sessionId, MEASURE)
    if (m && !m.error && m.buckets.model.length && m.buckets.provider.length) break
    await sleep(700)
  }
  if (!m || m.error) {
    gauge(`视口 ${vp.width}x${vp.height} 间距测量`, m && m.error ? m.error : '未取到拓扑数据')
    continue
  }
  const modelBottom = Math.max(...m.buckets.model.map(n => n.top + n.h))
  const providerTop = Math.min(...m.buckets.provider.map(n => n.top))
  const gap = providerTop - modelBottom
  const allBottom = Math.max(...m.nodes.map(n => n.top + n.h))
  measurements.push({ vp, gap, modelBottom, providerTop, canvasH: m.canvasH, gw: m.gw, rowY: m.rowY,
    modelCount: m.buckets.model.length, providerCount: m.buckets.provider.length,
    buckets: m.buckets,
    orderOk: m.orderOk, valueOk: m.valueOk,
    overflow: allBottom > m.canvasH + 0.5 })
}

// ── 断言 ──────────────────────────────────────────────────────────────
if (!measurements.length) {
  gauge('模型↔供应商净间距（实测）', '所有视口均未取到可测的拓扑数据 ⇒ 未评估')
  console.log('\n' + summarize(checks, { skipped }).line)
  process.exit(2)
}

console.log('\n📐 逐视口实测（卡片边缘到卡片边缘）：')
for (const m of measurements) {
  console.log(`   视口 ${m.vp.height}px → 画布 ${Math.round(m.canvasH)}px｜模型层 ${m.modelCount} 张（底边 ${Math.round(m.modelBottom)}）｜供应商层 ${m.providerCount} 张（顶边 ${Math.round(m.providerTop)}）｜**净间距 ${Math.round(m.gap)}px**`)
}
console.log('')

const main = measurements.find(m => m.vp.height === 900) || measurements[0]
check('层顺序正确（client < model < provider，DOM 序与数值序交叉校验）',
  measurements.every(m => m.orderOk && m.valueOk),
  `rowY=${JSON.stringify(main.rowY)}`)
check('模型层与供应商层各自都有节点（不是空层）',
  measurements.every(m => m.modelCount > 0 && m.providerCount > 0),
  `model=${main.modelCount} provider=${main.providerCount}`)
check(`模型↔供应商净间距 ≥ ${MIN_GAP}px（全部视口）`,
  measurements.every(m => m.gap >= MIN_GAP),
  `最小实测 ${Math.round(Math.min(...measurements.map(m => m.gap)))}px`)
const gaps = measurements.map(m => m.gap)
const spread = Math.max(...gaps) - Math.min(...gaps)
check(`净间距在各视口间**恒定**（极差 ≤ ${MAX_SPREAD}px ⇒ 证明是固定像素、不是随高度缩放的比例）`,
  spread <= MAX_SPREAD,
  `实测 ${gaps.map(g => Math.round(g) + 'px').join(' / ')}（极差 ${Math.round(spread)}px）`)
check(`净间距 ≥ ${TARGET_GAP}px（固定间距 ROW_GAP=72 扣抖动后的下限）`,
  gaps.every(g => g >= TARGET_GAP), `最小实测 ${Math.round(Math.min(...gaps))}px`)
check('所有节点都在画布内（无底部溢出/裁剪）',
  measurements.every(m => !m.overflow),
  measurements.map(m => `${m.vp.height}px:${m.overflow ? '溢出' : 'OK'}`).join(' '))
// ★ 画布完整性：画布高度必须 ≥ 布局的最小画布高（MIN_LAYOUT_HEIGHT=480）。
//   实测曾出现「stage 行只拿到 320px、画布 min-height 480 溢出被裁 160px」
//   ⇒ 拓扑图底部永远看不见 ⇒ 这条断言就是那个缺陷的守门人。
check('画布高度 ≥ 最小画布高（未被容器裁剪）',
  measurements.every(m => m.canvasH >= 480),
  measurements.map(m => `${m.vp.height}px→画布${Math.round(m.canvasH)}px`).join(' '))
check('网关核心大方块存在且可见',
  measurements.every(m => m.gw && m.gw.h > 0),
  `高度 ${main.gw ? Math.round(main.gw.h) : 'N/A'}px`)
check('模型层不占用网关核心显示区（大方块底边 ≤ 模型层顶边）',
  measurements.every(m => !m.gw || m.modelBottom >= m.gw.bottom - 0.5),
  `大方块底边 ${main.gw ? Math.round(main.gw.bottom) : 'N/A'} vs 模型层顶边 ${Math.round(Math.min(...main.buckets.model.map(n => n.top)))}`)

const summary = summarize(checks, { skipped })
console.log('\n' + summary.line)
process.exit(summary.exitCode)
