// workTypes 模块待翻清单导出：六语种中值 === en-US 的键（全键口径，带模块前缀）
// 用法：node scripts/worktypes-todo.mjs → /tmp/worktypes-todo.json + /tmp/worktypes-todo.txt
import { readFileSync, writeFileSync } from 'fs'
import { join } from 'path'

const DIR = join(process.cwd(), 'src', 'locales')
const LOCALES = { 'de-DE': 'de', 'es-ES': 'es', 'fr-FR': 'fr', 'ar-SA': 'ar', 'ja-JP': 'ja', 'zh-TW': 'tw' }
const MOD = 'workTypes'

function loadLocale(locale) {
  const src = readFileSync(join(DIR, locale, `${MOD}.ts`), 'utf8')
  if (/:(interface|type) /.test(src)) throw new Error(`非常规形态: ${locale}`)
  return new Function(src.replace('export default ', 'return '))()
}
function flat(o, p = '') {
  const out = {}
  for (const [k, v] of Object.entries(o)) {
    const path = p ? p + '.' + k : k
    if (typeof v === 'object' && v !== null && !Array.isArray(v)) Object.assign(out, flat(v, path))
    else out[path] = String(v)
  }
  return out
}

const en = flat(loadLocale('en-US'))
const zh = flat(loadLocale('zh-CN'))
const others = {}
for (const L of Object.keys(LOCALES)) others[L] = flat(loadLocale(L))

const union = new Set()
const missingIn = {}
for (const [L, m] of Object.entries(others)) {
  const ks = Object.keys(en).filter((k) => m[k] === en[k])
  missingIn[L] = ks
  ks.forEach((k) => union.add(k))
}

const rows = []
for (const k of [...union].sort()) {
  const miss = Object.keys(LOCALES).filter((L) => others[L][k] === en[k])
  rows.push({ key: k, missingIn: miss.map((L) => LOCALES[L]), zh: zh[k] ?? '(无)', en: en[k] })
}

writeFileSync('/tmp/worktypes-todo.json', JSON.stringify({ mod: MOD, unionCount: union.size, rows }, null, 1))
let txt = `# ${MOD} 待翻清单（六语种并集 ${union.size} 键）\n`
for (const r of rows) txt += `\n${r.key}\n  缺: ${r.missingIn.join(',')}\n  zh: ${r.zh}\n  en: ${r.en}\n`
writeFileSync('/tmp/worktypes-todo.txt', txt)
console.log(`并集 ${union.size} 键 → /tmp/worktypes-todo.json + .txt`)
for (const [L, ks] of Object.entries(missingIn)) console.log(LOCALES[L], ks.length)
