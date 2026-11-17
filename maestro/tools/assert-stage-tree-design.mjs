#!/usr/bin/env node
// maestro/tools/assert-stage-tree-design.mjs —— 请求阶段树（/live2）的**设计对照断言**。
//
// ★ 为什么需要它（2026-11-17 设计对齐轮）：
//   上一轮的 Maestro 资产只验「页面能开、元素在、数据是活的」，
//   **没有一条断言是「界面 == 设计方案」** ⇒ 于是线上长成什么样都没人管：
//   177 行后端心跳、阶段节点只有名字没有值、裸 i18n 键上屏 —— 全都「绿」着。
//   ⇒ 本脚本把**设计方案里的可观测事实**逐条写成断言：
//     ① 版面件齐（图例 / 列头 / 时间列 / 双模式 / 最右错误列）；
//     ② 列头与带**同宽**（错位的表头比没有表头更坏）；
//     ③ 内容诚实（无裸键、无心跳行、阶段节点有值、错误列右缘恒定）；
//     ④ 口径对拍（胶囊读数 == 实际带数）。
//
// 用法：
//   node maestro/tools/assert-stage-tree-design.mjs \
//     --cdp http://127.0.0.1:9222 \
//     --url https://llm.kxpms.cn \
//     --token-file /tmp/llmgw-jwt.txt
//
// ★ 安全约束：JWT 只从 --token-file 读（0600 临时文件），**不打印**。
// 退出码：0 = 全绿；1 = 有断言失败（**产品问题**）；2 = **量具失效**
//   （页面窗口内 0 条真实请求 ⇒ 依赖「有带」的断言**未评估**，不是产品缺陷；
//     线上实时流是有界滚动窗口 ⇒ 需先造流量再立刻重跑）。

import { readFileSync } from 'node:fs'
import { connect, evaluate, openPage, waitFor } from './cdp-client.mjs'
// ★ 判定口径单点化（两个断言工具共用一份，见 result.mjs）。
import { summarize } from './result.mjs'

function parseArgs(argv) {
  const out = { cdp: 'http://127.0.0.1:9222', url: '', tokenFile: '' }
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (a === '--cdp') out.cdp = argv[++i]
    else if (a === '--url') out.url = argv[++i]
    else if (a === '--token-file') out.tokenFile = argv[++i]
  }
  return out
}

const args = parseArgs(process.argv.slice(2))
if (!args.url || !args.tokenFile) {
  console.error('需要 --url 与 --token-file')
  process.exit(1)
}
const token = readFileSync(args.tokenFile, 'utf8').trim()

/** 页面内一次求值：版面契约 + 内容诚实 + 口径对拍。 */
const DESIGN_PROBE = `(() => {
  const q  = (s) => document.querySelector(s)
  const qa = (s) => Array.from(document.querySelectorAll(s))
  const tx = (el) => ((el && el.textContent) || '').replace(/\\s+/g, ' ').trim()
  const visible = (el) => !!(el && (el.offsetWidth || el.offsetHeight || el.getClientRects().length))

  const out = { ok: true, checks: [], facts: {} }
  const check = (name, pass, detail) => {
    out.checks.push({ name, pass: !!pass, detail: detail === undefined ? '' : String(detail) })
    if (!pass) out.ok = false
  }

  // ── ① 版面件齐 ────────────────────────────────────────────────────
  const legend = q('[data-testid="stage-tree-legend"]')
  check('图例行存在（设计第 4 轮：七态 + 两条轨）', !!legend, legend ? tx(legend).slice(0, 80) : '缺失')
  const legendGlyphs = legend ? legend.querySelectorAll('.legend__glyph') : []
  out.facts.legendGlyphs = legendGlyphs.length
  check('图例有 7 枚状态字形', legendGlyphs.length === 7, '实际 ' + legendGlyphs.length)

  const head = q('[data-testid="stage-tree-header"]')
  check('阶段列头存在（设计第 8 轮）', !!head, '')
  const headCells = head ? head.querySelectorAll('.stagehead__cell') : []
  out.facts.headCells = headCells.length
  check('列头有 9 列（时间 + 客户端 + 7 个发送轨阶段）', headCells.length === 9, '实际 ' + headCells.length)

  const bands = qa('article.band')
  out.facts.bands = bands.length
  const firstGrid = bands[0] ? bands[0].querySelector('.band__grid') : null
  const headCols = head ? getComputedStyle(head).gridTemplateColumns : ''
  const bandCols = firstGrid ? getComputedStyle(firstGrid).gridTemplateColumns : ''
  out.facts.headCols = headCols
  out.facts.bandCols = bandCols
  check('列头与请求带共用同一套列宽（不错位）', !!headCols && headCols === bandCols, headCols + ' vs ' + bandCols)

  const timeCells = qa('[data-testid^="stage-band-time-"]')
  out.facts.timeCells = timeCells.length
  const timeOk = timeCells.length > 0 && timeCells.every(c => /^\\d{2}:\\d{2}:\\d{2}$/.test(tx(c)))
  check('每条带都有时间列且格式为 HH:MM:SS', timeOk, timeCells.length ? tx(timeCells[0]) : '无')

  const modeFull = q('[data-testid="stage-mode-full"]')
  const modeCompact = q('[data-testid="stage-mode-compact"]')
  check('有「完整 / 紧凑」双模式切换（设计第 6 轮）', !!modeFull && !!modeCompact, '')

  // ── ② 内容诚实 ────────────────────────────────────────────────────
  const bodyText = document.body.innerText || ''
  const rawKey = bodyText.match(/liveStageTree\\.[A-Za-z_]+|liveTopology\\.[A-Za-z_]+/g)
  out.facts.rawI18nKeys = rawKey ? rawKey.slice(0, 5) : []
  check('界面上没有裸 i18n 键', !rawKey, rawKey ? rawKey.slice(0, 3).join(', ') : '')

  const idleBands = qa('[data-testid^="stage-band-idle-"]')
  out.facts.idleBands = idleBands.length
  check('没有把后端 idle_marker 心跳渲染成请求带', idleBands.length === 0, '实际 ' + idleBands.length)

  // 阶段节点有值：至少一条真实请求的发送轨节点显示的是值而不是「—」
  const sendNodes = qa('[data-testid^="stage-send-"]')
  let valued = 0
  sendNodes.forEach(n => {
    const v = n.querySelector('.band__node-value')
    const s = ((v && v.textContent) || '').trim()
    if (s && s !== '—') valued += 1
  })
  out.facts.sendNodes = sendNodes.length
  out.facts.valuedSendNodes = valued
  check('阶段节点显示的是值而不是只有名字', sendNodes.length === 0 || valued > 0,
        '有值节点 ' + valued + ' / ' + sendNodes.length)

  // ── ③ 口径对拍：胶囊读数 == 实际带数 ──────────────────────────────
  const capsule = q('[data-testid="stage-stats-open"]')
  const capsuleText = tx(capsule)
  out.facts.capsule = capsuleText
  const m = capsuleText.match(/(\\d+)\\s*请求/)
  if (m) {
    check('胶囊读数里的请求数 == 页面带数', Number(m[1]) === bands.length, m[1] + ' vs ' + bands.length)
  } else {
    check('胶囊读数可解析（窗口 · 请求数 · 成功率 · 在途）', false, capsuleText)
  }

  return out
})()`

/** 紧凑模式交互：切到紧凑 ⇒ 逐条验**设计第 6/7/8/9 轮的紧凑契约**（点它 ⇒ 切回完整）。 */
const COMPACT_PROBE = `(async () => {
  const q = (s) => document.querySelector(s)
  const qa = (s) => Array.from(document.querySelectorAll(s))
  const tx = (el) => ((el && el.textContent) || '').replace(/\\s+/g, ' ').trim()
  const out = { checks: [], facts: {} }
  const check = (name, pass, detail) => {
    out.checks.push({ name, pass: !!pass, detail: detail === undefined ? '' : String(detail) })
  }
  const btn = q('[data-testid="stage-mode-compact"]')
  if (!btn) { check('紧凑模式按钮存在', false, '缺失'); return out }
  btn.click()
  await new Promise((r) => setTimeout(r, 300))

  // ① 紧凑模式**不显示阶段列头**（设计原型：body.mode-compact .stagehead{display:none}）
  check('紧凑模式下阶段列头消失', !q('[data-testid="stage-tree-header"]'), '')

  // ② 紧凑模式**不再渲染发送/接收轨节点块**
  //   （实测过的坑：两套节点同时在场会抢同一批网格列 ⇒ 瓦片被挤到第二行、
  //     节点文字被压成「✓ 鉴 o…」。）
  const sendNodes = qa('[data-testid^="stage-send-"]')
  const recvNodes = qa('[data-testid^="stage-recv-"]')
  out.facts.sendNodes = sendNodes.length
  out.facts.recvNodes = recvNodes.length
  check('紧凑模式不渲染发送轨节点块', sendNodes.length === 0, '节点 ' + sendNodes.length)
  check('紧凑模式不渲染接收轨节点块', recvNodes.length === 0, '节点 ' + recvNodes.length)

  // ③ 11 段瓦片**在同一行**（单行流水）
  //   ★ 必须**按带分组**再比：不同带本来就不同行（每带一行），
  //     全局比 top 必然报「多行」⇒ 那是量具错了，不是版面错了。
  const tiles = qa('[data-testid^="stage-tile-"]')
  out.facts.tiles = tiles.length
  check('紧凑模式渲染 11 段瓦片', tiles.length > 0, '瓦片 ' + tiles.length + ' 个')
  const perBand = new Map()
  tiles.forEach(t => {
    const band = t.closest('article.band')
    if (!band) return
    const tops = perBand.get(band) || new Set()
    tops.add(Math.round(t.getBoundingClientRect().top))
    perBand.set(band, tops)
  })
  const multiRow = Array.from(perBand.values()).filter(s => s.size > 1)
  out.facts.bands = perBand.size
  out.facts.multiRowBands = multiRow.length
  check('每条带的 11 段瓦片在**同一行**（不换行）', perBand.size > 0 && multiRow.length === 0,
    '带 ' + perBand.size + ' 条 / 换行的带 ' + multiRow.length + ' 条')

  // ④ 瓦片**有图标**且描边生效
  //   ★ 这里验的是「v-html 注入的 SVG 能不能拿到 scoped 样式」：
  //     拿不到 ⇒ 退回浏览器默认（黑色填充、无描边）⇒ 画面上一坨黑块。
  const svg = tiles.length ? tiles[0].querySelector('svg') : null
  const stroke = svg ? getComputedStyle(svg).stroke : ''
  out.facts.iconStroke = stroke
  check('瓦片图标渲染且描边生效（非默认黑块）', !!svg && stroke && stroke !== 'none', stroke || '无 svg')

  // ⑤ 完成态**不画 ✓**（owner 第 8 轮：整个紧凑模式不要勾勾）
  const doneTiles = tiles.filter(t => /is-done/.test(t.className))
  const withCheck = doneTiles.filter(t => tx(t).indexOf('✓') >= 0)
  out.facts.doneTiles = doneTiles.length
  check('紧凑模式完成态**不画 ✓**', withCheck.length === 0, '带 ✓ 的完成瓦片 ' + withCheck.length + ' / ' + doneTiles.length)

  // ⑥ 右侧是**同一行**的摘要块（不是另起一行的摘要行）
  const cright = qa('[data-testid="stage-band-cright"]')
  out.facts.cright = cright.length
  check('紧凑模式右侧是同一行的摘要块', cright.length > 0, '块 ' + cright.length + ' 个')
  check('紧凑模式没有另起一行的摘要行', qa('.band__summary').length === 0, '')

  // ⑦ 轨道短线：瓦片带轨道标记，且该轨最后一段不画短线
  const rails = tiles.map(t => t.getAttribute('data-rail')).filter(Boolean)
  out.facts.rails = rails.length
  check('瓦片带发送/接收轨标记（轨道短线）', rails.length > 0, '带标记 ' + rails.length + ' 个')
  const tails = qa('[data-testid="stage-tile-provider"][data-tail="1"], [data-testid="stage-tile-reply"][data-tail="1"]')
  check('每条轨的最后一段标了 tail（后面不画短线）', tails.length >= 2, 'tail ' + tails.length + ' 个')

  // ⑧ 最右错误列：右缘恒定（能竖着扫）
  const errCols = qa('[data-testid="stage-band-errorcol"]')
  out.facts.errCols = errCols.length
  check('紧凑模式有最右错误列', errCols.length > 0, '列 ' + errCols.length + ' 个')
  const rights = errCols.map(c => Math.round(c.getBoundingClientRect().right))
  out.facts.errColRights = rights.slice(0, 6)
  const same = rights.length > 0 && rights.every(r => Math.abs(r - rights[0]) <= 1)
  check('错误列右缘恒定（能竖着扫）', same, rights.slice(0, 5).join(','))

  // ⑨ 紧凑树宽：固定 1400（错误列因此贴在**树的**右缘）
  const grid = q('.band__grid')
  const gridW = grid ? Math.round(grid.getBoundingClientRect().width) : 0
  out.facts.gridWidth = gridW
  check('紧凑树宽 ≥ 1400（错误列贴在树右缘）', gridW >= 1400, '实际 ' + gridW)

  // 切回完整模式
  const back = q('[data-testid="stage-mode-full"]')
  if (back) { back.click(); await new Promise((r) => setTimeout(r, 300)) }
  const sendBack = qa('[data-testid^="stage-send-"]')
  check('切回完整模式后发送轨节点回来', sendBack.length > 0, '节点 ' + sendBack.length)
  check('切回完整模式后阶段列头回来', !!q('[data-testid="stage-tree-header"]'), '')
  return out
})()`

async function main() {
  const { cdp, ws } = await connect(args.cdp)
  try {
    const { sessionId } = await openPage(cdp, 'about:blank')
    await cdp.send('Network.enable', {}, sessionId)
    await cdp.send('Runtime.enable', {}, sessionId)
    await cdp.send('Network.setCacheDisabled', { cacheDisabled: true }, sessionId)
    await cdp.send(
      'Emulation.setDeviceMetricsOverride',
      { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false },
      sessionId,
    )
    await cdp.send(
      'Network.setCookie',
      { name: 'llmgw_session', value: token, domain: new URL(args.url).hostname, path: '/' },
      sessionId,
    )
    await cdp.send('Page.navigate', { url: `${args.url}/live2` }, sessionId)
    await waitFor(cdp, sessionId, "document.querySelector('.stage-tree') !== null", 30000)
    // 等实时流落数据
    await new Promise((r) => setTimeout(r, 6000))

    const probe = await evaluate(cdp, sessionId, DESIGN_PROBE)
    const compact = await evaluate(cdp, sessionId, COMPACT_PROBE)

    // ★★ **量具自检**（2026-11-18 实测踩到）：窗口内 **0 条真实请求**时，
    //   「有带时」的那批版面断言**根本无法评估**。
    //   ★ 成因：线上实时流是**有界滚动窗口** —— 生产流量持续进入 ⇒
    //     合成的验收请求 1~2 分钟就被挤出去（本轮实测：行为层跑完还是 1 条带，
    //     设计层隔几十秒就变成 0 条 ⇒ 15 条红**全是量具问题**，不是产品缺陷）。
    //   ★ 纪律：**「没评估到」≠「评估失败」**。把量具失效伪装成 15 条产品红，
    //     会让复核者把工具问题误判成功能问题（比假绿更危险）。
    //   ⇒ 口径：依赖「有带」的断言在无带时标成 **○ 未评估**，
    //     整体报「量具失效」并用**专属退出码 2** —— 调用方据此先造流量再重跑。
    const BAND_DEPENDENT = new Set([
      '列头与请求带共用同一套列宽（不错位）',
      '每条带都有时间列且格式为 HH:MM:SS',
      '阶段节点显示的是值而不是只有名字',
      '胶囊读数里的请求数 == 页面带数',
      '紧凑模式渲染 11 段瓦片',
      '每条带的 11 段瓦片在**同一行**（不换行）',
      '瓦片图标渲染且描边生效（非默认黑块）',
      '紧凑模式完成态**不画 ✓**',
      '紧凑模式右侧是同一行的摘要块',
      '瓦片带发送/接收轨标记（轨道短线）',
      '每条轨的最后一段标了 tail（后面不画短线）',
      '紧凑模式有最右错误列',
      '错误列右缘恒定（能竖着扫）',
      '紧凑树宽 ≥ 1400（错误列贴在树右缘）',
      '切回完整模式后发送轨节点回来',
    ])
    const hasBands = (probe.facts.bands || 0) > 0
    const allChecks = [...probe.checks, ...compact.checks]
    const skipped = hasBands ? [] : allChecks.filter((c) => BAND_DEPENDENT.has(c.name))
    const skippedNames = new Set(skipped.map((c) => c.name))

    console.log('== 设计对照断言（/live2）==')
    for (const c of probe.checks) {
      if (skippedNames.has(c.name)) console.log(`  ○ ${c.name}  —— 未评估（窗口内无真实请求）`)
      else console.log(`  ${c.pass ? '✓' : '✗'} ${c.name}${c.detail ? '  —— ' + c.detail : ''}`)
    }
    console.log('  事实：', JSON.stringify(probe.facts, null, 0))

    console.log('== 紧凑模式（单行瓦片 + 最右错误列）==')
    for (const c of compact.checks) {
      if (skippedNames.has(c.name)) console.log(`  ○ ${c.name}  —— 未评估（窗口内无真实请求）`)
      else console.log(`  ${c.pass ? '✓' : '✗'} ${c.name}${c.detail ? '  —— ' + c.detail : ''}`)
    }
    console.log('  事实：', JSON.stringify(compact.facts, null, 0))

    // ★ 判定口径**单点化**（见 maestro/tools/result.mjs）：
    //   失败计数必须**把两层的检查都算进来**（早先只数第一层 ⇒ 第二层红了却报「PASS」——
    //   「断言红」被统计口径吃掉，等于没有断言）；
    //   且**未评估的项不计入失败**（它们没被评估，不是被评估为失败），
    //   「量具失效」用**专属退出码 2** 与「红（1）」区分开。
    const verdict = summarize(allChecks, { skipped })
    console.log(`\n${verdict.line}`)
    if (verdict.exitCode === 2) {
      console.log('  ⇒ 请先造真实流量，再**立刻**重跑本断言（线上实时流是有界滚动窗口）。')
    }
    ws.close()
    process.exit(verdict.exitCode)
  } catch (err) {
    console.error('assert-stage-tree-design failed:', err?.message || err)
    try {
      ws.close()
    } catch {}
    process.exit(1)
  }
}

await main()
