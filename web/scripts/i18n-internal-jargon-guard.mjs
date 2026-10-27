#!/usr/bin/env node
// i18n-internal-jargon-guard.mjs —— 「内部标识泄漏」门（语言包侧）
// 第 19 增量（impeccable `skill/reference/clarify.md`：
//   "Do not expose internal codes as the primary message."
//   Audit the language: … "internal jargon or assumed knowledge" …）
//
// 用法：
//   node scripts/i18n-internal-jargon-guard.mjs            # 检查；违规 ⇒ exit 1
//   node scripts/i18n-internal-jargon-guard.mjs --json     # 输出 JSON 报告
//   node scripts/i18n-internal-jargon-guard.mjs --selftest # 判据自证
//   node scripts/i18n-internal-jargon-guard.mjs --list     # 逐个标识符列出命中位置
//   node scripts/i18n-internal-jargon-guard.mjs --update-baseline
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 这道门补的是哪片空白（第 16 增量的错误出口门**只管「哪里报错」**）
//
//   round 16 的 `i18n-error-outlet-guard.mjs` 已经把 clarify.md 那句
//   "Do not expose internal codes as the primary message" 落到了**错误出口**上。
//   ★★ 但同一句纪律在**成功 / 说明 / 操作指引**里同样成立 —— 而那一片至今无人管：
//     「✓ 已清除 {n} 行失败的 node_probe_state，路由候选立即恢复」
//     「routing_overrides 表的每次变更均会记录操作者…」
//   ⇒ 用户在界面上**从来没被展示过**这些名字，却要读它们。
//
// ★ 判据为什么能跨 8 语种统一（第 12 增量踩过的死路在这里绕开了）
//
//   第 12 增量：语言包侧的位置信号只能是**键路径**，而键路径分不出
//   「错误陈述」与「计数摘要」⇒ 裸词表假阳性极高 ⇒ L1 只能列人工清单。
//   ★ 本门**不用词表、不用键路径、不用「散文长度」**——它只问一个跨语种一致的问题：
//       「这句用户可见的话里，出现了【裸的 snake_case 标识符】吗？」
//   ★ snake_case 在任何语言的正常散文里几乎都不出现 ⇒ 它本身就是「机器内部名」的形状标记。
//
// ★ 豁免簿按【标识符】登记（不是按键、不是按语种）⇒ ★★ 8 语种共用一张表
//   ⇒ 「同一个标识符在中文里豁免、在英文里却红」在结构上不可能发生。
//
// ★ 反向自证：豁免必须附**证人**（界面上真的把它展示出来的那个文件），
//   门会核对该文件里**仍然存在**该标识符 ⇒ ★★ 豁免不能靠「文件改了」继续挂着。

import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const REGISTRY = join(HERE, 'i18n-internal-jargon-registry.json')
const BASELINE = join(HERE, 'i18n-internal-jargon-baseline.json')

const argv = process.argv.slice(2)

// ── 加载判据（唯一实现：src/i18n/internalJargon.ts）────────────────────────
const mod = await import(join(WEB_ROOT, 'src/i18n/internalJargon.ts'))
const { scanInternalJargon, groupByIdentifier, runJargonSelftest } = mod

// ── 登记豁免簿 ───────────────────────────────────────────────────────────
function loadRegistry() {
  if (!existsSync(REGISTRY)) throw new Error(`缺登记簿：${REGISTRY}`)
  const raw = JSON.parse(readFileSync(REGISTRY, 'utf8'))
  const list = Array.isArray(raw.exemptions) ? raw.exemptions : []
  const set = new Set(list.map((e) => e.identifier))
  return { raw, list, set }
}

// ── 反向自证：豁免的「证人」必须还在 ─────────────────────────────────────
function checkWitnesses(list, webRoot) {
  const bad = []
  for (const e of list) {
    if (!e.witness) {
      if (e.category === 'payload-field' || e.category === 'ui-visible-term') {
        bad.push({ identifier: e.identifier, why: `类别 ${e.category} 必须附 witness（界面上真的展示它的地方）` })
      }
      continue
    }
    const p = join(webRoot, e.witness)
    if (!existsSync(p)) {
      bad.push({ identifier: e.identifier, why: `证人文件不存在：${e.witness}` })
      continue
    }
    if (!readFileSync(p, 'utf8').includes(e.identifier)) {
      bad.push({ identifier: e.identifier, why: `证人文件里已找不到该标识符（豁免已过期，须重新裁决）：${e.witness}` })
    }
  }
  return bad
}

if (argv.includes('--selftest')) {
  const r = runJargonSelftest()
  for (const f of r.failed) console.log(`  ✘ ${f}`)
  console.log(`\n自证 ${r.passed}/${r.total} ${r.failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`)
  process.exit(r.failed.length ? 1 : 0)
}

const registry = loadRegistry()
const witnessBad = checkWitnesses(registry.list, WEB_ROOT)
const findings = scanInternalJargon(registry.set)
const byId = groupByIdentifier(findings)

// ── 棘轮（只降不升）─────────────────────────────────────────────────────
const metrics = {
  distinctIdentifiers: byId.size,
  findings: findings.length,
}
let baseline = { count: 0, distinctIdentifiers: 0, history: [] }
if (existsSync(BASELINE)) baseline = JSON.parse(readFileSync(BASELINE, 'utf8'))
const regress = []
if (metrics.findings > (baseline.count ?? Infinity)) regress.push(`命中条目 ${metrics.findings} > 基线 ${baseline.count}`)
if (metrics.distinctIdentifiers > (baseline.distinctIdentifiers ?? Infinity)) regress.push(`命中标识符 ${metrics.distinctIdentifiers} > 基线 ${baseline.distinctIdentifiers}`)

// ★★ 棘轮基线【缺失或非法】⇒ ⇒ 棘轮没有上限 ⇒ ⇒ 这道门此刻等于没设门槛 ⇒ ⇒ 必须红。
// ★ 与 `copy-quality-audit.mjs` / `design-elevation-guard.mjs` 同族修法（★ 三处基线洞是同一类）。
// ★ 为什么这里更要紧：★ 本门的基线是**唯一**的棘轮（★ `if (findings.length)` 那条红线只兜「命中数」，
// ★ 兜不住「基线上限被谁调高」⇒ ⇒ 基线一空，棘轮整个静默失效，而门照样打「✅ PASS」。
const baselineUsable =
  typeof baseline?.count === 'number' &&
  typeof baseline?.distinctIdentifiers === 'number'

if (argv.includes('--list')) {
  console.log(`命中标识符 ${byId.size} 个 / 条目 ${findings.length} 条\n`)
  for (const [id, list] of [...byId.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`── ${id}（${list.length} 处）`)
    for (const f of list.slice(0, 10)) {
      console.log(`   ${f.locale}  ${f.key}`)
      console.log(`      ${f.text.replace(/\n/g, '\\n').slice(0, 140)}`)
    }
    if (list.length > 10) console.log(`   … 另 ${list.length - 10} 处`)
  }
  process.exit(0)
}

if (argv.includes('--json')) {
  console.log(JSON.stringify({
    metrics,
    baseline,
    baselineUsable,
    regress,
    witnessBad,
    findings,
    byIdentifier: Object.fromEntries([...byId.entries()].map(([k, v]) => [k, v.length])),
  }, null, 2))
  process.exit(regress.length || witnessBad.length || !baselineUsable ? 1 : 0)
}

if (argv.includes('--update-baseline')) {
  const next = {
    count: metrics.findings,
    distinctIdentifiers: metrics.distinctIdentifiers,
    history: [...(baseline.history ?? []), {
      at: '2026-10-27',
      note: '第 19 增量 · 「内部标识泄漏」轮 · 基线收紧',
      from: { count: baseline.count ?? null, distinctIdentifiers: baseline.distinctIdentifiers ?? null },
      to: metrics,
    }],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`基线已更新：count=${metrics.findings} distinctIdentifiers=${metrics.distinctIdentifiers}`)
  process.exit(0)
}

// ── 人读报告 ────────────────────────────────────────────────────────────
// ★ ★ `--update-baseline` 分支在上面就 return 了 ⇒ ⇒ 这里红**不会**挡住「重建基线」。
if (!baselineUsable) {
  console.error('❌ 基线缺失或非法：scripts/i18n-internal-jargon-baseline.json')
  console.error('   ⇒ 棘轮没有上限 ⇒ ⇒ 这道门此刻等于没设门槛 ⇒ ⇒ 必须红。')
  console.error('   修法：node scripts/i18n-internal-jargon-guard.mjs --update-baseline')
  process.exit(1)
}
console.log(`读数：命中标识符 ${metrics.distinctIdentifiers} 个 · 命中条目 ${metrics.findings} 条`)
console.log(`基线：条目 ≤ ${baseline.count} · 标识符 ≤ ${baseline.distinctIdentifiers}`)
if (witnessBad.length) {
  console.log('\n❌ 豁免登记失效（须重新裁决）：')
  for (const b of witnessBad) console.log(`   · ${b.identifier} —— ${b.why}`)
}
if (regress.length) {
  console.log('\n❌ 棘轮回退：')
  for (const r of regress) console.log(`   · ${r}`)
}
if (findings.length) {
  console.log('\n❌ 用户可见文案里出现未登记的内部标识：')
  for (const [id, list] of [...byId.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`   · ${id}（${list.length} 处）`)
    for (const f of list.slice(0, 3)) console.log(`       ${f.locale}  ${f.key}  ${f.text.replace(/\n/g, '\\n').slice(0, 110)}`)
    if (list.length > 3) console.log(`       … 另 ${list.length - 3} 处`)
  }
}
if (!findings.length && !regress.length && !witnessBad.length) {
  console.log('\n✅ 用户可见文案里没有未登记的内部标识 —— PASS')
  process.exit(0)
}
process.exit(1)
