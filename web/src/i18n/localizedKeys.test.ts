// localizedKeys.test.ts — 「新增键必须逐语种本地化」门（2026-10-21 新增）
//
// 为什么要这道门（一次真实的漏网，2026-10-20 轮引入）：
//   那一轮给 8 个语种各加了 3 个键，其中
//   `ops.overview.nodeDetailSuperAdminOnlyHint` 在 **ar-SA** 的值被填成了
//   **英文原文**（与 en-US 逐字相同）⇒ 阿拉伯语用户在节点详情抽屉里
//   看到的是英文提示。同一提交里的另外两个键（credentialMonitor.*）
//   在 ar-SA 却是**正经阿拉伯语** ⇒ 是漏，不是该 locale 的整体状态。
//
// 既有门为什么没抓到（量具的盲区，要说清楚）：
//   · `parity.test.ts` 只比**键集合**，不比**键值** ⇒ 英文占位照样"平"；
//   · `keys_referenced.test.ts` 只查**键是否存在**（引用侧）⇒ 同上；
//   · 硬编码中文棘轮（`hardcoded_cjk.baseline.test.ts`）**排除 src/locales/**
//     ⇒ 翻译成英文既不涨中文计数，也不被它管。
//   ⇒ 这三条门合起来也拦不住「键在、但值是英文」。
//
// 覆盖范围（重要，别误读成「全仓 i18n 已达标」）：
//   本门**只**覆盖 MUST_BE_LOCALIZED 登记册里的键。
//   存量里 **ar-SA/ops.ts 整个 ops 命名空间仍有大段英文**
//   （该文件 564 行里只有 14 行含阿拉伯字母），全量「值≠英文」扫描**必然是红的**
//   ⇒ 那会把增量信号淹没在存量噪声里。本门选择：登记册逐条收紧，
//   存量作为**已知负债**单独跟踪（见 docs/audit/2026-10-21-24h-audit-*.md）。
//
// 维护约定：
//   ① 新增任何**用户可见文案**的 i18n 键时，把它登记进 MUST_BE_LOCALIZED；
//   ② 若某键的合法译文**就是**与英文相同（如 'OK'、'HTTP 500'），
//      **不要**登记它——本门会误报；在登记处写明理由。
//   ③ 登记册只增不减（删条目等于撤掉一道门）。

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import vm from 'vm'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const LOCALES_DIR = join(__dirname, '..', 'locales')
const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']
const REFERENCE_LOCALE = 'en-US'

// ── 登记册 ────────────────────────────────────────────────────────────────
// `module` = locale/<locale>/<module>.ts 这个文件
// `path`   = 该文件**内部**的点分路径（不含 module 名前缀）
// `full`   = 运行时用的完整键（<module>.<path>），只用于报错信息
type Registered = { module: string; path: string; full: string; added: string; note?: string }
const MUST_BE_LOCALIZED: Registered[] = [
  {
    module: 'credentialMonitor',
    path: 'error.superAdminOnlyHint',
    full: 'credentialMonitor.error.superAdminOnlyHint',
    added: '2026-10-20',
    note: '凭据监控「状态修正」只读提示（ar-SA 曾误填英文，本门首条即为其而设）',
  },
  {
    module: 'credentialMonitor',
    path: 'error.superAdminOnlyError',
    full: 'credentialMonitor.error.superAdminOnlyError',
    added: '2026-10-20',
  },
  {
    module: 'ops',
    path: 'overview.nodeDetailSuperAdminOnlyHint',
    full: 'ops.overview.nodeDetailSuperAdminOnlyHint',
    added: '2026-10-20',
    note: '节点详情抽屉「非 super_admin」只读提示',
  },
  {
    module: 'ops',
    path: 'overview.nodeDetailDefaultTenantOnlyHint',
    full: 'ops.overview.nodeDetailDefaultTenantOnlyHint',
    added: '2026-10-21',
    note: '节点详情抽屉「非 default 租户」只读提示——2026-10-20 轮漏了它仍是硬编码中文',
  },
]

// ── locale 读取（与 parity.test.ts 同法：vm 跑 CommonJS 化的 .ts）─────────
function loadModuleFile(locale: string, moduleName: string): Record<string, unknown> {
  const absPath = join(LOCALES_DIR, locale, `${moduleName}.ts`)
  const code = readFileSync(absPath, 'utf8').replace(/^export default /m, 'module.exports = ')
  const moduleObj: { exports: Record<string, unknown> } = { exports: {} }
  const sandbox = { module: moduleObj }
  vm.createContext(sandbox)
  vm.runInContext(code, sandbox, { filename: absPath })
  return moduleObj.exports
}

/** 取点分路径上的叶子值；取不到返回 undefined。 */
function leaf(obj: unknown, dotted: string): unknown {
  let cur: unknown = obj
  for (const part of dotted.split('.')) {
    if (cur === null || typeof cur !== 'object') return undefined
    cur = (cur as Record<string, unknown>)[part]
  }
  return cur
}

describe('i18n 本地化登记册（新增键必须逐语种本地化）', () => {
  it('登记册本身非空（防空过：门被清空时这条会红）', () => {
    expect(MUST_BE_LOCALIZED.length).toBeGreaterThan(0)
  })

  it('每个登记键在 8 个语种里都存在，且是非空字符串', () => {
    const missing: string[] = []
    for (const reg of MUST_BE_LOCALIZED) {
      for (const locale of LOCALES) {
        const mod = loadModuleFile(locale, reg.module)
        const v = leaf(mod, reg.path)
        if (typeof v !== 'string' || v.trim() === '') {
          missing.push(`${locale}/${reg.module}.${reg.path} → ${JSON.stringify(v)}`)
        }
      }
    }
    expect(missing, `登记键在部分语种缺失/为空（${missing.length}）：\n` + missing.join('\n')).toEqual([])
  })

  it('★ 每个登记键在非英文语种里都不是英文原文的复制（防「ar-SA 填英文」）', () => {
    // 这是本门的核心断言：值必须**在每个语种各自本地化**，
    // 而不是把 en-US 的句子复制过去。en-US 自身是参照系，不参与比较。
    const copies: string[] = []
    for (const reg of MUST_BE_LOCALIZED) {
      const ref = leaf(loadModuleFile(REFERENCE_LOCALE, reg.module), reg.path)
      if (typeof ref !== 'string') continue
      for (const locale of LOCALES) {
        if (locale === REFERENCE_LOCALE) continue
        const v = leaf(loadModuleFile(locale, reg.module), reg.path)
        if (typeof v !== 'string') continue
        if (v === ref) copies.push(`${locale}/${reg.full} 与 en-US 逐字相同：${JSON.stringify(v)}`)
      }
    }
    expect(
      copies,
      `登记键在非英文语种里仍是英文原文（${copies.length}）：\n` + copies.join('\n'),
    ).toEqual([])
  })
})
