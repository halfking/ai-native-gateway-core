// scanner.ts — 全局 i18n 键扫描器（2026-07-07）
//
// 目的：在 CI 阶段一次性找出"源码里 t('xxx') 但 xxx 在任何 locale 文件里都
// 不存在"的所有情况，避免上线后才被用户看到"settings.all"这种 key 字符串。
//
// 三类常见 bug：
//   1. 平铺 key 写错命名空间：t('settings.all') 实际应是 t('settings.category.all')
//   2. 错别字/迁移遗漏：t('sessionsView.dashbord') 应该是 t('sessionsView.dashboard')
//   3. 辅助函数展开遗漏：pd('creds.x') 实际对应 providerDetail.creds.x
//
// 实现策略：
//   - 解析每个 .vue/.ts 文件，按行扫描 `t('literal')` / `tc('literal')` /
//     `HELPER('literal')` 等调用
//   - 静态字符串字面量才纳入审计；包含 `${}` 插值或变量拼接的视为动态
//   - 自动识别辅助函数 `const XX = (k) => t('prefix.' + k)` 并展开调用点
//   - 对每个引用，检查 (1) 在任何 locale 存在 (2) 在源 locale (zh-CN) 存在
//   - 报告 missing keys（带 file:line），未使用 keys（用于清理）
//
// 性能：纯正则 + 字符串处理，扫描 ~150 文件 < 1 秒。

import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import vm from 'node:vm'

// ---------------------------------------------------------------------------
// 类型定义
// ---------------------------------------------------------------------------

export type CallKind = 't' | 'tc' | '$t' | 'i18n.t' | 'helper'

export interface KeyReference {
  /** 源文件绝对路径 */
  file: string
  /** 1-based 行号 */
  line: number
  /** 1-based 列号 */
  column: number
  /** 解析后的完整 i18n key（helper 已展开） */
  key: string
  /** 原始字面量（helper 时是 helper 的入参） */
  raw: string
  /** 调用类型 */
  kind: CallKind
  /** helper 名称（kind=helper 时） */
  helper?: string
  /** 完整调用片段（用于 in-editor 跳转） */
  snippet: string
}

export interface LocaleKeySet {
  /** 已加载的 locale 列表 */
  locales: string[]
  /** 跨 locale 的并集（任一 locale 存在即视为存在） */
  union: Set<string>
  /** 源 locale (zh-CN) 的全集 */
  source: Set<string>
  /** 每个 locale 单独的 key 集（用于"在某些 locale 缺失"定位） */
  perLocale: Map<string, Set<string>>
}

/** 辅助函数展开表：helperName → prefix（不含末尾点） */
export type HelperMap = Map<string, string>

export interface ScanResult {
  references: KeyReference[]
  /** 引用了但 key 不在 union 中的引用 */
  missing: KeyReference[]
  /** 引用了但 key 不在 source locale (zh-CN) 中的引用 */
  missingInSource: KeyReference[]
  /** 在 source 中定义但未被任何源码引用的 keys（可能是死代码） */
  unusedInSource: string[]
  /** 在 source 中定义但当前扫描的所有 locale 都没引用的 keys（未翻译） */
  partiallyUnused: Array<{ key: string; missingIn: string[] }>
  /** 统计 */
  stats: {
    filesScanned: number
    referencesFound: number
    keysInUnion: number
    keysInSource: number
    helpersDetected: number
  }
}

// ---------------------------------------------------------------------------
// Locale 加载
// ---------------------------------------------------------------------------

function evalAsCjs(code: string, absPath: string): Record<string, unknown> {
  const moduleObj: { exports: Record<string, unknown> } = { exports: {} }
  const sandbox = { module: moduleObj }
  vm.createContext(sandbox)
  vm.runInContext(code, sandbox, { filename: absPath })
  return moduleObj.exports
}

function loadModuleFile(locale: string, moduleName: string, localesDir: string): Record<string, unknown> {
  const absPath = join(localesDir, locale, `${moduleName}.ts`)
  if (!existsSync(absPath)) return {}
  const src = readFileSync(absPath, 'utf8')
  const code = src.replace(/^export default /m, 'module.exports = ')
  return evalAsCjs(code, absPath)
}

function loadLocale(locale: string, localesDir: string): Record<string, unknown> {
  const indexPath = join(localesDir, locale, 'index.ts')
  if (!existsSync(indexPath)) return {}
  const src = readFileSync(indexPath, 'utf8')
  const importRe = /^\s*import\s+(\w+)\s+from\s+['"]\.\/(\w+)['"]\s*$/gm
  const merged: Record<string, unknown> = {}
  let m: RegExpExecArray | null
  while ((m = importRe.exec(src)) !== null) {
    const localName = m[1]
    const moduleName = m[2]
    merged[localName] = loadModuleFile(locale, moduleName, localesDir)
  }
  return merged
}

/** 把嵌套对象扁平化为点路径集合。
 *  数组也被视为叶子节点 — Vue i18n 允许 `t('foo.bar')` 返回整个数组
 *  供 `v-for` 渲染（如 sessions.list.tableHeaders、modulesView.integration.feishuSteps）。
 *  若跳过数组，这些引用会被误报为 missing key。 */
export function collectLeafKeys(obj: unknown, prefix = ''): Set<string> {
  const keys = new Set<string>()
  if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) return keys
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (Array.isArray(v)) {
      // 数组作为整体值被引用（t('path') 返回数组），记录其路径为有效 key
      keys.add(path)
    } else if (typeof v === 'object' && v !== null) {
      const nested = collectLeafKeys(v, path)
      nested.forEach((nk) => keys.add(nk))
    } else if (v !== null && v !== undefined) {
      keys.add(path)
    }
  }
  return keys
}

export function loadLocales(localesDir: string, source = 'zh-CN'): LocaleKeySet {
  const entries = readdirSync(localesDir, { withFileTypes: true })
  const locales = entries
    .filter((e) => e.isDirectory())
    .map((e) => e.name)
    .sort()
  const perLocale = new Map<string, Set<string>>()
  const union = new Set<string>()
  for (const loc of locales) {
    const keys = collectLeafKeys(loadLocale(loc, localesDir))
    perLocale.set(loc, keys)
    keys.forEach((k) => union.add(k))
  }
  return {
    locales,
    union,
    source: perLocale.get(source) ?? new Set(),
    perLocale,
  }
}

// ---------------------------------------------------------------------------
// 辅助函数识别
// ---------------------------------------------------------------------------

/** 匹配 helper 定义的常见模式：
 *    const XX = (k: string, ...) => td('prefix.' + k)
 *    const XX = (k: string, ...) => td(`prefix.${k}`)
 *    const XX = (k: string, ...) => td('prefix.' + k as never, params as never)
 */
const HELPER_DEF_RE = /\bconst\s+(\w+)\s*=\s*\(\s*k\s*(?::\s*string)?\s*(?:,\s*[^)]*)?\)\s*:\s*string\s*=>\s*\w+\(\s*(['"`])((?:[^'"`\\]|\\.)*?)\1\s*\+\s*k\b/g
/** 匹配 const XX = (k) => t(`prefix.${k}`) 反引号+插值 */
const HELPER_DEF_TPL_RE = /\bconst\s+(\w+)\s*=\s*\(\s*k\s*(?::\s*string)?\s*(?:,\s*[^)]*)?\)\s*:\s*string\s*=>\s*\w+\(\s*`([^`]+)\$\{k\}`/g

/** 解析单个文件中的 helper 定义 */
export function detectHelpersInFile(src: string): HelperMap {
  const map: HelperMap = new Map()
  const strip = (s: string) => s.replace(/\.+$/, '') // helper 作者常写 'foo.' + k，去掉尾点
  let m: RegExpExecArray | null
  HELPER_DEF_RE.lastIndex = 0
  while ((m = HELPER_DEF_RE.exec(src)) !== null) {
    const name = m[1]
    const prefix = strip(m[3])
    if (prefix) map.set(name, prefix)
  }
  HELPER_DEF_TPL_RE.lastIndex = 0
  while ((m = HELPER_DEF_TPL_RE.exec(src)) !== null) {
    const name = m[1]
    const prefix = strip(m[2])
    if (prefix) map.set(name, prefix)
  }
  return map
}

/** 全局 helper 映射（向后兼容；同名的 helper 取首次出现，新代码用 detectHelpersInFile） */
export function detectHelpers(srcDir: string, srcRoot?: string): HelperMap {
  const files = walkFiles(srcDir, /\.(vue|ts|tsx|js|mjs)$/)
  const map: HelperMap = new Map()
  for (const f of files) {
    if (f.includes('/locales/') || f.includes('/node_modules/')) continue
    const src = readFileSync(f, 'utf8')
    const fileHelpers = detectHelpersInFile(src)
    for (const [name, prefix] of fileHelpers) {
      // 同名 helper 跨文件冲突时保留首次看到的；不覆盖。
      // 推荐用 per-file scan（scanFile(absFile, detectHelpersInFile(src))）以避免歧义。
      if (!map.has(name)) map.set(name, prefix)
    }
  }
  return map
}

// ---------------------------------------------------------------------------
// 引用扫描
// ---------------------------------------------------------------------------

/** 匹配 t('literal') / tc('literal') / $t('literal') / i18n.t('literal') */
const T_CALL_RE = /\b(\$?t|tc|i18n\.t)\s*\(\s*(['"])([a-zA-Z][a-zA-Z0-9_.\-]*)\2/g
/** 匹配 HELPER('literal')（HELPER 在 helperMap 中） */
function helperCallRe(name: string): RegExp {
  return new RegExp(`\\b${name}\\s*\\(\\s*(['"])([a-zA-Z][a-zA-Z0-9_\\.\\-]*)\\1`, 'g')
}

export function scanFile(absFile: string, helpers?: HelperMap): KeyReference[] {
  const src = readFileSync(absFile, 'utf8')
  // 关键修复：每个文件用自己文件内的 helper 定义，避免跨文件同名 helper
  // （如 ModelsTab.vue 和 ProvidersView.vue 都有 `pm`，但前缀不同）冲突。
  const localHelpers = helpers ?? detectHelpersInFile(src)
  const out: KeyReference[] = []

  // 1) t/tc/$t/i18n.t
  T_CALL_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = T_CALL_RE.exec(src)) !== null) {
    const call = m[1]
    const key = m[3]
    const { line, column } = offsetToLineCol(src, m.index)
    const kind: CallKind = call === 'i18n.t' ? 'i18n.t' : (call as CallKind)
    out.push({
      file: absFile,
      line,
      column,
      key,
      raw: key,
      kind,
      snippet: m[0],
    })
  }

  // 2) 该文件内定义的 helpers
  for (const [name, prefix] of localHelpers) {
    const re = helperCallRe(name)
    re.lastIndex = 0
    while ((m = re.exec(src)) !== null) {
      const raw = m[2]
      const full = `${prefix}.${raw}`
      const { line, column } = offsetToLineCol(src, m.index)
      out.push({
        file: absFile,
        line,
        column,
        key: full,
        raw,
        kind: 'helper',
        helper: name,
        snippet: m[0],
      })
    }
  }

  return out
}

export function scanAll(srcDir: string, helpers?: HelperMap): {
  references: KeyReference[]
  filesScanned: number
} {
  const files = walkFiles(srcDir, /\.(vue|ts|tsx|js|mjs)$/).filter(
    (f) => !f.includes('/locales/') && !f.includes('/node_modules/'),
  )
  const references: KeyReference[] = []
  for (const f of files) {
    // per-file helper detection（不传全局 helpers，让 scanFile 自己解析）
    const refs = scanFile(f, helpers)
    references.push(...refs)
  }
  return { references, filesScanned: files.length }
}

// ---------------------------------------------------------------------------
// 完整流程
// ---------------------------------------------------------------------------

export function audit(srcDir: string, localesDir: string, opts?: { sourceLocale?: string }): ScanResult {
  const sourceLocale = opts?.sourceLocale ?? 'zh-CN'
  const locales = loadLocales(localesDir, sourceLocale)
  // 不传全局 helpers map — 让 scanFile 内部按文件检测，避开同名 helper 跨文件冲突
  const { references, filesScanned } = scanAll(srcDir)
  const referencedKeys = new Set<string>()
  references.forEach((r) => referencedKeys.add(r.key))

  const missing: KeyReference[] = []
  const missingInSource: KeyReference[] = []
  for (const r of references) {
    if (!locales.union.has(r.key)) missing.push(r)
    if (!locales.source.has(r.key)) missingInSource.push(r)
  }

  const unusedInSource: string[] = []
  locales.source.forEach((k) => {
    if (!referencedKeys.has(k)) unusedInSource.push(k)
  })

  const partiallyUnused: Array<{ key: string; missingIn: string[] }> = []
  for (const k of unusedInSource) {
    const missingIn: string[] = []
    for (const loc of locales.locales) {
      if (loc === sourceLocale) continue
      if (!locales.perLocale.get(loc)!.has(k)) missingIn.push(loc)
    }
    if (missingIn.length > 0) partiallyUnused.push({ key: k, missingIn })
  }

  return {
    references,
    missing,
    missingInSource,
    unusedInSource: unusedInSource.sort(),
    partiallyUnused,
    stats: {
      filesScanned,
      referencesFound: references.length,
      keysInUnion: locales.union.size,
      keysInSource: locales.source.size,
      // helpersDetected 在 per-file 模式下无意义：同名 helper 在不同文件可能前缀不同。
      // 报告总 helper 种类数（detectHelpers 全局 map）作为参考。
      helpersDetected: detectHelpers(srcDir).size,
    },
  }
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

function walkFiles(dir: string, extRe: RegExp, skipDirs: string[] = ['node_modules', 'dist', '.git', 'i18n']): string[] {
  const out: string[] = []
  const skip = new Set(skipDirs)
  function rec(d: string) {
    const entries = readdirSync(d, { withFileTypes: true })
    for (const e of entries) {
      const p = join(d, e.name)
      if (e.isDirectory()) {
        if (skip.has(e.name) || e.name.startsWith('.')) continue
        rec(p)
      } else if (e.isFile() && extRe.test(e.name) && !/\.test\.ts$/.test(e.name) && !/\.spec\.ts$/.test(e.name)) {
        out.push(p)
      }
    }
  }
  rec(dir)
  return out
}

function offsetToLineCol(src: string, offset: number): { line: number; column: number } {
  let line = 1
  let lastNl = -1
  for (let i = 0; i < offset; i++) {
    if (src.charCodeAt(i) === 10) {
      line++
      lastNl = i
    }
  }
  return { line, column: offset - lastNl }
}

// ---------------------------------------------------------------------------
// 报告格式化
// ---------------------------------------------------------------------------

export function formatReport(r: ScanResult, rootDir?: string): string {
  const rel = (f: string) => (rootDir ? relative(rootDir, f) : f)
  const lines: string[] = []
  lines.push('=== i18n audit report ===')
  lines.push(`files scanned:          ${r.stats.filesScanned}`)
  lines.push(`references found:       ${r.stats.referencesFound}`)
  lines.push(`keys in union:          ${r.stats.keysInUnion}`)
  lines.push(`keys in zh-CN (source): ${r.stats.keysInSource}`)
  lines.push(`helpers detected:       ${r.stats.helpersDetected}`)
  lines.push('')

  if (r.missing.length === 0) {
    lines.push('✅ no missing keys (in any locale)')
  } else {
    lines.push(`❌ missing keys (referenced but not in any locale): ${r.missing.length}`)
    const byFile = groupBy(r.missing, (m) => rel(m.file))
    for (const [file, refs] of byFile) {
      lines.push(`  ${file}`)
      for (const r of refs) {
        lines.push(`    ${String(r.line).padStart(4)}:${String(r.column).padStart(3)}  ${r.key}  ${r.snippet}`)
      }
    }
  }
  lines.push('')

  if (r.missingInSource.length > 0) {
    lines.push(`⚠️  referenced keys missing in source locale (zh-CN): ${r.missingInSource.length}`)
    const byFile = groupBy(r.missingInSource, (m) => rel(m.file))
    for (const [file, refs] of byFile) {
      lines.push(`  ${file}`)
      for (const r of refs) {
        lines.push(`    ${String(r.line).padStart(4)}:${String(r.column).padStart(3)}  ${r.key}`)
      }
    }
    lines.push('')
  }

  if (r.unusedInSource.length > 0) {
    lines.push(`ℹ️  unused keys in source locale (no reference in src/): ${r.unusedInSource.length}`)
    const list = r.unusedInSource.slice(0, 50)
    for (const k of list) lines.push(`  - ${k}`)
    if (r.unusedInSource.length > list.length) {
      lines.push(`  ... and ${r.unusedInSource.length - list.length} more`)
    }
  }
  return lines.join('\n')
}

function groupBy<T, K>(arr: T[], fn: (t: T) => K): Map<K, T[]> {
  const m = new Map<K, T[]>()
  for (const x of arr) {
    const k = fn(x)
    const list = m.get(k)
    if (list) list.push(x)
    else m.set(k, [x])
  }
  return m
}
