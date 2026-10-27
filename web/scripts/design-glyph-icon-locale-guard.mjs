#!/usr/bin/env node
// design-glyph-icon-locale-guard.mjs — 「语言包侧 emoji 冒充图标」门 CLI（2026-10-28，第 29 增量）
//
// 判据实现：src/i18n/glyphIconsLocale.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**：按仓内既有模式（copy-clarity-locale-guard.mjs）
//   用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★ 这道门补的是**覆盖面盲区**（★ 第 27 增量留下的）：
//   源码侧的 `design-ai-smell` 门（V3-glyph-as-icon）只扫**界面层源码**里的
//   `<button>` / `<a>` ⇒ 而 **8 个语言包的值**里的 emoji 从来没有任何门看过
//   ⇒ 实测 8 语种各 65~66 行、合计 **526 行**。
//
// ★ 判据依据：DESIGN.md §6「不许用 emoji / Unicode 字形冒充图标系统」
//   （与 impeccable craft-floor 的 ban-glyph-icons 同源）。
//
// 用法：
//   node scripts/design-glyph-icon-locale-guard.mjs              # 扫描，有违规 exit 1
//   node scripts/design-glyph-icon-locale-guard.mjs --json       # 机器可读
//   node scripts/design-glyph-icon-locale-guard.mjs --list       # 按语种分组的工作单
//   node scripts/design-glyph-icon-locale-guard.mjs --selftest   # 自证：违规→红 / 干净→绿

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'i18n', 'glyphIconsLocale.ts')
const implUrl = pathToFileURL(IMPL).href

const body = `
import { detectLocaleGlyphIcons, runLocaleGlyphSelftest } from ${JSON.stringify(implUrl)}
if (process.env.GLYPH_LOCALE_MODE === 'selftest') {
  const r = runLocaleGlyphSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const findings = detectLocaleGlyphIcons()
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
    env: { ...process.env, GLYPH_LOCALE_MODE: isSelftest ? 'selftest' : 'scan' },
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
    for (const f of list) console.log(`  ${f.key}  [${f.rule}]  「${f.text}」  —— ${f.glyphs.join(' ')}`)
  }
  console.log('\n──────── 按键汇总 ────────')
  for (const [key, list] of [...byKey.entries()].sort()) {
    console.log(`\n${key}  (${list.length} 语种)`)
    for (const f of list) console.log(`  ${f.locale.padEnd(6)} 「${f.text}」`)
  }
} else {
  for (const f of findings) {
    console.log(`${f.locale}  ${f.key}  [${f.rule}]  「${f.text}」  —— ${f.glyphs.join(' ')}`)
  }
  if (findings.length) {
    process.stderr.write(
      `\nFAIL: ${findings.length} 处语言包文案用 emoji / Unicode 字形冒充图标。\n`
    )
    process.stderr.write('修法：去掉 emoji（文字本身已表达语义），状态改由「软填充 + 文字 + 文字颜色」表达。\n')
    // ★ 用 exitCode 而不是 exit(1) ⇒ Node 会自然退出 ⇒ **stdout 刷完再走**
    //   （★ 第一版用 process.exit(1) ⇒ 输出被截断 ⇒ 消费方 JSON.parse 直接炸
    //    ⇒ ★ 「门自己的输出也属于门的契约」）
    process.exitCode = 1
  }
  console.log('✅ 语言包文案里没有 emoji 冒充图标 —— PASS')
}
if (findings.length) process.exitCode = 1
