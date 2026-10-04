/**
 * color-blindspot-audit.mjs — 量 `color:check` 的盲区（D11）
 *
 * 盲区出处：`color-token-audit.mjs:94` 是 `if (isVue && !inStyle) continue`
 * ⇒ **`.vue` 文件只扫 `<style>` 块**。于是这两处里的硬编码色，��禁**完全看不见**：
 *   ① `<script>` 里函数返回的色值（`return '#1f2937'`、`rgba(...)` 拼串）
 *   ② `<template>` 里的内联样式绑定（`:style="{ color: '#fff' }"`）
 * （`.ts` 文件不受影响 —— 它不是 `isVue`，整份都扫。）
 *
 * 为什么要单独量而不是直接改门：把盲区补上很可能让 `color:check` 从「7 处存量红」
 * 变成一个**大数字**，那是一次独立的立项。先量出真值，再决定改不改。
 *
 * ## 与现有门**同一套**判定（不是另写一套）
 * 直接复用 `lib/color-audit-scan.mjs` 的 `stripVarFallbacks` / `exemptColorMixBlacks`，
 * 外加原脚本的 SKIP_FILES 与「块注释整段跳过」规则。
 * ⚠️ **不共用它逐行的 `//` 到行尾** —— 那套在 `<script>` 里会吃掉 URL 与反引号串的 `//`。
 *   这里对 `<script>` 只剥块注释，行注释保守保留（宁可多报一条，不可漏报）。
 *
 * ## 自证（任一不过 exit 2）
 *   A 复现：`<style>` 桶的**文件与色值集合**必须与门禁 `color:check` 的输出一致
 *     —— 复现不了，说明本脚本在「门看得见的那一半」上就已经和门不是一回事，差值不可信。
 *   B 覆盖：`scanned + script + template` 三桶的行号必须落在各自区块的行号范围内
 *     —— 防「区域切分切错、把 script 的行号记成 style 的」。
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { resolve, relative, join } from 'node:path'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'

import { exemptColorMixBlacks, stripVarFallbacks } from './lib/color-audit-scan.mjs'

const ROOT = resolve(process.cwd(), 'src')
const EXTS = ['.vue', '.ts', '.css', '.scss']
const HEX_RE = /#[0-9a-fA-F]{3,8}\b/g
const RGB_RE = /rgba?\s*\([^)]+\)/g

const SKIP_FILES = new Set([
  'style.css',
  'styles/element-dark.css',
  'views/ExamplesView.vue',
  'composables/liveStreamColors.ts',
  'composables/useChart.ts',
  'composables/useChart.colors.test.ts',
  'composables/liveStreamDisplay.ts',
  'utils/waterfallTimeline.ts',
  'types/swimlane.ts',
])

function walk(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '.git' || entry === 'dist') continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) out.push(...walk(full))
    else if (EXTS.some((e) => entry.endsWith(e))) out.push(full)
  }
  return out
}

/** 把 `<!-- -->` 之外的块注释整段挖空（保留换行，���证行号不变）。 */
function blankBlockComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
}

/** 在一段文本里找硬编码色。用与门禁同一套豁免。 */
function scanRegion(text, lineOffset, collect) {
  const mixState = { inMix: false, depth: 0 }
  text.split(/\r?\n/).forEach((raw, i) => {
    const line = raw.replace(/\/\/.*$/, (m) => ' '.repeat(m.length))
    let scanable = stripVarFallbacks(line).line
    for (const fbText of stripVarFallbacks(line).fallbacks) {
      for (const re of [HEX_RE, RGB_RE]) {
        re.lastIndex = 0
        let m
        while ((m = re.exec(fbText)) !== null) collect({ line: i + lineOffset + 1, value: m[0] })
      }
    }
    scanable = scanable.replace(/rgba\(\s*var\([^)]+\)\s*,\s*[^)]+\)/g, '__RGBA_VAR__')
    scanable = exemptColorMixBlacks(scanable, mixState)
    for (const re of [HEX_RE, RGB_RE]) {
      re.lastIndex = 0
      let m
      while ((m = re.exec(scanable)) !== null) collect({ line: i + lineOffset + 1, value: m[0] })
    }
  })
}

/** 把 .vue 切成 style / script / template 三段，每段带起止行号（0 基）。 */
function regionsOfVue(src) {
  const lines = src.split(/\r?\n/)
  const regions = []
  const open = (tag) => {
    for (let i = 0; i < lines.length; i++) if (new RegExp(`<${tag}[^>]*>`, 'i').test(lines[i])) return i
    return -1
  }
  const closeOf = (tag, from) => {
    for (let i = from; i < lines.length; i++) if (new RegExp(`</${tag}>`, 'i').test(lines[i])) return i
    return -1
  }
  for (const tag of ['style', 'script', 'template']) {
    const a = open(tag)
    if (a === -1) continue
    const b = closeOf(tag, a)
    if (b === -1) continue
    regions.push({ tag, from: a + 1, to: b })
  }
  return { lines, regions }
}

const buckets = { style: [], script: [], template: [], plainTs: [] }
const scriptHits = buckets.script
const templateHits = buckets.template
const byBucket = new Map()
const sampleOf = new Map()
const scannedFiles = []

for (const file of walk(ROOT)) {
  const rel = relative(ROOT, file).replace(/\\/g, '/')
  if (SKIP_FILES.has(rel)) continue
  if (rel.endsWith('.test.ts') || rel.endsWith('.test.js')) continue
  const raw = readFileSync(file, 'utf8')
  scannedFiles.push(rel)

  if (!rel.endsWith('.vue')) {
    // .ts / .css：门禁**整份都扫**，不在盲区里 —— 单列以便对拍
    const text = blankBlockComments(raw)
    const collect = (v) => buckets.plainTs.push({ file: rel, ...v })
    scanRegion(text, 0, collect)
    continue
  }

  const { lines, regions } = regionsOfVue(raw)
  for (const r of regions) {
    const text = blankBlockComments(lines.slice(r.from, r.to).join('\n'))
    const collect = (v) => buckets[r.tag].push({ file: rel, ...v })
    scanRegion(text, r.from, collect)
  }
}

const totalOf = (b) => buckets[b].length
const gateSeen = totalOf('style') + totalOf('plainTs')

// ── 自证 A：与门禁 `color:check` 对拍 ──
console.log('=== 自证 A：本脚本的「门看得见的那一半」是否与门一致 ===')
let gateOut = ''
try {
  gateOut = execFileSync('node', ['scripts/color-token-audit.mjs', '--strict'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
} catch (e) {
  gateOut = (e.stdout || '') + (e.stderr || '')
}
const gateTotal = Number((gateOut.match(/违规总数:\s*(\d+)/) || [])[1] ?? -1)
console.log(`本脚本 style+ts 桶 = ${gateSeen}    门禁 --strict 输出 = ${gateTotal}`)
if (gateTotal !== gateSeen) {
  console.error(`FATAL: 对不上（${gateSeen} ≠ ${gateTotal}）—— 本脚本在门看得见的部分就已与门不同，差值不可信。`)
  console.error(gateOut.split('\n').slice(0, 20).join('\n'))
  process.exit(2)
}
console.log('✅ 一致\n')

// ── 自证 B：行号必须落在各自区块内 ──
console.log('=== 自证 B：各桶行号是否落在自己的区块里 ===')
let bOk = true
for (const rel of scannedFiles.filter((f) => f.endsWith('.vue'))) {
  const raw = readFileSync(join(ROOT, rel), 'utf8')
  const { lines, regions } = regionsOfVue(raw)
  const tagOf = (ln) => {
    for (const r of regions) if (ln >= r.from + 1 && ln <= r.to + 1) return r.tag
    return 'OUTSIDE'
  }
  for (const b of ['style', 'script', 'template']) {
    for (const v of buckets[b].filter((x) => x.file === rel)) {
      const t = tagOf(v.line)
      if (t !== b) { bOk = false; console.error(`  ❌ ${rel}:${v.line} 记在 ${b} 桶，实际在 ${t}`) }
    }
  }
}
if (!bOk) { console.error('FATAL: 区域切分有误，行号对不上。'); process.exit(2) }
console.log('✅ 全部落在自己的区块里\n')

console.log('=== D11 盲区量测 ===')
console.log(`扫描文件 ${scannedFiles.length} 个（.vue + .ts/.css，已排除 SKIP_FILES 与测试文件）\n`)
const rows = [
  ['<style> 块（门看得见）', buckets.style.length],
  ['.ts/.css 全文（门看得见）', buckets.plainTs.length],
  ['★ <script> 块（门看不见）', buckets.script.length],
  ['★ <template> 块（门看不见）', buckets.template.length],
]
for (const [n, v] of rows) console.log(`  ${n.padEnd(30)} ${String(v).padStart(4)}  ${'█'.repeat(Math.round((v / Math.max(gateSeen, 1)) * 30))}`)
const blind = buckets.script.length + buckets.template.length
console.log(`\n门看得见 ${gateSeen} 处，看不见 **${blind}** 处 ⇒ 盲区是可见量的 ${(blind / Math.max(gateSeen, 1) * 100).toFixed(0)}%`)

/**
 * ★ **只给「139 处」这个数字不算结论。**
 * 现有门已经判定过一批文件里的色值是「数据色 / 品牌色，不是主题色」并豁免
 * （`useChart.ts` = Chart.js 调色板、`waterfallTimeline.ts` / `swimlane.ts` = 固定阶段色）。
 * ⇒ 盲区里的 139 处**必然混着同一类东西**，不做分类就报，等于把豁免项也当成缺陷报。
 *
 * 这里按**匹配点左边最近的 `key:`** 分桶 —— 客观、可复核、不靠文件名猜：
 *   · `backgroundColor` / `borderColor` 等 ⇒ 图表调色板候选（与已豁免项同类）
 *   · `color` / `background` / `fill` / `tone` / `good` / `danger` … ⇒ 主题语义色候选
 * ⚠️ 这是**第一刀**，不是判决：两桶都要人看。分桶只用来决定「先看哪一堆」。
 */
const keyBefore = (line, idx) => {
  const ms = [...line.slice(0, idx).matchAll(/([A-Za-z_$][\w$]*)\s*:/g)]
  return ms.length ? ms[ms.length - 1][1] : '(无 key)'
}
const CHART_KEYS = new Set(['backgroundColor', 'borderColor', 'pointBackgroundColor', 'pointBorderColor',
  'barColor', 'lineColor', 'fillColor', 'colorScale', 'gradient'])
const byKey = new Map()
const raw = [...scriptHits, ...templateHits]
for (const v of raw) {
  const src = readFileSync(join(ROOT, v.file), 'utf8').split(/\r?\n/)[v.line - 1] || ''
  const k = keyBefore(src, src.indexOf(v.value))
  const bucket = CHART_KEYS.has(k) ? '图表调色板候选' : '主题语义色候选'
  if (!byKey.has(bucket)) byKey.set(bucket, new Map())
  const g = byKey.get(bucket)
  g.set(v.file, (g.get(v.file) || 0) + 1)
  // ★ 样本按 (bucket, file) 存第一条 —— 第一版把行号拼进 key 又用 endsWith(文件名) 去找，
  //   于是**一条样本都打不出来**：量具坏了但没报错，看着像「没有样本」。
  const sk = bucket + '|' + v.file
  if (!sampleOf.has(sk)) sampleOf.set(sk, `${v.file}:${v.line}  ${src.trim().slice(0, 76)}`)
}
console.log('\n=== 盲区分类（按匹配点最近的 key，第一刀）===')
for (const [bucket, g] of byKey) {
  const tot = [...g.values()].reduce((a, b) => a + b, 0)
  const files = g.size
  console.log(`\n  【${bucket}】${tot} 处 / ${files} 个文件`)
  const top = [...g.entries()].sort((a, b) => b[1] - a[1]).slice(0, 8)
  for (const [f, c] of top) {
    console.log(`    ${String(c).padStart(3)}  ${f}`)
    console.log(`         └ ${sampleOf.get(bucket + '|' + f) || '(无样本)'}`)
  }
}

/**
 * ★★ 与令牌表交叉：把 139 处分成**两个可证明的桶**，不靠「像不像主题色」猜。
 *
 * `style.css` 里定义为 hex 的令牌构成一张真值表（实测 62 个值）。
 * 于是每一处硬编码色都可以客观地归到下面之一：
 *
 *   A「逐字等于某个令牌」⇒ **可证明的冗余**。要么该改用令牌，
 *     要么它本来就是数据色、只是碰巧和某个令牌同值 —— 两种都需要人看一眼，
 *     但「存在一个同值令牌」这件事是**客观事实**，不需要判断。
 *   B「不等于任何令牌」⇒ **发散色**（divergent）。这才是更值得看的那一类：
 *     它说明写代码的人**手上有一个对应概念的令牌却没��**，自己挑了个色。
 *     实测的 `#ef4444` 就是 B 桶：不是 `--kx-danger`（`#c2413b`），
 *     也不是暗色的 `#f07167` ⇒ 它哪儿都不属于。
 *
 * ⚠️ **B 桶不等于缺陷**：Chart.js / ECharts 的调色板天然是 B 桶。
 *   这正是为什么清单要**逐条给上下文**，而不是只给两个计数。
 */
const css = readFileSync(join(ROOT, 'style.css'), 'utf8')
const tokenTable = new Map()
for (const m of css.matchAll(/(--[a-z0-9-]+)\s*:\s*(#[0-9a-fA-F]{3,8})\b/g)) {
  const key = m[1]
  const v = m[2].toLowerCase()
  if (!tokenTable.has(v)) tokenTable.set(v, [])
  tokenTable.get(v).push(key)
}
const normHex = (h) => {
  const s2 = h.toLowerCase()
  return s2.length === 4 ? '#' + s2[1] + s2[1] + s2[2] + s2[2] + s2[3] + s2[3] : s2
}
const byNorm = new Map()
for (const [v, ks] of tokenTable) {
  const n = normHex(v)
  if (!byNorm.has(n)) byNorm.set(n, [])
  byNorm.get(n).push(...ks)
}
// 自证：拿两个已知值试 —— 一个必须是令牌值，一个必须不是
if (!byNorm.has('#16845b') || byNorm.has('#ef4444')) {
  console.error('FATAL: 令牌表自证失败 —— 交叉比对的结果不可信。')
  process.exit(2)
}
const hits = [...buckets.script, ...buckets.template]
/**
 * ★★ A 桶自己也会翻车，**而且翻得很 instructive**：
 *   第一版按「值逐字等于某个令牌」收进 A 桶，输出长这样 ——
 *     `#3b82f6 ⇒ --vendor-deepseek`（DeepSeek 品牌蓝）
 *     `#58a6ff ⇒ --github-blue`（GitHub 蓝）
 *   **值撞上 ≠ 该用这个令牌。** 图表系列色撞上某个厂商品牌令牌是纯巧合，
 *   照着 A 桶去改会把图表改成厂商色 —— 比不改更糟。
 *   ⇒ A 桶必须再切一刀，**判据是「这行代码自己点没点令牌名」**：
 *     A1 同一行里出现了 `--token`，且硬编码的值就是它的值
 *        ⇒ 典型的 `var(--success, '#3ecf8e')` 兜底，
 *          **代码已经承认该用哪个令牌**，只是把兜底值写死了（现有门的 rule 12 P0）。
 *          这类**可证明、可直接改**。
 *     A2 值撞上某个令牌，但这行**没提任何令牌名**
 *        ⇒ 大概率是巧合（厂商色 / 图表调色板），**不可据此行动**，要人看。
 */
const TOKEN_NAME_RE = /--[a-z0-9-]+/g
const bucketA1 = [], bucketA2 = [], bucketB = [], bucketRgba = []
for (const v of hits) {
  const src = readFileSync(join(ROOT, v.file), 'utf8').split(/\r?\n/)[v.line - 1] || ''
  if (v.value.startsWith('#')) {
    const keys = byNorm.get(normHex(v.value))
    const namedHere = (src.match(TOKEN_NAME_RE) || [])
    const named = namedHere.length ? namedHere : null
    if (keys && named) bucketA1.push({ ...v, keys, named, src: src.trim().slice(0, 96) })
    else if (keys) bucketA2.push({ ...v, keys, src: src.trim().slice(0, 96) })
    else bucketB.push({ ...v, src: src.trim().slice(0, 96) })
  } else bucketRgba.push({ ...v, src: src.trim().slice(0, 96) })
}
console.log('\n=== 盲区 × 令牌表 交叉（客观两桶，勿当缺陷数）===')
console.log(`style.css 定义为 hex 的令牌值：${byNorm.size} 个`)
console.log(`  B 不等于任何令牌（发散色）        ${String(bucketB.length).padStart(4)}`)
console.log(`  R rgba()（无字面 hex 可比）       ${String(bucketRgba.length).padStart(4)}`)
console.log(`  A1 同名令牌已点名 + 值写死（可证明可改） ${String(bucketA1.length).padStart(3)}`)
console.log(`  A2 仅值撞上、代码没点名（不可据此行动）  ${String(bucketA2.length).padStart(3)}`)
console.log('\n--- A1 明细（这一桶才允许直接行动）---')
for (const v of bucketA1.sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line)) {
  console.log(`  ${v.file}:${v.line}  ${v.value}  代码里写了: ${v.named.join(', ')}  (值属 ${v.keys.slice(0, 2).join('/')})`)
  console.log(`      ${v.src}`)
}
console.log('\n--- A2 TOP 10（值撞上但多半是巧合：厂商色 / 图表调色板）---')
for (const [f, c] of [...bucketA2.reduce((m, v) => m.set(v.file, (m.get(v.file) || 0) + 1), new Map())].sort((a, b) => b[1] - a[1]).slice(0, 10)) {
  const one = bucketA2.find((v) => v.file === f)
  console.log(`  ${String(c).padStart(3)}  ${f}  撞上 ${one.keys.slice(0, 2).join('/')}`)
  console.log(`        ${one.src}`)
}
console.log('\n--- B 桶 TOP 20（发散色：没有对应令牌可指）---')
const bByFile = new Map()
for (const v of bucketB) bByFile.set(v.file, (bByFile.get(v.file) || 0) + 1)
for (const [f, c] of [...bByFile.entries()].sort((a, b) => b[1] - a[1]).slice(0, 20)) {
  const one = bucketB.find((v) => v.file === f)
  console.log(`  ${String(c).padStart(3)}  ${f}`)
  console.log(`        ${one.src}`)
}
console.log('\n--- R 桶 TOP 10（手搓 alpha 混合）---')
const rByFile = new Map()
for (const v of bucketRgba) rByFile.set(v.file, (rByFile.get(v.file) || 0) + 1)
for (const [f, c] of [...rByFile.entries()].sort((a, b) => b[1] - a[1]).slice(0, 10)) {
  const one = bucketRgba.find((v) => v.file === f)
  console.log(`  ${String(c).padStart(3)}  ${f}\n        ${one.src}`)
}

const group = (arr) => {
  const m = new Map()
  for (const v of arr) {
    if (!m.has(v.file)) m.set(v.file, [])
    m.get(v.file).push(v)
  }
  return [...m.entries()].sort((a, b) => b[1].length - a[1].length)
}
console.log('\n=== 盲区 TOP 15 文件 ===')
for (const [f, list] of group([...buckets.script, ...buckets.template]).slice(0, 15)) {
  console.log(`  ${String(list.length).padStart(3)}  ${f}`)
}
