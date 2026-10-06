// verify-i18n-parity.selftest.mjs — 给 i18n 键集门自己搭前提
//
// 照 verify-touch-targets.selftest.mjs 的教训：一道只会放行的门，
// 与一个坏掉的门，在绿灯时完全无法区分。
//
// 本门要挡的缺陷在本专题真实发生过（2026-10-06 §11.20 / §11.29）：
//   · 键插错词典（`usage.costTrend` 落进 `models:`）→ 运行时显示字面量
//   · 两侧插到不同词典（`keys.errForbidden` zh 在 keys、en 在 home）
//     → **英文用户看到裸键名**，而中文用户完全正常 ⇒ 没有任何中文侧测试能发现
//   第二条尤其说明为什么必须有静态门：缺陷只在一侧存在，
//   靠「跑一遍页面看看」只能发现出问题的那一侧。
//
// 样本覆盖 5 组（正反两侧都有 —— 只测「该过的」是判据自证的陷阱）：
//   1. 正例：键集完全一致 → 放行
//   2. 反例：仅 zh 多一个键 → 拦下（并要报出键名）
//   3. 反例：仅 en 多一个键 → 拦下
//   4. ★ 反例：值 === 键名（漏翻译）→ 拦下
//   5. ★ 反例：词典文件缺失 / 解析出 0 键 → 报错而非静默放行
//
// 用法：node scripts/verify-i18n-parity.selftest.mjs

import { mkdtempSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const GATE = join(HERE, 'verify-i18n-parity.mjs')

let pass = 0
let fail = 0

function makeRepo({ zh, en, omitZh = false, omitEn = false } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'i18n-selftest-'))
  mkdirSync(join(root, 'src/i18n'), { recursive: true })
  mkdirSync(join(root, 'scripts'), { recursive: true })
  if (!omitZh) writeFileSync(join(root, 'src/i18n/zh-CN.ts'), zh ?? "export const zhCN = {\n  a: {\n    b: '值',\n  },\n} as const\n")
  if (!omitEn) writeFileSync(join(root, 'src/i18n/en-US.ts'), en ?? "export const enUS = {\n  a: {\n    b: 'value',\n  },\n} as const\n")
  writeFileSync(join(root, 'scripts/verify-i18n-parity.mjs'), execFileSync('cat', [GATE]))
  return root
}

function runGate(root) {
  try {
    return { code: 0, out: execFileSync('node', [join(root, 'scripts/verify-i18n-parity.mjs')], { encoding: 'utf8' }) }
  } catch (e) {
    return { code: e.status ?? 1, out: `${e.stdout ?? ''}${e.stderr ?? ''}` }
  }
}

function check(name, cond, detail = '') {
  if (cond) { pass++; console.log(`  ✓ ${name}`) }
  else { fail++; console.error(`  ✗ ${name}${detail ? `\n      ${String(detail).trim().slice(0, 300)}` : ''}`) }
}

// ★ 样本必须与真实词典**同形**：每行一个键、嵌套对象独立成行、末行 `} as const`。
//   解析器是按行工作的（刻意不 import，避免依赖编译链），
//   写成 `{ a: { b: 'x' } }` 这种单行紧凑形会解析出 0 键 ——
//   那不是门的 bug，是样本与被测格式不一致（见 §11.29 的教训）。
const doc = (body) => `export const D = {\n${body}\n} as const\n`

// ── 1. 正例 ─────────────────────────────────────────────────────────────
{
  console.log('\n[1] 正例：键集一致放行')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n    more: '更多',\n  },\n  keys: {\n    requests: '请求数',\n    errForbidden: '无权限',\n  },"),
    en: doc("  nav: {\n    home: 'Home',\n    more: 'More',\n  },\n  keys: {\n    requests: 'Requests',\n    errForbidden: 'No permission',\n  },"),
  })
  const r = runGate(root)
  check('键集一致 → 放行', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 2. 仅 zh 多键 ───────────────────────────────────────────────────────
{
  console.log('\n[2] 反例：仅 zh-CN 多一个键')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n  },\n  keys: {\n    errForbidden: '无权限',\n  },"),
    en: doc("  nav: {\n    home: 'Home',\n  },\n  keys: {\n    requests: 'Requests',\n  },"),
  })
  const r = runGate(root)
  check('仅 zh 多键 → 拦下', r.code === 1, r.out)
  check('报出了具体键名 keys.errForbidden', r.out.includes('keys.errForbidden'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 3. 仅 en 多键 ───────────────────────────────────────────────────────
{
  console.log('\n[3] 反例：仅 en-US 多一个键（本专题真实发生过的形态）')
  const root = makeRepo({
    zh: doc("  keys: {\n    requests: '请求数',\n  },"),
    en: doc("  home: {\n    errForbidden: 'No permission',\n  },\n  keys: {\n    requests: 'Requests',\n  },"),
  })
  const r = runGate(root)
  check('仅 en 多键 → 拦下', r.code === 1, r.out)
  check('报出 home.errForbidden', r.out.includes('home.errForbidden'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 4. 值 === 键名（漏翻译）────────────────────────────────────────────
{
  console.log('\n[4] 反例：值写成键名本身（运行时显示裸键）')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n  },"),
    en: doc("  nav: {\n    home: 'home',\n  },"),
  })
  const r = runGate(root)
  check("值 === 键名 → 拦下", r.code === 1, r.out)
  check('报出未翻译提示', r.out.includes('未翻译'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 5. ★ 缺文件 / 解析出 0 键 ─────────────────────────────────────────
{
  console.log('\n[5] 反例：词典缺失不得静默放行')
  const root = makeRepo({ omitEn: true })
  const r = runGate(root)
  check('en-US 缺失 → 报错', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}
{
  console.log('\n[5b] 反例：解析出 0 键（路径对但格式变了）')
  const root = makeRepo({ zh: 'export const zhCN = {}\n', en: 'export const enUS = {}\n' })
  const r = runGate(root)
  check('两侧都 0 键 → 报错', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}

console.log(`\ni18n-parity selftest: ${pass} passed, ${fail} failed`)
process.exit(fail === 0 ? 0 : 1)
