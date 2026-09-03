#!/usr/bin/env node
// i18n-cjk-count.mjs — 硬编码中文计数 + baseline 阻断门 CLI（2026-09-04）
//
// 用法：
//   node scripts/i18n-cjk-count.mjs                     # 对比 baseline，超过则 exit 1
//   node scripts/i18n-cjk-count.mjs --json              # 输出 JSON（count + top files）
//   node scripts/i18n-cjk-count.mjs --update-baseline   # 把当前计数写入 baseline（清完一批后手动降棘轮）
//
// 实现：复用 src/i18n/hardcodedCjk.ts 的计数逻辑（vitest 测试同样复用，
// 保证 CLI 与测试口径一致）。通过 Node 22+ 的 --experimental-strip-types
// 执行 .ts（与 i18n-audit.mjs / i18n-fix.mjs 同一套模式）。

import { spawnSync } from 'node:child_process'
import { writeFileSync, unlinkSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')

function parseArgs(argv) {
  const opts = { json: false, updateBaseline: false }
  for (const a of argv) {
    if (a === '--json') opts.json = true
    else if (a === '--update-baseline') opts.updateBaseline = true
  }
  return opts
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  const mod = pathToFileURL(join(ROOT, 'src', 'i18n', 'hardcodedCjk.ts')).href
  const code = `
    import { countAll, readBaseline, writeBaseline } from ${JSON.stringify(mod)}
    const { count, files } = countAll()
    if (process.argv.includes('--update-baseline')) {
      const b = writeBaseline(count)
      process.stdout.write(JSON.stringify({ updated: b.count }))
    } else if (process.argv.includes('--json')) {
      const baseline = readBaseline()
      process.stdout.write(JSON.stringify({ count, baseline: baseline ? baseline.count : null, top: files.slice(0, 20) }, null, 2))
    } else {
      const baseline = readBaseline()
      const lines = ['hardcoded CJK count: ' + count]
      if (!baseline) {
        process.stderr.write('error: baseline file scripts/i18n-cjk-baseline.json missing or invalid\\n')
        process.stderr.write('run: node scripts/i18n-cjk-count.mjs --update-baseline\\n')
        process.exit(1)
      }
      lines.push('baseline:            ' + baseline.count + ' (' + baseline.updated + ')')
      if (count > baseline.count) {
        process.stdout.write(lines.join('\\n') + '\\n')
        process.stderr.write('\\n❌ hardcoded CJK count INCREASED by ' + (count - baseline.count) + ' (ratchet violated)\\n')
        process.stderr.write('   Fix the new hardcoded Chinese (use t(\\'key\\') + locale files),\\n')
        process.stderr.write('   or lower it before ratcheting: node scripts/i18n-cjk-count.mjs --update-baseline\\n')
        process.exit(1)
      }
      if (count < baseline.count) {
        lines.push('✅ count decreased by ' + (baseline.count - count) + ' — ratchet down with:')
        lines.push('   node scripts/i18n-cjk-count.mjs --update-baseline')
      } else {
        lines.push('✅ within baseline')
      }
      process.stdout.write(lines.join('\\n') + '\\n')
    }
  `
  const tmpFile = join(ROOT, '.i18n-cjk-count-tmp.mjs')
  writeFileSync(tmpFile, code)

  const nodeMajor = parseInt(process.versions.node.split('.')[0], 10)
  const nodeArgs = [tmpFile, ...process.argv.slice(2)]
  if (nodeMajor >= 22) {
    nodeArgs.unshift('--experimental-strip-types', '--no-warnings')
  }
  const r = spawnSync(process.execPath, nodeArgs, { stdio: 'inherit', cwd: ROOT })
  try { unlinkSync(tmpFile) } catch { /* best effort */ }
  if (r.status !== 0) process.exit(r.status || 1)
}

function dirname(p) {
  return p.split('/').slice(0, -1).join('/')
}

main().catch((e) => {
  console.error('i18n-cjk-count failed:', e)
  process.exit(1)
})
