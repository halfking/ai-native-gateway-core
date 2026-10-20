#!/usr/bin/env node
// i18n-key-parity-count.mjs — 「跨语种键存在性平价」质量门 CLI（2026-10-21，R56 轮）
//
// 用法：
//   node scripts/i18n-key-parity-count.mjs                    # 对比 baseline，超过则 exit 1
//   node scripts/i18n-key-parity-count.mjs --json             # 输出 JSON（实测 + 阈值）
//   node scripts/i18n-key-parity-count.mjs --update-baseline  # 写 baseline（**只许降不许升**）
//
// 实现：复用 src/i18n/localeKeyParity.ts（测试与 CLI 同源），
// 与 i18n-locale-value-count.mjs 同一套模式（Node 22+ --experimental-strip-types 跑 .ts）。
//
// ⚠️ **CLI 是写 baseline 的那个出口**：一个数错目标的量具正是从这儿把错的数字
//   固化进 baseline 的。所以：
//   ① CLI 侧也要跑**口径自证**（assertSelfCheckKeyParity），自证不过 ⇒ 拒绝报告/写基线；
//   ② --update-baseline **只许降不许升**（writeKeyParityBaseline 会抛错）——
//      「把基线调高」必须人工显式改 JSON 并在 commit 消息里写明理由。

import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(fileURLToPath(new URL('.', import.meta.url)).slice(0, -1), '..')

function dirname(p) {
  return p.slice(0, p.lastIndexOf('/'))
}

async function main() {
  const argv = process.argv.slice(2)
  const mod = pathToFileURL(join(ROOT, 'src', 'i18n', 'localeKeyParity.ts')).href
  const code = `
    import { measureRepoKeyParity, buildKeyParityBaseline, readKeyParityBaseline, writeKeyParityBaseline, assertSelfCheckKeyParity } from ${JSON.stringify(mod)}
    // ⚠️ 口径自证在 CLI 侧也要跑：CLI 是写 baseline 的出口。
    const failedSelfCheck = assertSelfCheckKeyParity().filter((c) => !c.ok)
    if (failedSelfCheck.length) {
      process.stderr.write('FATAL: locale key parity self-check failed — refusing to report or write a baseline:\\n')
      for (const c of failedSelfCheck) {
        process.stderr.write('  ' + c.id + ' ' + c.desc + ' — expected=' + c.expected + ' actual=' + c.actual + '\\n')
      }
      process.exit(2)
    }
    const report = measureRepoKeyParity()
    const baseline = readKeyParityBaseline()
    const draft = buildKeyParityBaseline(report)
    const out = {
      unionSize: report.unionSize,
      sources: report.sources,
      hardGateViolations: report.missingInLocale,
      dynamicCallSites: report.dynamicCallSites,
      baseline,
      current: draft,
    }
    if (process.argv.includes('--update-baseline')) {
      try {
        const b = writeKeyParityBaseline(draft)
        process.stdout.write(JSON.stringify({ updated: b }, null, 2) + '\\n')
      } catch (e) {
        process.stderr.write('FATAL: ' + e.message + '\\n')
        process.exit(1)
      }
    } else if (process.argv.includes('--json')) {
      process.stdout.write(JSON.stringify(out, null, 2) + '\\n')
    } else {
      if (!baseline) {
        process.stderr.write('error: baseline file src/i18n/localeKeyParityBaseline.json missing or invalid\\n')
        process.stderr.write('run: node scripts/i18n-key-parity-count.mjs --update-baseline\\n')
        process.exit(1)
      }
      const lines = []
      lines.push('union keys: ' + report.unionSize)
      lines.push('reachable keys: ' + report.sources.total + ' (literal ' + report.sources.literal + ' + template ' + report.sources.template + ')')
      lines.push('hard gate violations (must be 0): ' + report.missingInLocale.length)
      for (const v of report.missingInLocale.slice(0, 20)) {
        lines.push('  ' + v.key + ' missing in ' + v.missingIn.join(','))
      }
      lines.push('dynamic call sites (not statically resolvable): ' + report.dynamicCallSites)
      let bad = report.missingInLocale.length
      for (const loc of Object.keys(draft.missingVsUnion)) {
        const limit = baseline.missingVsUnion?.[loc]
        const cur = draft.missingVsUnion[loc]
        const mark = typeof limit === 'number' && cur > limit ? '  OVER LIMIT' : ''
        if (mark) bad++
        lines.push('missing-vs-union ' + loc.padEnd(6) + ': ' + cur + ' (limit ' + limit + ')' + mark)
      }
      {
        const limit = baseline.asymmetricKeys
        const cur = draft.asymmetricKeys
        const mark = typeof limit === 'number' && cur > limit ? '  OVER LIMIT' : ''
        if (mark) bad++
        lines.push('asymmetric keys'.padEnd(25) + ': ' + cur + ' (limit ' + limit + ')' + mark)
      }
      process.stdout.write(lines.join('\\n') + '\\n')
      if (bad > 0) {
        process.stderr.write('\\nFAIL: ' + bad + ' metric(s) over the ratchet baseline\\n')
        process.exit(1)
      }
      process.stdout.write('\\nPASS: hard gate clean and all metrics within the ratchet baseline\\n')
    }
  `
  const { spawnSync } = await import('node:child_process')
  // ⚠️ 必须把 argv **转发**给子进程：`node -e` 的子进程看不到父进程的 argv。
  //   且转发要用 `--` 分隔 —— 否则 `--json` 会被 node 当成自己的选项（bad option）。
  const res = spawnSync(
    process.execPath,
    ['--experimental-strip-types', '--no-warnings', '-e', code, '--', ...argv],
    { stdio: 'inherit', cwd: ROOT },
  )
  process.exit(res.status ?? 1)
}

main()
