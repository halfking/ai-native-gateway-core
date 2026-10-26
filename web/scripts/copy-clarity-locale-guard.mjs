#!/usr/bin/env node
// copy-clarity-locale-guard.mjs — 「语言包侧清晰文案」门 CLI（2026-10-27，第 12 增量）
//
// 判据实现：src/i18n/copyClarityLocale.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**：按仓内既有模式（copy-clarity-guard.mjs / i18n-cjk-count.mjs）
//   用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// 两条判据（来自 impeccable skill/reference/clarify.md，与源码侧同源）：
//   L2 别用手势   —— 手势词必须点名具体目标（引号内容 / 模板插值 / 箭头）
//   L3 通用空状态 —— 空状态位的值整句等于通用空状态短语（没点名「缺的是什么」）
//
// ★ 与源码侧门（copy-clarity-guard.mjs）的关系：
//   源码侧门只扫**界面层源码**；语言包侧门扫**8 个语种的语言包值**。
//   ⇒ 两者合起来才覆盖「用户实际看到的文案」。
//
// ★ 本轮**不含** L1（错误三问）：键路径在语言包里不区分「错误陈述」与「计数摘要」
//   ⇒ 裸词表假阳性率高 ⇒ 该族列人工清单（审计档），下一轮再设硬闸。
//
// 用法：
//   node scripts/copy-clarity-locale-guard.mjs              # 扫描，有违规 exit 1
//   node scripts/copy-clarity-locale-guard.mjs --json       # 机器可读
//   node scripts/copy-clarity-locale-guard.mjs --list       # 按语种分组的工作单
//   node scripts/copy-clarity-locale-guard.mjs --selftest   # 自证：违规→红 / 干净→绿

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'i18n', 'copyClarityLocale.ts')
const implUrl = pathToFileURL(IMPL).href

const body = `
import { detectLocaleClarity, runLocaleSelftest } from ${JSON.stringify(implUrl)}
if (process.env.CLARITY_LOCALE_MODE === 'selftest') {
  const r = runLocaleSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const findings = detectLocaleClarity()
console.log(JSON.stringify(findings))
`

const isSelftest = process.argv.includes('--selftest')

const r = spawnSync(
  process.execPath,
  ['--experimental-strip-types', '--no-warnings', '--input-type=module', '-e', body],
  {
    cwd: ROOT,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    env: { ...process.env, CLARITY_LOCALE_MODE: isSelftest ? 'selftest' : 'scan' },
  }
)

if (r.status !== 0 && r.stdout.trim() === '') {
  process.stderr.write(r.stderr || '')
  process.exit(r.status || 2)
}

if (isSelftest) {
  process.stdout.write(r.stdout)
  process.exit(r.status || 0)
}

let findings = []
try {
  findings = JSON.parse(r.stdout.trim() || '[]')
} catch {
  process.stderr.write(r.stdout || '')
  process.stderr.write(r.stderr || '')
  process.exit(2)
}

if (process.argv.includes('--json')) {
  console.log(JSON.stringify({ count: findings.length, findings }, null, 2))
} else if (process.argv.includes('--list')) {
  const byLocale = new Map()
  for (const f of findings) {
    if (!byLocale.has(f.locale)) byLocale.set(f.locale, [])
    byLocale.get(f.locale).push(f)
  }
  const byKey = new Map()
  for (const f of findings) {
    if (!byKey.has(f.key)) byKey.set(f.key, [])
    byKey.get(f.key).push(f)
  }
  console.log(`违规 ${findings.length} 处 / ${byLocale.size} 语种 / ${byKey.size} 键`)
  for (const [locale, list] of byLocale) {
    console.log(`\n${locale}  (${list.length})`)
    for (const f of list) console.log(`  ${f.key}  [${f.rule}]  「${f.text}」  —— ${f.reason}`)
  }
  console.log('\n──────── 按键汇总 ────────')
  for (const [key, list] of [...byKey.entries()].sort()) {
    console.log(`\n${key}  (${list.length} 语种)`)
    for (const f of list) console.log(`  ${f.locale.padEnd(6)} 「${f.text}」`)
  }
} else {
  for (const f of findings) {
    console.log(`${f.locale}  ${f.key}  [${f.rule}]  「${f.text}」  —— ${f.reason}`)
  }
  if (findings.length) {
    process.stderr.write(
      `\nFAIL: ${findings.length} 处语言包文案用手势代替动作 / 空状态没点名缺什么。\n`
    )
    process.stderr.write('修法：手势词改成「动词+宾语」；空状态点名缺的是什么（并给出下一步）。\n')
    process.exit(1)
  }
  console.log('✅ 语言包文案说得清「怎么办」—— PASS')
}
if (findings.length) process.exit(1)
