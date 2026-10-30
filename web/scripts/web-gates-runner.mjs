/**
 * web-gates-runner.mjs — 「前端门总清单」的单一执行入口（2026-10-30 · r1050 轮）
 *
 * # 这道入口补的是什么
 *
 * ★★ 2026-10-30 r1050 轮实测：**`web/package.json` 里有 32 个 `:check` 类门脚本，
 *   ★★ 而 `verify.sh`（官方门 / CI 主路径）只登记了其中 9 个**
 *   ⇒ ★★★ **19 个门在 CI 上一次都没被执行过**
 *   ★★★ **其中就有 `i18n:locale-value:check`**（★ 语言包值棘轮门 —— ★★ r1050 候选 B
 *   ★★ 那一轮反复跑过它，★ ★★ 而它在 CI 上**从未跑过**）
 *   ⇒ ★★★ 同样地，**31 个 `:selftest` 脚本在 web 侧也一个都没进强制路径**
 *   ★★★ （只有 web-mobile 的 `gate:selftest` 在）⇒ ★★ **一道门自己的判据坏了
 *   ★★ （恒绿 / 恒红）没人知道** —— ★★ 而「自证没被执行」= 门的存在性没有保障。
 *
 * ★★★ 而这正是本仓反复吃过的那亏（`verify.sh` 自记）：
 *   「只登记在 package.json ⇒ 一处都没引用 ⇒ 从未被执行过 ⇒ **一门没人跑的守卫是文档，不是门**」
 *
 * # 为什么做成「清单 + 一个入口」而不是把 50 条命令塞进 verify.sh
 *
 * ★ ① **单一真值**：★ 清单在这里一处 ⇒ verify.sh 只调一个入口 ⇒
 *   ★★ 将来加一道门 ⇒ 只改清单一处 ⇒ 不会出现「加了门忘了登记」或
 *   ★★ 「verify.sh 与清单两处各写一份、彼此漂移」。
 * ★ ② **可执行性有牙**：★ 本入口**自己**会校验清单里的每个脚本都真实存在于
 *   ★ `package.json` ⇒ ★★ **清单里写错名字 ⇒ 本入口自己红** ⇒ 不用等 CI。
 * ★ ③ **失败即停且指名道姓**：★ 任一门红 ⇒ 打印「哪个门红了 + 它的 rc」并 exit 1。
 *
 * # 类别与「为什么这样分」
 *
 *   `gates`     —— 判据类门（对仓内真实内容下判决）
 *   `selftests` —— 判据**自身**的可执行自证（证明判据有牙、不是恒绿门）
 *
 * ★★ **两类都必须进强制路径**：★
 *   ★ 只跑门不跑自证 ⇒ ★★ 门坏了（判据恒绿）没人知道（★ 这正是本轮的缺口）；
 *   ★ 只跑自证不跑门 ⇒ ★★ 门不在任何路径上执行（★ 这也是本轮的缺口）。
 *
 * # ★★ 刻意排除的项（每条都写了理由，不许静默省略）
 *
 *   · `ui:sweep:selftest` —— ★ **需要 playwright + chromium 浏览器**；
 *     ★ 该脚本在缺件时**显式 throw 并提示 `npx playwright install chromium`**
 *     ⇒ ★★ 那是**环境缺件**，不是代码缺陷 ⇒ ★★ 不进 CI（CI 不装浏览器）；
 *     ★★ 但**不静默**：★ 本入口在报告里**显式列出**「已排除项 + 排除理由」。
 *   · `i18n:check:strict` —— ★ 它跑的 7 个测试文件**已被 vitest 全量覆盖**
 *     （★ `vite.config.ts` 的 `include` 是 `src/` 下所有 `.test.ts` / `.spec.ts`）⇒
 *     ★★ 唯一多出来的是环境变量 `I18N_STRICT=1`（★ 让 `keys_referenced.test.ts`
 *     ★★ 从 warn-only 变 fail）⇒ ★★ **必须单独跑一次**，否则那道门恒绿。
 *
 * # 用法
 *
 *   node scripts/web-gates-runner.mjs            # 跑清单里全部（门 + selftest）
 *   node scripts/web-gates-runner.mjs --list     # 只打印清单，不执行
 *   node scripts/web-gates-runner.mjs --only gates
 *   node scripts/web-gates-runner.mjs --only selftests
 */

import { readFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const PKG = JSON.parse(readFileSync(resolve(WEB, 'package.json'), 'utf8'))

// ─────────────────────────────────────────────────────────────
// 清单（★ 单一真值 ★）
// ─────────────────────────────────────────────────────────────

/** 判据类门：对仓内真实内容下判决。 */
const GATES = [
  // ── 表格语义（r1050 候选 C 新增）──
  'html-table:check:strict',
  // ── UI 契约 ──
  'ui:check:strict',
  'responsive:check',
  'element:check',
  'color:check',
  'file-inputs:check',
  // ── i18n ──
  'i18n:check',
  'i18n:check:strict',
  'i18n:cjk:check',
  'i18n:locale-value:check',
  'i18n:key-parity:check',
  'i18n:hidden-key:check',
  'i18n:shared-dead:check',
  'i18n:empty-state:check',
  'i18n:error-clarity:check',
  'i18n:error-outlet:check',
  'i18n:jargon:check',
  // ── 文案质量 ──
  'copy-quality:check',
  'copy-clarity:check',
  'copy-clarity-locale:check',
  // ── 设计体系（design-* 族）──
  'design-ai-smell:check',
  'design-elevation:check',
  'design-primary-focus:check',
  'design-semantic-color:check',
  'design-primary-clarity:check',
  'design-type-scale:check',
  'design-spacing-scale:check',
  'design-action-density:check',
  'design-motion:check',
  'design-glyph-icon-locale:check',
]

/** 判据自身的可执行自证（证明判据有牙、不是恒绿门）。 */
const SELFTESTS = [
  'ui:selftest',
  'html-table:selftest',
  'i18n:hidden-key:selftest',
  'i18n:shared-dead:selftest',
  'i18n:empty-state:selftest',
  'i18n:error-clarity:selftest',
  'i18n:error-outlet:selftest',
  'i18n:jargon:selftest',
  'i18n:jargon:runtime:selftest',
  'copy-quality:selftest',
  'copy-clarity:selftest',
  'copy-clarity-locale:selftest',
  'design-ai-smell:selftest',
  'design-elevation:selftest',
  'design-elevation:runtime:selftest',
  'design-primary-focus:selftest',
  'design-primary-focus:runtime:selftest',
  'design-semantic-color:selftest',
  'design-semantic-color:runtime:selftest',
  'design-primary-clarity:selftest',
  'design-primary-clarity:runtime:selftest',
  // ★★ `ui:sweep:selftest` **不在这里** —— ★ 它需要 playwright + chromium ⇒
  //   ★ 缺件时显式 throw ⇒ 环境缺件 ⇒ CI 装不了浏览器 ⇒ 见 EXCLUDED。
  'design-type-scale:selftest',
  'design-spacing-scale:selftest',
  'design-action-density:selftest',
  'design-icon-size:runtime-check:selftest',
  'design-metric-skeleton:runtime-check:selftest',
  'design-nested-card:runtime-check:selftest',
  'design-hero-metric:runtime-check:selftest',
  'design-motion:selftest',
  'design-glyph-icon-locale:selftest',
]

/**
 * ★★ 刻意排除的项 —— **不静默**：★ 每次运行都在报告里列出「排除了什么 + 为什么」。
 * ★★ 排除项若将来满足条件 ⇒ 从这里移除并挪进上面的清单即可。
 */
const EXCLUDED = [
  {
    name: 'ui:sweep:selftest',
    reason:
      '需要 playwright + chromium 浏览器；缺件时该脚本显式 throw 并提示 ' +
      '`npx playwright install chromium` ⇒ 那是**环境缺件**不是代码缺陷 ⇒ CI 不装浏览器 ⇒ 不进 CI',
  },
]

// ─────────────────────────────────────────────────────────────
// 清单自身的可执行性（★ 本入口自己有牙 ★）
// ─────────────────────────────────────────────────────────────

function checkManifest() {
  const missing = []
  for (const [kind, list] of [['gate', GATES], ['selftest', SELFTESTS]]) {
    for (const name of list) {
      if (!PKG.scripts[name]) missing.push(`${kind}: ${name}（package.json 里没有这个脚本）`)
    }
  }
  // ★★ 排除项的语义是「**不执行**」，不是「不存在」⇒ ★★ 它们**本来就该**在
  //   package.json 里存在（★ 那正是「已登记但刻意不进 CI」的形态）⇒
  //   ⇒ 这里要查的是**别的**：★ 排除项的名字是否**同时**出现在执行清单里
  //   ⇒ ★★ 那才是真矛盾（★ 既是排除项又被执行 ⇒ 排除声明是假的）。
  for (const e of EXCLUDED) {
    if (GATES.includes(e.name) || SELFTESTS.includes(e.name)) {
      missing.push(`excluded(但它同时在执行清单里 ⇒ 排除声明是假的): ${e.name}`)
    }
  }
  return missing
}

// ─────────────────────────────────────────────────────────────
// 执行
// ─────────────────────────────────────────────────────────────

function runOne(name) {
  const pm = process.env.WEB_PM === 'npm' ? 'npm' : 'pnpm'
  const r = spawnSync(pm, ['run', name], {
    cwd: WEB,
    stdio: ['ignore', 'pipe', 'pipe'],
    encoding: 'utf8',
    env: process.env,
  })
  return { name, rc: r.status ?? 1, out: (r.stdout || '') + (r.stderr || '') }
}

function main() {
  const argv = process.argv.slice(2)

  const missing = checkManifest()
  if (missing.length > 0) {
    console.error('✘ 门清单自身有问题（★ 本入口自己有牙 ⇒ 不必等 CI 才发现）：')
    for (const m of missing) console.error(`  ${m}`)
    process.exit(2)
  }

  if (argv.includes('--list')) {
    console.log(`门（${GATES.length}）：`)
    for (const g of GATES) console.log(`  ${g}`)
    console.log(`\n自证（${SELFTESTS.length}）：`)
    for (const s of SELFTESTS) console.log(`  ${s}`)
    console.log(`\n刻意排除（${EXCLUDED.length}，★ 不静默）：`)
    for (const e of EXCLUDED) console.log(`  ${e.name} — ${e.reason}`)
    return
  }

  const only = argv.includes('--only') ? argv[argv.indexOf('--only') + 1] : null
  const plan = []
  if (!only || only === 'gates') plan.push(...GATES.map((n) => [n, 'gate']))
  if (!only || only === 'selftests') plan.push(...SELFTESTS.map((n) => [n, 'selftest']))

  console.log(`[web-gates] 开始执行 ${plan.length} 项（门 ${GATES.length} + 自证 ${SELFTESTS.length}）`)

  const failed = []
  for (const [name, kind] of plan) {
    const r = runOne(name)
    const tag = r.rc === 0 ? '✔' : '✘'
    console.log(`  ${tag} ${kind.padEnd(8)} ${name.padEnd(42)} rc=${r.rc}`)
    if (r.rc !== 0) {
      failed.push(r)
      // ★ 失败即打印其输出尾部（★ 让人不必重跑就能看到原因）
      const tail = r.out.trim().split('\n').slice(-12).join('\n')
      console.log(`    ── 输出尾部 ──\n${tail.split('\n').map((l) => '    ' + l).join('\n')}`)
    }
  }

  console.log('')
  if (EXCLUDED.length > 0) {
    console.log(`ℹ 刻意排除 ${EXCLUDED.length} 项（★ 不静默）：`)
    for (const e of EXCLUDED) console.log(`  ${e.name} — ${e.reason}`)
    console.log('')
  }

  if (failed.length > 0) {
    console.error(`✘ ${failed.length} 项失败：`)
    for (const f of failed) console.error(`  ${f.name} (rc=${f.rc})`)
    process.exit(1)
  }
  console.log(`✔ 全部 ${plan.length} 项通过`)
}

main()
