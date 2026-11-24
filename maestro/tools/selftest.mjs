// maestro/tools/selftest.mjs —— 断言**判定口径**的自检（不需要浏览器、不需要线上）。
//
// ★ 为什么要有它：判定口径曾经被写过两遍（设计层一套、行为层一套），
//   且两处都先后犯过「只数第一层 ⇒ 第二层的红被吃掉、照样报 PASS」。
//   ⇒ 把口径抽到 result.mjs 后，用本脚本把**三种结局**钉死：
//     绿 → 退出码 0 + 「RESULT: PASS（N 条）」
//     红 → 退出码 1 + 「RESULT: FAIL（k / N 条）」   ← 含「红在第二层」这一历史事故
//     量具失效 → 退出码 2 + 「RESULT: 量具失效（…这不是产品缺陷）」
//
// 用法：node maestro/tools/selftest.mjs
// 退出码：0 = 自检全绿；1 = 自检失败。

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { summarize, mergeExitCodes, EXIT_OK, EXIT_FAIL, EXIT_GAUGE } from './result.mjs'

const cases = []
const t = (name, fn) => {
  try {
    fn()
    cases.push({ name, ok: true })
  } catch (e) {
    cases.push({ name, ok: false, err: e.message })
  }
}
const eq = (a, b, what) => {
  if (a !== b) throw new Error(`${what || ''} 期望 ${JSON.stringify(b)}，实际 ${JSON.stringify(a)}`)
}

t('全绿 ⇒ 退出码 0 且打印 PASS（N 条）', () => {
  const r = summarize([
    { name: 'a', pass: true },
    { name: 'b', pass: true },
  ])
  eq(r.exitCode, EXIT_OK, '退出码')
  eq(r.failed, 0, '失败数')
  eq(r.total, 2, '总数')
  eq(r.line, 'RESULT: PASS（2 条）', '结论行')
})

t('★ 红在第二层 ⇒ 退出码 1 且被计入（历史事故：只数第一层会漏）', () => {
  const r = summarize([
    { name: '第一层1', pass: true },
    { name: '第一层2', pass: true },
    { name: '第二层1', pass: false },
  ])
  eq(r.exitCode, EXIT_FAIL, '退出码')
  eq(r.failed, 1, '失败数（第二层的红必须被计入）')
  eq(r.total, 3, '总数')
  eq(r.line, 'RESULT: FAIL（1 / 3 条）', '结论行')
})

t('红在第一层 ⇒ 同样计入', () => {
  const r = summarize([
    { name: '第一层1', pass: false },
    { name: '第二层1', pass: true },
  ])
  eq(r.exitCode, EXIT_FAIL, '退出码')
  eq(r.failed, 1, '失败数')
})

t('★ 量具失效 ⇒ 退出码 2，且「未评估」不计入失败', () => {
  const r = summarize(
    [
      { name: '可评估的', pass: true },
      { name: '依赖真实请求的', pass: false }, // 未评估 ⇒ 实际并未被评估为失败
      { name: '依赖真实请求的2', pass: false },
    ],
    { skipped: ['依赖真实请求的', '依赖真实请求的2'] },
  )
  eq(r.exitCode, EXIT_GAUGE, '退出码')
  eq(r.failed, 0, '失败数（未评估的不算失败）')
  eq(r.skipped, 2, '未评估数')
  if (!r.line.includes('这不是产品缺陷')) throw new Error('结论行必须写明「这不是产品缺陷」')
  if (!r.line.includes('量具失效')) throw new Error('结论行必须写明「量具失效」')
})

t('量具失效优先于「红」（量具没测到 ⇒ 不知道产品好不好）', () => {
  const r = summarize([{ name: 'x', pass: false }], { skipped: ['y'] })
  eq(r.exitCode, EXIT_GAUGE, '退出码')
})

t('空数组 ⇒ 全绿', () => {
  const r = summarize([])
  eq(r.exitCode, EXIT_OK, '退出码')
  eq(r.total, 0, '总数')
})

// ── 跨工具合并（mergeExitCodes）──────────────────────────────────────────
// ★ 为什么单独钉：runner 若自己写 if 链，就是**第三份判定口径** ⇒ 必然漂移。
//   2026-11-25 审计轮实测：某 runner 的 if 链按顺序先判「探针量具失效」，
//   于是「探针量具失效 + 补充层红」被报成量具失效 ⇒ **红被吃掉**
//   —— 这与本文件头注记录的历史事故（第二层的红被统计口径吃掉）是同一类。
t('★ 跨工具合并：量具失效 **不吃掉** 另一个工具的红（红优先）', () => {
  eq(mergeExitCodes([EXIT_GAUGE, EXIT_FAIL]), EXIT_FAIL, '退出码')
  eq(mergeExitCodes([EXIT_FAIL, EXIT_GAUGE]), EXIT_FAIL, '退出码（顺序无关）')
})

t('跨工具合并：没有红但有量具失效 ⇒ 量具失效', () => {
  eq(mergeExitCodes([EXIT_OK, EXIT_GAUGE]), EXIT_GAUGE, '退出码')
  eq(mergeExitCodes([EXIT_GAUGE, EXIT_OK]), EXIT_GAUGE, '退出码（顺序无关）')
})

t('跨工具合并：全绿 ⇒ 绿', () => {
  eq(mergeExitCodes([EXIT_OK, EXIT_OK]), EXIT_OK, '退出码')
  eq(mergeExitCodes([]), EXIT_OK, '空数组')
})

t('★ 跨工具合并 与 工具内 summarize 的优先级**故意不同**（不是漂移）', () => {
  // 工具内：量具失效优先（这一轮压根没测到 ⇒ 红可能是假象 ⇒ 报量具失效更诚实）
  eq(summarize([{ name: 'x', pass: false }], { skipped: ['y'] }).exitCode, EXIT_GAUGE, '工具内')
  // 跨工具：红优先（另一层的量具失效不该掩盖本层已确证的缺陷）
  eq(mergeExitCodes([EXIT_GAUGE, EXIT_FAIL]), EXIT_FAIL, '跨工具')
})

// ── 防回归：runner 的判定必须走唯一真源 ───────────────────────────────────
// ★ 这条钉的是**「不要把判定口径又写一遍」**这件事本身：
//   语义钉测只能证明 mergeExitCodes 对 ⇒ 证明不了 runner 真的在用它
//   ⇒ 所以再钉一条静态检查：runner 必须调 mergeExitCodes，
//     且**不许**再出现自己判 rc 的 if 链（那正是审计轮实测漂移的形态）。
// ★ 范围只钉本轮改过的 runner（不擅自扩面到其它 runner —— 它们的判定是各自的老写法，
//   要统一需单独一轮，且要先确认那些 flow 的失败语义与本链一致）。
t('★ 防回归：run-stats-range-picker.sh 的判定必须调 mergeExitCodes（不自己写 if 链）', () => {
  const src = readFileSync(join(import.meta.dirname, '..', 'run-stats-range-picker.sh'), 'utf8')
  if (!src.includes('mergeExitCodes')) {
    throw new Error('runner 没调 result.mjs 的 mergeExitCodes ⇒ 判定口径又变成第三份')
  }
  if (/\$\{[A-Z_]+_RC\}"\s*-eq\s*[012]/.test(src)) {
    throw new Error('runner 里出现了自己判 rc 的 if 链 ⇒ 判定口径漂移（应一律交给 mergeExitCodes）')
  }
})

let bad = 0
for (const c of cases) {
  console.log(`  ${c.ok ? '✓' : '✗'} ${c.name}${c.ok ? '' : '  —— ' + c.err}`)
  if (!c.ok) bad++
}
console.log(`\nSELFTEST: ${bad === 0 ? 'PASS' : `FAIL（${bad} / ${cases.length}）`}`)
process.exit(bad === 0 ? 0 : 1)
