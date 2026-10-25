#!/usr/bin/env node
// i18n-locale-value-count.mjs — 「键在、值没翻」质量门 CLI（2026-10-21）
//
// 用法：
//   node scripts/i18n-locale-value-count.mjs                  # 对比 baseline，超过则 exit 1
//   node scripts/i18n-locale-value-count.mjs --json           # 输出 JSON（各语种实测 + 阈值）
//   node scripts/i18n-locale-value-count.mjs --update-baseline  # 把当前值写入 baseline（**只许降不许升**）
//
// 实现：复用 src/i18n/localeValueQuality.ts 的计数逻辑（测试与 CLI 同源），
// 与 i18n-cjk-count.mjs 同一套模式（Node 22+ --experimental-strip-types 跑 .ts）。
//
// ⚠️ **CLI 是写 baseline 的那个出口**：一个数错目标的量具正是从这儿把错的数字
//   固化进 baseline 的。所以：
//   ① CLI 侧也要跑**口径自证**（assertSelfCheckCounting），自证不过 ⇒ 拒绝报告/写基线；
//   ② --update-baseline **只许降不许升**（writeBaseline 会抛错）——
//      「把基线调高」必须人工显式改 JSON 并在 commit 消息里写明理由。

import { writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')

function dirname(p) {
  return p.slice(0, p.lastIndexOf('/'))
}

async function main() {
  const argv = process.argv.slice(2)
  const asJson = argv.includes('--json')
  const updateBaseline = argv.includes('--update-baseline')
  const mod = pathToFileURL(join(ROOT, 'src', 'i18n', 'localeValueQuality.ts')).href
  const code = `
    import { measureAll, buildBaseline, readBaseline, writeBaseline, assertSelfCheckCounting } from ${JSON.stringify(mod)}
    // ⚠️ 口径自证在 CLI 侧也要跑：CLI 是写 baseline 的出口。
    const failedSelfCheck = assertSelfCheckCounting().filter((c) => !c.ok)
    if (failedSelfCheck.length) {
      process.stderr.write('FATAL: locale value counter self-check failed — refusing to report or write a baseline:\\n')
      for (const c of failedSelfCheck) {
        process.stderr.write('  ' + c.id + ' ' + c.desc + ' — expected=' + c.expected + ' actual=' + c.actual + '\\n')
      }
      process.exit(2)
    }
    const { byLocale, byLocaleAllKeys, referencedCount } = measureAll()
    const baseline = readBaseline()
    const draft = buildBaseline(byLocale, byLocaleAllKeys)
    const out = {
      referencedCount,
      baseline,
      current: draft,
      byLocale: Object.fromEntries([...byLocale].map(([k, v]) => [k, v])),
      byLocaleAllKeys: Object.fromEntries([...byLocaleAllKeys].map(([k, v]) => [k, v])),
    }
    if (process.argv.includes('--update-baseline')) {
      try {
        const b = writeBaseline(draft)
        process.stdout.write(JSON.stringify({ updated: b }, null, 2) + '\\n')
      } catch (e) {
        process.stderr.write('FATAL: ' + e.message + '\\n')
        process.exit(1)
      }
    } else if (process.argv.includes('--json')) {
      process.stdout.write(JSON.stringify(out, null, 2) + '\\n')
    } else {
      if (!baseline) {
        process.stderr.write('error: baseline file src/i18n/localeValueBaseline.json missing or invalid\\n')
        process.stderr.write('run: node scripts/i18n-locale-value-count.mjs --update-baseline\\n')
        process.exit(1)
      }
      const lines = [
        'referenced i18n keys: ' + referencedCount,
        'en-US values containing CJK (limit 0): ' + draft.enUSCjk,
      ]
      let bad = 0
      for (const loc of Object.keys(draft.untranslated)) {
        const limit = baseline.untranslated?.[loc]
        const cur = draft.untranslated[loc]
        const mark = typeof limit === 'number' && cur > limit ? '  OVER LIMIT' : ''
        if (mark) bad++
        lines.push('untranslated ' + loc.padEnd(6) + ': ' + cur + ' (limit ' + limit + ')' + mark)
      }
      for (const loc of Object.keys(draft.cjkInNonCjkLocale)) {
        const limit = baseline.cjkInNonCjkLocale?.[loc]
        const cur = draft.cjkInNonCjkLocale[loc]
        const mark = typeof limit === 'number' && cur > limit ? '  OVER LIMIT' : ''
        if (mark) bad++
        lines.push('cjk-in-value ' + loc.padEnd(6) + ': ' + cur + ' (limit ' + limit + ')' + mark)
      }
      // ★ sameAsSource：值仍等于源语言 zh-CN（源值含汉字）的条数 ——【全量键】口径。
      //   口径保守（含繁简同形 / 通用汉字）⇒ 是指示器；棘轮方向仍成立。
      for (const loc of Object.keys(draft.sameAsSource ?? {})) {
        const limit = baseline.sameAsSource?.[loc]
        const cur = draft.sameAsSource[loc]
        const mark = typeof limit === 'number' && cur > limit ? '  OVER LIMIT' : ''
        if (mark) bad++
        lines.push('same-as-source ' + loc.padEnd(6) + ': ' + cur + ' (limit ' + limit + ')' + mark)
      }
      // ★ cjkInNonCjkLocaleAllKeys：非中日语种里含汉字的值数 ——【全量键】口径。
      //   这条治「引用集口径对动态键失效」那个盲区（nav.item.* 等运行时拼键）。
      for (const loc of Object.keys(draft.cjkInNonCjkLocaleAllKeys ?? {})) {
        const limit = baseline.cjkInNonCjkLocaleAllKeys?.[loc]
        const cur = draft.cjkInNonCjkLocaleAllKeys[loc]
        const mark = typeof limit === 'number' && cur > limit ? '  OVER LIMIT' : ''
        if (mark) bad++
        lines.push('cjk-all-keys ' + loc.padEnd(6) + ': ' + cur + ' (limit ' + limit + ')' + mark)
      }
      process.stdout.write(lines.join('\\n') + '\\n')
      if (bad > 0) {
        process.stderr.write('\\nFAIL: ' + bad + ' metric(s) over the ratchet baseline\\n')
        process.exit(1)
      }
      process.stdout.write('\\nPASS: all metrics within the ratchet baseline\\n')
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
