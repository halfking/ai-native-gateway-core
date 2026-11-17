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

import { summarize, EXIT_OK, EXIT_FAIL, EXIT_GAUGE } from './result.mjs'

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

let bad = 0
for (const c of cases) {
  console.log(`  ${c.ok ? '✓' : '✗'} ${c.name}${c.ok ? '' : '  —— ' + c.err}`)
  if (!c.ok) bad++
}
console.log(`\nSELFTEST: ${bad === 0 ? 'PASS' : `FAIL（${bad} / ${cases.length}）`}`)
process.exit(bad === 0 ? 0 : 1)
