// probe-f1-legend.mjs —— 一次性取证探针：紧凑模式下【图例字形是否随模式切换】
// 口径（设计规则 15 / 最终方案说明:61）：
//   · 紧凑模式：图例 done/pending/unmeasured 字形必须为空串（syncLegendGlyphs 真换字形，不是透明化）
//   · 紧凑模式：document.body.innerText 中 ✓ 出现 0 次
//   · 完整模式（对照组）：图例 done 字形必须是 '✓'
import { connect, evaluate, openPage, waitFor } from './cdp-client.mjs'

function parseArgs(argv) {
  const out = { cdp: 'http://127.0.0.1:9222', url: 'https://llm.kxpms.cn', tokenFile: '/tmp/llmgw-jwt.txt' }
  for (let i = 2; i < argv.length; i += 2) {
    const k = argv[i].replace(/^--/, '')
    out[k] = argv[i + 1]
  }
  return out
}

// ★ 2026-11-18 改口径：原来**只留紧凑模式**的结果（`out.legend` 被覆盖两次）
//   ⇒ 完整模式的「对照组」证据被冲掉 ⇒ 探针自己也犯了「只留单边、丢对照」的错。
//   ⇒ 现在两种模式**各留一份**：完整模式是紧凑模式的对照组（必须有 ✓），
//     没有对照组的「紧凑模式 ✓=0」只能证明「什么都没画」，不能证明「按规则画对了」。
const PROBE = `(async () => {
  const q = (s) => document.querySelector(s)
  const qa = (s) => Array.from(document.querySelectorAll(s))
  const legendGlyphs = () => qa('[data-testid="stage-tree-legend"] .legend__glyph').map((el) => ({
    cls: el.className, text: (el.textContent || '').trim(),
  }))
  const checkCount = () => (document.body.innerText.match(/✓/g) || []).length
  const out = { full: null, compact: null }
  const fullBtn = q('[data-testid="stage-mode-full"]')
  if (fullBtn) { fullBtn.click(); await new Promise((r) => setTimeout(r, 400)) }
  out.full = { legend: legendGlyphs(), checkCount: checkCount() }
  const cBtn = q('[data-testid="stage-mode-compact"]')
  if (cBtn) {
    cBtn.click()
    await new Promise((r) => setTimeout(r, 500))
    out.compact = {
      legend: legendGlyphs(),
      checkCount: checkCount(),
      checkSample: (document.body.innerText.match(/.{0,18}✓.{0,18}/g) || []).slice(0, 5).join(' | '),
    }
  }
  return out
})()`

const args = parseArgs(process.argv)
const token = (await import('node:fs')).readFileSync(args.tokenFile, 'utf8').trim()
const { cdp } = await connect(args.cdp)
const { sessionId } = await openPage(cdp, 'about:blank')
await cdp.send('Network.setCookie', { name: 'llmgw_session', value: token, domain: new URL(args.url).hostname, path: '/' }, sessionId)
await cdp.send('Page.navigate', { url: `${args.url}/live2` }, sessionId)
await waitFor(cdp, sessionId, "document.querySelector('.stage-tree') !== null", 30000)
await new Promise((r) => setTimeout(r, 4000))
const r = await evaluate(cdp, sessionId, PROBE)
console.log(JSON.stringify(r, null, 2))
process.exit(0)
