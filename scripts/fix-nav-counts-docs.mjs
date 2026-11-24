// fix-nav-counts-docs.mjs — 一次性脚本：同步「取消 /admin/session-analytics 总览页菜单项」
// 带来的 navCounts 记账变化（条目 68→67、落地路径 67→66、折叠 54→53、分组项数 65→64）。
//
// ★ 用**整段字面量**替换，不用宽泛正则：
//   `68</span>` 后紧跟 `67</span>` 时，粗替换会把两个 span 粘成 `6867`。
//   本脚本每条规则都是一段完整原文 → 整段换掉，绝不误伤相邻数字。
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(HERE, '..')
const DOC_DIR = join(ROOT, 'docs/03-design/02-feature-design')

const FILES = [
  'simple-expert-mode-design.html',
  'simple-mode-concept.html',
  'simple-mode-ui-mockup.html',
]

/** [原文, 新文] —— 原文必须是文件中真实存在的连续片段。 */
const RULES = [
  // ── simple-expert-mode-design.html ──
  [
    '专家面 <span class="num">68</span>项 / <span class="num">67 个落地路径 / <span class="num">53</span> 条收进「高级」折叠组',
    '专家面 <span class="num">67</span>项 / <span class="num">66 个落地路径 / <span class="num">53</span> 条收进「高级」折叠组',
  ],
  [
    '简易模式导航可见页面降到 14，54 条收进高级折叠组',
    '简易模式导航可见页面降到 14，53 条收进高级折叠组',
  ],
  // ── simple-mode-concept.html ──
  ['68 个专家面入口逐条去了哪里', '67 个专家面入口逐条去了哪里'],
  ['各算一个入口，共68项', '各算一个入口，共67项'],
  [
    '「高级」展开后是二级分组，54 条按专家面的分组归位',
    '「高级」展开后是二级分组，53 条按专家面的分组归位',
  ],
  ['54 条进「高级」折叠组，展开可见', '53 条进「高级」折叠组，展开可见'],
  [
    '（8 组64项 +顶层3项 =66，落地路径66 个）',
    '（8 组64项 +顶层3项 =67，落地路径66 个）',
  ],
  // ── simple-mode-ui-mockup.html ──
  ['68 个专家面入口的逐条归属', '67 个专家面入口的逐条归属'],
  ['<span class="d">导航68项，全部子系统启动</span>', '<span class="d">导航67项，全部子系统启动</span>'],
  [
    '导航从68项收到14 个页面，54 条折叠',
    '导航从67项收到14 个页面，53 条折叠',
  ],
  ['<span class="badge badge-muted">8 组 +顶层 ·68项</span>', '<span class="badge badge-muted">8 组 +顶层 ·67项</span>'],
  ['<span class="badge badge-primary">14 个页面可见 ·54 条折叠</span>', '<span class="badge badge-primary">14 个页面可见 ·53 条折叠</span>'],
]

let total = 0
for (const f of FILES) {
  const p = join(DOC_DIR, f)
  let src = readFileSync(p, 'utf8')
  let n = 0
  for (const [from, to] of RULES) {
    const parts = src.split(from)
    if (parts.length > 1) {
      n += parts.length - 1
      src = parts.join(to)
    }
  }
  if (n > 0) {
    writeFileSync(p, src, 'utf8')
    console.log(`${f}: ${n} 处`)
    total += n
  } else {
    console.log(`${f}: 0 处`)
  }
}
console.log(`总计 ${total} 处`)
