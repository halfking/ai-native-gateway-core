/**
 * color-token-audit.mjs — 扫描 web/src 中所有硬编码颜色 (#xxx / rgb / rgba),
 * 输出报告(JSON),可用于 CI 阻断。
 *
 * 使用:
 *   node scripts/color-token-audit.mjs                  # 报告到 stdout
 *   node scripts/color-token-audit.mjs --json reports/color-audit.json
 *   node scripts/color-token-audit.mjs --strict         # CI 模式,有违规则 exit 1
 */

import { readFileSync, readdirSync, statSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve, relative, join } from 'node:path'

const ROOT = resolve(process.cwd(), 'src')
const EXTS = ['.vue', '.ts', '.css', '.scss']

// 已知豁免:这些文件允许 hex/rgb
//   - style.css : token 定义本身,允许 hex
//   - liveStreamColors.ts : 厂商品牌色映射(数据色,不是主题色)
//   - useChart.ts : Chart.js 调色板(ECharts 系列色,不是主题色)
//   - waterfallTimeline.ts : 泳道阶段色,每个阶段一个固定色,不是主题色
//   - liveStreamDisplay.ts : hex→rgba 工具函数代码里有伪 rgba(...,${alpha}) 字符串
const SKIP_FILES = new Set([
  'style.css',
  'composables/liveStreamColors.ts',
  'composables/useChart.ts',
  'composables/liveStreamDisplay.ts',
  'utils/waterfallTimeline.ts',
  'types/swimlane.ts',
])

const HEX_RE = /#[0-9a-fA-F]{3,8}\b/g
const RGB_RE = /rgba?\s*\([^)]+\)/g

function walk(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '.git' || entry === 'dist') continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) out.push(...walk(full))
    else if (EXTS.some(ext => entry.endsWith(ext))) out.push(full)
  }
  return out
}

function scanFile(file) {
  const rel = relative(ROOT, file).replace(/\\/g, '/')
  if (SKIP_FILES.has(rel)) return []
  const source = readFileSync(file, 'utf8')
  const violations = []
  const lines = source.split(/\r?\n/)

  // .vue 文件 — 只扫 <style> 块
  const isVue = rel.endsWith('.vue')
  // 允许非 .vue 的纯 .css 文件在 token 定义块内出现 hex/rgb(:root, html[data-theme])
  const isCss = rel.endsWith('.css') || rel.endsWith('.scss')

  let inStyle = false
  let styleDepth = 0
  // token 块追踪:进入 :root 或 html[data-theme=...] 时开启,遇到 } 关闭
  let inTokenBlock = false
  let tokenBraceDepth = 0
  let generalBraceDepth = 0

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (isVue) {
      if (!inStyle) {
        if (/<style[^>]*>/.test(line)) { inStyle = true; styleDepth = 1; continue }
      } else {
        if (/<style[^>]*>/.test(line)) styleDepth++
        if (/<\/style>/.test(line)) {
          styleDepth--
          if (styleDepth === 0) { inStyle = false; inTokenBlock = false }
          continue
        }
      }
    }
    if (isVue && !inStyle) continue

    // 追踪 :root / html[data-theme=...] 块
    if (isCss && !isVue) {
      if (/^\s*:root\s*[,{]/.test(line) || /html\[data-theme[^\]]*\]\s*\{/.test(line)) {
        inTokenBlock = true
        tokenBraceDepth = (line.match(/\{/g) || []).length - (line.match(/\}/g) || []).length
        if (tokenBraceDepth <= 0) { inTokenBlock = false }
        continue
      }
      if (inTokenBlock) {
        tokenBraceDepth += (line.match(/\{/g) || []).length - (line.match(/\}/g) || []).length
        if (tokenBraceDepth <= 0) inTokenBlock = false
        continue
      }
    }

    // 简单去除行注释 / 块注释(单行内)
    const cleaned = line
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/\/\/.*$/, '')

    // 跳过 var(--xxx, fallback) 形式里的 fallback 颜色(var() 的兜底,不实际生效)
    // 跳过 svg/image 的 fill/color 属性里被引号包裹的 hex 值(ECharts / canvas 图,数据色不在主题范围)
    // 也跳过形如 #123 hex 的 svg 内部 path 表达式
    let scanable = cleaned
    // 删除 var(--anything, color) 里的 color 部分(后出现的逗号到右括号)
    scanable = scanable.replace(/var\(\s*[A-Za-z0-9_-]+(?:--[A-Za-z0-9_-]+)?\s*,[^)]+\)/g, (m) => {
      // 替换 var() 内容为 placeholder
      return m.replace(/,(.+)$/, ',__FALLBACK__)').replace(/,(.+)\)/, ',__FALLBACK__)')
    })
    // 删除 rgba(var(--xxx), 0.X) 这种 CSS 现代用法(rgb 三元组从 var() 注入)
    scanable = scanable.replace(/rgba\(\s*var\([^)]+\)\s*,\s*[^)]+\)/g, '__RGBA_VAR__')

    for (const re of [HEX_RE, RGB_RE]) {
      re.lastIndex = 0
      let m
      while ((m = re.exec(scanable)) !== null) {
        violations.push({
          file: rel,
          line: i + 1,
          col: m.index + 1,
          value: m[0],
        })
      }
    }
  }
  return violations
}

const args = process.argv.slice(2)
const jsonIdx = args.indexOf('--json')
const outPath = jsonIdx >= 0 ? args[jsonIdx + 1] : null
const strict = args.includes('--strict')

const files = walk(ROOT)
const all = []
for (const f of files) all.push(...scanFile(f))

const byFile = new Map()
for (const v of all) {
  if (!byFile.has(v.file)) byFile.set(v.file, [])
  byFile.get(v.file).push(v)
}

const summary = {
  scanned_files: files.length,
  violating_files: byFile.size,
  total_violations: all.length,
}

if (outPath) {
  mkdirSync(resolve(process.cwd(), 'reports'), { recursive: true })
  writeFileSync(resolve(process.cwd(), outPath), JSON.stringify({ summary, files: Object.fromEntries(byFile) }, null, 2))
}

console.log('=== color-token-audit ===')
console.log(`扫描文件: ${summary.scanned_files}`)
console.log(`违规文件: ${summary.violating_files}`)
console.log(`违规总数: ${summary.total_violations}`)
if (all.length > 0 && !outPath) {
  console.log('\nTop 15 违规文件:')
  for (const [f, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length).slice(0, 15)) {
    console.log(`  ${list.length.toString().padStart(3)}  ${f}`)
  }
}

if (strict && all.length > 0) {
  console.error(`\n[strict] 发现 ${all.length} 处硬编码颜色,请修复后再提交。`)
  process.exit(1)
}
