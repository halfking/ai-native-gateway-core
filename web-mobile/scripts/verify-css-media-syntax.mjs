#!/usr/bin/env node
// verify-css-media-syntax.mjs — 范围语法门禁（UI 规范 01 §3）。
// iOS 15 WebView/Safari 15 不支持 Media Queries Level 4 范围语法
// (width<=959.98px)；Vite 压缩会产出该语法导致整层响应式被丢弃。
// 本门扫描 src/**/*.css 与 *.vue 的 <style> 块，范围语法命中数必须为 0。
// --self-test：用内置正/负样本打真扫描器（门必须在自己的测试数据上不误响）。
//
// ★ 判据（2026-10-07 修，合并自 feat 轮 cd93a3f64）：只查 <= / >= 是不够的。
//   MQ4 范围语法的四种书写里，单侧 (width < 600px)、值在左 (600px < width)
//   和双向 (400px < width <= 600px) 都含裸 < / >，「特性在左 + <=」的窄正则
//   一条都抓不到 → 门恒绿。范围语法的判断依据是「媒体特性位置出现比较
//   运算符」，运算符含 < <= > >= 四种，且两侧都可能是值或特性。
//   配套自测：scripts/verify-css-media-syntax.selftest.mjs（六类样本，
//   含跨行 / 嵌套选择器 / 注释示例 / src 缺失不静默放行）。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')

// 括注内出现裸 < 或 > 即判范围语法。传统媒体特性（(min-width: 700px) /
// (hover) / (aspect-ratio: 16/9) / (orientation: landscape)）一个都没有这两个
// 字符；范围语法四种形态全都有。限定在括注内是为了放过嵌套选择器
// （div > span）。
const RANGE_RE = /\([^()]*[<>][^()]*\)/

function collectFiles(dir, out) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name.startsWith('.')) continue
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) collectFiles(p, out)
    else if (name.endsWith('.css') || name.endsWith('.vue')) out.push(p)
  }
  return out
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

export function scanSources(files) {
  const violations = []
  for (const f of files) {
    const text = stripComments(readFileSync(f, 'utf8'))
    // .vue 只扫 <style> 块：模板/脚本里的比较运算符不是媒体查询。
    let styleText = text
    if (f.endsWith('.vue')) {
      const blocks = [...text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1] ?? '')
      styleText = blocks.join('\n')
    }
    const lineOf = (idx) => styleText.slice(0, idx).split('\n').length
    for (const { at, prelude } of mediaPreludes(styleText)) {
      const m = prelude.match(RANGE_RE)
      if (m) violations.push({ file: f, hits: [`${lineOf(at)}: ${m[0].replace(/\s+/g, ' ')}`] })
    }
  }
  return violations
}

function main() {
  const selfTest = process.argv.includes('--self-test')
  if (selfTest) {
    // 用内联正/负样本直接打同一条 RANGE_RE（与扫描器共用，防实现与自检漂移）。
    const sampleBad = '@media (width<=959.98px) { a { color: red } }'
    const sampleGood = '@media (max-width: 959.98px) { a { color: red } }'
    const badHits = sampleBad.match(RANGE_RE)?.length ?? 0
    const goodHits = sampleGood.match(RANGE_RE)?.length ?? 0
    if (badHits !== 1) {
      console.error(`[css-media-syntax] self-test FAIL: negative sample should hit once, got ${badHits}`)
      process.exit(1)
    }
    if (goodHits !== 0) {
      console.error(`[css-media-syntax] self-test FAIL: positive sample should not hit, got ${goodHits}`)
      process.exit(1)
    }
    console.log('[css-media-syntax] self-test OK (1 negative hit / 0 positive hit)')
    process.exit(0)
  }

  const files = collectFiles(join(ROOT, 'src'), [])
  const violations = scanSources(files)
  if (violations.length > 0) {
    console.error('[css-media-syntax] range-syntax violations found:')
    for (const v of violations) {
      for (const h of v.hits) console.error(`  RANGE-SYNTAX ${v.file}:${h}`)
    }
    console.error('Use traditional (max-width: …) syntax — Safari 15 drops range syntax at parse time (UI spec 01 §3).')
    process.exit(1)
  }
  console.log(`[css-media-syntax] OK — scanned ${files.length} files, 0 range-syntax hits`)
}

main()
