#!/usr/bin/env node
// i18n-hidden-key-guard.mjs — 「藏在常量里的 i18n 键」门（2026-10-27，第 10 增量）
//
// 背景（实测，不是推演）：
//   本轮一度把品牌标题写成 `export const SITE_TITLE_KEY = 'app.brand.siteTitle'`
//   并在模板里用 `t(SITE_TITLE_KEY)`。⇒ **引用集扫描器完全看不见这个键**
//   （scanner.ts 只认字面量）⇒ 它被算成「未引用」⇒ 于是
//   **它逃出了「用户可见简体残留 = 0」那道硬闸的视野**。
//   实测引用集增量：常量写法 = +11（键少 2 个），字面量写法 = +13。
//
// 规则：
//   i18n 键必须以**字面量**出现在调用点。藏在常量里的键对引用集不可见，
//   因而既不被「缺键」审计覆盖，也不被「用户可见」硬闸覆盖 ⇒ 静默失守。
//
// 判据（窄口径，宁可漏也不误伤）：
//   ① 调用点形如 t(IDENT) / $t(IDENT) / tc(IDENT) / i18n.t(IDENT)，
//      且 IDENT 是**裸标识符**（不是字符串字面量、不是模板串、不是表达式）。
//   ② IDENT 必须能**解析**成一个字符串常量：
//      · 同文件的 `const X = '...'` / `export const X = '...'`，或
//      · 从相对路径 import 进来的模块里的 `export const X = '...'`。
//   ③ 该常量的值必须形如**点分 i18n 键**（`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9_]+)+$`）。
//      ⇒ 存储键（'llmgw_device_name'）、枚举值（'active'）、URL 片段等**不**命中，
//        避免把「碰巧也叫 KEY 的常量」误判成 i18n 键。
//
// 用法：
//   node scripts/i18n-hidden-key-guard.mjs              # 扫描 src/，有违规 exit 1
//   node scripts/i18n-hidden-key-guard.mjs --json
//   node scripts/i18n-hidden-key-guard.mjs --selftest   # 自证：违规→红 / 干净→绿

import { mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const SRC = join(WEB, 'src')

const EXCLUDED_DIRS = new Set(['node_modules', 'dist', '.git', 'locales', 'i18n'])

/** i18n 键形状：至少两段、点分、段名以字母开头。 */
const DOTTED_KEY = /^[a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_]+)+$/
/** 调用点：t( / $t( / tc( / i18n.t( 后紧跟一个裸标识符。 */
const CALL_RE = /\b(?:i18n\.t|\$?t|tc)\(\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*[,)]/g
/** 字符串常量声明：`export const X = '...'` / `const X = '...'`。 */
const CONST_RE = /(?:export\s+)?const\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(['"])((?:\\.|(?!\2)[^\\])*)\2/g
/** import { A, B } from './x' —— 只解析相对路径。 */
const IMPORT_RE = /import\s*\{([^}]*)\}\s*from\s*['"](\.[^'"]*)['"]/g

function walk(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name)
    if (e.isDirectory()) {
      if (EXCLUDED_DIRS.has(e.name) || e.name.startsWith('.')) continue
      walk(p, out)
    } else if (
      e.isFile() &&
      /\.(vue|ts|tsx|js|mjs)$/.test(e.name) &&
      !/\.test\.[cm]?[jt]s?$/.test(e.name) &&
      !/\.spec\.[cm]?[jt]s?$/.test(e.name)
    ) {
      out.push(p)
    }
  }
  return out
}

/** 抽出文件里所有「字符串常量声明」⇒ name → value。 */
function constsOf(src) {
  const map = new Map()
  CONST_RE.lastIndex = 0
  let m
  while ((m = CONST_RE.exec(src)) !== null) {
    const name = m[1]
    const value = m[3].replace(/\\'/g, "'").replace(/\\"/g, '"').replace(/\\\\/g, '\\')
    if (!map.has(name)) map.set(name, value)
  }
  return map
}

/** 抽出文件里从相对路径 import 的具名符号。 */
function relativeImportsOf(src) {
  const out = []
  IMPORT_RE.lastIndex = 0
  let m
  while ((m = IMPORT_RE.exec(src)) !== null) {
    for (const raw of m[1].split(',')) {
      const name = raw.trim().split(/\s+as\s+/).pop()?.trim()
      if (name) out.push({ name, spec: m[2] })
    }
  }
  return out
}

function resolveModule(fromFile, spec) {
  const base = resolve(dirname(fromFile), spec)
  const cands = [base, `${base}.ts`, `${base}.js`, `${base}.vue`, join(base, 'index.ts'), join(base, 'index.js')]
  for (const c of cands) {
    try {
      if (statSync(c).isFile()) return c
    } catch {
      /* not a file */
    }
  }
  return null
}

/**
 * 剥离注释，**保留偏移量**（注释里的非换行字符一律换成空格）。
 *
 * ★ 为什么必须剥：本门的第一版把 `brand.ts` 里那段**说明文案**
 *   （「`t(SITE_TITLE_KEY)` 对扫描器不可见」）判成了违规 ——
 *   一道会把自己的文档判红的门，迟早被人关掉。
 *   （同族：i18n-cjk-count 的注释剥离、copy-quality-audit 的「注释里的引号不算文案」夹具。）
 *
 * ★ 形态学：字符串 / 模板串内部的 `//` `/*` **不是**注释；
 *   模板串里的 `${ ... }` 是**代码**（里面的注释也要剥）。
 */
export function stripComments(src) {
  const out = src.split('')
  const blank = (from, to) => {
    for (let i = from; i < to && i < out.length; i++) {
      if (out[i] !== '\n') out[i] = ' '
    }
  }
  // 帧栈：{type:'sq'|'dq'|'tpl'|'line'|'block'|'code', depth}
  const stack = []
  let i = 0
  const n = src.length
  while (i < n) {
    const f = stack[stack.length - 1]
    const type = f ? f.type : 'code'
    if (type === 'line') {
      if (src[i] === '\n') stack.pop()
      else out[i] = ' '
      i++
      continue
    }
    if (type === 'block') {
      if (src[i] === '*' && src[i + 1] === '/') {
        blank(i, i + 2)
        i += 2
        stack.pop()
        continue
      }
      if (src[i] !== '\n') out[i] = ' '
      i++
      continue
    }
    if (type === 'sq' || type === 'dq') {
      const q = type === 'sq' ? "'" : '"'
      if (src[i] === '\\') {
        i += 2
        continue
      }
      if (src[i] === q) {
        stack.pop()
        i++
        continue
      }
      i++
      continue
    }
    if (type === 'tpl') {
      if (src[i] === '\\') {
        i += 2
        continue
      }
      if (src[i] === '`') {
        stack.pop()
        i++
        continue
      }
      if (src[i] === '$' && src[i + 1] === '{') {
        stack.push({ type: 'code', depth: 0 })
        i += 2
        continue
      }
      i++
      continue
    }
    // code
    if (src[i] === '/' && src[i + 1] === '/') {
      blank(i, i + 2)
      i += 2
      stack.push({ type: 'line' })
      continue
    }
    if (src[i] === '/' && src[i + 1] === '*') {
      blank(i, i + 2)
      i += 2
      stack.push({ type: 'block' })
      continue
    }
    if (src[i] === "'") {
      stack.push({ type: 'sq' })
      i++
      continue
    }
    if (src[i] === '"') {
      stack.push({ type: 'dq' })
      i++
      continue
    }
    if (src[i] === '`') {
      stack.push({ type: 'tpl' })
      i++
      continue
    }
    if (f && f.type === 'code') {
      if (src[i] === '{') {
        f.depth++
        i++
        continue
      }
      if (src[i] === '}') {
        if (f.depth === 0) {
          stack.pop()
          i++
          continue
        }
        f.depth--
        i++
        continue
      }
    }
    i++
  }
  return out.join('')
}

/**
 * 核心检测。`files` 可注入（自证用），默认扫 src/。
 * @returns {{file:string,line:number,col:number,constName:string,key:string}[]}
 */
export function detectHiddenKeys(files) {
  const list = files ?? walk(SRC)
  const srcCache = new Map()
  const constCache = new Map()
  // ★ 读进来先**剥注释**（保留偏移）—— 注释里的 t(IDENT) 不是调用点。
  const readSrc = (p) => {
    if (!srcCache.has(p)) srcCache.set(p, stripComments(readFileSync(p, 'utf8')))
    return srcCache.get(p)
  }
  const constsOfFile = (p) => {
    if (!constCache.has(p)) constCache.set(p, constsOf(readSrc(p)))
    return constCache.get(p)
  }

  const findings = []
  for (const file of list) {
    const src = readSrc(file)
    const local = constsOfFile(file)
    // import 进来的符号 ⇒ 常量表（只解析相对路径的模块）
    const imported = new Map()
    for (const imp of relativeImportsOf(src)) {
      const mod = resolveModule(file, imp.spec)
      if (!mod) continue
      const v = constsOfFile(mod).get(imp.name)
      if (v !== undefined) imported.set(imp.name, v)
    }

    CALL_RE.lastIndex = 0
    let m
    while ((m = CALL_RE.exec(src)) !== null) {
      const ident = m[1]
      const value = local.has(ident) ? local.get(ident) : imported.get(ident)
      if (value === undefined) continue
      if (!DOTTED_KEY.test(value)) continue
      const upto = src.slice(0, m.index)
      findings.push({
        file: relative(WEB, file),
        line: upto.split('\n').length,
        col: m.index - upto.lastIndexOf('\n'),
        constName: ident,
        key: value,
      })
    }
  }
  return findings
}

// ─────────────────────────── 自证 ───────────────────────────
//
// ★ 自证必须覆盖「已证明会把量具打偏」的样本：
//   · 干净样本：字面量调用、动态变量调用（不可解析）、存储键常量（非点分形状）
//   · 违规样本：跨模块 import 的键常量 + 同文件的键常量
//   ⇒ 只测正例的自证，会漏掉「判据根本认不出这一类」这种失效。

const SELFTEST = [
  {
    name: 'A 字面量调用 → 必须判绿',
    files: { 'a.ts': `const x = t('nav.item.overview')` },
    expect: 0,
  },
  {
    name: 'B 动态变量（不可解析）→ 必须判绿',
    files: { 'b.ts': `function f(dynamicKey: string) { return t(dynamicKey) }` },
    expect: 0,
  },
  {
    name: 'C 存储键常量（非点分形状）→ 必须判绿',
    files: { 'c.ts': `const STORAGE_KEY = 'llmgw_device_name'\nconst v = t(STORAGE_KEY)` },
    expect: 0,
  },
  {
    name: 'D 同文件的键常量 → 必须判红',
    files: { 'd.ts': `const SITE_TITLE_KEY = 'app.brand.siteTitle'\nconst v = t(SITE_TITLE_KEY)` },
    expect: 1,
  },
  {
    name: 'E 跨模块 import 的键常量 → 必须判红',
    files: {
      'brand.ts': `export const SITE_TITLE_KEY = 'app.brand.siteTitle'`,
      'e.ts': `import { SITE_TITLE_KEY } from './brand'\nconst v = t(SITE_TITLE_KEY)`,
    },
    expect: 1,
  },
  {
    name: 'G 注释里的 t(CONST) 不是调用点 → 必须判绿',
    files: {
      'g.ts': `// 说明：t(SITE_TITLE_KEY) 对扫描器不可见\n/* t(OTHER_KEY) 也是注释 */\nconst SITE_TITLE_KEY = 'app.brand.siteTitle'\nconst v = 1`,
    },
    expect: 0,
  },
  {
    name: 'H 模板串 ${ } 里的注释要剥，但 ${} 里的调用要抓 → 必须判红 1',
    files: {
      'h.ts': "const K = 'app.brand.alt'\nconst s = `x ${t(K) /* c */} y`",
    },
    expect: 1,
  },
  {
    name: 'F $t / tc / i18n.t 变体 → 必须判红',
    files: {
      'f.ts': `const K = 'app.brand.alt'\nconst a = $t(K)\nconst b = tc(K)\nconst c = i18n.t(K)`,
    },
    expect: 3,
  },
]

function runSelftest() {
  // 自证工件放系统临时目录，**不进仓**（仓内 .tmp-* 会被 git 当未跟踪文件）。
  const tmp = join(tmpdir(), 'i18n-hidden-key-selftest')
  let failed = 0
  for (const c of SELFTEST) {
    const written = []
    for (const [name, content] of Object.entries(c.files)) {
      const p = join(tmp, name)
      mkdirSync(dirname(p), { recursive: true })
      writeFileSync(p, content)
      written.push(p)
    }
    const got = detectHiddenKeys(written).length
    const ok = got === c.expect
    if (!ok) failed++
    console.log(`${ok ? '✅' : '❌'} ${c.name} —— 命中 ${got}（期望 ${c.expect}）`)
    for (const p of written) {
      try {
        rmSync(p)
      } catch {
        /* ignore */
      }
    }
  }
  try {
    rmSync(tmp, { recursive: true, force: true })
  } catch {
    /* ignore */
  }
  if (failed) {
    console.error(`\nFATAL: 自证 ${failed}/${SELFTEST.length} 失败 —— 量具本身不可信，拒绝报告。`)
    process.exit(2)
  }
  console.log(`\n自证 ${SELFTEST.length}/${SELFTEST.length} 全过 —— 量具有牙。`)
}

if (process.argv.includes('--selftest')) {
  runSelftest()
} else {
  const findings = detectHiddenKeys()
  if (process.argv.includes('--json')) {
    console.log(JSON.stringify({ count: findings.length, findings }, null, 2))
  } else {
    for (const f of findings) {
      console.log(`${f.file}:${f.line}:${f.col}  ${f.constName} → '${f.key}'  （键藏在常量里，引用集扫描器看不见）`)
    }
    if (findings.length) {
      console.error(`\nFAIL: ${findings.length} 处 i18n 键藏在常量里 ⇒ 逃出「用户可见」硬闸的视野。`)
      console.error('修法：调用点改写字面量（t(\'app.brand.siteTitle\')）。')
      process.exit(1)
    }
    console.log('✅ 没有「藏在常量里的 i18n 键」—— PASS')
  }
  if (findings.length) process.exit(1)
}
