#!/usr/bin/env node
// run-vitest-stability.mjs — 稳定性连跑器。
//
// ★ 为什么需要它（2026-10-06 实测教训）：
//   手工 `for i in $(seq 1 10); do npx vitest run | grep '^ +Tests +'; done`
//   只保留汇总行。一旦某次出现 `2 failed | 1003 passed`，**失败用例名
//   就永远丢了** —— 当时那轮循环没有把完整输出落盘，之后再跑 40 次也没复现，
//   导致这次 flaky 到提交时仍然**无法定位**。
//   ⇒ 门禁脚本必须**默认落盘完整输出**，而不是出问题时才想起来加。
//
// 用法：
//   node scripts/run-vitest-stability.mjs                 # 连跑 10 次
//   node scripts/run-vitest-stability.mjs --runs=30       # 连跑 30 次
//   node scripts/run-vitest-stability.mjs --out=/tmp/x    # 指定快照目录
//   node scripts/run-vitest-stability.mjs --run=single src/views/Foo.spec.ts
//
// 行为：
//   · 每次都打印 `Tests` 汇总行；
//   · 只要某次出现 failed，就把**完整输出**写进快照目录，并打印失败用例名
//     与失败所在文件；
//   · 末尾给出总次数 / 失败次数 / 快照清单；
//   · 有失败则 exit 1（可直接串进 CI）。
//
// ★ 故意不做的事：不「重跑到绿为止」。重跑会把第一次的失败**掩盖**掉，
//   而这里要的是**第一次**的结果。

import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const args = process.argv.slice(2)

function opt(name, dflt) {
  const hit = args.find((a) => a.startsWith(`--${name}=`))
  return hit ? hit.slice(name.length + 3) : dflt
}

const runs = Number(opt('runs', '10'))
const outDir = resolve(opt('out', 'test-stability-snapshots'))
const only = opt('run', null)

if (!Number.isInteger(runs) || runs < 1) {
  console.error(`--runs 必须是 ≥1 的整数，收到：${opt('runs', '10')}`)
  process.exit(2)
}

const vitestArgs = ['vitest', 'run']
if (only) vitestArgs.push(only)

mkdirSync(outDir, { recursive: true })

let failedRuns = 0
const snapshots = []

for (let i = 1; i <= runs; i++) {
  let out = ''
  let threw = false
  try {
    out = execFileSync('npx', vitestArgs, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
  } catch (e) {
    // vitest 用非零退出码表达「有用例失败」——这**不是**脚本错误，要接住输出。
    threw = true
    out = (e.stdout ?? '') + (e.stderr ?? '')
  }

  const summary = out.split('\n').find((l) => /^\s*Tests\s+/.test(l))?.trim() ?? '(no Tests line)'
  const hasFailure = /\bfailed\b/.test(summary)

  if (!hasFailure && !threw) {
    console.log(`run#${i} ${summary}`)
    continue
  }

  // 有失败，或 vitest 异常退出（连 Tests 行都没出来）⇒ 一律落盘。
  failedRuns++
  const file = resolve(outDir, `run-${String(i).padStart(2, '0')}.txt`)
  writeFileSync(file, out, 'utf8')
  snapshots.push(file)
  console.log(`run#${i} ${summary}  ← 快照: ${file}`)

  // 打印失败用例名与所在文件：这是手工循环丢掉的那部分证据。
  for (const line of out.split('\n')) {
    if (/^\s+×/.test(line) || /^ FAIL /.test(line)) console.log(`    ${line.trim()}`)
  }
}

console.log('─'.repeat(60))
console.log(`总次数 ${runs} · 失败次数 ${failedRuns} · 快照 ${snapshots.length} 份`)
if (snapshots.length) {
  console.log('快照清单：')
  for (const f of snapshots) console.log(`  ${f}`)
  console.log('★ 用「快照里的 Tests 行 + × 行 + FAIL 行」定位，不要只看汇总。')
} else {
  console.log(`（${runs} 次全绿。若此前记录过 flaky，说明本次未复现 —— 这不是「已修复」，` +
    '只是没抓到；连跑器存在的意义就是下次抓到时留下证据。）')
}

process.exit(failedRuns > 0 ? 1 : 0)