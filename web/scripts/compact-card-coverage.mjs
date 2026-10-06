#!/usr/bin/env node
/**
 * compact-card-coverage.mjs — compact 卡片形态覆盖率棘轮门
 * （UI规范 03 §3.4「禁止用横滑宽表代替卡片」/ 10 §4.6.37）
 *
 * ## 这道门守的是「不让它继续恶化」
 *
 * 03 §3.4 写着：**禁止**用「横滑宽表」代替卡片；`DataTable` 的横滚 + `min-width`
 * 是**存量降级**兜底，**新列表页不得只交横滚**。
 *
 * 2026-10-06 实测：`src/views` 里 61 个含 `<table>` 的视图中，
 *   · 16 个用了 `ResponsiveDataView` ⇒ compact 切卡片（已覆盖）
 *   · 45 个只有裸表格 ⇒ compact 下是横滚宽表（**存量降级**）
 *
 * ★ 45 个**全部早于** `ResponsiveDataView` 的引入日（2026-10-04），
 *   所以它们是合规的存量，**不是违规**。本门不判它们红。
 *
 * ## 那它守什么
 *
 * 守的是**棘轮**：存量清单与实测集合**双向相等**。
 *   · 新增一个只有裸表格、没走 `ResponsiveDataView` 的视图 ⇒ **红**（新页面违规）
 *   · 基线里某条已被改造（补了卡片形态）⇒ **红**（提示从基线移除，别让清单腐烂）
 *
 * 只报不判的普查另有其人（`compact-table-census.mjs` 查横滚容器覆盖）；
 * 本门是**会红的**。
 *
 * 用法：
 *   node scripts/compact-card-coverage.mjs                  # 判红
 *   node scripts/compact-card-coverage.mjs --json           # 机器可读
 *   node scripts/compact-card-coverage.mjs --update-baseline  # 改造后收基线
 *   node scripts/compact-card-coverage.mjs --self-test      # 自检（反例必须能红）
 */

import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB_ROOT = resolve(HERE, '..')
const VIEWS = join(WEB_ROOT, 'src', 'views')
const BASELINE = join(HERE, 'compact-card-coverage-baseline.json')

const argv = process.argv.slice(2)
const asJson = argv.includes('--json')
const selfTest = argv.includes('--self-test')
const updateBaseline = argv.includes('--update-baseline')

function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    const full = join(dir, e)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (e.endsWith('.vue') && !e.endsWith('.spec.vue')) out.push(full)
  }
  return out
}

/** 扫视图：含 `<table` 且未用 `ResponsiveDataView` ⇒ compact 下是横滚宽表。 */
export function scan(root = VIEWS) {
  const fallback = []
  let tables = 0
  for (const f of walk(root)) {
    const src = readFileSync(f, 'utf8')
    if (!/<table\b/.test(src)) continue
    tables++
    if (/<ResponsiveDataView\b/.test(src)) continue
    fallback.push(relative(WEB_ROOT, f).split('\\').join('/'))
  }
  return { tables, fallback: fallback.sort() }
}

export function readBaseline(file = BASELINE) {
  try {
    const j = JSON.parse(readFileSync(file, 'utf8'))
    return Array.isArray(j.fallback) ? j.fallback.slice().sort() : []
  } catch {
    return []
  }
}

export function diff(baseline, measured) {
  const b = new Set(baseline)
  const m = new Set(measured)
  return {
    added: measured.filter((x) => !b.has(x)), // 新增的横滚宽表视图（新页面违规）
    removed: baseline.filter((x) => !m.has(x)), // 基线里已不成立（已改造，请收基线）
  }
}

if (selfTest) {
  // ---- 自检：正例放行，两类反例都必须能红 ----
  let failed = 0
  const check = (name, cond, detail = '') => {
    console.log(`  ${cond ? '✓' : '✗'} ${name}${detail ? ' — ' + detail : ''}`)
    if (!cond) failed++
  }

  const base = ['src/views/A.vue', 'src/views/B.vue']
  check('空变更：双向都为空 ⇒ 放行', diff(base, base).added.length === 0 && diff(base, base).removed.length === 0)

  const d1 = diff(base, [...base, 'src/views/New.vue'])
  check('★ 新增横滚宽表视图 ⇒ 报出', d1.added.length === 1 && d1.added[0] === 'src/views/New.vue')

  const d2 = diff(base, ['src/views/A.vue'])
  check('★ 基线条目已改造 ⇒ 报出（提示收基线）', d2.removed.length === 1 && d2.removed[0] === 'src/views/B.vue')

  const d3 = diff(base, [...base.filter((x) => x !== 'src/views/B.vue'), 'src/views/X.vue'])
  check('同时增删都要报出来', d3.added.length === 1 && d3.removed.length === 1)

  // 真实扫描可运行
  const s = scan()
  check(`真实扫描可运行（${s.tables} 个含表视图）`, s.tables > 0)
  check('回落清单与基线当前一致（跑真门）', diff(readBaseline(), s.fallback).added.length === 0)

  console.log(`\ncompact-card-coverage selftest: ${failed === 0 ? '全部通过' : failed + ' 条失败'}`)
  process.exit(failed === 0 ? 0 : 1)
}

const { tables, fallback } = scan()
const baseline = readBaseline()
const d = diff(baseline, fallback)

if (asJson) {
  console.log(
    JSON.stringify(
      { tables, cardCovered: tables - fallback.length, fallback, baseline, added: d.added, removed: d.removed },
      null,
      2,
    ),
  )
  process.exit(d.added.length + d.removed.length === 0 ? 0 : 1)
}

if (updateBaseline) {
  writeFileSync(
    BASELINE,
    JSON.stringify(
      {
        _comment:
          'compact 下仍是「横滚宽表」的**存量**视图（UI规范 03 §3.4 存量降级兜底）。' +
          '新增条目必须同时给出卡片形态，否则本门会红；已改造的条目请一并删除。' +
          '生成于 node scripts/compact-card-coverage.mjs --update-baseline',
        baselineCutoff: 'ResponsiveDataView 引入日 2026-10-04 之前',
        fallback,
      },
      null,
      2,
    ) + '\n',
  )
  console.log(`基线已更新：${fallback.length} 条存量降级视图`)
  process.exit(0)
}

console.log('compact 卡片形态覆盖率（UI规范 03 §3.4）')
console.log('='.repeat(64))
console.log(`  含 <table> 的视图        ${tables}`)
console.log(`  · 已覆盖（compact 切卡片）${tables - fallback.length}`)
console.log(`  · 存量降级（横滚宽表）    ${fallback.length}`)
console.log(`  · 覆盖率                  ${(((tables - fallback.length) / tables) * 100).toFixed(1)}%`)
console.log('='.repeat(64))

if (d.added.length === 0 && d.removed.length === 0) {
  console.log('✅ 基线与实测一致（无新增横滚宽表视图，无已收口未更新）')
  process.exit(0)
}

if (d.added.length) {
  console.error(`\n❌ 新增 ${d.added.length} 个 compact 下仍是「横滚宽表」的视图：`)
  for (const f of d.added) console.error(`     ${f}`)
  console.error('   03 §3.4：新列表页不得只交横滚 ⇒ 请改用 `ResponsiveDataView`（compact 切卡片），')
  console.error('   或确认它确属存量降级后再 `node scripts/compact-card-coverage.mjs --update-baseline`。')
}
if (d.removed.length) {
  console.error(`\n❌ 基线里有 ${d.removed.length} 条已不成立（该视图已补卡片形态或已删）：`)
  for (const f of d.removed) console.error(`     ${f}`)
  console.error('   请跑 `node scripts/compact-card-coverage.mjs --update-baseline` 收基线，别让清单腐烂。')
}
process.exit(1)
