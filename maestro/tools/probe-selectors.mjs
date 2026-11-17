// maestro/tools/probe-selectors.mjs —— 实测 Maestro web 驱动到底认哪种选择器。
//
// ★ 为什么需要它（2026-11-17「打开功能」轮）：
//   仓内两处记载**互相矛盾**，而本轮要给一个**全新页面**写 flow：
//     · 6e4fa2a6f 的提交信息：「flow 改用 id:（data-testid）」⇒ 暗示 id: 能选 data-testid；
//     · maestro/flows/11-simple-overview.yaml 文件头 ③（1f051a7b0 补）：
//       「id: 只匹配 HTML 的 id 属性，不匹配 data-testid（实测 tapOn id: work-mode-switch ⇒ Element not found）」。
//   ⇒ 两侧都是**二手记载**，且都不是在本轮要验的页面上测的。
//   ⇒ 本脚本在**真实浏览器**里把 /live2 的实际 DOM 抓出来，逐个问：
//     「这个元素能被 text: 命中吗？能被 id: 命中吗？它的完整文本是什么？」
//   ⇒ 结论落到输出里，flow 的写法照着输出写，不再靠猜。
//
// 用法：
//   node maestro/tools/probe-selectors.mjs --url https://llm.kxpms.cn \
//     --token-file /tmp/llmgw-jwt.txt [--cdp http://127.0.0.1:9222]
//
// ★ 安全约束：JWT 只从 --token-file 读（0600 临时文件），**不打印**。
// 退出码：0 = 探针跑通（不代表断言全绿）；1 = 探针自身失败。

import { readFileSync } from 'node:fs'
import { connect, evaluate, openPage, waitFor } from './cdp-client.mjs'

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

const { cdp, ws } = await connect(args.cdp)
try {
  const { targetId, sessionId } = await openPage(cdp, 'about:blank')
  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Network.enable', {}, sessionId)
  await cdp.send('Runtime.enable', {}, sessionId)

  // 注入会话 cookie（HttpOnly 的 llmgw_session 走 CDP Network.setCookie）
  await cdp.send(
    'Network.setCookie',
    { name: 'llmgw_session', value: token, domain: new URL(args.url).hostname, path: '/' },
    sessionId,
  )
  await cdp.send('Page.navigate', { url: `${args.url}/live2` }, sessionId)
  // 等 SPA 渲染完（vue-router 已接管 DOM）
  await waitFor(cdp, sessionId, `document.querySelector('.stage-tree') !== null`, 30000)
  await new Promise((r) => setTimeout(r, 4000))

  const probe = await evaluate(
    cdp,
    sessionId,
    `(() => {
      const pick = (sel) => {
        const el = document.querySelector(sel)
        if (!el) return { sel, found: false }
        return {
          sel,
          found: true,
          tag: el.tagName,
          id: el.id || '',
          testId: el.getAttribute('data-testid') || '',
          // Maestro 的 text: 匹配的是「元素完整文本」—— 这里把子节点的文本
          // 也并进来（textContent），近似 Maestro 的 element text。
          text: (el.textContent || '').replace(/\\s+/g, ' ').trim(),
          visible: !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length),
        }
      }
      const sels = [
        '.stage-tree__title',
        '.stage-tree__conn',
        '.stage-tree__stats',
        '.stage-tree__filters',
        '.stage-tree__canvas',
        '.stage-tree__empty',
        '[data-testid="stage-stats-open"]',
        '[data-testid="stage-tree-empty"]',
        '[data-testid="stage-tree-canvas"]',
        '[data-testid="live-stage-tree-link"]',
        '.live-topology__stagetree',
        '.stage-tree',
        'h2',
      ]
      return {
        url: location.href,
        title: document.title,
        items: sels.map(pick),
        // 页面里所有可见的「短文本」元素（长度 ≤ 24），供 flow 挑断言文案用
        shortTexts: Array.from(document.querySelectorAll('button,span,a,h2,p,div'))
          .filter((e) => {
            const t = (e.textContent || '').replace(/\\s+/g, ' ').trim()
            if (!t || t.length > 24) return false
            if (!e.offsetWidth && !e.offsetHeight) return false
            // 只留「叶子节点」：父元素的 textContent 会包含子元素文本
            return !Array.from(e.children).some((c) => (c.textContent || '').trim() === t)
          })
          .map((e) => ({
            tag: e.tagName,
            cls: e.className && typeof e.className === 'string' ? e.className : '',
            id: e.id || '',
            testId: e.getAttribute('data-testid') || '',
            text: (e.textContent || '').replace(/\\s+/g, ' ').trim(),
          }))
          .slice(0, 120),
      }
    })()`,
  )

  console.log('URL:', probe.url)
  console.log('TITLE:', probe.title)
  console.log('\n=== 关键元素 ===')
  for (const it of probe.items) {
    if (!it.found) {
      console.log(`  ✗ ${it.sel} —— 不存在`)
      continue
    }
    console.log(
      `  ✓ ${it.sel}\n      tag=${it.tag} id="${it.id}" testid="${it.testId}" visible=${it.visible}\n      text="${it.text}"`,
    )
  }
  console.log('\n=== 可见短文本（候选断言文案）===')
  for (const it of probe.shortTexts) {
    console.log(`  [${it.tag}] id="${it.id}" testid="${it.testId}" cls="${it.cls.slice(0, 40)}" → "${it.text}"`)
  }
  ws.close()
} catch (err) {
  console.error('probe failed:', err?.message || err)
  try {
    ws.close()
  } catch {}
  process.exit(1)
}
