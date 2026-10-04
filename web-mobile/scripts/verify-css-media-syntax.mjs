#!/usr/bin/env node
// verify-css-media-syntax.mjs — UI规范 01 §3 仓库门禁的 web-mobile 镜像。
// 扫描 src/**/*.{css,vue}：Media Queries Level 4 范围语法（(width<=…) /
// (400px<=width<=…)）计数必须为 0，否则 Safari 15 WebView 在解析期整块丢弃
// 该 @media（iPhone 6s / iOS 15.8.3 实证，nbjl-3 先例）。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = new URL('..', import.meta.url).pathname
const SRC = join(ROOT, 'src')

// 范围语法特征：比较运算符出现在媒体特性位置。传统语法的值侧可能出现
// "<=" 只在 (width<=N) 这种「特性<=值」形态——媒体查询传统写法里不存在
// <= 字符，因此裸扫 <= / >= 出现在 @media 行内即判违规。
const RANGE_RE = /\(\s*(?:min-|max-)?[a-z-]+\s*(?:<=|>=)/
const files = []

function walk(dir) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    const st = statSync(full)
    if (st.isDirectory()) walk(full)
    else if (/\.(css|vue)$/.test(name)) files.push(full)
  }
}

walk(SRC)
let bad = 0
for (const f of files) {
  const text = readFileSync(f, 'utf8')
  for (const line of text.split('\n')) {
    if (line.includes('@media') && RANGE_RE.test(line)) {
      console.error(`RANGE-SYNTAX ${f}: ${line.trim()}`)
      bad++
    }
  }
}
if (bad > 0) {
  console.error(`\n${bad} range-syntax media query(ies) found — must be 0 (UI规范 01 §3).`)
  process.exit(1)
}
console.log(`css-media-syntax OK: 0 range syntax in ${files.length} files`)
