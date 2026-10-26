#!/usr/bin/env node
// design-ai-smell-guard.mjs — 「AI 化视觉痕迹」门 CLI（2026-10-27，第 17 增量）
//
// 判据实现：src/design/aiSmells.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/copy-clarity-guard.mjs 同一范式）：
//   判据要读 .vue 的 <style> 段与 .css 全文 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// 判据来源：impeccable skill/reference/craft-floor.md（Refuse 段 + codex 段）
//          + 仓内 DESIGN.md §4 排版 / §5 抬升 / §6 组件 / §8 Do Not。
//
//   V1 幽灵卡片    同一表面既声明描边又声明抬升阴影（抬升只声明一次）
//   V2 彩色侧边条  ≥2px 彩色侧边条（border-left/right 与 inset 阴影伪装**两种写法**）
//   V3 字形当图标  emoji / Unicode 字形冒充图标系统
//   V4 字重 ≥800
//   V5 uppercase   中文界面里 uppercase 无意义
//   V6 渐变文字
//   V7 硬偏移阴影  零模糊 + 非零偏移
//   V8 字距过紧    < −0.04em
//
// 用法：
//   node scripts/design-ai-smell-guard.mjs              # 扫描，有违规 exit 1
//   node scripts/design-ai-smell-guard.mjs --json       # 机器可读
//   node scripts/design-ai-smell-guard.mjs --list       # 按文件分组的工作单
//   node scripts/design-ai-smell-guard.mjs --selftest   # 自证：判据本身对不对

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'aiSmells.ts')
const REGISTRY = join(ROOT, 'scripts', 'design-ai-smell-registry.json')

const implUrl = pathToFileURL(IMPL).href
const registryJson = existsSync(REGISTRY) ? readFileSync(REGISTRY, 'utf8') : '[]'

const body = `
import { detectAiSmells, runSelftest } from ${JSON.stringify(implUrl)}
import { readFileSync } from 'node:fs'
const registry = JSON.parse(${JSON.stringify(registryJson)})
if (process.env.SMELL_MODE === 'selftest') {
  const r = runSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const findings = detectAiSmells({
  rootDir: ${JSON.stringify(join(ROOT, 'src'))},
  repoDir: ${JSON.stringify(ROOT)},
  registry,
})
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
    env: { ...process.env, SMELL_MODE: isSelftest ? 'selftest' : 'scan' },
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
  const byRule = new Map()
  for (const f of findings) byRule.set(f.rule, (byRule.get(f.rule) || 0) + 1)
  console.log(`违规 ${findings.length} 处 / ${byFile.size} 文件`)
  console.log('按判据：' + [...byRule.entries()].map(([k, v]) => `${k}=${v}`).join('  '))
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) console.log(`  ${f.line}  [${f.rule}]  ${f.selector}  「${f.text}」  —— ${f.reason}`)
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}  [${f.rule}]  ${f.selector}  「${f.text}」  —— ${f.reason}`)
  }
  if (findings.length) {
    console.error(`\nFAIL: ${findings.length} 处 AI 化视觉痕迹。`)
    console.error('修法：抬升只声明一次（描边 XOR 阴影）；侧边条换成状态点/软填充/徽标；')
    console.error('      字形换成 SVG 图标；字重 ≤700；去掉 uppercase；渐变文字改用字重或字号。')
    process.exit(1)
  }
  console.log('✅ 没有 AI 化视觉痕迹 —— PASS')
}
if (findings.length) process.exit(1)
