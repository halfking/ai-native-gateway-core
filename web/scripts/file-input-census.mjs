/**
 * file-input-census.mjs — C2 门禁：每个 `<input type="file">` 必须在登记表里，
 * 且登记内容必须与文件里的真实状态一致（docs/UI规范 19 §4.2）。
 *
 * ## 四条判据（照参考仓 19 §4.2 的契约，stale-route 是本仓补的第 4 条）
 *
 *   ① unregistered 未登记   —— 新增入口忘了登记
 *   ② lying         登记说谎 —— 登记 native-bridge，但文件里没有对桥的调用
 *   ③ stale         陈旧条目 —— 登记里有，文件里已删
 *   ④ stale-route   登记过时 —— 登记 web-only，但文件里已经出现桥调用
 *
 * ★ ② 与 ④ 是**同一件事的两个方向**。只做 ② 不做 ④，等于门只会在
 *   「登记比现实超前」时红，而「现实超前于登记」（有人接了桥却没改登记）会静默。
 *
 * ★ **登记表记的是当前真实状态，不是目标状态**（参考仓原话）。
 *   本仓两个入口目前都只走网页 `<input>`、**没有任何壳桥调用**，
 *   所以登记为 `web-only` + 原因。若此刻为了「将来会接线」登记成
 *   `native-bridge`，登记当场就在说谎，② 会当场变红。
 *
 * ## ★ 为什么必须有 `--self-test`
 *
 * 计数型判据一旦自身失灵，**永远是绿的**。参考仓 19 §5 记了它自己 4 条失灵，
 * 全部是「长度敏感的窗口匹配」导致的假绿。本门因此内置自检：
 * 用**已知坏样本**先证明四条判据都会红，再去相信它在真实树上的绿。
 *
 * 用法：
 *   node scripts/file-input-census.mjs              # 门禁（有红则 rc=1）
 *   node scripts/file-input-census.mjs --self-test  # 自检（判据自身失灵则 rc=1）
 */
import { readFileSync, readdirSync, statSync, mkdirSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve, relative, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = resolve(__dirname, '..', 'src')

/**
 * ★ 登记表：写明**当前真实状态**。
 *   route: 'web-only' | 'native-bridge'
 *   reason: 为什么是这个 route（必填，防止后来人把它当目标态改掉）
 *   maxBytes / serverLimit: 客户端上限必须能追到后端的同一个数
 */
const FILE_INPUTS = [
  {
    file: 'views/PricingManagementView.vue',
    route: 'web-only',
    reason: '定价 CSV 走 POST /admin/pricing/import（multipart），壳文件桥未接线',
    handler: 'onFileChange',
    maxBytes: 'PRICING_CSV_MAX_BYTES',
    serverLimit: 'admin/pricing.go:429 ParseMultipartForm(10<<20)',
  },
  {
    file: 'views/AnnotationView.vue',
    route: 'web-only',
    reason: 'corrections CSV 走 POST /api/admin/task-profile/corrections/import（裸 text/csv），壳文件桥未接线',
    handler: 'handleImportCSV',
    maxBytes: 'CORRECTIONS_CSV_MAX_BYTES',
    serverLimit: 'taskprofile/handler.go:350 MaxBytesReader(32<<20)',
  },
]

/** 壳文件桥的调用名。本仓尚无实现，所以 ②/④ 平时都不该触发。 */
const BRIDGE_CALLS = /\b(pickImportFile|setFileImportBridge|FileImportBridge)\b/

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist') continue
    const full = join(dir, name)
    if (statSync(full).isDirectory()) yield* walk(full)
    else if (full.endsWith('.vue')) yield full
  }
}

const norm = (p) => p.split('\\').join('/')

/**
 * 扫出所有生产 `<input type="file">`。测试桩不算（参考仓 19 §5 失灵 4 的同款修法）。
 *
 * ⚠️ 排除正则必须同时覆盖 `.spec.vue` / `.test.vue` —— 只写
 *   `\.(spec|test)\.[jt]sx?$` 会漏掉组件测试桩，把它们报成生产缺口。
 *   本轮自检 S5 就是靠这条抓到的：第一版断言只查 `multi-input`，
 *   于是「spec.vue 被误计」在自检里是**绿的**。
 */
const TEST_STUB = /\.(spec|test)\.[jt]sx?$|\.(spec|test)\.vue$/

function scanFileInputs(root) {
  const out = []
  for (const abs of walk(root)) {
    if (TEST_STUB.test(abs)) continue
    const t = readFileSync(abs, 'utf8')
    const tpl = t.match(/<template>([\s\S]*)<\/template>/)
    const markup = tpl ? tpl[1] : t
    for (const m of markup.matchAll(/<input\b[^>]*>/g)) {
      if (!/type\s*=\s*["']file["']/.test(m[0])) continue
      out.push({ file: norm(relative(root, abs)), tag: m[0].replace(/\s+/g, ' ') })
    }
  }
  return out
}

/**
 * 跑门禁。`registry` 可注入 —— 自检靠它构造各种「坏登记」，
 * 而不是去改模块级常量（那会让自检和真实门禁共用同一份状态）。
 */
function runGate(root, registry = FILE_INPUTS) {
  const problems = []
  const found = scanFileInputs(root)
  const perFile = new Map()
  for (const f of found) perFile.set(f.file, (perFile.get(f.file) ?? 0) + 1)

  for (const [file, n] of perFile) {
    if (n > 1) {
      problems.push({ kind: 'multi-input', file, detail: `同一文件有 ${n} 个 <input type="file">，登记按文件粒度，请确认它们走同一条规则` })
    }
  }
  // ① 未登记
  for (const file of perFile.keys()) {
    if (!registry.some((r) => r.file === file)) {
      problems.push({ kind: 'unregistered', file, detail: '生产 <input type="file"> 未在登记表里' })
    }
  }
  for (const r of registry) {
    const abs = join(root, r.file)
    if (!perFile.has(r.file)) {
      problems.push({ kind: 'stale', file: r.file, detail: '登记里有，但文件里已找不到 <input type="file">' })
      continue
    }
    const bridged = BRIDGE_CALLS.test(readFileSync(abs, 'utf8'))
    // ② 登记说谎：登记 native-bridge 但没调用
    if (r.route === 'native-bridge' && !bridged) {
      problems.push({ kind: 'lying', file: r.file, detail: '登记为 native-bridge，但文件里没有对壳文件桥的调用（登记在说谎）' })
    }
    // ④ 登记过时：登记 web-only 但已接桥
    if (r.route === 'web-only' && bridged) {
      problems.push({ kind: 'stale-route', file: r.file, detail: '登记为 web-only，但文件里已出现壳文件桥调用（登记过时了）' })
    }
  }
  return { problems, found, perFile }
}

// ───────────────────────── 自检 ─────────────────────────
const SEED = (body = 'function onChange() {}') => `
<template>
  <div><input type="file" accept=".csv" @change="onChange" /></div>
</template>
<script setup lang="ts">
${body}
</script>
`

function selfTest() {
  const sandbox = mkdtempSync(join(tmpdir(), 'c2-selftest-'))
  const results = []
  const put = (rel, body) => {
    const p = join(sandbox, rel)
    mkdirSync(dirname(p), { recursive: true })
    writeFileSync(p, body)
  }
  const kinds = (r) => r.problems.map((p) => p.kind)
  const reg = (over = {}) => [{ file: 'views/A.vue', route: 'web-only', reason: '自检用', ...over }]

  try {
    // S0 对照：文件与登记一致 ⇒ 零问题
    put('views/A.vue', SEED())
    let r = runGate(sandbox, reg())
    results.push({ id: 'S0 对照（一致态）', ok: r.problems.length === 0, note: `实际 ${r.problems.length} 条，应为 0` })

    // S1 ① 未登记
    put('views/B.vue', SEED())
    r = runGate(sandbox, reg())
    results.push({ id: 'S1 ① 未登记', ok: kinds(r).includes('unregistered'), note: `kinds=[${kinds(r)}]` })
    rmSync(join(sandbox, 'views/B.vue'))

    // S2 ③ 陈旧条目
    r = runGate(sandbox, reg({ file: 'views/Gone.vue' }))
    results.push({ id: 'S2 ③ 陈旧条目', ok: kinds(r).includes('stale'), note: `kinds=[${kinds(r)}]` })

    // S3 ② 登记说谎：登记 native-bridge，文件里没接桥
    r = runGate(sandbox, reg({ route: 'native-bridge' }))
    results.push({ id: 'S3 ② 登记说谎', ok: kinds(r).includes('lying'), note: `kinds=[${kinds(r)}]` })

    // S4 ④ 登记过时：文件接了桥，登记还写 web-only
    put('views/A.vue', SEED('function onChange() { pickImportFile() }'))
    r = runGate(sandbox, reg())
    results.push({ id: 'S4 ④ 登记过时', ok: kinds(r).includes('stale-route'), note: `kinds=[${kinds(r)}]` })
    put('views/A.vue', SEED()) // 复位

    // S5 组件测试桩不算生产缺口
    // ⚠️ 断言必须**点名道姓**：只看「有没有 multi-input」会被别的 kinds 掩盖，
    //    第一版就是这么假绿的 —— 真正的漏网是「A.spec.vue 被报成 unregistered」。
    put('views/A.spec.vue', SEED())
    r = runGate(sandbox, reg())
    const leaks = r.problems.filter((p) => p.file === 'views/A.spec.vue')
    results.push({
      id: 'S5 组件测试桩不算生产缺口',
      ok: leaks.length === 0 && r.found.length === 1,
      note: `spec.vue 泄漏 ${leaks.length} 条（kinds=[${leaks.map((p) => p.kind)}]）；found=${r.found.length} 应为 1`,
    })
    rmSync(join(sandbox, 'views/A.spec.vue'))

    // S6 扫描面不是空的（防止「扫不到东西所以永远绿」）
    put('views/A.vue', SEED())
    r = runGate(sandbox, reg())
    results.push({ id: 'S6 扫描面非空', ok: r.found.length === 1, note: `found=${r.found.length}，应为 1` })
  } finally {
    try { rmSync(sandbox, { recursive: true, force: true }) } catch { /* 留给系统清理 */ }
  }
  return results
}

const args = process.argv.slice(2)

if (args.includes('--self-test')) {
  const results = selfTest()
  console.log('=== C2 门禁自检：用已知坏样本先证明判据会红 ===\n')
  for (const r of results) console.log(`${r.ok ? '🟢' : '🔴'} ${r.id}  ${r.note}`)
  const bad = results.filter((r) => !r.ok).length
  console.log(`\n自检 ${results.length - bad}/${results.length} 条判据已证明有牙`)
  console.log('★ 计数型判据自身失灵时永远是绿的 —— 自检不绿就不要相信门禁的绿。')
  process.exit(bad === 0 ? 0 : 1)
}

const { problems, found } = runGate(SRC)
console.log('=== C2 文件输入登记门禁 ===')
console.log(`扫描 ${found.length} 个生产 <input type="file">；登记 ${FILE_INPUTS.length} 条\n`)
if (problems.length === 0) {
  console.log('🟢 全部已登记，且登记内容与文件真实状态一致：')
  for (const r of FILE_INPUTS) {
    console.log(`   · ${r.file}`)
    console.log(`       route=[${r.route}] handler=${r.handler}`)
    console.log(`       上限 ${r.maxBytes} ← ${r.serverLimit}`)
  }
} else {
  for (const p of problems) console.log(`🔴 [${p.kind}] ${p.file} — ${p.detail}`)
  console.log(`\n${problems.length} 处问题`)
}
process.exit(problems.length === 0 ? 0 : 1)
