#!/usr/bin/env node
/**
 * i18n-jargon-runtime-check.mjs —— 「内部标识泄漏」的【运行时】核对（第 19 增量）
 *
 * 为什么要有这道工具（它和 i18n-internal-jargon-guard.mjs 是【两把尺】，不是重复）
 * ──────────────────────────────────────────────────────────────────────────────
 *  `i18n-internal-jargon-guard.mjs` 读的是**语言包源码文本**；
 *  本工具读的是**浏览器渲染出来的字**。
 *
 *  两者能看见的东西不一样：
 *   · 某个键在界面上根本没被引用 ⇒ 源码合规但用户看到的是**键路径**；
 *   · 某个值在语言包里干净、但被组件用 `{{ raw }}` 之类的方式拼进去 ⇒ 源码合规、屏幕泄漏；
 *   · ★ 本仓 `vue-i18n` 设了 `missingWarn: false`
 *     ⇒ ★★ **「控制台没报缺键」是空的判据** ⇒ 正确判据是「渲染文本里有没有键路径」。
 *
 * 判据（4 条）
 * ──────────
 *   R1 **屏幕上没有未登记的内部标识** —— 与源码门【同一个判据实现】
 *      （`detectInternalJargon`，同一张豁免簿），但粒度是**DOM 文本节点**。
 *   R2 **屏幕上没有 i18n 键路径** —— 缺键在界面上就长这样（缺键必须显形为键路径）。
 *      识别方式：首段 ∈ 语言包顶层命名空间集合 ⇒ 不误抓 `example.com` / `e.g.`。
 *   R3 **控制台无错误**（`Runtime.consoleAPICalled` + `Runtime.exceptionThrown`）。
 *   R4 **改写后的文案真的上屏** —— 对【确定性渲染】的键，断言屏上文字包含
 *      该语种语言包里的值（不是键路径、不是空串）。
 *
 * ★★ 本轮实测踩到并已修掉的三个坑（都写在这里，免得下一个人重踩）
 * ───────────────────────────────────────────────────────────────────────────
 *   ① **换语种会被 app 自己覆盖**：语种在**模块加载时**读 localStorage，
 *      而 app 的 `usePersistedValue` 在**页面卸载时**（pagehide/beforeunload flush）
 *      会把内存里的语种**写回** localStorage ⇒ 在 app 页面上设 localStorage 会被冲掉。
 *      ⇒ 修法：在**同源静态资源页**（如 `/favicon.svg`，没有 app JS ⇒ 没有 flush）上写 localStorage，
 *        再导航到 app 页；并**核对 `<html lang>` 与请求语种一致**，不一致就报页面错误（不静默判绿）。
 *   ② **不能用 innerText 判 R1**：浏览器解析完标签后 innerText 里只剩裸文字
 *      ⇒ 源码门 J3 豁免的 `<code>tenant_id</code>` 会**全部误判成泄漏**（实测 9 处）。
 *      ⇒ 修法：逐**文本节点**判，并识别 `<code>/<pre>/<kbd>/<samp>` 祖先（J3 的运行时形态）。
 *   ③ **J2「整串即标识符」的运行时粒度是文本节点**，不是整条页面文本
 *      （表格里一格 mono 文本 `code_audit` 就是「一个标识符占一格」）。
 *
 * 用法
 * ────
 *   node scripts/i18n-jargon-runtime-check.mjs                  # 全部页面 × 指定语种
 *   node scripts/i18n-jargon-runtime-check.mjs --selftest       # 判据自证（不需要浏览器）
 *   node scripts/i18n-jargon-runtime-check.mjs --pages /models,/routing-policy
 *   node scripts/i18n-jargon-runtime-check.mjs --locales zh-CN,en,ja,ar
 *   node scripts/i18n-jargon-runtime-check.mjs --json out.json
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（无头模式 + CDP）。
 *   **没有 Chrome 时本工具不产出任何判定** ⇒ 打印 ⏭ SKIP 并说明「未测量」，
 *   ★ 绝不把「没量到」写成「通过」——两者必须分开（LESSONS.md §450 形态八）。
 */
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const REGISTRY = join(HERE, 'i18n-internal-jargon-registry.json')

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'
/** 同源静态资源页：没有 app JS ⇒ 不会有人把 localStorage 写回去。 */
const STATIC_PAGE = '/favicon.svg'

/** 语言包代码 → 浏览器 localStorage 里的「当前代码」（见 src/i18n.ts 的 STATIC_LOCALES / LAZY_LOADERS）。 */
export const LOCALE_STORAGE_CODE = {
  'zh-CN': 'zh-CN',
  'en-US': 'en',
  'zh-TW': 'zh-TW',
  'ja-JP': 'ja',
  'de-DE': 'de',
  'fr-FR': 'fr',
  'es-ES': 'es',
  'ar-SA': 'ar',
}

/** 默认页面：覆盖本轮 16 个改写键中**能在页面上确定性渲染**的那些。 */
export const DEFAULT_PAGES = [
  '/',
  '/models',
  '/routing-policy',
  '/routing/overrides/audit',
  '/routing-v2/auto-ops?tab=tuning',
  '/routing-v2/auto-ops?tab=profiles',
  '/ops/vibecoding',
]

/** 页面 → 该页面上【确定性渲染】的改写键（R4 断言用）。 */
export const PAGE_TARGET_KEYS = {
  '/routing/overrides/audit': ['routingAudit.subtitle'],
  '/routing-policy': ['routing.policy.formulaPrefix'],
}

// ── 运行时数据准备（判据唯一实现就来自这里）─────────────────────────────
const mod = await import(join(WEB_ROOT, 'src/i18n/internalJargon.ts'))
const quality = await import(join(WEB_ROOT, 'src/i18n/localeValueQuality.ts'))
const { detectInternalJargon } = mod
const { allLocaleValues } = quality

const registryRaw = JSON.parse(readFileSync(REGISTRY, 'utf8'))
const exempt = new Set(registryRaw.exemptions.map((e) => e.identifier))
const localeValues = allLocaleValues()

/** 语言包顶层命名空间集合（任一语种的全部分段首段，跨语种一致）。 */
const namespaces = new Set()
for (const [, values] of localeValues) {
  for (const key of Object.keys(values)) {
    const root = key.split('.')[0]
    if (root) namespaces.add(root)
  }
}

/** 目标键在各语种里的值。 */
function expectedValue(locale, key) {
  const v = localeValues.get(locale)
  return v ? v[key] : undefined
}

// ── 纯函数（可被 --selftest 直接驱动）────────────────────────────────────

/**
 * ★★ R1 的**运行时形态**：判据必须在**DOM 文本节点**这一粒度上跑，而不是在整页 innerText 上跑。
 *
 * ★ 为什么要改粒度（本轮实测踩到）：
 *   · 源码门 J3 豁免「用户对照的代码」——语言包里的 `<code>tenant_id</code>` 不判。
 *     但**浏览器解析完标签后，innerText 里只剩 `tenant_id`，标签没了**
 *     ⇒ 用 innerText 扫屏会把 J3 豁免的形态**全部误判成泄漏**（实测 9 处，全是 `<code>` 里的策略字段名）。
 *   · J2「整串即标识符」在语言包里判的是**整条值**；在运行时真正的粒度是**文本节点**
 *     （表格里的一格 mono 文本节点 `code_audit` 就是「一个标识符占一格」⇒ 同属 J2 形态）。
 *   ⇒ 所以运行时必须**逐文本节点**判，并额外识别 `<code>/<pre>/<kbd>/<samp>` 祖先
 *     ——那是 J3 在 DOM 里的形态。
 *
 * ★ 与源码门**共用同一份判据实现**（`detectInternalJargon`）⇒ 同一个口径只有一份实现。
 */
export function judgeScreenNodes(nodes, detect) {
  const out = []
  for (const node of nodes) {
    if (node.inCode) continue // J3 的运行时形态：用户对照的代码
    const f = detect(node.text)
    if (f) out.push({ text: node.text, identifiers: f.identifiers })
  }
  return out
}

/**
 * R2：屏上文本里的 i18n 键路径。
 * @param text       可见文本
 * @param namespaces 语言包顶层命名空间集合（如 app / routingAudit / providerDetail …）
 */
export function findKeyPaths(text, namespaces) {
  const out = []
  if (!text) return out
  const roots = [...namespaces].sort((a, b) => b.length - a.length).join('|')
  if (!roots) return out
  const re = new RegExp(`\\b(?:${roots})\\.[A-Za-z][A-Za-z0-9_]*(?:\\.[A-Za-z][A-Za-z0-9_]*)*`, 'g')
  for (const m of text.matchAll(re)) out.push(m[0])
  return [...new Set(out)]
}

/**
 * R4：目标键的值是否真的上屏。
 * ★ 不能用「删掉占位符再整体包含」——删完会留下双空格（`已清除 {n} 条` ⇒ `已清除  条`）
 *   而屏上是 `已清除 12 条` ⇒ 子串匹配必然失配（★ 这条是自证当场抓出来的）。
 * ⇒ 改成：按占位符**切段**，每段（去掉首尾空白后）都必须出现在屏上。
 * ★ 少于 4 个字符的段视作【弱证据】（如 `)`），不单独成立，但也不误判失败。
 */
export function valueAppearsOnScreen(expected, screenText) {
  if (!expected || !screenText) return false
  const segments = expected
    .split(/\{[^{}]*\}/)
    .map((s) => s.trim())
    .filter(Boolean)
  const meaningful = segments.filter((s) => s.length >= 4)
  if (!meaningful.length) return false
  return meaningful.every((s) => screenText.includes(s))
}

export function runJargonRuntimeSelftest() {
  const cases = []
  const t = (name, fn) => {
    try { fn(); cases.push({ name, ok: true }) }
    catch (e) { cases.push({ name, ok: false, msg: e.message }) }
  }
  const eq = (a, b, why) => { if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${why}：期望 ${JSON.stringify(b)}，实际 ${JSON.stringify(a)}`) }
  // ★ 自证必须走**真判据**（同一个 detectInternalJargon），不是另写一套
  const detect = (text) => detectInternalJargon('probe', text, 'zh-CN', exempt)

  t('R1：裸 snake_case 散文被判红', () => {
    const r = judgeScreenNodes([{ text: '已清除 12 行失败的 node_probe_state', inCode: false }], detect)
    eq(r.length, 1, '命中条数')
    eq(r[0].identifiers, ['node_probe_state'], '标识符')
  })
  t('R1：J2 —— 整串即标识符（表格里一格）不判', () => {
    const r = judgeScreenNodes([{ text: 'code_audit', inCode: false }], detect)
    eq(r.length, 0, '不应判红')
  })
  t('R1：J3 —— <code> 内的标识符不判（运行时形态）', () => {
    const r = judgeScreenNodes([{ text: 'algorithm_version', inCode: true }], detect)
    eq(r.length, 0, '不应判红')
  })
  t('R1：J4 —— 豁免簿里的标识符不判', () => {
    const r = judgeScreenNodes([{ text: 'composite_score = 手工序号', inCode: false }], detect)
    eq(r.length, 0, '不应判红')
  })
  t('R1：J3b —— 模板插值里的占位符名不判', () => {
    const r = judgeScreenNodes([{ text: '按 {session_load} 计分', inCode: false }], detect)
    eq(r.length, 0, '不应判红')
  })
  t('R1：普通散文里的普通词不判', () => {
    const r = judgeScreenNodes([{ text: '路由候选已重新可用', inCode: false }], detect)
    eq(r.length, 0, '假阳性')
  })
  t('R2：i18n 键路径被抓出来', () => {
    eq(findKeyPaths('渲染失败：providerDetail.models.resetNodeProbeTitle', ['providerDetail']), ['providerDetail.models.resetNodeProbeTitle'], '命中')
  })
  t('R2：URL / 示例引导词不算', () => {
    eq(findKeyPaths('见 api.example.com 与 e.g. mail.tm', ['providerDetail', 'routingAudit']), [], '假阳性')
  })
  t('R2：非命名空间首段不算', () => {
    eq(findKeyPaths('foo.bar.baz', ['providerDetail']), [], '假阳性')
  })
  t('R4：值上屏（占位符已被替换）⇒ 命中', () => {
    eq(valueAppearsOnScreen('✓ 已清除 {n} 条失败的探测记录', '✓ 已清除 12 条失败的探测记录，路由候选立即恢复'), true, '应命中')
  })
  t('R4：键路径上屏 ⇒ 不算命中（缺键不算渲染成功）', () => {
    eq(valueAppearsOnScreen('✓ 已清除 {n} 条失败的探测记录', 'providerDetail.models.resetNodeProbeDone'), false, '不应命中')
  })
  t('R4：值全是占位符 ⇒ 不算命中', () => {
    eq(valueAppearsOnScreen('{n}', '12'), false, '不应命中')
  })
  t('★ 判据本身不含全局状态（同一个输入两次结果一致）', () => {
    const a = judgeScreenNodes([{ text: 'node_probe_state x', inCode: false }], detect)
    const b = judgeScreenNodes([{ text: 'node_probe_state x', inCode: false }], detect)
    eq(a, b, '幂等')
  })
  t('★ 豁免集合变化会改变结论（证明 R1 真的读了豁免表）', () => {
    const withEx = detectInternalJargon('probe', 'node_probe_state x', 'zh-CN', new Set(['node_probe_state']))
    eq(withEx, null, '豁免后应为空')
  })
  t('★ 命名空间集合变化会改变结论（证明 R2 真的读了命名空间表）', () => {
    eq(findKeyPaths('a.b.c', ['a']).length, 1, '含 a 时应命中')
    eq(findKeyPaths('a.b.c', ['z']).length, 0, '不含 a 时不应命中')
  })
  t('★ inCode 标志会改变结论（证明 R1 真的读了 DOM 形态）', () => {
    eq(judgeScreenNodes([{ text: 'node_probe_state x', inCode: true }], detect).length, 0, 'inCode 时不判红')
    eq(judgeScreenNodes([{ text: 'node_probe_state x', inCode: false }], detect).length, 1, '非 inCode 时判红')
  })

  const failed = cases.filter((c) => !c.ok)
  for (const c of cases) console.log(`  ${c.ok ? '✓' : '✘'} ${c.name}${c.ok ? '' : ' —— ' + c.msg}`)
  console.log(`\n自证 ${cases.length - failed.length}/${cases.length} ${failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`)
  return failed.length === 0
}

// ── 页内探针（在浏览器上下文里跑）────────────────────────────────────────
/** @param shapeSrc 与判据同一个正则的 source（形状预筛，避免回传整页文本） */
const PROBE = (shapeSrc) => String.raw`
(() => {
  const SHAPE = new RegExp(${JSON.stringify(shapeSrc)}, 'g');
  const nodes = [];
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let n;
  while ((n = walker.nextNode())) {
    const t = n.nodeValue || '';
    SHAPE.lastIndex = 0;
    if (!SHAPE.test(t)) continue;
    const el = n.parentElement;
    if (!el) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.display === 'none') continue;
    if (!el.getClientRects().length) continue;   // 不可见 ⇒ 用户看不到
    nodes.push({ text: t.slice(0, 400), inCode: !!el.closest('code, pre, kbd, samp') });
  }
  return {
    nodes,
    lang: document.documentElement.getAttribute('lang') || '',
    dir: document.documentElement.getAttribute('dir') || '',
    text: (document.body && document.body.innerText ? document.body.innerText : '').slice(0, 200000),
  };
})()
`

// ── CDP 驱动 ────────────────────────────────────────────────────────────
async function runChrome(port, pages, locales) {
  const profile = mkdtempSync(join(tmpdir(), 'jargon-runtime-'))
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    '--window-size=1440,900', 'about:blank',
  ], { stdio: 'ignore' })

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  for (let i = 0; i < 80; i++) {
    try { if ((await fetch(`http://127.0.0.1:${port}/json/version`)).ok) break } catch { /* 等 */ }
    await sleep(250)
  }
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
  const target = list.find((t) => t.type === 'page')
  if (!target) throw new Error('找不到 page target')

  const ws = new WebSocket(target.webSocketDebuggerUrl)
  await new Promise((res, rej) => {
    ws.addEventListener('open', res, { once: true })
    ws.addEventListener('error', rej, { once: true })
  })
  let seq = 0
  const pending = new Map()
  const consoleErrors = []
  ws.addEventListener('message', (ev) => {
    const msg = JSON.parse(ev.data)
    if (msg.method === 'Runtime.consoleAPICalled' && msg.params.type === 'error') {
      consoleErrors.push({ kind: 'console.error', text: (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ').slice(0, 300) })
    }
    if (msg.method === 'Runtime.exceptionThrown') {
      consoleErrors.push({ kind: 'exception', text: String(msg.params.exceptionDetails?.exception?.description || msg.params.exceptionDetails?.text || '').slice(0, 300) })
    }
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id)
      pending.delete(msg.id)
      if (msg.error) reject(new Error(msg.error.message))
      else resolve(msg.result)
    }
  })
  const cdp = (method, params = {}) => {
    const id = ++seq
    ws.send(JSON.stringify({ id, method, params }))
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject })
      setTimeout(() => { if (pending.has(id)) { pending.delete(id); reject(new Error(`CDP 超时: ${method}`)) } }, 60000)
    })
  }
  const evaluate = async (expression) => {
    const r = await cdp('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true, userGesture: true })
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    return r.result.value
  }
  await cdp('Page.enable')
  await cdp('Runtime.enable')

  const report = { rows: [], errors: [] }
  for (const locale of locales) {
    const storageCode = LOCALE_STORAGE_CODE[locale]
    if (!storageCode) { report.errors.push({ locale, error: `未知语种 ${locale}` }); continue }
    // ★ 在**同源静态资源页**上写 localStorage：那里没有 app JS ⇒ 没有 pagehide flush 把语种写回去
    await cdp('Page.navigate', { url: BASE + STATIC_PAGE })
    await sleep(600)
    await evaluate(`(() => { localStorage.setItem('llmgw_locale', ${JSON.stringify(storageCode)}); return localStorage.getItem('llmgw_locale') })()`)

    for (const p of pages) {
      const before = consoleErrors.length
      try {
        await cdp('Page.navigate', { url: BASE + p })
        await sleep(3000)
        const probe = await evaluate(PROBE(mod.JARGON_ID_RE.source))
        const screen = probe.text || ''

        // ★ 换语种是否真的生效？<html lang> 是渲染语种的【可观测真值】
        const renderedLang = probe.lang
        if (renderedLang !== storageCode) {
          report.errors.push({ locale, page: p, error: `渲染语种 ${renderedLang} ≠ 请求语种 ${storageCode}（本行不判定）` })
          console.log(`✘ [${locale}] ${p}  渲染语种 ${renderedLang} ≠ 请求 ${storageCode} ⇒ 本行不判定`)
          continue
        }

        const judged = judgeScreenNodes(probe.nodes, (text) => detectInternalJargon('runtime', text, locale, exempt))
        const jargon = [...new Set(judged.flatMap((j) => j.identifiers))]
        const keyPaths = findKeyPaths(screen, namespaces)

        const targets = (PAGE_TARGET_KEYS[p] || [])
        const missing = []
        for (const key of targets) {
          const exp = expectedValue(locale, key)
          if (exp === undefined) { missing.push({ key, why: '语言包里没有这个键' }); continue }
          if (!valueAppearsOnScreen(exp, screen)) missing.push({ key, why: '值未上屏', expected: exp })
        }

        const errs = consoleErrors.slice(before)
        report.rows.push({ locale, page: p, jargon, keyPaths, missing, consoleErrors: errs, renderedLang })
        const bad = jargon.length + keyPaths.length + missing.length
        console.log(`${bad ? '✘' : '✔'} [${locale}] ${p}  内部标识 ${jargon.length} · 键路径 ${keyPaths.length} · 未上屏 ${missing.length} · console 错误 ${errs.length}`)
        for (const j of jargon.slice(0, 5)) console.log(`     ✘ 内部标识：${j}`)
        for (const k of keyPaths.slice(0, 5)) console.log(`     ✘ 键路径：${k}`)
        for (const m of missing.slice(0, 5)) {
          console.log(`     ✘ 未上屏：${m.key} —— ${m.why}`)
          if (m.expected !== undefined) console.log(`        期望值：${JSON.stringify(m.expected).slice(0, 200)}`)
          console.log(`        屏上片段：${JSON.stringify(screen.slice(0, 300))}`)
        }
        for (const e of errs.slice(0, 3)) console.log(`     · console ${e.kind}: ${e.text}`)
      } catch (e) {
        report.errors.push({ locale, page: p, error: String(e?.message || e) })
        console.log(`✘ [${locale}] ${p} 失败: ${e?.message || e}`)
      }
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── CLI ──────────────────────────────────────────────────────────────────
const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  process.exit(runJargonRuntimeSelftest() ? 0 : 1)
}

const chrome = CHROME_CANDIDATES.find((p) => existsSync(p))
if (!chrome) {
  console.log('⏭ SKIP —— 未找到 Chrome 可执行文件 ⇒ **本轮未测量**（不是「通过」）')
  console.log('   可用 CHROME_PATH=/path/to/chrome 指定；或先装 Google Chrome。')
  process.exit(0)
}

const pagesArg = argv.find((a) => a.startsWith('--pages='))
const pages = pagesArg ? pagesArg.slice('--pages='.length).split(',').map((s) => s.trim()).filter(Boolean) : DEFAULT_PAGES
const localesArg = argv.find((a) => a.startsWith('--locales='))
const locales = localesArg
  ? localesArg.slice('--locales='.length).split(',').map((s) => s.trim()).filter(Boolean)
  : ['zh-CN', 'en-US', 'ja-JP', 'ar-SA']
const jsonOut = argv.includes('--json') ? argv[argv.indexOf('--json') + 1] : null

console.log(`运行时内部标识核对 —— ${BASE} · ${pages.length} 页 × ${locales.length} 语种 · Chrome: ${chrome}\n`)
const report = await runChrome(9335 + (process.pid % 50), pages, locales)

let jargon = 0, keyPaths = 0, missing = 0, consoleErrs = 0
for (const row of report.rows) {
  jargon += row.jargon.length
  keyPaths += row.keyPaths.length
  missing += row.missing.length
  consoleErrs += row.consoleErrors.length
}
if (jsonOut) writeFileSync(jsonOut, JSON.stringify(report, null, 2))

console.log(`\n════ 汇总 ════`)
console.log(`判定 ${report.rows.length} 次（页 × 语种）· 屏上内部标识 ${jargon} 处 · 屏上键路径 ${keyPaths} 处 · 该上屏却没上屏 ${missing} 处 · console 错误 ${consoleErrs} 条`)
if (report.errors.length) for (const e of report.errors) console.log(`   ✘ ${e.locale ?? ''} ${e.page ?? ''}: ${e.error}`)
if (jargon === 0 && keyPaths === 0 && missing === 0 && report.errors.length === 0 && report.rows.length > 0) {
  console.log('✅ 屏幕上没有内部标识、没有缺键键路径、改写后的文案确实上屏 —— PASS')
  process.exit(0)
}
console.log('❌ 运行时存在内部标识 / 键路径 / 未上屏，或有页面没测到 —— FAIL')
process.exit(1)
