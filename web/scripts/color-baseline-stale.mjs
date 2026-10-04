#!/usr/bin/env node
/**
 * color-baseline-stale.mjs — 检出 `color-audit-baseline.json` 里的**悬空键**。
 *
 * ## 为什么需要这道门（它防的洞是实测出来的）
 *
 * `color:check` 的基线 key 粒度是 `file:value`。**每修好一条违规，
 * 那条 key 就留在 baseline 里**，而该值在代码里已经不存在了 ——
 * 于是「这个值在这个文件里」从此**永久免检**。
 *
 * 2026-10-04 实测：baseline 44 条、现存违规 40 条，差集 4 条全部是悬空。
 * 把其中一个 `rgba(0, 0, 0, 0.2)` 放回 `AnnotationView.vue`，
 * `color:check` 报 **rc=0 PASS** —— 一个**全新的**硬编码颜色就这么溜过去了。
 *
 * 关键点：**这不影响任何 rc**，所以它不会以「门变红」的形式出现，
 * 只会**静默变松**。本脚本就是那个「会变松」的信号。
 *
 * ## 为什么不重新实现扫描
 *
 * 直接复用 `color-token-audit.mjs` 的 `--json` 产出。
 * 自己再写一份扫描 ⇒ 出现**第二个 SSOT**，两份规则迟早漂移，
 * 而漂移的方向正是「本脚本少报 ⇒ 门失效」。
 *
 * 用法：
 *   node scripts/color-baseline-stale.mjs                     # 检现行 baseline
 *   node scripts/color-baseline-stale.mjs --baseline <path>   # 检指定 baseline
 * 退出码：0 = 无悬空键；1 = 有悬空键（逐条列出）。
 */

import { readFileSync, rmSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'

const CWD = process.cwd()
const AUDIT = resolve(CWD, 'scripts/color-token-audit.mjs')
const args = process.argv.slice(2)
const idx = args.indexOf('--baseline')
const baselinePath = resolve(CWD, idx >= 0 ? args[idx + 1] : 'scripts/color-audit-baseline.json')

// 1) 复用生产扫描，拿到现存违规。--json 模式不判 rc，恒 rc=0。
const jsonPath = resolve(CWD, '.color-audit-stale-tmp.json')
try {
  execFileSync(process.execPath, [AUDIT, '--json', jsonPath], { cwd: CWD, stdio: 'pipe' })
  const report = JSON.parse(readFileSync(jsonPath, 'utf8'))
  const now = new Set()
  for (const list of Object.values(report.files ?? {})) {
    for (const v of list) now.add(`${v.file}:${v.value}`)
  }

  // 2) 读 baseline，算差集。
  const baseline = JSON.parse(readFileSync(baselinePath, 'utf8')).violations ?? []
  const base = new Set(baseline.map((v) => `${v.file}:${v.value}`))

  // 量具自证：两侧都不能空。空集会让「没有悬空键」变成恒真的废话。
  const problems = []
  if (base.size === 0) problems.push('baseline 为空 —— 是不是路径写错了？')
  if (now.size === 0) problems.push('扫描到 0 处违规 —— 量具没跑起来或路径错了，不能据此说「无悬空」')
  if (base.size < now.size) {
    problems.push(`baseline(${base.size}) 比现存违规(${now.size}) 还少 ⇒ 键格式或粒度变了，先核对再信本脚本`)
  }

  if (problems.length > 0) {
    console.error('[stale] 量具自证未通过：')
    for (const p of problems) console.error(`  - ${p}`)
    process.exit(1)
  }

  const stale = [...base].filter((k) => !now.has(k)).sort()
  const missing = [...now].filter((k) => !base.has(k)).sort()

  console.log('=== color-baseline-stale ===')
  console.log(`现存违规: ${now.size}   baseline 键: ${base.size}`)
  console.log(`悬空键（键在、违规已不在代码里）: ${stale.length}`)
  console.log(`新增违规（有、baseline 无 —— 由 color:check 拦）: ${missing.length}`)

  if (stale.length > 0) {
    console.error('\n[stale] 下列键正在让对应值在该文件里**永久免检**：')
    for (const k of stale) console.error(`  - ${k}`)
    console.error('\n处置：跑 `pnpm color:baseline:update` 重新生成，然后单独评审这个 diff。')
    console.error('      ⚠️ 它现在是绿的 —— 因为悬空键不影响任何 rc，只会让门静默变松。')
    process.exit(1)
  }
  console.log('\n[stale] PASS — 无悬空键。')
} finally {
  rmSync(jsonPath, { force: true })
}
