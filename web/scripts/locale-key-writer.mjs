#!/usr/bin/env node
// locale-key-writer.mjs — 按「键 → 值」批量写语言包（2026-10-27，第 11 增量）
//
// ★ 为什么需要它：第 11 增量要同时改 8 个语种、约 99 个键。
//   让人手改 8×83 处 = 8 份必然漂移的抄写 ⇒ 键集合/占位符很容易对不上。
//   本脚本让「键」是**唯一事实源**：一份 {key: value} JSON ⇒ 8 个语种同构落地。
//
// ★ 实现要点：
//   ① **AST 定位**：用 TypeScript 解析器找属性节点 ⇒ 不会误伤字符串里出现的同形文本。
//   ② **保格式**：只替换「值字面量」这一段文本，其余原样保留。
//   ③ **缺段自动创建**：路径里缺的中间层（`probeHealth.detail.*`）会按仓内 2 空格缩进建出来。
//   ④ **命名空间 = 文件名**：`common.feedback.x` ⇒ `src/locales/<locale>/common.ts`。
//      唯一例外：`public` ⇒ `publicPortal.ts`。
//   ⑤ **占位符原样**：值里已有的 `{name}` 不会被改写（本脚本只做整体替换）。
//
// 用法：
//   node scripts/locale-key-writer.mjs --locale zh-CN --keys /tmp/keys.json [--dry-run]
//   node scripts/locale-key-writer.mjs --locale zh-CN --keys /tmp/keys.json --check
//
// --check = 只校验（键是否已存在、值是否一致、占位符是否一致），不写盘。

import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const LOCALES_DIR = join(WEB, 'src', 'locales')

/** 命名空间 → 文件名（唯一例外：public ⇒ publicPortal）。 */
const NS_FILE = { public: 'publicPortal' }

function fileFor(locale, ns) {
  const base = NS_FILE[ns] ?? ns
  return join(LOCALES_DIR, locale, `${base}.ts`)
}

/** 把值渲染成单引号字面量（仓内惯例）。 */
function lit(value) {
  return `'${String(value)
    .replace(/\\/g, '\\\\')
    .replace(/'/g, "\\'")
    .replace(/\n/g, '\\n')
    .replace(/\r/g, '\\r')
    .replace(/\t/g, '\\t')}'`
}

/**
 * 取对象字面量里名为 name 的属性（只认 PropertyAssignment）。
 *
 * ★ 第 16 增量修正：**数字键**也必须认。
 *   `errors.byStatus.400` 在语言包里是 `400: '…'`（NumericLiteral）。
 *   第一版只认 Identifier / StringLiteral ⇒ 数字键被当成「缺失」
 *   ⇒ 写入器会**追加一个同名重复键**（`400: '新值'` 出现在旧值下面）
 *   ⇒ 对象字面量出现重复键 ⇒ 运行时取到的是**最后一个**（看起来「写了但没生效」）。
 *   ⇒ 这正是「静默的错误落地」：工具报告「已写入」，值却可能没换掉。
 */
function findProp(objNode, name) {
  for (const p of objNode.properties) {
    if (!ts.isPropertyAssignment(p)) continue
    const n = p.name
    const key = ts.isIdentifier(n) ? n.text
      : ts.isStringLiteral(n) ? n.text
      : ts.isNumericLiteral(n) ? String(n.text)
      : null
    if (key === name) return p
  }
  return null
}

/** 对象字面量内部属性的缩进（取第一个有缩进的属性；空对象回退给父级 +2）。 */
function childIndent(sf, objNode, fallback) {
  for (const p of objNode.properties) {
    const line = sf.getLineAndCharacterOfPosition(p.getStart(sf)).line
    const text = sf.text.split('\n')[line]
    const m = /^\s*/.exec(text)
    if (m && m[0].length) return m[0]
  }
  return fallback
}

/**
 * 递归构建一段缺失的子树文本（每行都带正确缩进）。
 * @param segs 缺失的路径段（从当前层开始）
 * @param value 叶子值
 * @param baseIndent 当前层子属性的缩进（如 '  ' 或 '    '）
 */
function buildSubtree(segs, value, baseIndent) {
  const lines = []
  let depth = 0
  for (let i = 0; i < segs.length; i++) {
    const seg = segs[i]
    const pad = baseIndent + '  '.repeat(depth)
    const last = i === segs.length - 1
    lines.push(last ? `${pad}${seg}: ${lit(value)},` : `${pad}${seg}: {`)
    if (!last) depth++
  }
  for (let d = depth - 1; d >= 0; d--) {
    lines.push(baseIndent + '  '.repeat(d) + '},')
  }
  return lines.join('\n')
}

/**
 * 把一个键写进某个 locale 文件。
 * @returns {{changed:boolean, detail:string}}
 */
export function writeKey(locale, key, value, dryRun = false) {
  const segs = key.split('.')
  const ns = segs[0]
  const file = fileFor(locale, ns)
  if (!existsSync(file)) return { changed: false, detail: `文件不存在：${file}` }
  const src = readFileSync(file, 'utf8')
  const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)

  // 找 `export default { … }`
  let root = null
  for (const st of sf.statements) {
    if (ts.isExportAssignment(st) && ts.isObjectLiteralExpression(st.expression)) {
      root = st.expression
      break
    }
  }
  if (!root) return { changed: false, detail: '找不到 export default 对象' }

  const edits = []
  /**
   * @param objNode 当前对象字面量
   * @param rest 剩余路径段（**已剥掉命名空间段**）
   * @param objIndent 当前对象的**子属性**缩进
   */
  const walk = (objNode, rest, objIndent) => {
    const name = rest[0]
    const prop = findProp(objNode, name)
    if (!prop) {
      // 整段缺失 ⇒ 在对象末尾追加（先补一个逗号，再追加新块）
      const last = objNode.properties.length ? objNode.properties[objNode.properties.length - 1] : null
      const at = last ? last.getEnd() : objNode.getStart(sf) + 1
      // ★ 尾逗号是**独立 token**，在属性节点**之外** ⇒ 插入点必须落在它的**后面**。
      //   （第一版插在属性末尾 ⇒ 原逗号被甩到最后一条新属性后面 ⇒ 语法错；
      //     第二版改看 getText().endsWith(',') ⇒ 永远判成「没有逗号」
      //     ⇒ 每次追加都多补一个逗号 ⇒ `highlightOff: '…',,` 也是语法错。）
      const tail = src.slice(at)
      const mComma = tail.match(/^([ \t\r\n]*)(,)/)
      let insertAt = at
      let needsComma = false
      if (mComma) {
        insertAt = at + mComma[0].length // 落在尾逗号之后
      } else if (last) {
        needsComma = true // 原本没有尾逗号 ⇒ 补一个
      }
      const block = buildSubtree(rest, value, objIndent)
      edits.push({
        pos: insertAt,
        end: insertAt,
        text: `${needsComma ? ',' : ''}\n${block}`,
      })
      return
    }
    if (rest.length === 1) {
      const init = prop.initializer
      if (ts.isStringLiteral(init) && init.text === value) return // 已是目标值
      edits.push({ pos: init.getStart(sf), end: init.getEnd(), text: lit(value) })
      return
    }
    if (!ts.isObjectLiteralExpression(prop.initializer)) {
      throw new Error(`${key}: ${name} 不是对象，无法写入子键`)
    }
    walk(prop.initializer, rest.slice(1), objIndent + '  ')
  }

  try {
    // ★ 命名空间段 = 文件名 ⇒ 文件内路径要**剥掉第一段**。
    //   （第一版忘了剥 ⇒ `common.feedback.loadFailed` 被当成根下的 `common` 段，
    //     结果是「值被追加到根上、原值没被替换」——一次静默的错误落地。）
    walk(root, segs.slice(1), '  ')
  } catch (e) {
    return { changed: false, detail: String(e.message || e) }
  }
  if (!edits.length) return { changed: false, detail: '已是目标值' }

  let out = src
  for (const e of edits.sort((a, b) => b.pos - a.pos)) {
    out = out.slice(0, e.pos) + e.text + out.slice(e.end)
  }
  if (!dryRun) writeFileSync(file, out)
  return { changed: true, detail: `${file} ← ${key}${dryRun ? '（dry-run）' : ''}` }
}

/** 读一个键在某个 locale 里的现值（没有则 undefined）。 */
export function readKey(locale, key) {
  const segs = key.split('.')
  const file = fileFor(locale, segs[0])
  if (!existsSync(file)) return undefined
  const src = readFileSync(file, 'utf8')
  const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  let root = null
  for (const st of sf.statements) {
    if (ts.isExportAssignment(st) && ts.isObjectLiteralExpression(st.expression)) {
      root = st.expression
      break
    }
  }
  if (!root) return undefined
  let node = root
  for (const seg of segs.slice(1)) {
    const prop = findProp(node, seg)
    if (!prop) return undefined
    node = prop.initializer
  }
  if (!ts.isStringLiteral(node)) return undefined
  return node.text
}

/** 取值里的占位符名集合。 */
export function placeholders(value) {
  return [...String(value).matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()
}

// ───────────────────────── CLI ─────────────────────────

function parseArgs(argv) {
  const opts = { locale: '', keys: '', dryRun: false, check: false }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--locale') opts.locale = argv[++i]
    else if (a === '--keys') opts.keys = argv[++i]
    else if (a === '--dry-run') opts.dryRun = true
    else if (a === '--check') opts.check = true
  }
  return opts
}

if (process.argv[1] && process.argv[1].endsWith('locale-key-writer.mjs')) {
  const opts = parseArgs(process.argv.slice(2))
  if (!opts.locale || !opts.keys) {
    console.error('用法：node scripts/locale-key-writer.mjs --locale <locale> --keys <json> [--dry-run|--check]')
    process.exit(2)
  }
  const raw = JSON.parse(readFileSync(opts.keys, 'utf8'))
  const entries = Array.isArray(raw) ? raw.map((x) => [x.key, x.value]) : Object.entries(raw)
  let changed = 0
  const problems = []
  for (const [key, value] of entries) {
    const cur = readKey(opts.locale, key)
    if (opts.check) {
      if (cur === undefined) problems.push(`✗ ${key}：不存在`)
      else if (cur !== value) problems.push(`✗ ${key}：现值「${cur}」≠ 期望「${value}」`)
      else if (placeholders(cur).join(',') !== placeholders(value).join(','))
        problems.push(`✗ ${key}：占位符不一致 ${placeholders(cur)} vs ${placeholders(value)}`)
      continue
    }
    if (opts.dryRun) {
      const r = writeKey(opts.locale, key, value, true)
      if (r.changed) {
        changed++
        console.log(`(dry-run) ${r.detail}`)
      }
      continue
    }
    const r = writeKey(opts.locale, key, value)
    if (r.changed) {
      changed++
      console.log(`✓ ${r.detail}`)
    }
  }
  if (opts.check) {
    if (problems.length) {
      problems.forEach((p) => console.log(p))
      console.error(`\nFAIL: ${problems.length}/${entries.length} 条不符。`)
      process.exit(1)
    }
    console.log(`✅ ${entries.length}/${entries.length} 条全部一致。`)
  } else {
    console.log(`\n${opts.locale}：写入 ${changed}/${entries.length} 条${opts.dryRun ? '（dry-run）' : ''}。`)
  }
}
