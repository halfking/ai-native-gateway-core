/**
 * html-table-semantics-audit.mjs — 「表格语义合规」门（2026-10-30 · r1050 候选 C）
 *
 * # 这道门补的是什么
 *
 * `web/src/views/RequestLogsView.vue` 的「会话缓存机制」说明表写成
 * `<table><tr>…` —— `<tr>` 直接挂在 `<table>` 下，**既无 `<thead>` 也无 `<tbody>`**。
 *
 * ★ 这类结构的后果不是「不好看」，而是**语义层被浏览器悄悄改写**：
 *   实测（jsdom，与浏览器同源的 HTML 解析器）：
 *     `<table><tr><td>x</td></tr></table>` 解析后 `table.children` = `[TBODY]`
 *   ⇒ 浏览器会**隐式插入**一个 `<tbody>`，把作者写的结构改掉。
 *   而 Vue 的**编译期模板**走的是另一条路（`innerHTML` 挂载），
 *   拿不到这个容错 ⇒ 同一份模板在编译期与运行期的 DOM 形状可以不一致。
 *   ⇒ 这也是 HTML lint 类工具（Vite / eslint-plugin-vue 的 `vue/valid-v-for` 族
 *     与浏览器的 `element-permitted-content` 检查）会报的原因：
 *     **`<tr>` 不是 `<table>` 的合法直接子节点**。
 *
 * ★★ 为什么不写成一个「一次性脚本」而是落成门：
 *   仓内纪律明写「临时 grep/脚本只能找线索，不能下判决」——
 *   一次性脚本没有回归保护，下一次有人写回 `<table><tr>` 时没有任何东西会红。
 *   ⇒ 本门与同族脚本（`ui-audit.mjs` / `compact-table-census.mjs` /
 *     `color-token-audit.mjs` / `deadcode-scan.mjs` …）同形：可复跑、有自证。
 *
 * # 判据
 *
 *   对 `src/` 下每个 `.vue` 文件的 **`<template>` 块**（不是整文件！）做一次
 *   深度配对扫描，判定是否存在「`<tr>` 的父节点是 `<table>`」。
 *
 * ★★ **为什么只判 `<tr>` 直挂 `<table>` 这一种，不做完整的 HTML 合法性判定**：
 *   ① 完整的 HTML 内容模型判定需要一张 per-element 的 permitted-content 表，
 *      自造一份就是「第二份真值」（仓内明令禁止）；
 *      ★ 而 HTML 规范本身是唯一事实源，但仓内没有这张表的权威副本。
 *   ② 本门只覆盖**本仓真实出现过的那一族**（有据可查：r1050 候选 C 的实测样本），
 *      判据窄但**每一处都会红**——比宽而漏好。
 *   ③ 其余形态（`<td>` 直挂 `<table>`、`<th>` 直挂 `<table>` 等）由浏览器容错，
 *      本仓实测未出现，且不属本轮范围。
 *
 * # ★★ 为什么必须先剥 `<script>`/`<style>` 再扫（掩码优先）
 *
 *   模板字符串里可能内嵌 HTML 片段（如 `h('table', …)` 或反引号里的 `<tr>`）。
 *   ⇒ 若对**整文件**做标签配对，`<script>` 内的字面量会污染栈 ⇒ 假阳性。
 *   ⇒ 本门只取 `<template>` 块（`vue/compiler-sfc` 的 `descriptor.template.content`），
 *     它天然不含 `<script>`/`<style>` 内容 ⇒ 栈不会被污染。
 *
 * # 用法
 *
 *   node scripts/html-table-semantics-audit.mjs             # 人读报告
 *   node scripts/html-table-semantics-audit.mjs --strict    # 有违规 exit 1（CI 门）
 *   node scripts/html-table-semantics-audit.mjs --selftest  # 自证（植入违规 → 判红）
 *
 * ★ 自证必须**两个方向**都成立：
 *   「植入违规 → 判红」证明判据有牙；
 *   「干净样本 → 判绿」证明判据不是恒红门。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const { parse } = require('vue/compiler-sfc')

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const SRC = resolve(WEB, 'src')

// ─────────────────────────────────────────────────────────────
// 扫描面
// ─────────────────────────────────────────────────────────────

function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (p.endsWith('.vue')) out.push(p)
  }
  return out
}

/**
 * 返回「`<tr>` 的父节点是 `<table>`」的命中列表。
 *
 * ★ 栈式配对扫描：开标签入栈、闭标签出栈（`lastIndexOf` 找最近同名的那个）、
 *   自闭合标签不入栈。
 * ★ Vue 模板里的 `<tr v-for=…>` 属于「带属性的开标签」，不是自闭合 ⇒ 正常入栈。
 * ★ HTML 里 `<tr>`/`<td>`/`<th>` 的可选结束标签写法（省略 `</tr>`）在本仓未出现；
 *   若出现，`lastIndexOf` 的出栈语义仍能保持“最近的同名标签先闭合”的直觉。
 */
export function findTrDirectlyUnderTable(templateContent) {
  const re = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)([^>]*?)(\/?)>/g
  const stack = []
  const hits = []
  let m
  while ((m = re.exec(templateContent)) !== null) {
    const [, closing, tag, , selfClosing] = m
    if (closing) {
      const i = stack.lastIndexOf(tag)
      if (i >= 0) stack.splice(i, 1)
      continue
    }
    if (selfClosing) continue
    const parent = stack[stack.length - 1]
    if (tag === 'tr' && parent === 'table') {
      const line = templateContent.slice(0, m.index).split('\n').length
      hits.push({ line, tag, parent })
    }
    stack.push(tag)
  }
  return hits
}

function scanAll() {
  const files = walk(SRC).sort()
  const findings = []
  for (const f of files) {
    const rel = relative(WEB, f).split('\\').join('/')
    const { descriptor } = parse(readFileSync(f, 'utf8'), { filename: f })
    if (!descriptor.template) continue
    for (const h of findTrDirectlyUnderTable(descriptor.template.content)) {
      findings.push({ file: rel, line: h.line })
    }
  }
  return { files, findings }
}

// ─────────────────────────────────────────────────────────────
// 自证（--selftest）
// ─────────────────────────────────────────────────────────────

const LEGAL = '<table><thead><tr><th>a</th></tr></thead><tbody><tr><td>x</td></tr></tbody></table>'
const DEFECT = '<table><tr><td>x</td></tr></table>'

function selftest() {
  let failed = 0
  const check = (name, ok, detail) => {
    console.log(`${ok ? '  ✔' : '  ✘'} ${name}${detail ? ` — ${detail}` : ''}`)
    if (!ok) failed++
  }
  console.log('自证：')

  check(
    '干净样本（table>thead/tbody>tr）判绿',
    findTrDirectlyUnderTable(LEGAL).length === 0,
    `命中 ${findTrDirectlyUnderTable(LEGAL).length}（期望 0）`,
  )
  check(
    '缺陷样本（table>tr）判红',
    findTrDirectlyUnderTable(DEFECT).length === 1,
    `命中 ${findTrDirectlyUnderTable(DEFECT).length}（期望 1）`,
  )
  check(
    '嵌套 table 里的 tr 不误报（父节点是内层 table ⇒ 命中）',
    findTrDirectlyUnderTable('<table><tbody><tr><td><table><tr><td>y</td></tr></table></td></tr></tbody></table>')
      .length === 1,
    '内层 table 的 tr 确为缺陷 ⇒ 命中 1（期望 1）',
  )
  check(
    'script 块里的字面量不被扫（真实通道：template 块不含 script 内容）',
    (() => {
      // 真实路径：整份 SFC → compiler-sfc 抽出 template 块 → 块内不含 <script> 内容
      const sfc = `<template><div>ok</div></template>\n<script setup lang="ts">\nconst s = '<table><tr><td>x</td></tr></table>'\n</script>\n`
      const { descriptor } = parse(sfc, { filename: 'probe.vue' })
      if (!descriptor.template) return false
      return findTrDirectlyUnderTable(descriptor.template.content).length === 0
    })(),
    '期望 0',
  )

  const { findings } = scanAll()
  check(
    '真实仓当前无命中（本门不是恒红门）',
    findings.length === 0,
    `命中 ${findings.length}（期望 0）`,
  )

  if (failed > 0) {
    console.log(`\n自证失败 ${failed} 项`)
    process.exit(2)
  }
  console.log('\n自证全部通过')
}

// ─────────────────────────────────────────────────────────────
// 入口
// ─────────────────────────────────────────────────────────────

const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  selftest()
} else {
  const { files, findings } = scanAll()
  console.log(`扫描 ${files.length} 个 .vue 文件（src/ 全量）`)
  if (findings.length === 0) {
    console.log('✔ 表格语义合规：无「<tr> 直挂 <table>」')
  } else {
    console.log(`✘ 违规 ${findings.length} 处（「<tr>」不得是「<table>」的直接子节点）：`)
    for (const f of findings) console.log(`  ${f.file}:${f.line}`)
    console.log('\n修法：给表头行包 <thead>、数据行包 <tbody>。')
  }
  if (argv.includes('--strict') && findings.length > 0) process.exit(1)
}
