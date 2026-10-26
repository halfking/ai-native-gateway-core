#!/usr/bin/env node
// copy-clarity-guard.mjs — 「清晰文案」门 CLI（2026-10-27，第 11 增量）
//
// 判据实现：src/i18n/copyClarity.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**：判据需要 Vue 感知的注释剥离（`maskSource`，TypeScript），
//   所以按仓内既有模式（i18n-cjk-count.mjs / i18n-audit.mjs）用
//   spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// 三条判据（来自 impeccable skill/reference/clarify.md）：
//   R1 错误三问 —— 失败类文案必须给出恢复动作（缺「怎么办」= 没说完）
//   R2 别用手势 —— 手势词必须点名具体目标（「点这里」是手，不是动作）
//   R3 通用空状态 —— 整句等于通用空状态短语（没点名「缺的是什么」）
//
// 用法：
//   node scripts/copy-clarity-guard.mjs              # 扫描，有违规 exit 1
//   node scripts/copy-clarity-guard.mjs --json       # 机器可读
//   node scripts/copy-clarity-guard.mjs --list       # 按文件分组的工作单
//   node scripts/copy-clarity-guard.mjs --selftest   # 自证：违规→红 / 干净→绿

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'i18n', 'copyClarity.ts')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { detectClarity, runSelftest } from ${JSON.stringify(implUrl)}
if (process.env.CLARITY_MODE === 'selftest') {
  const r = runSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const findings = detectClarity()
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
    env: { ...process.env, CLARITY_MODE: isSelftest ? 'selftest' : 'scan' },
  },
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
  const byFile = new Map()
  for (const f of findings) {
    if (!byFile.has(f.file)) byFile.set(f.file, [])
    byFile.get(f.file).push(f)
  }
  console.log(`违规 ${findings.length} 处 / ${byFile.size} 文件`)
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) console.log(`  ${f.line}:${f.col}  [${f.rule}]  「${f.text}」  —— ${f.reason}`)
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}:${f.col}  [${f.rule}]  「${f.text}」  —— ${f.reason}`)
  }
  if (findings.length) {
    console.error(`\nFAIL: ${findings.length} 处文案说不清「怎么办」/ 用手势代替动作 / 空状态没点名缺什么。`)
    console.error('修法：错误文案补上恢复动作并改走语言包；手势词改成「动词+宾语」；空状态点名缺的是什么。')
    process.exit(1)
  }
  console.log('✅ 文案说得清「怎么办」—— PASS')
}
if (findings.length) process.exit(1)
