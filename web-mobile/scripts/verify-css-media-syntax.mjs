#!/usr/bin/env node
// verify-css-media-syntax.mjs — UI规范 01 §3 仓库门禁的 web-mobile 镜像。
// 扫描 src/**/*.{css,vue}：Media Queries Level 4 范围语法（(width<…) /
// (width<=…) / (400px<width<=600px) / (600px>width) …）计数必须为 0，否则
// Safari 15 WebView 在解析期整块丢弃该 @media（iPhone 6s / iOS 15.8.3 实证，
// nbjl-3 先例）。
//
// ★ 只查 <= / >= 是不够的（2026-10-07 修）。MQ4 范围语法的四种书写里，
//   单侧 (width < 600px) 和双向 (400px < width <= 600px) 都含裸 < / >，
//   旧正则一条都抓不到 → 门恒绿。范围语法的判断依据是「媒体特性位置出现
//   比较运算符」，运算符含 < <= > >= 四种，且两侧都可能是值或特性。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = new URL('..', import.meta.url).pathname
const SRC = join(ROOT, 'src')

// 括注内出现裸 < 或 > 即判范围语法。传统媒体特性（(min-width: 700px) /
// (hover) / (aspect-ratio: 16/9) / (orientation: landscape)）一个都没有这两个
// 字符；范围语法四种形态全都有。限定在括注内是为了放过同行的嵌套选择器
// （@media screen { div > span { … } }）。
const RANGE_RE = /\([^()]*[<>][^()]*\)/

const files = []

function walk(dir) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    const st = statSync(full)
    if (st.isDirectory()) walk(full)
    else if (/\.(css|vue)$/.test(name)) files.push(full)
  }
}

// 注释必须先摘掉，否则 /* 例：@media (width<600px) */ 这类文档示例会把门带偏。
function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
}

// @media 的条件（prelude）可以跨行写：
//   @media (
//     width < 600px
//   ) { … }
// 所以按「@media 到深度 0 的第一个 {」取前奏，不能逐行判。
function mediaPreludes(text) {
  const out = []
  for (let i = text.indexOf('@media'); i !== -1; i = text.indexOf('@media', i + 1)) {
    let depth = 0
    let j = i
    for (; j < text.length; j++) {
      const c = text[j]
      if (c === '(') depth++
      else if (c === ')') depth--
      else if (c === '{' && depth === 0) break
      else if (c === '}' && depth === 0) break // 未闭合的畸形块，放弃这一条
    }
    out.push({ at: i, prelude: text.slice(i, j === text.length ? j : j + 1) })
  }
  return out
}

walk(SRC)
let bad = 0
for (const f of files) {
  const text = stripComments(readFileSync(f, 'utf8'))
  const lineOf = (idx) => text.slice(0, idx).split('\n').length
  for (const { at, prelude } of mediaPreludes(text)) {
    const m = prelude.match(RANGE_RE)
    if (m) {
      console.error(`RANGE-SYNTAX ${f}:${lineOf(at)}  ${m[0].replace(/\s+/g, ' ')}`)
      bad++
    }
  }
}
if (bad > 0) {
  console.error(`\n${bad} range-syntax media query(ies) found — must be 0 (UI规范 01 §3).`)
  process.exit(1)
}
console.log(`css-media-syntax OK: 0 range syntax in ${files.length} files`)