#!/usr/bin/env node
// anchor-check.mjs —— 注释锚点体检。
//
// 背景：本仓库的前端源码大量用「后端实现位置」当注释锚点（`admin/request_journey.go:265`），
// 读者靠它跳过去核对。这类锚点一旦漂移，注释就从「证据」退化成「装饰」——
// 而且漂移本身不会让任何测试变红，所以只能靠一个显式的门来盯。
//
// 它能判的只有一件事：**行号越界**（锚点指向的行超过目标文件行数）。
// 它判不了「行还在但内容已经换了」——那需要语义比对，本脚本没有。
// ★ 不要试图加一条「锚点行不含文件名 ⇒ 漂移」的启发式：Go 的函数名本来就不重复
//   文件名（`newWrapAdmin` 在 `main_admin_wrappers.go` 里），那条判据在 Go 上
//   恒假阳性（实测 278 条全是噪音）。宁可少判，不可造红。
//
// 用法：node scripts/anchor-check.mjs [repoRoot]
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname, resolve, basename } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = resolve(process.argv[2] ?? resolve(dirname(fileURLToPath(import.meta.url)), '..', '..'))
const SRC = join(REPO, 'web-mobile/src')

function walk(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name === 'dist' || e.name === '.git') continue
    const p = join(dir, e.name)
    if (e.isSymbolicLink()) continue          // 悬空软链会让 statSync 抛错
    if (e.isDirectory()) walk(p, out)
    else if (/\.(ts|vue|go)$/.test(e.name)) out.push(p)
  }
  return out
}

// 锚点解析：优先相对路径唯一匹配 → 逐级放宽后缀 → basename（仅唯一时）。
// ★ 绝不能「按 basename 取第一个」：仓里 main.go / store.go 都有同名文件，
//   上一版那样写会把 cmd/gateway/main.go（7000+ 行）解析成另一个 262 行的
//   main.go，凭空报出 34 个越界。多态集合不能只看第一个。
const allFiles = walk(REPO)
const relIndex = new Map()
const byBase = new Map()
for (const f of allFiles) {
  relIndex.set(f.slice(REPO.length + 1), f)
  const b = basename(f)
  if (!byBase.has(b)) byBase.set(b, [])
  byBase.get(b).push(f)
}
let ambiguous = 0

function resolveRef(ref) {
  const norm = ref.replace(/^\.\//, '')
  if (relIndex.has(norm)) return relIndex.get(norm)
  const parts = norm.split('/')
  for (let start = 1; start < parts.length; start++) {
    const suf = parts.slice(start).join('/')
    const hits = allFiles.filter((f) => f.endsWith('/' + suf))
    if (hits.length === 1) return hits[0]
    if (hits.length > 1) { ambiguous++; return null }
  }
  const cands = byBase.get(basename(norm))
  if (cands?.length === 1) return cands[0]
  if (cands?.length > 1) { ambiguous++; return null }
  return null
}

const ANCHOR = /(?:^|[\s`（(])((?:[\w.-]+\/)*[\w.-]+\.(?:go|ts|vue)):(\d+)(?:-(\d+))?/g
let checked = 0, oob = 0
const rows = []

if (!existsSyncSafe(SRC)) {
  console.error(`找不到 ${SRC}`)
  process.exit(2)
}
function existsSyncSafe(p) { try { readFileSync(p); return true } catch { try { readdirSync(p); return true } catch { return false } } }

for (const f of walk(SRC)) {
  const lines = readFileSync(f, 'utf8').split('\n')
  lines.forEach((line, i) => {
    ANCHOR.lastIndex = 0
    let m
    while ((m = ANCHOR.exec(line))) {
      const [, ref, a, b] = m
      const target = resolveRef(ref)
      if (!target) continue                     // 歧义或仓外 —— 不猜，跳过
      const len = readFileSync(target, 'utf8').split('\n').length
      const aN = Number(a), bN = Number(b ?? a)
      checked++
      if (aN > len || bN > len || aN < 1) {
        oob++
        rows.push({ at: `${f.slice(REPO.length)}:${i + 1}`, ref, len })
      }
    }
  })
}

console.log(`锚点体检  源码文件=${walk(SRC).length}  锚点=${checked}  越界=${oob}  同名歧义跳过=${ambiguous}`)
for (const r of rows) console.log(`  [OOB] ${r.at} → ${r.ref}（该文件只有 ${r.len} 行）`)

if (oob > 0) {
  console.error(`\n❌ ${oob} 个注释锚点越界 —— 注释指向的后端位置已经不存在了`)
  process.exit(1)
}
console.log('✅ 无越界锚点')