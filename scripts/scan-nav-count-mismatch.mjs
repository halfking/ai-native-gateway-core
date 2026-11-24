// scan-nav-count-mismatch.mjs — 一次性脚本：复刻 navCounts.test.ts 的数字规则，
// 扫描三份方案文档，列出**与锁定值不符**的每一处（含上下文），便于定点改字面量。
import { readFileSync } from 'node:fs'
import { resolve, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(HERE, '..')
const DOC_DIR = join(ROOT, 'docs/03-design/02-feature-design')

const EXPECTED_ITEMS = 67
const EXPECTED_PATHS = 66
const EXPECTED_FOLDED = 53
const GROUP_ITEMS = 64

const DOCS = [
  'simple-expert-mode-design.html',
  'simple-mode-concept.html',
  'simple-mode-ui-mockup.html',
]

const RULES = [
  { label: '条目数', re: /(\d+)\s*个(?:导航入口|专家面入口|入口)/g, expected: EXPECTED_ITEMS },
  { label: '条目数(后)', re: /(?:导航入口|专家面入口|入口)\s*(\d+)\s*个/g, expected: EXPECTED_ITEMS },
  { label: '条目数(项形态)', re: /专家面\s*(\d+)\s*项/g, expected: EXPECTED_ITEMS },
  { label: '条目数(导航N项)', re: /导航\s*(\d+)\s*项/g, expected: EXPECTED_ITEMS },
  { label: '条目数(各算一个入口)', re: /各算一个入口，共\s*(\d+)\s*项/g, expected: EXPECTED_ITEMS },
  { label: '条目数(收到N项)', re: /(\d+)\s*项\s*收到/g, expected: EXPECTED_ITEMS },
  { label: '条目数(·N项)', re: /·\s*(\d+)\s*项(?!\s*设置)/g, expected: EXPECTED_ITEMS },
  { label: '落地路径数', re: /(\d+)\s*个落地路径/g, expected: EXPECTED_PATHS },
  { label: '落地路径数(后)', re: /落地路径\s*(\d+)\s*个/g, expected: EXPECTED_PATHS },
  {
    label: '折叠条数',
    re: /(\d+)\s*条\s*(?:收进|收在|按专家面|专家面收进|折叠|进「高级」|收进高级折叠|·\s*展开后按专家面)/g,
    expected: EXPECTED_FOLDED,
  },
]

const DECOMP = {
  re: /(\d+)\s*(?:个分组|组)\s*(\d+)\s*项\s*\+\s*顶层\s*(\d+)\s*项(?:\s*=\s*(\d+))?/g,
  groups: 8,
  groupItems: GROUP_ITEMS,
  primaryItems: 3,
  total: EXPECTED_ITEMS,
}

let bad = 0
for (const file of DOCS) {
  const text = readFileSync(join(DOC_DIR, file), 'utf8')
  const lines = text.split('\n')
  for (const rule of RULES) {
    for (const m of text.matchAll(rule.re)) {
      const v = Number(m[1])
      if (v !== rule.expected) {
        const lineNo = text.slice(0, m.index).split('\n').length
        const line = lines[lineNo - 1] ?? ''
        console.log(`✗ ${file}:${lineNo}  [${rule.label}] 期望 ${rule.expected} 实得 ${v}`)
        console.log(`    ${line.trim().slice(0, 190)}`)
        bad++
      }
    }
  }
  for (const m of text.matchAll(DECOMP.re)) {
    const g = Number(m[1]), gi = Number(m[2]), p = Number(m[3]), t = m[4] !== undefined ? Number(m[4]) : null
    if (g !== DECOMP.groups || gi !== DECOMP.groupItems || p !== DECOMP.primaryItems || (t !== null && t !== DECOMP.total)) {
      const lineNo = text.slice(0, m.index).split('\n').length
      console.log(`✗ ${file}:${lineNo}  [分解式] 期望 ${DECOMP.groups}组${DECOMP.groupItems}项+${DECOMP.primaryItems}项=${DECOMP.total} 实得 ${g}组${gi}项+${p}项${t !== null ? `=${t}` : ''}`)
      console.log(`    ${(lines[lineNo - 1] ?? '').trim().slice(0, 190)}`)
      bad++
    }
  }
}
console.log(bad === 0 ? '\n✓ 全部匹配' : `\n共 ${bad} 处不匹配`)
