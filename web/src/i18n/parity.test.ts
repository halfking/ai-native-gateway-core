import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, existsSync } from 'fs'
import { dirname, join } from 'path'
import { fileURLToPath } from 'url'
import vm from 'vm'

// parity.test.ts — i18n key parity gate (added 2026-07-02)
//
// Enforces that every locale exposes a SUPERSET of the zh-CN (source-of-truth)
// leaf keys. This is the test that would have caught the manual scan finding
// where `routing.overview.loading` was missing in en-US + es-ES.
//
// Strategy:
//   1. Load each locale's <locale>/index.ts (and per-module files) via Node's
//      `vm` module — we can't `import` `.ts` files in vitest without bringing
//      in ts-node / a transformer, and we explicitly do NOT want to add deps.
//   2. Recursively collect leaf key paths from the resulting object graph,
//      skipping arrays (we only assert string leaves for now).
//   3. For each non-zh-CN locale, assert zh-CN's leaf key set ⊆ locale's leaf
//      key set. Locales may have *extra* keys (e.g. plural forms, RTL-aware
//      strings), but never a key that zh-CN has.
//   4. Also assert each locale's index.ts imports the same set of module files
//      as zh-CN — this catches the "module entirely missing in a locale" case
//      even when the key set happens to align by coincidence.
//
// NOTE: This test does NOT modify any locale file. It is read-only.

// ---------------------------------------------------------------------------
// Setup — derive __dirname equivalent because the project uses
// module: ESNext, so the global `__dirname` is not guaranteed.
// ---------------------------------------------------------------------------
const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)

// 此文件位于 src/i18n/parity.test.ts，而 locales 目录位于 src/locales，
// 因此需要向上一级。此前写成 join(__dirname, 'locales') 指向了不存在的
// src/i18n/locales，导致整个 parity 测试 ENOENT 失败、从未真正守护 i18n
// 一致性（6 种语言的 audit 命名空间缺失就是因此漏过）。
const LOCALES_DIR = join(__dirname, '..', 'locales')
const SOURCE_LOCALE = 'zh-CN'
const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

// ---------------------------------------------------------------------------
// Loader — read a .ts file, strip `export default`, eval as CommonJS in vm.
// We avoid ESM `import()` of `.ts` files because that would require
// ts-node/esbuild-register, which we deliberately don't add.
//
// Per-module files (common.ts, routing.ts, etc.) only contain
// `export default { ... }` with literal string values, so a plain
// `export default ` → `module.exports = ` rewrite is enough.
//
// index.ts files contain `import x from './x'` statements that need to be
// resolved transitively. We do that by recursively loading each referenced
// module via the same vm-based loader, then returning the merged object
// matching the `export default { common, nav, ... }` shape.
// ---------------------------------------------------------------------------
function evalAsCjs(code: string, absPath: string): Record<string, unknown> {
  // `vm.runInContext` runs a fresh Script — it does NOT provide a `module`
  // global by default. We need to inject one so the rewritten
  // `module.exports = { ... }` statement can assign into our shared object.
  const moduleObj: { exports: Record<string, unknown> } = { exports: {} }
  const sandbox = { module: moduleObj }
  vm.createContext(sandbox)
  vm.runInContext(code, sandbox, { filename: absPath })
  return moduleObj.exports
}

function loadModuleFile(locale: string, moduleName: string): Record<string, unknown> {
  const absPath = join(LOCALES_DIR, locale, `${moduleName}.ts`)
  const src = readFileSync(absPath, 'utf8')
  // Per-module files only ever have a top-level `export default {...}`.
  const code = src.replace(/^export default /m, 'module.exports = ')
  return evalAsCjs(code, absPath)
}

function loadLocale(locale: string): Record<string, unknown> {
  const indexPath = join(LOCALES_DIR, locale, 'index.ts')
  const src = readFileSync(indexPath, 'utf8')

  // Resolve every `import x from './x'` line by recursively loading that
  // module file with loadModuleFile. We don't need vm for index.ts because
  // the `export default { a, b, ... }` literal is just an object-spread —
  // we can build it directly from the per-module exports we just collected.
  const importRe = /^\s*import\s+(\w+)\s+from\s+['"]\.\/(\w+)['"]\s*$/gm
  const merged: Record<string, unknown> = {}
  let m: RegExpExecArray | null
  while ((m = importRe.exec(src)) !== null) {
    const localName = m[1]
    const moduleName = m[2]
    merged[localName] = loadModuleFile(locale, moduleName)
  }
  return merged
}

// ---------------------------------------------------------------------------
// Leaf-key collector — recurse into nested objects, skip arrays, return the
// set of dotted key paths whose value is a primitive (string/number/boolean).
// ---------------------------------------------------------------------------
type JsonScalar = string | number | boolean | null
type JsonObject = { [k: string]: JsonScalar | JsonObject | unknown[] }

function isPlainObject(v: unknown): v is JsonObject {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

export function collectLeafKeys(obj: unknown, prefix = ''): Set<string> {
  const keys = new Set<string>()
  if (!isPlainObject(obj)) return keys
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (Array.isArray(v)) {
      // We don't recurse into arrays — they're typically string lists
      // (e.g. `[role1, role2]`) and don't represent i18n leaves in this
      // codebase. Including them would create false positives.
      continue
    }
    if (isPlainObject(v)) {
      const nested = collectLeafKeys(v, path)
      nested.forEach((nk) => keys.add(nk))
    } else {
      // Leaf — only count primitives, ignore null/undefined.
      if (v === null || v === undefined) continue
      keys.add(path)
    }
  }
  return keys
}

// ---------------------------------------------------------------------------
// Module-name extractor — parse `import x from './x'` lines from index.ts.
// This gives us the set of modules each locale's index.ts wires up, which
// is the "module registration parity" check (#7 in the brief).
// ---------------------------------------------------------------------------
function extractModuleNames(indexTsPath: string): Set<string> {
  const src = readFileSync(indexTsPath, 'utf8')
  const names = new Set<string>()
  // Match `import foo from './foo'` or `import foo from "./foo"`.
  const re = /^\s*import\s+\w+\s+from\s+['"]\.\/(\w+)['"]/gm
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) {
    names.add(m[1])
  }
  return names
}

// ---------------------------------------------------------------------------
// Diff helper — return sorted list of keys present in `source` but missing
// from `target`. Used to produce the failure message described in the brief.
// ---------------------------------------------------------------------------
function missingKeys(source: Set<string>, target: Set<string>): string[] {
  const out: string[] = []
  source.forEach((k) => {
    if (!target.has(k)) out.push(k)
  })
  return out.sort()
}

function formatMissingBlock(locale: string, module: string, missing: string[]): string {
  const header = `Module: ${module}, Locale: ${locale}\nMissing keys (${missing.length}):`
  const body = missing.map((k) => `  - ${k}`).join('\n')
  return `${header}\n${body}`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------
describe('i18n parity gate', () => {
  it('source-of-truth zh-CN has leaf keys (sanity)', () => {
    const zhKeys = collectLeafKeys(loadLocale(SOURCE_LOCALE))
    expect(zhKeys.size).toBeGreaterThan(0)
  })

  it('every locale index.ts imports the same modules as zh-CN', () => {
    const sourceModules = extractModuleNames(join(LOCALES_DIR, SOURCE_LOCALE, 'index.ts'))
    expect(sourceModules.size).toBeGreaterThan(0)

    const failures: string[] = []
    for (const locale of LOCALES) {
      if (locale === SOURCE_LOCALE) continue
      const localeModules = extractModuleNames(join(LOCALES_DIR, locale, 'index.ts'))
      const missing = missingKeys(sourceModules, localeModules)
      if (missing.length > 0) {
        failures.push(
          `Locale: ${locale}\nMissing module imports (${missing.length}):\n` +
            missing.map((m) => `  - ${m}`).join('\n'),
        )
      }
    }
    expect(failures, failures.join('\n\n')).toEqual([])
  })

  it('every locale is a superset of zh-CN leaf keys (merged view)', () => {
    const sourceKeys = collectLeafKeys(loadLocale(SOURCE_LOCALE))
    const failures: string[] = []

    for (const locale of LOCALES) {
      if (locale === SOURCE_LOCALE) continue
      const localeKeys = collectLeafKeys(loadLocale(locale))
      const missing = missingKeys(sourceKeys, localeKeys)
      if (missing.length > 0) {
        failures.push(formatMissingBlock(locale, '(index)', missing))
      }
    }
    expect(failures, failures.join('\n\n')).toEqual([])
  })

  it('every locale is a superset of zh-CN leaf keys per-module', () => {
    // This is the granular check (#5 in the brief): even if the merged
    // index.ts happens to align, we want to surface WHICH module lost a
    // key. This is the check that would have produced the desired failure
    // message for `routing.overview.loading` in en-US/es-ES.
    const failures: string[] = []
    const sourceModules = extractModuleNames(join(LOCALES_DIR, SOURCE_LOCALE, 'index.ts'))

    for (const module of sourceModules) {
      const sourceModulePath = join(LOCALES_DIR, SOURCE_LOCALE, `${module}.ts`)
      if (!existsSync(sourceModulePath)) {
        // Defensive: skip if a module file is missing in zh-CN itself.
        // This shouldn't happen, but we don't want a missing zh-CN file
        // to mask a real parity bug in another locale.
        continue
      }
      const sourceKeys = collectLeafKeys(loadModuleFile(SOURCE_LOCALE, module))
      if (sourceKeys.size === 0) continue

      for (const locale of LOCALES) {
        if (locale === SOURCE_LOCALE) continue
        const localeModulePath = join(LOCALES_DIR, locale, `${module}.ts`)
        if (!existsSync(localeModulePath)) {
          failures.push(
            `Module: ${module}, Locale: ${locale}\n` +
              `Missing module file (${localeModulePath})`,
          )
          continue
        }
        const localeKeys = collectLeafKeys(loadModuleFile(locale, module))
        const missing = missingKeys(sourceKeys, localeKeys)
        if (missing.length > 0) {
          failures.push(formatMissingBlock(locale, module, missing))
        }
      }
    }
    expect(failures, failures.join('\n\n')).toEqual([])
  })

  // -------------------------------------------------------------------------
  // Regression guard for v735 (2026-07-03): CredsTab.vue referenced the
  // plan_type dropdown labels via `creds.planTypes.tokenPlan` (camelCase)
  // but every locale defined them as `creds.planTypes.token_plan`
  // (snake_case). vue-i18n's missing-key fallback rendered the raw key
  // path in the dropdown. The locale-side superset test above cannot
  // catch this kind of bug because both shapes exist in zh-CN — only the
  // Vue call site disagrees. This test scans .vue/.ts files for i18n key
  // string literals and asserts each one resolves in zh-CN (the SSOT).
  // -------------------------------------------------------------------------
  it('every i18n key referenced in src resolves in zh-CN (SSOT)', () => {
    // Regression guard for v735 (2026-07-03): CredsTab.vue referenced the
    // plan_type dropdown labels via `creds.planTypes.tokenPlan` (camelCase)
    // but every locale defined them as `creds.planTypes.token_plan`
    // (snake_case). vue-i18n's missing-key fallback rendered the raw key
    // path in the dropdown. The locale-side superset test above cannot
    // catch this kind of bug because both shapes exist in zh-CN — only the
    // Vue call site disagrees.
    //
    // This test scans `creds.*` references inside CredsTab.vue and asserts
    // each one resolves under `providerDetail.creds.*` in zh-CN. We keep
    // the scope narrow (a single file) because the broader `pd(/tt()/pp()`
    // helper family lives across many files and each helper binds its own
    // namespace — generalising this scan to cover every helper reliably
    // is a separate refactor (see the file-level TODO below).
    //
    // TODO(2026-07-03): broaden this check to all of `src/**`. A robust
    // approach is to instantiate vue-i18n with zh-CN and call `t(key)` on
    // every literal in `src/**`, then assert the returned string !== key.
    // That requires importing the i18n instance (and a vitest setup file)
    // and is out of scope for the v735 fix.
    const credsTab = join(__dirname, '..', 'views', 'provider-detail', 'CredsTab.vue')
    const src = readFileSync(credsTab, 'utf8')
    const referenced = new Set<string>()
    const re = /\bpd\(\s*['"](creds\.[a-zA-Z0-9_.]+)['"]/g
    let m: RegExpExecArray | null
    while ((m = re.exec(src)) !== null) referenced.add(m[1])

    // Each `creds.x` literal resolves under `providerDetail.creds.x` because
    // CredsTab.vue's `pd` helper prepends `providerDetail.`.
    const zhKeys = collectLeafKeys(loadModuleFile(SOURCE_LOCALE, 'providerDetail'))
    const missing = [...referenced]
      .map((k) => `providerDetail.${k}`)
      .filter((full) => !zhKeys.has(full.replace(/^providerDetail\./, '')))
      .sort()
    expect(missing, `i18n keys referenced in CredsTab.vue but missing in zh-CN (${missing.length}):\n` + missing.map((k) => `  - ${k}`).join('\n')).toEqual([])
  })
})