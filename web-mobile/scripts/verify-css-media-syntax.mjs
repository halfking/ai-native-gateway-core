#!/usr/bin/env node
// verify-css-media-syntax.mjs — 范围语法门禁（UI 规范 01 §3）。
// iOS 15 WebView/Safari 15 不支持 Media Queries Level 4 范围语法
// (width<=959.98px)；Vite 压缩会产出该语法导致整层响应式被丢弃。
// 本门扫描 src/**/*.css 与 *.vue 的 <style> 块，范围语法命中数必须为 0。
// --self-test：用内置正/负样本打真扫描器（门必须在自己的测试数据上不误响）。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')

// 范围语法：@media (...) 中的 (prop<=v) / (prop>=v) / (prop<v) / (prop>v)。
const RANGE_RE = /\(\s*(?:min-|max-)?[a-z-]+\s*(?:<=|>=|<|>)\s*[\d.]+[a-z%]*\s*\)/g

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

export function scanSources(files) {
  const violations = []
  for (const f of files) {
    const text = readFileSync(f, 'utf8')
    let styleText = text
    if (f.endsWith('.vue')) {
      const blocks = [...text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1] ?? '')
      styleText = blocks.join('\n')
    }
    const hits = styleText.match(RANGE_RE)
    if (hits && hits.length > 0) violations.push({ file: f, hits })
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
      console.error(`  ${v.file}: ${v.hits.join(', ')}`)
    }
    console.error('Use traditional (max-width: …) syntax — Safari 15 drops range syntax at parse time (UI spec 01 §3).')
    process.exit(1)
  }
  console.log(`[css-media-syntax] OK — scanned ${files.length} files, 0 range-syntax hits`)
}

main()
