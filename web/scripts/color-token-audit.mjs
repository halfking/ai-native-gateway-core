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
  // 暗色桥接层:fill 层级用 color-mix(#ffffff/#000000 ...) 做透明度混合,
  // 白/黑是混合成分而非主题色
  'styles/element-dark.css',
  // 终端风代码块:双主题固定暗底(--bg-elevated 指向固定 #1e1e2e 表面)+
  // 固定浅字,是刻意的「两个主题下都像终端」设计(文件头注释锚定)
  'views/ExamplesView.vue',
  'composables/liveStreamColors.ts',
  'composables/useChart.ts',
  'composables/useChart.colors.test.ts',
  'composables/liveStreamDisplay.ts',
  'utils/waterfallTimeline.ts',
  'types/swimlane.ts',
])

const HEX_RE = /#[0-9a-fA-F]{3,8}\b/g
const RGB_RE = /rgba?\s*\([^)]+\)/g

const MIX_BLACKWHITE_RE = /#000000\b|#000\b|#ffffff\b|#fff\b/gi

// 平衡括号提取行内每个 color-mix(...) 并仅豁免其黑白成分
function exemptColorMixBlacks(line) {
  let out = ''
  let i = 0
  for (;;) {
    const idx = line.indexOf('color-mix(', i)
    if (idx === -1) return out + line.slice(i)
    out += line.slice(i, idx)
    let depth = 1
    let j = idx + 'color-mix('.length
    while (j < line.length && depth > 0) {
      if (line[j] === '(') depth++
      else if (line[j] === ')') depth--
      j++
    }
    out += line.slice(idx, j).replace(MIX_BLACKWHITE_RE, '__MIX__')
    i = j
  }
}

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
  // 测试文件里的颜色是断言字符串(如 toContain('#ffffff')),不是主题样式
  if (rel.endsWith('.test.ts') || rel.endsWith('.test.js')) return []
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
  // 跨行块注释追踪:注释中间的行(历史修复说明等)不参与扫描
  let inBlockComment = false

  for (let i = 0; i < lines.length; i++) {
    let line = lines[i]
    if (isVue) {
      if (!inStyle) {
        if (/<style[^>]*>/.test(line)) { inStyle = true; styleDepth = 1; continue }
      } else {
        if (/<style[^>]*>/.test(line)) styleDepth++
        if (/<\/style>/.test(line)) {
          styleDepth--
          if (styleDepth === 0) { inStyle = false; inTokenBlock = false; inBlockComment = false }
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
    if (inBlockComment) {
      const end = line.indexOf('*/')
      if (end === -1) continue
      inBlockComment = false
      line = line.slice(end + 2)
    }
    const open = line.indexOf('/*')
    if (open !== -1 && line.indexOf('*/', open + 2) === -1) {
      inBlockComment = true
      line = line.slice(0, open)
    }
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
    // color-mix() 只豁免黑白成分(#000/#fff 是明度调节,两主题语义一致);
    // 其余成分色(潜在品牌/数据色硬编码)照常报告,避免豁免面过宽造成漏报。
    // 成分提取用平衡括号扫描:简单 [^)]* 会在内嵌 var(--x) 的闭括号处截断,
    // 导致尾部成分(#000 等)漏出豁免范围(2026-09-13 审计轮实测修正)
    scanable = exemptColorMixBlacks(scanable)

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
