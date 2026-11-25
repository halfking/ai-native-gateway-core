// 全库 untranslated 明细扫描：每个语种与 en-US 逐字相同的键，按模块分组
// 口径与 localeValueBaseline.json 的 metric 一致：全键（ALL keys）、值字节级等于 en-US
import { readFileSync } from 'fs'
import { join } from 'path'
import vm from 'vm'

const DIR = join(process.cwd(), 'src', 'locales')
const LOCALES = ['de-DE', 'es-ES', 'fr-FR', 'ar-SA', 'ja-JP', 'zh-TW']

function loadModuleFile(locale, moduleName) {
  const f = join(DIR, locale, `${moduleName}.ts`)
  const src = readFileSync(f, 'utf8')
  const m = { exports: {} }
  const sandbox = { module: m }
  vm.createContext(sandbox)
  vm.runInContext(src.replace(/^export default /m, 'module.exports = '), sandbox, { filename: f })
  return m.exports
}

function loadLocale(locale) {
  const src = readFileSync(join(DIR, locale, 'index.ts'), 'utf8')
  const merged = {}
  const re = /^\s*import\s+(\w+)\s+from\s+['"]\.\/(\w+)['"]\s*$/gm
  let m
  while ((m = re.exec(src)) !== null) merged[m[1]] = loadModuleFile(locale, m[2])
  return merged
}

function flat(o, p = '') {
  const out = {}
  for (const [k, v] of Object.entries(o)) {
    const path = p ? p + '.' + k : k
    if (typeof v === 'object' && v !== null && !Array.isArray(v)) Object.assign(out, flat(v, path))
    else if (v !== null && v !== undefined) out[path] = String(v)
  }
  return out
}

const en = flat(loadLocale('en-US'))
console.log(`en-US 全库叶子数: ${Object.keys(en).length}\n`)

const detail = {}
for (const L of LOCALES) {
  const t = flat(loadLocale(L))
  const un = []
  for (const [k, v] of Object.entries(en)) {
    if (t[k] === v) un.push(k)
  }
  const byMod = {}
  for (const k of un) {
    const mod = k.split('.')[0]
    byMod[mod] = (byMod[mod] || 0) + 1
  }
  detail[L] = un
  const sorted = Object.entries(byMod).sort((a, b) => b[1] - a[1])
  console.log(`=== ${L}: ${un.length} 键（前 12 模块）===`)
  console.log(sorted.slice(0, 12).map(([m, c]) => `${m}:${c}`).join('  '))
  console.log('')
}

// 四拉丁语种共有的键（＝一次补译四语种收益最大）
const common = detail['de-DE'].filter((k) =>
  detail['es-ES'].includes(k) && detail['fr-FR'].includes(k) && detail['ar-SA'].includes(k)
)
console.log(`\n=== 四拉丁语种共有的 untranslated 键: ${common.length} ===`)

// 输出 JSON 供后续翻译脚本消费
import { writeFileSync } from 'fs'
writeFileSync(
  '/tmp/i18n-untranslated-detail.json',
  JSON.stringify({ generatedAt: new Date().toISOString(), en, detail, common }, null, 1)
)
console.log('明细已写 /tmp/i18n-untranslated-detail.json')
