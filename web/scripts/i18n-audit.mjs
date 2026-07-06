#!/usr/bin/env node
// i18n-audit.mjs — 2026-07-07 全局 i18n 引用审计 CLI
//
// 用法：
//   node scripts/i18n-audit.mjs                      # 人类可读报告（默认）
//   node scripts/i18n-audit.mjs --json               # 输出 JSON 给 CI / 工具消费
//   node scripts/i18n-audit.mjs --missing-only       # 只打印 missing keys
//   node scripts/i18n-audit.mjs --strict             # 0 missing 才退出 0；否则退出 1
//   node scripts/i18n-audit.mjs --unused             # 额外打印 source locale 中未被引用的 key
//   node scripts/i18n-audit.mjs --src=<dir>          # 自定义源码目录（默认 web/src）
//   node scripts/i18n-audit.mjs --locales=<dir>      # 自定义 locale 目录
//   node scripts/i18n-audit.mjs --source-locale=en-US # 自定义源 locale
//
// 实现：从 web/src/i18n/scanner.ts 复用 audit() 逻辑。
// scanner.ts 是 .ts，但本脚本直接通过 --experimental-strip-types 或
// 预编译调用。为避免引入 tsx/ts-node，我们用一个极简的 shim：直接在脚本里
// 用 dynamic import 调 .ts（Node 22+ 默认支持 strip-types）。

import { spawnSync } from 'node:child_process'
import { existsSync, writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')

function parseArgs(argv) {
  const opts = {
    src: join(ROOT, 'src'),
    locales: join(ROOT, 'src', 'locales'),
    sourceLocale: 'zh-CN',
    json: false,
    missingOnly: false,
    strict: false,
    unused: false,
    help: false,
    outFile: null,
  }
  for (const a of argv) {
    if (a === '--json') opts.json = true
    else if (a === '--missing-only') opts.missingOnly = true
    else if (a === '--strict') opts.strict = true
    else if (a === '--unused') opts.unused = true
    else if (a === '--help' || a === '-h') opts.help = true
    else if (a.startsWith('--src=')) opts.src = resolve(a.slice(6))
    else if (a.startsWith('--locales=')) opts.locales = resolve(a.slice(10))
    else if (a.startsWith('--source-locale=')) opts.sourceLocale = a.slice(16)
    else if (a.startsWith('--out=')) opts.outFile = resolve(a.slice(6))
  }
  return opts
}

function printHelp() {
  console.log(`
i18n-audit — 2026-07-07 全局 i18n 引用审计

USAGE
  node scripts/i18n-audit.mjs [options]

OPTIONS
  --json              输出 JSON 报告（供 CI 消费）
  --missing-only      只打印 missing keys
  --strict            有任何 missing 退出码 1
  --unused            额外打印 source locale 中未引用的 keys
  --src=<dir>         源码目录（默认 web/src）
  --locales=<dir>     locale 目录（默认 web/src/locales）
  --source-locale=xx  源 locale（默认 zh-CN）
  --out=<file>        把报告写到文件
  -h, --help          显示帮助

EXAMPLES
  node scripts/i18n-audit.mjs
  node scripts/i18n-audit.mjs --json --out=reports/i18n-audit.json
  node scripts/i18n-audit.mjs --strict
`)
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  if (opts.help) {
    printHelp()
    process.exit(0)
  }
  if (!existsSync(opts.src)) {
    console.error(`error: --src not found: ${opts.src}`)
    process.exit(2)
  }
  if (!existsSync(opts.locales)) {
    console.error(`error: --locales not found: ${opts.locales}`)
    process.exit(2)
  }

  // Run scanner.ts via Node's --experimental-strip-types (Node 22+).
  // We avoid hard dep on tsx/ts-node; fall back to a hand-rolled .mjs port
  // if the runtime is too old.
  const scannerTs = join(ROOT, 'src', 'i18n', 'scanner.ts')
  const code = `
    import { audit, formatReport } from ${JSON.stringify(pathToFileURL(scannerTs).href)}
    const r = audit(${JSON.stringify(opts.src)}, ${JSON.stringify(opts.locales)}, { sourceLocale: ${JSON.stringify(opts.sourceLocale)} })
    if (process.argv.includes('--json')) {
      process.stdout.write(JSON.stringify(r, null, 2))
    } else if (process.argv.includes('--missing-only')) {
      // 仅 missing
      const lines = []
      const groupByFile = new Map()
      for (const m of r.missing) {
        const f = m.file
        if (!groupByFile.has(f)) groupByFile.set(f, [])
        groupByFile.get(f).push(m)
      }
      for (const [f, refs] of groupByFile) {
        lines.push(f.replace(${JSON.stringify(opts.src + '/')}, ''))
        for (const m of refs) lines.push(\`  \${String(m.line).padStart(4)}:\${String(m.column).padStart(3)}  \${m.key}\`)
      }
      process.stdout.write(lines.join('\\n') + '\\n')
    } else {
      process.stdout.write(formatReport(r, ${JSON.stringify(opts.src)}))
    }
    process.exit(0)
  `
  const tmpFile = join(ROOT, '.i18n-audit-tmp.mjs')
  writeFileSync(tmpFile, code)

  const nodeMajor = parseInt(process.versions.node.split('.')[0], 10)
  const nodeArgs = [tmpFile, ...process.argv.slice(2).filter(a => !a.startsWith('--out='))]
  if (nodeMajor >= 22) {
    nodeArgs.unshift('--experimental-strip-types', '--no-warnings')
  }

  const r = spawnSync(process.execPath, nodeArgs, {
    stdio: 'inherit',
    cwd: ROOT,
  })
  if (r.status !== 0) process.exit(r.status || 1)

  // strict: re-run via JSON to read counts (only if user requested --strict)
  if (opts.strict || opts.outFile) {
    const jsonArgs = [tmpFile, '--json', ...process.argv.slice(2).filter(a => !a.startsWith('--out='))]
    if (nodeMajor >= 22) jsonArgs.unshift('--experimental-strip-types', '--no-warnings')
    const r2 = spawnSync(process.execPath, jsonArgs, { cwd: ROOT })
    if (opts.outFile) {
      const dir = dirname(opts.outFile)
      if (!existsSync(dir)) mkdirSync(dir, { recursive: true })
      writeFileSync(opts.outFile, r2.stdout)
      console.error(`\nwrote report to ${opts.outFile}`)
    }
    if (opts.strict) {
      try {
        const data = JSON.parse(r2.stdout.toString())
        if (data.missing.length > 0) {
          console.error(`\n❌ --strict: ${data.missing.length} missing keys (exit 1)`)
          process.exit(1)
        }
      } catch (e) {
        console.error('error: failed to parse JSON output for --strict check', e)
        process.exit(1)
      }
    }
  }
}

main().catch((e) => {
  console.error('i18n-audit failed:', e)
  process.exit(1)
})
