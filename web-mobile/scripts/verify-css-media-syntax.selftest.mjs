// verify-css-media-syntax.selftest.mjs — 给 @media 范围语法门自己搭前提。
//
// ★ 为什么这道门尤其需要自测（2026-10-07 实证）：
//   这道门的判据是「范围语法计数 == 0」。旧实现只匹配 <= / >=，
//   而 MQ4 范围语法有四种书写，其中三种含**裸 < / >**：
//     (width < 600px)  (600px < width)  (400px < width <= 600px)
//   ⇒ 门对三种形态恒绿，而这三种在 iPhone 6s / iOS 15.8.3 上和
//   (width <= 600px) 一样会被整块丢弃。
//   「门恒绿」和「仓库没有违规」在输出上完全一样 —— 这正是恒真判据。
//   ⇒ 这里必须逐个形态证明「会红」，否则修完也无法证明修对了。
//
// 样本分六类：
//   1. ★ 四种范围语法形态逐一必须红（含旧门漏掉的三种）
//   2. 跨行写法必须红（旧门逐行判，跨行必漏）
//   3. 反例：传统特性必须绿（min-width / hover / 比例 / 枚举值）
//   4. ★ 同行嵌套选择器的裸 > 不能误报
//   5. ★ 注释里的示例不能误报
//   6. 目录为空/缺失不能静默放行（防「扫了 0 个也报 OK」）
//
// 用法：node scripts/verify-css-media-syntax.selftest.mjs

import { mkdtempSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const GATE = join(HERE, 'verify-css-media-syntax.mjs')

let pass = 0
let fail = 0

// ★ 门把 ROOT 定义为「脚本所在目录的父目录」（web-mobile/scripts/ → web-mobile/），
//   所以门必须放在 <root>/scripts/，否则它会扫 <root>/.. —— 一个 .css 都没有，
//   然后报出一个漂亮的绿色。
function makeRepo(files) {
  const root = mkdtempSync(join(tmpdir(), 'css-selftest-'))
  mkdirSync(join(root, 'src'), { recursive: true })
  mkdirSync(join(root, 'scripts'), { recursive: true })
  for (const [rel, content] of Object.entries(files)) {
    writeFileSync(join(root, rel), content)
  }
  writeFileSync(join(root, 'scripts/verify-css-media-syntax.mjs'), execFileSync('cat', [GATE]))
  return root
}

function runGate(root) {
  try {
    const out = execFileSync('node', [join(root, 'scripts/verify-css-media-syntax.mjs')], { encoding: 'utf8' })
    return { code: 0, out }
  } catch (e) {
    return { code: e.status ?? 1, out: `${e.stdout ?? ''}${e.stderr ?? ''}` }
  }
}

function check(name, cond, detail = '') {
  if (cond) {
    pass++
    console.log(`  ✓ ${name}`)
  } else {
    fail++
    console.error(`  ✗ ${name}${detail ? `\n      ${detail}` : ''}`)
  }
}

// ── 1. ★ 四种范围语法形态逐一必须红 ──────────────────────────────────────
//     后三条是旧门漏掉的；放这里就是为了让「修对了」变成可证伪的断言。
{
  console.log('\n[1] 四种范围语法形态逐一拦下')
  const forms = [
    ['单侧 <= 特性在左', '@media (width <= 600px) { .a { color: red } }'],
    ['单侧 < 特性在左（旧门漏）', '@media (width < 600px) { .a { color: red } }'],
    ['单侧 < 值在左（旧门漏）', '@media (600px < width) { .a { color: red } }'],
    ['双向范围（旧门漏，真实最常见）', '@media (400px < width <= 600px) { .a { color: red } }'],
    ['单侧 >= 反向', '@media (width >= 900px) { .a { color: red } }'],
    ['特性在左的裸 >', '@media (width > 900px) { .a { color: red } }'],
  ]
  for (const [name, css] of forms) {
    const root = makeRepo({ 'src/probe.css': css })
    const r = runGate(root)
    check(`${name} 被拦下`, r.code === 1 && /RANGE-SYNTAX/.test(r.out), r.out)
    rmSync(root, { recursive: true, force: true })
  }
}

// ── 2. 跨行写法必须红（旧门逐行判，跨行必漏）─────────────────────────────
{
  console.log('\n[2] 跨行 @media 条件必须拦下')
  const root = makeRepo({
    'src/probe.css': '.a { color: red }\n@media (\n  width < 600px\n) { .a { color: blue } }\n',
  })
  const r = runGate(root)
  check('@media 条件跨三行时仍被拦下', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 3. 反例：传统媒体特性必须绿 ──────────────────────────────────────────
{
  console.log('\n[3] 传统特性放行')
  const root = makeRepo({
    'src/probe.css': [
      '@media (min-width: 600px) { .a { color: red } }',
      '@media screen and (max-width: 960px) { .a { color: blue } }',
      '@media (pointer: coarse) { .a { color: green } }',
      '@media (prefers-reduced-motion: reduce) { .a { color: teal } }',
      '@media (aspect-ratio: 16/9) { .a { color: gray } }',
      '@media (orientation: landscape) { .a { color: pink } }',
      '@media not all and (hover) { .a { color: olive } }',
      '@media (min-width: 600px) and (max-width: 960px) { .a { color: navy } }',
      '',
    ].join('\n'),
  })
  const r = runGate(root)
  check('8 种传统写法全部放行', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 4. ★ 同行嵌套选择器的裸 > 不能误报 ───────────────────────────────────
{
  console.log('\n[4] 嵌套选择器不误报')
  const root = makeRepo({
    'src/probe.css': '@media print { div > span { color: red } }\n@media (min-width: 600px) { ul > li { color: blue } }\n',
  })
  const r = runGate(root)
  check('@media 块内的 div > span 不被判成范围语法', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 5. ★ 注释里的示例不能误报 ───────────────────────────────────────────
{
  console.log('\n[5] 注释内容不误报')
  const root = makeRepo({
    'src/probe.css': '/* 反例留档：不要写 @media (width <= 600px) */\n@media (min-width: 600px) { .a { color: red } }\n',
  })
  const r = runGate(root)
  check('注释里的范围语法示例不误报', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 6. 目录缺失不能静默放行（防「扫了 0 个也报 OK」）─────────────────────
{
  console.log('\n[6] src 缺失的行为')
  const root = mkdtempSync(join(tmpdir(), 'css-empty-'))
  mkdirSync(join(root, 'scripts'), { recursive: true })
  writeFileSync(join(root, 'scripts/verify-css-media-syntax.mjs'), execFileSync('cat', [GATE]))
  const r = runGate(root)
  check('src 不存在时报错而非静默放行', r.code !== 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

console.log(`\ncss-media-syntax selftest: ${pass} passed, ${fail} failed`)
process.exit(fail === 0 ? 0 : 1)