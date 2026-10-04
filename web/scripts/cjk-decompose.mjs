/**
 * cjk-decompose.mjs — 把硬编码中文计数**逐口径拆开**，回答「现行口径里有多少根本不是要守的东西」。
 *
 * ⚠️ **本脚本的「精确」层在第三版之前是错的，而且四轮自证全绿。**
 *   第三版用 `ts.createScanner` 独立扫注释区间。独立扫描器**不跟踪模板串的替换嵌套**：
 *   源码里一旦出现带 `${}` 的模板串，它就再也不产出注释 token（`skipTrivia` 两种都试过，
 *   都是 0 条）。实测 `AuditLogView.vue` 的 <script>：真注释区间 33 条，扫描器只认出 14 条。
 *   ⇒ 它报的「精确值 5533」**不是真值**，只是比真值多 37。
 *   ⇒ 致命之处不在数字错，而在**失败形态与正常完全同形**：计数只会**偏大**，
 *     方向正常、不报错、不破坏单调性 —— 于是自证 B（单调）/C（交叉）全过，
 *     而自证 D 的三条探针里**没有一个含 `${}` 的模板串**。
 *   ⇒ 判别动作：换注释识别实现时，探针必须包含一个**已证明会把量具打偏**的样本。
 *     2026-10-06 已补进生产实现的自证 D9 / D13 / D14 / D15。
 *
 * 现在的口径（与 src/i18n/hardcodedCjk.ts 共用同一实现，不重复实现）：
 *   旧口径（只跳整行注释）7030 → **现行口径（只跳真注释）5496**
 *   注释里占旧口径 1534 段 = 21.8%，它们永远不会出现在屏幕上。
 *
 * 用法：node scripts/cjk-decompose.mjs   （只读诊断，**不是门禁**，不改 baseline）
 *
 * 自证四条，任一不过就 exit 2（**每条都打印一行 ✅；脚本自己会数自己打了几条**）：
 *   A 独立复算 —— 本脚本独立读盘算出的「掩码 + 整行快路径」必须**逐位**等于 CLI 输出。
 *     顺带证明整行快路径已彻底冗余（两者相等 ⇒ 删掉它不改变任何数）。
 *   B 单调     —— 旧口径 ≥ 现行口径（剥掉的内容只会变多 ⇒ 计数只能变小）
 *   C 交叉     —— 现行口径必须同时 ≤ 旧口径 与 ≤「只去 HTML 注释」的朴素值
 *   D 委托     —— 生产实现的 16 条口径自证必须全绿（直接转述，不自己重算）
 */
import { readFileSync, readdirSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

// 本文件 import 的是 .ts（生产实现），必须带 --experimental-strip-types 跑。
// 没带就把自己带 flag 重启一次；这一段失败会让「诊断跑不出来」看起来像「数字变了」。
if (!process.env.CJK_DECOMPOSE_STRIP) {
  const r = spawnSync(process.execPath, ['--experimental-strip-types', '--no-warnings', fileURLToPath(import.meta.url)], {
    stdio: 'inherit',
    env: { ...process.env, CJK_DECOMPOSE_STRIP: '1' },
  })
  process.exit(r.status ?? 1)
}

const __dirname = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = join(__dirname, '..')
const SRC = join(WEB_ROOT, 'src')
const rel = (p) => p.slice(SRC.length + 1)
const mod = await import(pathToFileURL(join(SRC, 'i18n', 'hardcodedCjk.ts')).href)
const { maskSource, countFile, selfCheckCounting } = mod

const EX = new Set(['node_modules', 'dist', '.git', 'locales', 'i18n'])
const files = []
;(function rec(d) {
  for (const e of readdirSync(d, { withFileTypes: true })) {
    const p = join(d, e.name)
    if (e.isDirectory()) { if (EX.has(e.name) || e.name.startsWith('.')) continue; rec(p) }
    else if (e.isFile() && /\.(vue|ts|tsx|js|mjs)$/.test(e.name) && !/\.(test|spec)\.[cm]?[jt]s$/.test(e.name)) files.push(p)
  }
})(SRC)

const CJK = /[一-鿿]+/g
const cjk = (s) => (s.match(CJK) || []).length

/** 历史口径：只跳「trim 后以 // * /* 开头」的整行。保留它是为了让口径变更可对比。 */
function countLegacy(src) {
  let n = 0
  for (const line of src.split('\n')) {
    const t = line.trim()
    if (t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')) continue
    n += cjk(line)
  }
  return n
}

/** 朴素版：纯正则切注释。**已知它会误吃字符串里的 `/*`**，这里只用来当反面教材。 */
const naiveCut = (src) =>
  src.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
     .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))

// ⚠️ 「数脚本实际打出几条 ✅」——头注释写几条不算数。
let okLines = 0
const pass = (label, msg = '') => { okLines++; console.log(`✅ ${label}${msg ? ' — ' + msg : ''}\n`) }
const fatal = (msg) => { console.error(`FATAL: ${msg}`); process.exit(2) }

let legacy = 0, current = 0, withFastPath = 0, naive = 0, l2 = 0
const perFile = []
for (const f of files) {
  const src = readFileSync(f, 'utf8')
  const masked = maskSource(f, src)
  const a = countLegacy(src)
  const b = cjk(masked)
  // 独立复算「掩码 + 整行快路径」：用于自证 A，也用来证明快路径已冗余。
  let w = 0
  for (const line of masked.split('\n')) {
    const t = line.trim()
    if (t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')) continue
    w += cjk(line)
  }
  legacy += a; current += b; withFastPath += w
  naive += countLegacy(naiveCut(src))
  l2 += countLegacy(src.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' ')))
  if (a > 0) perFile.push({ f: rel(f), a, b, only: a - b })
}

console.log('=== 自证 A：本脚本独立复算的「掩码 + 整行快路径」是否逐位等于 CLI ===')
const out = spawnSync(process.execPath, [join(WEB_ROOT, 'scripts', 'i18n-cjk-count.mjs')], { cwd: WEB_ROOT, encoding: 'utf8' })
const cliCount = Number((out.stdout || '').match(/hardcoded CJK count:\s*(\d+)/)?.[1] ?? -1)
console.log(`  本脚本复算 = ${withFastPath}    CLI = ${cliCount}    CLI rc = ${out.status}`)
if (cliCount !== withFastPath) fatal(`复现失败（${withFastPath} ≠ ${cliCount}）—— 本脚本量具不可信，中止。`)
if (withFastPath !== current) fatal(`整行快路径仍有余量（${withFastPath} ≠ ${current}）—— 有人把非注释行当注释删了，或掩码有洞。`)
pass('A 复现 + 快路径冗余', `${withFastPath} == ${current}`)

console.log('=== 自证 B：单调性（旧口径 ≥ 现行口径）===')
console.log(`  旧=${legacy}  现行=${current}`)
if (!(legacy >= current)) fatal('不单调 —— 计数涨了，说明掩码在往回加东西。')
pass('B 单调')

console.log('=== 自证 C：交叉不变式（现行必须同时 ≤ 旧口径 与 ≤ 只去 HTML 注释的朴素值）===')
console.log(`旧=${legacy}  L2(只去 HTML 注释)=${l2}  现行=${current}`)
if (!(current <= l2 && current <= legacy)) fatal('不变式被破 —— 别读下面的结论。')
pass('C 两条都满足')

console.log('=== 自证 D：委托生产实现的 16 条口径自证 ===')
const cases = selfCheckCounting()
const failed = cases.filter((c) => !c.ok)
for (const c of cases) console.log(`  ${c.ok ? '✅' : '❌'} ${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`)
if (failed.length) fatal(`生产实现的口径自证有 ${failed.length} 条不过，中止。`)
if (cases.length !== 16) fatal(`口径自证只跑出 ${cases.length} 条（期望 16）—— 探针被删过或没跑全。`)
okLines++
console.log(`✅ D 生产实现 ${cases.length} 条口径自证全绿\n`)

console.log('=== 两种口径对拍 ===')
console.log(`旧口径（只跳整行注释）        ${legacy}`)
console.log(`现行口径（只跳真注释）        ${current}   ← 门禁采用`)
console.log(`朴素正则（疑似误吃真代码）    ${naive}   差 ${naive - current}`)
console.log(`\n注释里占旧口径的 ${legacy - current} 段 = ${(((legacy - current) / legacy) * 100).toFixed(1)}%`)

console.log('\n=== 注释占用 TOP 12（旧 − 现行）===')
perFile.sort((a, b) => b.only - a.only).slice(0, 12)
  .forEach((r) => console.log(`  ${String(r.only).padStart(4)} / ${String(r.a).padStart(4)}  ${r.f}`))

console.log(`\n（本脚本共打出 ${okLines} 条 ✅：A / B / C / D）`)
if (okLines !== 4) fatal(`头注释声明 4 条自证，实际只打出 ${okLines} 条。`)
