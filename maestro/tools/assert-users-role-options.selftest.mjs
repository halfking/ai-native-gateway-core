#!/usr/bin/env node
// maestro/tools/assert-users-role-options.selftest.mjs —— 角色下拉探针的**判据自检**
//
// ★ 为什么要有它（2026-11-26 补真机覆盖轮）：
//   本轮写这个探针时，**自证开关自己坏过一次**：参数解析把无值 flag（`--negate`）
//   落成 undefined ⇒ `NEGATE` 恒为 false ⇒ 跑 `--negate` 出来还是 **PASS（11 条）**
//   ⇒ 判据「看起来能自证、其实没反」⇒ ★ 这类「量具自身的静默失效」比没量具更坏
//   （它让人以为绿是真的）。
//   ⇒ 这里把「自证开关必须真的生效」用**静态钉**钉死，并配**变异反向臂**
//     （把钉所依赖的片段改坏 ⇒ 检查必须红）⇒ 避免「钉本身恒绿」。
//
// ★ 为什么是静态钉而不是 import 纯逻辑：
//   探针是**顶层 await 的脚本**（一 import 就会真跑、连生产）⇒ 不能像
//   observe-error-playbook.selftest.mjs 那样 import 它的纯逻辑。
//   ⇒ 改为读源码文本做静态钉（成本低、无网络、无生产副作用）。
//
// 用法：node maestro/tools/assert-users-role-options.selftest.mjs
// 退出码：0 = 自检全绿；1 = 自检失败。

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const TARGET = resolve(here, 'assert-users-role-options.mjs')

const src = readFileSync(TARGET, 'utf8')

/**
 * 对探针源码跑一组静态钉。
 * @returns {string[]} 失败的钉（空数组 = 全绿）
 */
function auditSource(s) {
  const failed = []
  const need = (name, cond, detail) => {
    if (!cond) failed.push(`${name}${detail ? ' —— ' + detail : ''}`)
  }

  // 钉 1：无值 flag 必须落成 true（★ 本轮真实踩过的坑）
  need(
    '无值 flag 解析落成 true',
    /next !== undefined && !next\.startsWith\('--'\)\s*\?\s*next\s*:\s*true/.test(s),
    '参数解析里找不到「无值 flag ⇒ true」的分支（⇒ --negate 会静默失效）',
  )
  // 钉 2：NEGATE 必须认「flag 本身就是 true」这一形态
  need(
    'NEGATE 认 args.negate === true',
    /args\.negate === true/.test(s),
    'NEGATE 只认字符串 "true" ⇒ 传 --negate（无值）时不生效',
  )
  // 钉 3：至少 4 处期望取反（★ A2/A3/B2/B4 四个对照点都要能反）
  const negateUses = (s.match(/NEGATE\s*\?/g) || []).length
  need('取反点 ≥ 4 处', negateUses >= 4, `只找到 ${negateUses} 处`)

  // 钉 4：判定口径必须走 result.mjs（仓内唯一真源），不得自写退出码
  need(
    '判定走 result.mjs 的 summarize',
    /import\s*\{[^}]*summarize[^}]*\}\s*from\s*'\.\/result\.mjs'/.test(s),
    '探针没有 import result.mjs 的 summarize ⇒ 判定口径会与仓内其它工具漂移',
  )
  need('退出码来自 summarize 的 exitCode', /exitCode/.test(s), '退出码不是 summarize().exitCode')

  // 钉 5：文件头注释必须登记 --negate 的用途（文档一致性）
  need('文件头登记了 --negate 用法', /--negate/.test(s), '文件头没登记 --negate')

  return failed
}

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

// ── 正向臂：真源码必须全绿 ───────────────────────────────────────────────
t('真源码通过全部静态钉', () => {
  const failed = auditSource(src)
  eq(failed.length, 0, `失败的钉：${failed.join(' | ')}`)
})

// ── 反向臂 1：把「无值 flag ⇒ true」的解析改坏 ⇒ 必须红 ──────────────────
t('变异臂1：破坏无值 flag 解析后静态钉转红', () => {
  const mutated = src.replace(
    /next !== undefined && !next\.startsWith\('--'\)\s*\?\s*next\s*:\s*true/,
    'next',
  )
  const failed = auditSource(mutated)
  if (failed.length === 0) throw new Error('把解析改坏后静态钉仍全绿 ⇒ 钉是恒绿的')
})

// ── 反向臂 2：把取反点全删掉 ⇒ 必须红 ───────────────────────────────────
t('变异臂2：删掉全部取反点后静态钉转红', () => {
  const mutated = src.replace(/NEGATE\s*\?/g, 'false ?')
  const failed = auditSource(mutated)
  if (failed.length === 0) throw new Error('删掉取反后静态钉仍全绿 ⇒ 钉是恒绿的')
})

// ── 反向臂 3：把判定口径从 result.mjs 摘掉 ⇒ 必须红 ─────────────────────
t('变异臂3：绕开 result.mjs 后静态钉转红', () => {
  // ★ 变异必须真的改语义：只加注释是**改不坏**的（正则照样匹配）⇒ 那是「变异没落地」
  //   ⇒ 判据红的原因就会是「变异预期挑错」，不是判据本身 ⇒ 这里改成换源模块。
  const mutated = src.replace(/from\s*'\.\/result\.mjs'/, "from './my-own-result.mjs'")
  if (mutated === src) throw new Error('变异没落地（源码里找不到 result.mjs 的 import）')
  const failed = auditSource(mutated)
  if (failed.length === 0) throw new Error('绕开 result.mjs 后静态钉仍全绿 ⇒ 钉是恒绿的')
})

let failed = 0
for (const c of cases) {
  if (c.ok) console.log(`✅ ${c.name}`)
  else {
    console.log(`❌ ${c.name}  — ${c.err}`)
    failed += 1
  }
}
console.log(`\nRESULT: ${failed === 0 ? 'PASS' : 'FAIL'}（${cases.length - failed} / ${cases.length} 条）`)
process.exit(failed === 0 ? 0 : 1)
