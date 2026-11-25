// 抽样校对：从六语种 800 条写回值中分层抽取 80 条，输出 zh/en/译文 对照供逐键审读
// 用法：node scripts/sample-sessions-review.mjs
import { readFileSync } from 'fs'
import { TRANSLATIONS } from '/tmp/sessions-translations.mjs'

const LOCALES = { de: 'de-DE', es: 'es-ES', fr: 'fr-FR', ar: 'ar-SA', ja: 'ja-JP', tw: 'zh-TW' }
const APPLIED = { 'de-DE': 193, 'es-ES': 192, 'fr-FR': 186, 'ar-SA': 195, 'ja-JP': 19, 'zh-TW': 15 }

function loadLocale(path) {
  const src = readFileSync(path, 'utf8')
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

const en = loadLocale('src/locales/en-US/sessions.ts')
const zh = loadLocale('src/locales/zh-CN/sessions.ts')
const out = []
const seenKeys = new Set()

// 抽样策略：① 全部占位符键（高险）；② 全部长句（≥60字符，高险）；③ 每语种按映射顺序均匀抽足配额
const quota = { de: 20, es: 19, fr: 19, ar: 15, ja: 4, tw: 3 }
const lines = []
for (const [abbr, locale] of Object.entries(LOCALES)) {
  const mappedKeys = Object.keys(TRANSLATIONS).filter((k) => TRANSLATIONS[k][abbr] != null)
  // 分层：占位符键全收 + 长句全收 + 均匀抽样补足配额
  const phKeys = mappedKeys.filter((k) => /\{[a-zA-Z]+\}/.test(String(get(en, k))))
  const longKeys = mappedKeys.filter((k) => !phKeys.includes(k) && String(get(en, k)).length >= 60)
  const rest = mappedKeys.filter((k) => !phKeys.includes(k) && !longKeys.includes(k))
  const q = quota[abbr]
  const step = Math.max(1, Math.floor(rest.length / Math.max(1, q - phKeys.length - longKeys.length)))
  const uniform = rest.filter((_, i) => i % step === 0).slice(0, Math.max(0, q - phKeys.length - longKeys.length))
  const sample = [...phKeys, ...longKeys, ...uniform].slice(0, q)

  for (const k of sample) {
    const cur = get(loadLocale(`src/locales/${locale}/sessions.ts`), k)
    const enVal = get(en, k)
    const twVal = TRANSLATIONS[k][abbr]
    if (cur !== twVal) throw new Error(`${locale} ${k} 落盘值与映射不一致!`)
    const zhVal = get(zh, k)
    lines.push({ locale, key: k, zh: zhVal, en: enVal, val: cur })
  }
}

// 落盘为可审读文本
let text = `# sessions 六语种抽样校对（${lines.length} 条 / 800 条，抽样口径见 scripts/sample-sessions-review.mjs）\n`
let lastLocale = ''
for (const l of lines) {
  if (l.locale !== lastLocale) { text += `\n## ${l.locale}\n\n`; lastLocale = l.locale }
  text += `### ${l.key}\n- zh: ${l.zh}\n- en: ${l.en}\n- 译: ${l.val}\n`
}
const fs = await import('fs')
fs.writeFileSync('/tmp/sessions-sample-review.txt', text)
console.log(`sampled=${lines.length} -> /tmp/sessions-sample-review.txt`)
