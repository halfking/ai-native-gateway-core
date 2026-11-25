// sessions 模块六语种翻译写回（2026-11-25 补课轮）
// 用法：node scripts/apply-sessions-translations.mjs
// 口径：只替换「当前值与 en-US 逐字相同」的占位键；替换前逐键断言，替换后重载复核。
import { readFileSync, writeFileSync } from 'fs'
import { TRANSLATIONS, checkPlaceholders } from '/tmp/sessions-translations.mjs'

const LOCALES = { de: 'de-DE', es: 'es-ES', fr: 'fr-FR', ar: 'ar-SA', ja: 'ja-JP', tw: 'zh-TW' }

function loadLocale(path) {
  const src = readFileSync(path, 'utf8')
  if (/: *(interface|type) /.test(src)) throw new Error(`非常规形态: ${path}`)
  const obj = new Function(src.replace('export default ', 'return '))()
  if (obj == null || typeof obj !== 'object') throw new Error(`解析失败: ${path}`)
  return obj
}

function get(obj, flatKey) {
  let cur = obj
  for (const seg of flatKey.split('.')) {
    if (cur == null || typeof cur !== 'object' || !(seg in cur)) return undefined
    cur = cur[seg]
  }
  return cur
}

// 逐行扫描 + 路径栈，返回 [{lineNo, indent, key, quote, rawValue, hasComma, flatKey}]
function scanLeaves(src) {
  const lines = src.split('\n')
  const stack = []
  const leaves = []
  const openRe = /^(\s*)([A-Za-z0-9_]+): \{$/
  const closeRe = /^(\s*)\},?$/
  const leafRe = /^(\s*)([A-Za-z0-9_]+): ('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"),?$/
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line.trim() || line.trim().startsWith('//')) continue
    let m = openRe.exec(line)
    if (m) { stack.push(m[2]); continue }
    m = closeRe.exec(line)
    if (m) { stack.pop(); continue }
    m = leafRe.exec(line)
    if (m) {
      const [, indent, key, quoted] = m
      const quote = quoted[0]
      const rawValue = quoted.slice(1, -1)
      leaves.push({
        lineNo: i, indent, key, quote, rawValue, hasComma: /,$/.test(line),
        flatKey: [...stack, key].join('.'),
      })
    }
  }
  return { lines, leaves }
}

function encodeValue(val, quote) {
  if (quote === "'") return `'${val.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`
  return `"${val.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

let totalApplied = 0
const summary = {}

for (const [abbr, locale] of Object.entries(LOCALES)) {
  const path = `src/locales/${locale}/sessions.ts`
  const src = readFileSync(path, 'utf8')
  const enObj = loadLocale('src/locales/en-US/sessions.ts')
  const curObj = loadLocale(path)
  const { lines, leaves } = scanLeaves(src)

  const leafByKey = new Map()
  for (const leaf of leaves) {
    if (leafByKey.has(leaf.flatKey)) throw new Error(`${locale} 重复叶子路径: ${leaf.flatKey}`)
    leafByKey.set(leaf.flatKey, leaf)
  }

  // 覆盖率自检：映射里的键必须真实存在于该 locale 文件
  const mappedKeys = Object.keys(TRANSLATIONS).filter((k) => TRANSLATIONS[k][abbr] != null)
  const missing = mappedKeys.filter((k) => !leafByKey.has(k))
  if (missing.length) throw new Error(`${locale} 映射键不存在于文件: ${missing.join(', ')}`)

  let applied = 0, skippedAlreadyReal = 0, noops = 0
  const appliedKeys = []
  for (const flatKey of mappedKeys) {
    const newVal = TRANSLATIONS[flatKey][abbr]
    const leaf = leafByKey.get(flatKey)
    const enVal = get(enObj, flatKey)
    const curVal = get(curObj, flatKey)
    if (curVal === undefined) throw new Error(`${locale} ${flatKey} 文件缺键`)
    if (enVal === undefined) throw new Error(`en-US 缺键 ${flatKey}`)
    if (curVal !== enVal) { skippedAlreadyReal++; continue } // 已有真译文（并行车道或既有），不动
    if (newVal === curVal) { noops++; continue }
    checkPlaceholders(flatKey, enVal, newVal)
    const line = lines[leaf.lineNo]
    const rebuilt = `${leaf.indent}${leaf.key}: ${encodeValue(newVal, leaf.quote)}${leaf.hasComma ? ',' : ''}`
    lines[leaf.lineNo] = rebuilt
    applied++
    appliedKeys.push(flatKey)
  }

  const out = lines.join('\n')
  // 写后复核：重载逐键比对
  const reloaded = loadLocaleFromSrc(out)
  for (const k of appliedKeys) {
    const want = TRANSLATIONS[k][abbr]
    const got = get(reloaded, k)
    if (got !== want) throw new Error(`${locale} ${k} 写后复核失败: ${JSON.stringify(got)} != ${JSON.stringify(want)}`)
  }
  if (applied) writeFileSync(path, out)

  summary[locale] = { applied, skippedAlreadyReal, noops, mappedTotal: mappedKeys.length }
  totalApplied += applied
  console.log(`${locale}: applied=${applied} alreadyReal=${skippedAlreadyReal} noop=${noops} / mapped=${mappedKeys.length}`)
}

function loadLocaleFromSrc(src) {
  return new Function(src.replace('export default ', 'return '))()
}

console.log(`TOTAL applied=${totalApplied}`)
if (totalApplied === 0) { console.log('（零写入：全部已是真译文或无待翻）'); }
