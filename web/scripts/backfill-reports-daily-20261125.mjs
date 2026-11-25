// backfill-reports-daily-20261125.mjs — 一次性补齐 reports.daily.* 与
// reports.legacySnapshotView 在 6 个非中英语种的缺失（225 键 × 6 语种）。
//
// 背景（2026-11-25 审计轮）：
//   vitest 8 红中 6 条根因 = reports 模块在 zh-TW/ja-JP/de-DE/fr-FR/es-ES/ar-SA
//   缺 `daily` 子树（224 叶）与 `legacySnapshotView`（1 键）。
//   对账页（DailyReconciliationPanel / DailySupplierInvoices）在这些语种下
//   退化为裸 key ⇒ 产品缺陷，不是量具缺陷。
//
// 策略（与 scripts/sync-partial-translations.mjs 同款惯例）：
//   - zh-TW：从 zh-CN 复制（简繁同文，符合仓内既有惯例）
//   - ja-JP：从 zh-CN 复制（保留既有日语翻译，仅补缺失键）
//   - de-DE / fr-FR / es-ES / ar-SA：从 en-US 复制（英文占位，便于人工精翻）
//   - 已存在的键跳过，绝不覆盖
//
// 写入方式：把整个 `daily: { ... }` 块与 `legacySnapshotView` 行插入到
// 该 locale reports.ts 的 `export default {` 之后（块级插入，不重排既有键）。
import { readFileSync, writeFileSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import vm from 'node:vm'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')
const DRY_RUN = process.argv.includes('--dry-run')

const TARGETS = ['zh-TW', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']
const SOURCE_BY_LOCALE = {
  'zh-TW': 'zh-CN',
  'ja-JP': 'zh-CN',
  'de-DE': 'en-US',
  'fr-FR': 'en-US',
  'es-ES': 'en-US',
  'ar-SA': 'en-US',
}

function loadModule(locale, name) {
  const src = readFileSync(join(LOCALES_DIR, locale, `${name}.ts`), 'utf8')
  const code = src.replace(/^export default /m, 'module.exports = ')
  const mod = { exports: {} }
  const sandbox = { module: mod }
  vm.createContext(sandbox)
  vm.runInContext(code, sandbox, { filename: name })
  return mod.exports
}

function fmt(v, indent) {
  const pad = ' '.repeat(indent)
  if (typeof v === 'string') {
    const q = v.includes("'") ? '"' : "'"
    return `${q}${v.replace(/\\/g, '\\\\').replace(/\n/g, '\\n')}${q}`
  }
  if (v && typeof v === 'object') {
    const inner = Object.entries(v)
      .map(([k, val]) => `${pad}  ${k}: ${fmt(val, indent + 2)},`)
      .join('\n')
    return `{\n${inner}\n${pad}}`
  }
  return JSON.stringify(v)
}

let changed = 0
for (const locale of TARGETS) {
  const source = SOURCE_BY_LOCALE[locale]
  const srcData = loadModule(source, 'reports')
  const path = join(LOCALES_DIR, locale, 'reports.ts')
  const src = readFileSync(path, 'utf8')

  if (/^  daily\s*:\s*\{/m.test(src)) {
    console.log(`${locale}: daily 已存在，跳过`)
    continue
  }

  const lines = []
  lines.push(`  // ── 2026-11-25 补齐轮：reports.daily.*（对账证据页）从 ${source} 镜像 ──`)
  lines.push(`  // 背景：此前 6 个语种缺整个 daily 子树 ⇒ 对账页在这些语种下渲染裸 key。`)
  lines.push(`  // de/fr/es/ar 为英文占位（后续人工精翻）；zh-TW/ja-JP 复用中文为过渡期文案。`)
  lines.push(`  legacySnapshotView: ${fmt(srcData.legacySnapshotView, 2)},`)
  lines.push(`  daily: ${fmt(srcData.daily, 2)},`)
  const block = lines.join('\n') + '\n'

  const next = src.replace(/^export default \{\n/m, `export default {\n${block}\n`)
  if (next === src) {
    console.error(`${locale}: 未找到 export default { 锚点，放弃`)
    process.exit(1)
  }
  if (!DRY_RUN) writeFileSync(path, next)
  console.log(`${locale}: 补 legacySnapshotView + daily(${Object.keys(srcData.daily).length} 顶层键) ← ${source}`)
  changed++
}
console.log(DRY_RUN ? '[dry-run] 无写入' : `完成：${changed} 个语种已写入`)
