// verify-comment-boundaries.selftest.mjs — 给「块注释边界」门自己搭前提
//
// 照 verify-i18n-parity.selftest.mjs 的教训：**一道只会放行的门，与一个坏掉的门，
// 在绿灯时完全无法区分。** 这道门在本专题里第一版就当场自曝了假阳性
// （见门文件头「第一版没有处理正则字面量」那段），所以自测不是形式。
//
// ★★ 本门要挡的缺陷是**已在本专题真实发生、且没有任何一道门报过**的那个：
//   `src/api/orphanLedger.spec.ts` 的文件头注释从 `/**` 开启后丢了 ` * ` 前缀，
//   于是没闭合，把两行 `const ROOT` / `const API_DIR` 吞进了注释。
//   vue-tsc 绿（TS2451 看不见 —— 注释已把那两行从 program 里摘掉）、
//   vitest 绿、孤儿棘轮 15 条断言全绿（因为另一份声明还活着）。
//   ⇒ **恰恰因为它是重复的，它才被伪装成无害。**
//
// 样本覆盖 7 组（正反两侧都有 —— 只测「该过的」是判据自证的陷阱）：
//   1. 正例：注释带 ` * ` 前缀且正常闭合 → 放行
//   2. ★ 反例：**逐字复刻本批受损样本** → 拦下，且报出被吞的两行行号
//   3. 反例：字符串里的 `'/*'` 与 `"*/"` → 不得被当成注释（放行）
//   4. ★ 反例：**正则字面量里的字符类 `['"]`** → 不得被当成引号（放行）
//      （这是本门第一版的假阳性来源；漏掉这组 = 门会在正常仓库上乱咬）
//   5. ★ 反例：**缩进**在注释里的代码示例（` * const x = 1`）→ 放行（防过头）
//   6. 反例：`//` 行注释里出现 `/*` → 放行
//   7. ★ 反例：src 目录缺失 / 0 文件 → 报错而非静默放行
//
// 用法：node scripts/verify-comment-boundaries.selftest.mjs

import { mkdtempSync, writeFileSync, mkdirSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const GATE = join(HERE, 'verify-comment-boundaries.mjs')
const NL = '\n'

let pass = 0
let fail = 0

function check(label, cond, out) {
  if (cond) {
    pass++
    console.log('  ✓ ' + label)
  } else {
    fail++
    console.error('  ✗ ' + label)
    if (out) console.error('    --- 门输出 ---' + NL + out)
  }
}

/** 造一个临时仓：`<root>/scripts/<门>` + `<root>/src/<文件…>`。 */
function makeRepo(files, { omitSrc = false } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'comment-boundary-selftest-'))
  mkdirSync(join(root, 'scripts'), { recursive: true })
  writeFileSync(join(root, 'scripts', 'verify-comment-boundaries.mjs'), readFileSync(GATE, 'utf8'))
  if (!omitSrc) {
    mkdirSync(join(root, 'src'), { recursive: true })
    for (const [name, body] of Object.entries(files)) {
      const p = join(root, 'src', name)
      mkdirSync(dirname(p), { recursive: true })
      writeFileSync(p, body)
    }
  }
  return root
}

function runGate(root) {
  try {
    const out = execFileSync(process.execPath, [join(root, 'scripts', 'verify-comment-boundaries.mjs')], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    return { code: 0, out }
  } catch (e) {
    return { code: e.status ?? -1, out: String(e.stdout ?? '') + String(e.stderr ?? '') }
  }
}

const GOOD_FILE = [
  '/**',
  ' * 正常的文件头。',
  ' * 注释正文都带 ` * ` 前缀。',
  ' */',
  '',
  'const ROOT = 1',
  'export function ok() {',
  '  return ROOT',
  '}',
  '',
].join(NL)

// ── 1. 正例 ───────────────────────────────────────────────────────────────
{
  console.log(NL + '[1] 正例：注释前缀完整、正常闭合')
  const root = makeRepo({ 'a.ts': GOOD_FILE })
  const r = runGate(root)
  check('放行', r.code === 0, r.out)
  check('报出了扫描文件数（证明真的扫了）', /1 个文件/.test(r.out), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 2. ★ 反例：逐字复刻本批受损样本 ───────────────────────────────────────
{
  console.log(NL + '[2] ★ 反例：文件头注释丢 ` * ` 前缀 ⇒ 未闭合 ⇒ 吞掉两行 const')
  const brokenLines = [
    "import { join } from 'node:path'",
    '',
    '/**',
    ' * 文件头。正文带前缀。',
    ' *',
    ' * ## 小节',
    '',
    '这一段丢了前缀，注释没有闭合。',
    '',
    "const ROOT = join(process.cwd(), 'src')",
    "const API_DIR = join(ROOT, 'api')",
    '',
    "/** 本该独立成段的一段文档注释 */",
    "const KNOWN: readonly string[] = ['x']",
    '',
    '// 真正的声明在后面还活着一份，所以 TS 与 vitest 全绿。',
    "const ROOT = join(process.cwd(), 'src')",
    '',
  ]
  const broken = brokenLines.join(NL)
  const root = makeRepo({ 'orphanish.ts': broken })
  const r = runGate(root)
  // ★★★ 期望行号**从夹具推导**，不手数（第一版我手数成 9，真实是 10 ——
  //   门是对的、断言是错的。手数的期望值天生会随夹具微调而腐烂，
  //   而「门报的行号对不对」正是这条判据的全部内容 ⇒ 绝不能手数）。
  const rootLine = brokenLines.findIndex((l) => l.startsWith('const ROOT')) + 1
  const apiLine = brokenLines.findIndex((l) => l.startsWith('const API_DIR')) + 1
  check('拦下', r.code === 1, r.out)
  check(`报出了第一处被吞的行号 ${rootLine}`, new RegExp(`orphanish[.]ts:${rootLine}\\b`).test(r.out), r.out)
  check(`报出了第二处被吞的行号 ${apiLine}`, new RegExp(`orphanish[.]ts:${apiLine}\\b`).test(r.out), r.out)
  check('报出了该行的代码形态 const ROOT', /const ROOT/.test(r.out), r.out)
  check(
    '★ 没有把后半段「活着的那份」误报（只报 2 行不是 3 行）',
    (r.out.match(/orphanish[.]ts:\d+/g) || []).length === 2,
    r.out,
  )
  check('★ 提示里点明了成因', /前缀/.test(r.out), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 3. 反例：字符串里的注释形态 ───────────────────────────────────────────
{
  console.log(NL + '[3] 反例：字符串里的 /* 与 */ 不得被当成注释')
  const f = [
    "const A = '/*'",
    'const B = "*/"',
    "const C = 'const ROOT = 1'",
    'export { A, B, C }',
    '',
  ].join(NL)
  const root = makeRepo({ 'str.ts': f })
  const r = runGate(root)
  check('放行（字符串不是注释）', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 4. ★ 反例：正则字面量里的 ['"] ────────────────────────────────────────
{
  console.log(NL + '[4] ★ 反例：正则字面量里的字符类 ["] 不得被当成引号（本门第一版的假阳性来源）')
  // ★ 逐字取自 orphanLedger.spec.ts:145 —— 字符类里同时含单双引号。
  const f = [
    'function importsOf(blob: string): string[] {',
    "  const abs = [...blob.matchAll(/from\\s+['\"][^'\"]*api\\/([A-Za-z0-9_]+)['\"]/g)].map((m) => m[1] as string)",
    "  const rel = [...blob.matchAll(/from\\s+['\"]\\.\\/(?![.])([A-Za-z0-9_]+)['\"]/g)].map((m) => m[1] as string)",
    '  return [...abs, ...rel]',
    '}',
    '',
    'const AFTER_REGEX = 1',
    '',
  ].join(NL)
  const root = makeRepo({ 'regex.ts': f })
  const r = runGate(root)
  check('放行（正则里的引号不是引号）', r.code === 0, r.out)
  check(
    '★ 正则之后的顶格代码没有被误判成「在注释里」',
    !/regex[.]ts:7\b/.test(r.out),
    r.out,
  )
  rmSync(root, { recursive: true, force: true })
}

// ── 5. ★ 反例：缩进在注释里的代码示例（防过头）─────────────────────────────
{
  console.log(NL + '[5] ★ 反例：注释里缩进的代码示例是文档，不得拦下')
  const f = [
    '/**',
    ' * 用法示例：',
    ' * const ROOT = 1',
    ' * export function ok() {}',
    ' */',
    "export const V = 'x'",
    '',
  ].join(NL)
  const root = makeRepo({ 'doc.ts': f })
  const r = runGate(root)
  check('放行（缩进散文不是藏代码）', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 6. 反例：行注释里的 /* ────────────────────────────────────────────────
{
  console.log(NL + '[6] 反例：// 行注释里出现 /* 不得开启块注释')
  const f = [
    "// 说明：这里写 /* 只是举例",
    'const A = 1',
    '',
  ].join(NL)
  const root = makeRepo({ 'line.ts': f })
  const r = runGate(root)
  check('放行', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 7. ★ 反例：src 缺失 / 0 文件不得静默放行 ──────────────────────────────
{
  console.log(NL + '[7] ★ 反例：src 目录缺失不得静默放行')
  const root = makeRepo({}, { omitSrc: true })
  const r = runGate(root)
  check('报错（而不是 OK）', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}
{
  console.log(NL + '[7b] 反例：src 存在但 0 个源文件不得静默放行')
  const root = makeRepo({ 'README.md': '# 不是源文件' })
  const r = runGate(root)
  check('报错（扫不到候选源文件必须红）', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}

console.log(NL + 'comment-boundaries selftest: ' + pass + ' passed, ' + fail + ' failed')
process.exit(fail === 0 ? 0 : 1)
