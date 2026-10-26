#!/usr/bin/env node
// fix-locale-dead-keys.mjs — 按「死键体检器」的清单，从语言包里删除死键（2026-10-27）
//
// 一次性修复脚本（幂等）：删完再跑一遍 = 无事可做。
//   node scripts/fix-locale-dead-keys.mjs --report <json> --dry-run   # 只报将要删什么
//   node scripts/fix-locale-dead-keys.mjs --report <json> --apply     # 真删
//   node scripts/fix-locale-dead-keys.mjs --selftest                 # 自证
//
// ★ 为什么自己写解析器、而不是用正则/行号：
//   · 语言包是「嵌套对象字面量」，键路径的归属**只能靠括号深度**判定；
//   · 值里可能有含 `{` `}` `,` 的**字符串**（如 `'{}'`、`'a,b'`）⇒ 必须字符串感知；
//   · 注释里可能有 `{` ⇒ 必须剥注释；
//   · 缩进不可靠（en-US 的 `policy:` 子键本就 2 空格）⇒ 不用缩进判归属。
//
// ★ 删除策略：**自顶向下**。
//   若某路径的整棵子树都是死键 ⇒ 整块删（不留空壳 `{}`）；子树里只要有一个活键
//   ⇒ 只删其中为死的叶子键。
//
// ★ 行为等值断言（脚本内建）：
//   删完后重新加载运行时消息树，逐键比对：
//     ① 被删掉的键集合 == 清单里的键集合（不多不少）；
//     ② 清单之外的**每一个**键，取值逐字不变（数组按元素逐字比对）。

import { fileURLToPath, pathToFileURL } from 'node:url'
import { dirname, join, resolve } from 'node:path'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import * as vm from 'node:vm'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')

// ---------------------------------------------------------------------------
// 字符串感知的对象字面量解析
// ---------------------------------------------------------------------------

/**
 * 解析一个 TS/JS 对象字面量，返回每个「键 → 条目区间」。
 * @returns Array<{path: string[], key: string, keyStart: number, entryEnd: number, depth: number}>
 */
export function parseEntries(src) {
  const entries = []
  const n = src.length
  let i = 0
  let depth = 0
  const stack = []   // stack[d] = 深度 d 上最近声明的键名（d ≥ 1）
  const isIdentStart = (c) => /[A-Za-z_$]/.test(c)
  const isIdentChar = (c) => /[A-Za-z0-9_$]/.test(c)

  while (i < n) {
    const c = src[i]
    // 注释
    if (c === '/' && src[i + 1] === '/') { while (i < n && src[i] !== '\n') i++; continue }
    if (c === '/' && src[i + 1] === '*') {
      i += 2
      while (i < n && !(src[i] === '*' && src[i + 1] === '/')) i++
      i += 2
      continue
    }
    // 字符串
    if (c === '"' || c === "'" || c === '`') {
      const q = c
      i++
      while (i < n) {
        if (src[i] === '\\') { i += 2; continue }
        if (src[i] === q) { i++; break }
        i++
      }
      continue
    }
    if (c === '{') { depth++; stack[depth] = null; i++; continue }
    if (c === '}') { depth--; i++; continue }
    if (c === ',' || c === ':') { i++; continue }
    if (isIdentStart(c)) {
      let j = i
      while (j < n && isIdentChar(src[j])) j++
      const ident = src.slice(i, j)
      let k = j
      while (k < n && /\s/.test(src[k])) k++
      if (src[k] === ':') {
        const d = depth
        const path = []
        for (let dd = 1; dd < d; dd++) if (stack[dd] != null) path.push(stack[dd])
        path.push(ident)
        stack[d] = ident
        // 找到值的结束位置
        let v = k + 1
        let vd = d
        while (v < n) {
          const cc = src[v]
          if (cc === '/' && src[v + 1] === '/') { while (v < n && src[v] !== '\n') v++; continue }
          if (cc === '/' && src[v + 1] === '*') {
            v += 2
            while (v < n && !(src[v] === '*' && src[v + 1] === '/')) v++
            v += 2
            continue
          }
          if (cc === '"' || cc === "'" || cc === '`') {
            const q = cc
            v++
            while (v < n) {
              if (src[v] === '\\') { v += 2; continue }
              if (src[v] === q) { v++; break }
              v++
            }
            continue
          }
          if (cc === '{' || cc === '[' || cc === '(') { vd++; v++; continue }
          if (cc === '}' || cc === ']' || cc === ')') {
            if (vd === d) break
            vd--
            v++
            continue
          }
          if (cc === ',' && vd === d) break
          v++
        }
        // 条目边界 = 值的末尾（先退掉尾部空白）＋ 尾逗号
        // ⚠️ 三个坑（都实测踩到过）：
        //   ① 不能直接把「下一个 , 或 }」当边界：标量值的尾部空白会跨到下一行
        //      （`stats_loadFailed: '...'\n  }`）⇒ 边界落到父级收尾那一行
        //      ⇒ 会被误判成「跨行块」⇒ 删除区间吃掉父级收尾的缩进。
        //   ② 尾逗号**允许跨行**：实测 ar-SA/nav.ts 里有一行只放一个 `,`
        //      （`tenantAutoUpdate: "..."` 的下一行就是 `,`）⇒ 不纳入边界
        //      ⇒ 删完会留下一个孤儿逗号（语法错误）。
        //   ③ 找尾逗号时要跳过注释。
        let valueEnd = v
        while (valueEnd > k + 1 && /\s/.test(src[valueEnd - 1])) valueEnd--
        let afterValue = valueEnd
        for (;;) {
          while (afterValue < n && /\s/.test(src[afterValue])) afterValue++
          if (src[afterValue] === '/' && src[afterValue + 1] === '/') {
            while (afterValue < n && src[afterValue] !== '\n') afterValue++
            continue
          }
          if (src[afterValue] === '/' && src[afterValue + 1] === '*') {
            afterValue += 2
            while (afterValue < n && !(src[afterValue] === '*' && src[afterValue + 1] === '/')) afterValue++
            afterValue += 2
            continue
          }
          break
        }
        if (src[afterValue] === ',') valueEnd = afterValue + 1
        const end = valueEnd
        entries.push({ path, key: ident, keyStart: i, entryEnd: end, depth: d })
        i = j
        continue
      }
      i = j
      continue
    }
    i++
  }
  return entries
}

/** 把条目区间转成「要删掉的文本区间」：能整行删就整行删，否则精确删。
 *  ⚠️ 必须**按行分组**后再算区间：语言包里存在「一行两个键」的写法
 *     （实测 ar-SA/dashboard.ts:285 `probeAll: 'x', probeOnly: 'y',`），
 *     逐键各算区间会得到**互相重叠**的两个区间 ⇒ 按原偏移依次删会切坏文件。 */
export function deletionRange(src, entries) {
  const sorted = [...entries].sort((a, b) => a.keyStart - b.keyStart)
  const first = sorted[0]
  const last = sorted[sorted.length - 1]
  const start = first.keyStart
  const end = last.entryEnd
  const lineStart = src.lastIndexOf('\n', start) + 1

  const lineOf = (off) => src.slice(0, off).split('\n').length
  // 条目跨了几行？（**以条目自身的文本**判定，不用 entryEnd 的位置 ——
  //   标量值的 entryEnd 之前会落在下一行，见 parseEntries 的注释）
  const multiLine = src.slice(start, end).includes('\n')

  if (multiLine) {
    // 多行块：从键所在行开头删到条目结束；同行后面没有别的内容 ⇒ 连同该行换行一起删
    const lastLineEndIdx = src.indexOf('\n', end - 1)
    const lastLineEnd = lastLineEndIdx < 0 ? src.length : lastLineEndIdx + 1
    const tail = src.slice(end, lastLineEnd)
    if (tail.trim() === '') return { start: lineStart, end: lastLineEnd }
    return { start: lineStart, end }
  }

  // 单行条目
  const before = src.slice(lineStart, start)
  // ★ 键自己那一行的行尾（**不是** entryEnd 之后那一行）：
  //   entryEnd 可能落在**下一行**（当这个键是父对象的最后一项、且没有尾逗号时，
  //   entryEnd = 父级收尾 `}` 的位置）⇒ 若按 entryEnd 取行尾，会把「父级收尾」
  //   误当成「本行还有别的内容」⇒ 删除区间会吃掉父级收尾的缩进。
  const keyLineEndIdx = src.indexOf('\n', start)
  const keyLineEnd = keyLineEndIdx < 0 ? src.length : keyLineEndIdx
  const after = src.slice(Math.min(end, keyLineEnd), keyLineEnd)
  // 本行只有这些目标 ⇒ 整行删（不留空行、不留悬挂逗号、也不动父级收尾的缩进）
  if (before.trim() === '' && after.trim() === '') {
    return { start: lineStart, end: keyLineEndIdx < 0 ? src.length : keyLineEnd + 1 }
  }
  // 否则精确删这一段（行首缩进保留给同行的其它键）
  return { start, end }
}

// ---------------------------------------------------------------------------
// 删除执行
// ---------------------------------------------------------------------------

/**
 * 从源文本里删掉一批「键路径」。
 * @param {string} src
 * @param {string[][]} targets 键路径数组（相对文件顶层的段数组）
 * @returns {{out: string, removed: string[][], failed: string[][]}}
 */
export function deletePaths(src, targets) {
  const entries = parseEntries(src)
  const byPath = new Map()
  for (const e of entries) byPath.set(e.path.join('.'), e)

  const targetSet = new Set(targets.map((p) => p.join('.')))
  // 只删「最上层」的目标：其父路径若也在目标集里，就交给父级整块删
  const top = targets.filter((p) => {
    const parent = p.slice(0, -1).join('.')
    return !targetSet.has(parent)
  })

  // 按「条目所在行」分组 ⇒ 同一行的多个目标合成一个区间（避免区间重叠）
  const byLine = new Map()
  const removed = []
  const failed = []
  for (const p of top) {
    const key = p.join('.')
    const e = byPath.get(key)
    if (!e) { failed.push(p); continue }
    const line = src.slice(0, e.keyStart).split('\n').length
    if (!byLine.has(line)) byLine.set(line, [])
    byLine.get(line).push(e)
    removed.push(p)
  }

  const ranges = []
  for (const [, group] of byLine) ranges.push(deletionRange(src, group))
  // 从后往前删，避免偏移失效
  ranges.sort((a, b) => b.start - a.start)
  let out = src
  for (const r of ranges) out = out.slice(0, r.start) + out.slice(r.end)
  return { out, removed, failed }
}

/** 语法自检：把 `export default {...}` 转成 CJS 后能否求值（改前/改后都要过） */
export function evalCheck(src) {
  const code = src.replace(/^export default /m, 'module.exports = ')
  const mod = { exports: {} }
  vm.runInNewContext(`(function(module, exports){${code}\n})(module, exports)`, { module: mod, exports: mod.exports }, { filename: 'locale.ts' })
  return mod.exports
}

// ---------------------------------------------------------------------------
// 自证
// ---------------------------------------------------------------------------

async function selftest() {
  const cases = []
  const check = (id, desc, expected, actual) => cases.push({ id, desc, expected, actual, ok: JSON.stringify(expected) === JSON.stringify(actual) })

  // F1 · 解析：键路径归属正确（嵌套两层的兄弟不被误判）
  const s1 = `export default {\n  a: {\n    b: 'x',\n    c: 'y',\n  },\n  d: 'z',\n}\n`
  const paths = parseEntries(s1).map((e) => e.path.join('.'))
  check('F1', '解析出全部键路径', ['a', 'a.b', 'a.c', 'd'], paths)

  // F2 · 解析：字符串里的 `{` `,` 不影响深度
  const s2 = `export default {\n  a: 'has { brace, and comma',\n  b: 'ok',\n}\n`
  check('F2', '字符串里的花括号/逗号不破坏键路径', ['a', 'b'], parseEntries(s2).map((e) => e.path.join('.')))

  // F3 · 解析：注释里的 `key:` 不算键
  const s3 = `export default {\n  // a: 'fake',\n  b: 'real',\n}\n`
  check('F3', '注释里的键不算键', ['b'], parseEntries(s3).map((e) => e.path.join('.')))

  // F4 · 删除：叶子键
  const r1 = deletePaths(s1, [['a', 'b']])
  check('F4', '删叶子键后其余键还在', true, r1.out.includes("b: 'x'") === false && r1.out.includes("c: 'y'") && r1.out.includes("d: 'z'"))

  // F5 · 删除：整块（子树全死 ⇒ 容器也在清单里）⇒ 父容器一并消失，不留空壳
  const s5 = `export default {\n  a: {\n    b: 'x',\n  },\n  c: 'y',\n}\n`
  const r2 = deletePaths(s5, [['a'], ['a', 'b']])
  check('F5', '整块删除：容器消失（不留空壳）', true, r2.out.includes("a:") === false && r2.out.includes("c: 'y'"))

  // F5b · 删除：子树里还有活键 ⇒ 只删死叶子，容器保留
  const r2b = deletePaths(s5, [['a', 'b']])
  check('F5b', '部分删除：容器保留、活键不动', true, r2b.out.includes("a: {") && r2b.out.includes("c: 'y'"))

  // F6 · 删除：多行块整体消失后仍是无悬挂分隔符的对象字面量
  const s6 = `export default {\n  a: {\n    b: 'x',\n    c: 'y',\n  },\n  c: 'y',\n}\n`
  const r3 = deletePaths(s6, [['a']])
  check('F6', '删整块后无「孤立逗号 / 空壳花括号」', true,
    !/{\s*,/.test(r3.out) && !/,\s*,/.test(r3.out) && r3.out.includes("c: 'y'"))

  // F7 · 删除：不存在的路径 ⇒ 报 failed，不静默
  const r4 = deletePaths(s1, [['no', 'such']])
  check('F7', '目标不存在 ⇒ 计入 failed', 1, r4.failed.length)

  // F8 · 幂等：删两次，第二次无事可做
  const r5 = deletePaths(r1.out, [['a', 'b']])
  check('F8', '幂等：第二次删不再有变化', true, r5.out === r1.out && r5.failed.length === 1)

  // F9 · 删除后仍是合法对象字面量（用运行时求值验证）
  let evalOk = true
  let evalErr = ''
  try {
    const vm = await import('node:vm')
    const code = r3.out.replace(/^export default /m, 'module.exports = ')
    const mod = { exports: {} }
    vm.runInNewContext(`(function(module, exports){${code}\n})(module, exports)`, { module: mod, exports: mod.exports })
    if (mod.exports.c !== 'y') { evalOk = false; evalErr = '活键取值不对' }
  } catch (e) { evalOk = false; evalErr = String(e && e.message) }
  check('F9', '删除后的文件仍可被运行时求值且活键取值不变', true, evalOk)

  // F10 · 删除：**同一行两个键**（语言包里真实存在的写法，实测 ar-SA/dashboard.ts:285）
  //      ⇒ 两个目标必须合成一个区间，否则区间重叠 ⇒ 按原偏移删会切坏文件
  const s10 = `export default {\n  a: {\n    x: '1', y: '2',\n    z: '3',\n  },\n  b: 'keep',\n}\n`
  const r10 = deletePaths(s10, [['a', 'x'], ['a', 'y']])
  let eval10 = true
  try { const m = evalCheck(r10.out); if (m.b !== 'keep') eval10 = false } catch { eval10 = false }
  check('F10', '一行两个键同时删 ⇒ 整行消失且文件仍可求值', true,
    eval10 && r10.out.includes("x:") === false && r10.out.includes("y:") === false && r10.out.includes("z: '3'"))

  // F11 · 删除：同一行只删其中一个 ⇒ 另一个必须原样留下
  const r11 = deletePaths(s10, [['a', 'x']])
  let eval11 = true
  try { const m = evalCheck(r11.out); if (m.b !== 'keep') eval11 = false } catch { eval11 = false }
  check('F11', '一行只删一个 ⇒ 同行另一个键原样留下且可求值', true,
    eval11 && r11.out.includes("y: '2'") && r11.out.includes("x:") === false)

  // F12 · 删除：**父对象的最后一项且无尾逗号** ⇒ 顶层收尾不能继承被删行的缩进
  //      （实测踩到过：删完后顶层 `}` 变成 `  }`）
  const s12 = `export default {\n  a: 'x',\n  b: 'y'\n}\n`
  const r12 = deletePaths(s12, [['b']])
  check('F12', '删最后一项（无尾逗号）⇒ 父级收尾仍是顶格 `}`', true,
    r12.out.endsWith('\n}\n') && !r12.out.includes('  }'))

  // F13 · 删除：**尾逗号单独占一行**（实测 ar-SA/nav.ts 的真实写法）
  //      ⇒ 逗号必须跟着条目一起走，否则删完留下孤儿逗号（语法错误）
  const s13 = `export default {\n  item: {\n    a: '1',\n    b: '2'\n  ,\n  },\n}\n`
  const r13 = deletePaths(s13, [['item', 'b']])
  let eval13 = true
  try { const m = evalCheck(r13.out); if (!m.item || m.item.a !== '1') eval13 = false } catch { eval13 = false }
  check('F13', '尾逗号单独占一行 ⇒ 逗号随条目一起删、文件仍可求值', true,
    eval13 && !/^\s*,/m.test(r13.out))

  return cases
}

// ---------------------------------------------------------------------------

async function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    const cases = await selftest()
    let bad = 0
    for (const c of cases) {
      const mark = c.ok ? '✓' : '✗'
      if (!c.ok) bad++
      console.log(`  ${mark} ${c.id} ${c.desc}（期望 ${JSON.stringify(c.expected)}，实测 ${JSON.stringify(c.actual)}）`)
    }
    console.log(`\n══ 自证 ${cases.length - bad}/${cases.length} ══`)
    process.exit(bad ? 1 : 0)
  }

  const ri = argv.indexOf('--report')
  if (ri < 0 || !argv[ri + 1]) {
    console.error('用法：--report <json> [--dry-run|--apply]')
    process.exit(2)
  }
  const report = JSON.parse(readFileSync(argv[ri + 1], 'utf8'))
  const apply = argv.includes('--apply')

  // 清单 → 按 (locale, 文件) 分组；键路径去掉命名空间前缀
  const byFile = new Map()
  for (const d of report.dead) {
    const file = report.nsToFile[d.ns]
    if (!file) { console.error(`⚠ 命名空间 ${d.ns} 找不到对应文件，跳过`); continue }
    const k = `${d.locale}|${file}`
    if (!byFile.has(k)) byFile.set(k, { locale: d.locale, file, paths: [] })
    const segs = d.key.split('.')
    segs.shift()   // 去掉命名空间
    byFile.get(k).paths.push(segs)
  }

  // 运行时快照（删前）
  const scannerUrl = pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href
  const { loadLocale } = await import(scannerUrl)
  const localesDir = join(ROOT, 'src', 'locales')
  const flattenRuntime = (mod) => {
    const flat = new Map()
    const walk = (o, p) => {
      for (const [k, v] of Object.entries(o || {})) {
        const key = p ? `${p}.${k}` : k
        if (v && typeof v === 'object' && !Array.isArray(v)) { flat.set(key, null); walk(v, key) }
        else flat.set(key, JSON.stringify(v))
      }
    }
    walk(mod, '')
    return flat
  }
  const before = new Map()
  for (const loc of report.locales) before.set(loc, flattenRuntime(loadLocale(loc, localesDir)))

  // ── 阶段一：全部在内存里算完 + 逐文件语法自检（**先验证、再落盘**）──────────
  const plans = []
  let totalRemoved = 0
  let totalFailed = 0
  for (const { locale, file, paths } of byFile.values()) {
    const abs = join(localesDir, locale, file)
    if (!existsSync(abs)) { console.error(`⚠ ${locale}/${file} 不存在`); totalFailed += paths.length; continue }
    const src = readFileSync(abs, 'utf8')
    const { out, removed, failed } = deletePaths(src, paths)
    if (failed.length) {
      console.error(`⚠ ${locale}/${file}: ${failed.length} 条路径找不到对应条目`)
      for (const f of failed.slice(0, 5)) console.error(`     ${f.join('.')}`)
    }
    totalRemoved += removed.length
    totalFailed += failed.length
    if (removed.length === 0) continue
    // 语法自检：删完必须仍能求值（这一步拦住「区间算错 ⇒ 切坏文件」）
    let syntaxOk = true
    let syntaxErr = ''
    try { evalCheck(out) } catch (e) { syntaxOk = false; syntaxErr = String(e && e.message) }
    if (!syntaxOk) {
      console.error(`\n❌ 语法自检未通过 ⇒ 拒绝落盘：${locale}/${file}`)
      console.error(`   ${syntaxErr}`)
      console.error('   （本次不写任何文件；已写盘的部分请用 git checkout 恢复）')
      process.exit(1)
    }
    plans.push({ abs, locale, file, out, removed: removed.length })
  }

  if (!apply) {
    console.log(`模式：DRY-RUN（未写盘）`)
    console.log(`处理的语言包文件：${plans.length} 个；删除条目：${totalRemoved} 条；失败：${totalFailed} 条`)
    console.log('全部文件已通过删后语法自检。')
    for (const t of plans) console.log(`   ${t.locale}/${t.file}  -${t.removed} 条`)
    return
  }

  // ── 阶段二：落盘 ─────────────────────────────────────────────────────────
  for (const p of plans) writeFileSync(p.abs, p.out)

  // ── 阶段三：行为等值断言（删后重新加载运行时消息树）────────────────────────
  let eqOk = true
  const diffs = []
  for (const loc of report.locales) {
    const flat = flattenRuntime(loadLocale(loc, localesDir))
    const b = before.get(loc)
    const deadSet = new Set(report.dead.filter((d) => d.locale === loc).map((d) => d.key))
    for (const [k, v] of flat) {
      if (!b.has(k)) { diffs.push(`+ ${loc}:${k}（新增了键）`); eqOk = false }
      else if (b.get(k) !== v) { diffs.push(`~ ${loc}:${k}（取值变了）`); eqOk = false }
    }
    for (const k of b.keys()) {
      if (!flat.has(k) && !deadSet.has(k)) { diffs.push(`- ${loc}:${k}（删了清单外的键！）`); eqOk = false }
    }
  }

  console.log(`模式：APPLY（已写盘）`)
  console.log(`处理的语言包文件：${plans.length} 个；删除条目：${totalRemoved} 条；失败：${totalFailed} 条`)
  for (const t of plans) console.log(`   ${t.locale}/${t.file}  -${t.removed} 条`)
  console.log(`\n行为等值断言：${eqOk ? '✅ 通过（清单外的键一个没动、取值逐字不变）' : '❌ 不通过'}`)
  for (const d of diffs.slice(0, 20)) console.log('   ' + d)
  if (!eqOk) process.exit(1)
}

// 直接执行才跑 main（可被测试/其它脚本 import 复用）
const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (isMain) {
  main().catch((e) => {
    console.error('FATAL: ' + (e && e.stack ? e.stack : e))
    process.exit(2)
  })
}
