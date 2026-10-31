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
  // ★★ **本入口自己的自证**（★ 2026-10-31 12h 审计轮加）★★ ★ 它证明「本入口
  // ★★ 认得所有门族脚本 ⇒ ⇒ 不会有门掉出清单」⇒ ⇒ ★★ 而这正是本轮实测到的
  // ★★ P1 的口径 ⇒ ⇒ ⇒ ★★ 没有它，本入口自己就会是那种「自己有病却照常跑」的门。
  'web-gates:selftest',
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
  {
    name: 'i18n:check:json',
    reason:
      '与已入清单的 `i18n:check` 是**同一个判据**（scripts/i18n-audit.mjs）的两种输出形态：' +
      '`i18n:check` 跑文本审计 + CJK 计数；`i18n:check:json` 只把同一份审计结果写成 ' +
      'reports/i18n-audit.json 报告文件 ⇒ **判据无新增**、只多一份产物 ⇒ 不重复跑',
  },
  {
    // ★★★ **2026-10-31 12h 审计轮补登记**：★★★ ★ 本入口的口径放宽到「段名以
    // ★★★ 'check' 结尾」之后，★ `typecheck`（★ 段名 'typecheck'）**也**落进门族 ⇒ ⇒
    // ★★★ ⇒ 它必须被登记（★ 否则反向覆盖检查会红 ⇒ ⇒ ⇒ ★ 而它**确实是个检查**）。
    // ★★★ ⇒ 它由 **verify.sh 的前端 typecheck 步骤**单独执行（★ verify.sh:259
    // ★★★ `pm_run typecheck web` ⇒ ★ 在 web-gates 步骤之前）⇒ ⇒ ⇒ ★ 所以不进本入口
    // ★★★ ⇒ ★ **登记在 EXCLUDED 里并写明理由** ⇒ ⇒ ⇒ ★ 而不是让它「看起来被漏掉」。
    name: 'typecheck',
    reason:
      '★ 由 verify.sh 的前端步骤单独执行（★ `pm_run typecheck web` ⇒ ★ 在 web-gates ' +
      '步骤之前）⇒ ⇒ ⇒ ★ 已在 CI 上执行 ⇒ ⇒ ⇒ 不在本入口重复跑 ⇒ ⇒ ⇒ ★ 登记在这里' +
      '是**为了「不静默」**（★ 口径放宽后它落进门族 ⇒ ⇒ 必须有地方收它）',
  },
  // ★★★ **2026-10-31 12h 审计轮补登记（P1）**：★★★ ★ 这 6 道是**真门**
  // ★★★ （★ 各自 `process.exit(1)` 表示违例 ⇒ ★ 退出码 = 判决）⇒ ⇒ ★★ 而它们此前
  // ★★★ **既不在 GATES、也不在 EXCLUDED** ⇒ ⇒ ⇒ ★★ ⇒ **反向覆盖检查看不见它们**
  // ★★★ ⇒ ⇒ ⇒ ★★ ⇒ 「在 CI 上一次都没被执行过」这个病**对它们依然成立** ⇒ ⇒ ⇒
  // ★★★ ⇒ 登记在这里（★ 带理由 ⇒ 不静默）⇒ ⇒ ★ 它们要跑需要：
  // ★★★   ① **chromium 二进制**（★ 脚本硬编码找 /usr/bin/chromium 等路径）⇒ ⇒
  // ★★★      ② **一个跑起来的 dev server**（★ 默认 http://localhost:5200）⇒ ⇒
  // ★★★ ⇒ ⇒ ★ 两者都是**环境缺件**（★ 同 `ui:sweep:selftest`）⇒ ⇒ ⇒ ★ 不进 CI。
  {
    name: 'design-spacing-scale:runtime-check',
    reason:
      '运行时尺：★ 需要 chromium 二进制 + 跑起来的 dev server（默认 http://localhost:5200）' +
      '⇒ ⇒ 两者都是环境缺件（同 `ui:sweep:selftest`）⇒ ⇒ CI 不装浏览器、不起服务 ⇒ 不进 CI；' +
      '★ 源码尺 `design-spacing-scale:check` 已进 CI ⇒ 两把尺互为对拍的关系保留',
  },
  {
    name: 'design-action-density:runtime-check',
    reason:
      '运行时尺：★ 需要 chromium 二进制 + 跑起来的 dev server ⇒ ⇒ 环境缺件 ⇒ 不进 CI；' +
      '★ 源码尺 `design-action-density:check` 已进 CI',
  },
  {
    name: 'design-icon-size:runtime-check',
    reason:
      '运行时尺：★ 需要 chromium 二进制 + 跑起来的 dev server ⇒ ⇒ 环境缺件 ⇒ 不进 CI；' +
      '★ 自证 `design-icon-size:runtime-check:selftest` 已进 CI ⇒ ⇒ ★ **判据自身有牙这件事**' +
      '在 CI 上仍被证明（★ 只有「量浏览器」那一步在 CI 上做不了）',
  },
  {
    name: 'design-metric-skeleton:runtime-check',
    reason:
      '运行时尺：★ 需要 chromium 二进制 + 跑起来的 dev server ⇒ ⇒ 环境缺件 ⇒ 不进 CI；' +
      '★ 自证 `design-metric-skeleton:runtime-check:selftest` 已进 CI',
  },
  {
    name: 'design-nested-card:runtime-check',
    reason:
      '运行时尺：★ 需要 chromium 二进制 + 跑起来的 dev server ⇒ ⇒ 环境缺件 ⇒ 不进 CI；' +
      '★ 自证 `design-nested-card:runtime-check:selftest` 已进 CI',
  },
  {
    name: 'design-hero-metric:runtime-check',
    reason:
      '运行时尺：★ 需要 chromium 二进制 + 跑起来的 dev server ⇒ ⇒ 环境缺件 ⇒ 不进 CI；' +
      '★ 自证 `design-hero-metric:runtime-check:selftest` 已进 CI',
  },
]

// ─────────────────────────────────────────────────────────────
// 清单自身的可执行性（★ 本入口自己有牙 ★）
// ─────────────────────────────────────────────────────────────

/**
 * 判断 package.json 里一个脚本是否属于「门/自证」族。
 *
 * ★ 口径：脚本名里含 `:check` 段（判据类）或 `:selftest` 段（自证类）。
 * ★ 用段匹配而不是 `endsWith(':check')` ⇒ `i18n:check:strict`、
 * ★ `html-table:check:strict` 这类「变体名」也算门族成员。
 *
 * ★★★ **2026-10-31 12h 审计轮修口径（P1）**：★★★ ★ 原口径是「段里**恰好**等于
 * ★★★ 'check'」⇒ ⇒ ★★ 而 `design-*:runtime-check` 的段名是 **'runtime-check'**
 * ★★★ ⇒ ⇒ ≠ 'check' ⇒ ⇒ ★★ ⇒ `familyOf` 返回 **null** ⇒ ⇒ ★★ ⇒ 那 6 道真门
 * ★★★ **既不在 GATES、也不在 EXCLUDED** ⇒ ⇒ ⇒ ★★ ⇒ **反向覆盖检查根本看不见它们**
 * ★★★ ⇒ ⇒ ⇒ ★★ ⇒ 「在 CI 上一次都没被执行过」这个病**对它们依然成立** ⇒ ⇒ ⇒
 * ★★★ ⇒ ⇒ **修法**：★ 段名以 **'check' 结尾**即算门族成员（★ 'check' / 'runtime-check'
 * ★★★ / 未来任何 'xxx-check'）⇒ ⇒ ⇒ ★ 而 'selftest' 仍然**优先**判定
 * ★★★ （★ `:runtime-check:selftest` 仍算自证，不是门）。
 */
function familyOf(name) {
  const segs = name.split(':')
  if (segs.includes('selftest')) return 'selftest'
  if (segs.some((seg) => seg.endsWith('check'))) return 'gate'
  return null
}

/**
 * 某个 package.json 脚本是否已被清单覆盖。
 *
 * ★ 两种覆盖方式：
 *   ① **精确命中** —— 它自己就在清单里；
 *   ② **被严格变体覆盖** —— 清单里有 `X:strict` 这类更严的变体，而它是 `X` 本身
 *      （★ 如 `ui:check` 被 `ui:check:strict` 覆盖、`html-table:check` 被
 *      ★ `html-table:check:strict` 覆盖 ⇒ 跑更严的那个即已覆盖更松的那个）。
 *
 * ★ 方向很重要：★ 只认「更严覆盖更松」（`X` 被 `X:...` 覆盖），
 * ★ **不认**「更松覆盖更严」（★ `i18n:check` 在清单里 **不能** 让
 * ★ `i18n:check:strict` 视为已覆盖 ⇒ 否则 `i18n:check:strict` 这个
 * ★ 「让 keys_referenced 从 warn-only 变 fail」的严格变体会被悄悄放过）。
 */
function isCovered(name, list) {
  if (list.includes(name)) return true
  return list.some((g) => g.startsWith(name + ':'))
}

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
    // ★★★ **2026-10-31 12h 审计轮补（P2）**：★★★ ★ 排除项必须**写理由**，★
    // ★★★ 而原实现只把 reason **打印**出来、从**不校验** ⇒ ⇒ ★★ ⇒ 一条空理由的
    // ★★★ 排除 = 「悄悄把一道门踢出 CI」⇒ ⇒ ★★ ⇒ 标题还写着「不静默」⇒ ⇒ ⇒ ★
    // ★★★ ⇒ 所以这里查：**理由不许为空**。
    if (!e.reason || !e.reason.trim()) {
      missing.push(`excluded(但它没写理由 ⇒ 排除就是静默的): ${e.name}`)
    }
    // ★ 排除项的名字必须**真的存在于 package.json** ⇒ ⇒ 否则一次拼写错误 =
    // ★ 「以为排除了某道门，其实那道门还在清单外」（★ 而清单外的门本就不跑 ⇒
    // ★ ⇒ 等于「排除声明指向了一个不存在的东西」⇒ ⇒ 查出来）。
    if (!PKG.scripts[e.name]) {
      missing.push(`excluded(但 package.json 里没有这个脚本 ⇒ 排除声明指向了不存在的东西): ${e.name}`)
    }
  }
  return missing
}

/**
 * ★★ **反向覆盖检查**（2026-10-31 12h 审计轮 P1-1 补上）：
 * ★★ package.json 里**每一个**门族/自证族脚本，要么在清单里，要么在 EXCLUDED
 * ★★ 里带理由 ⇒ **否则本入口自己红**。
 *
 * ★★★ 为什么必须有这一条（★ 这正是本入口存在理由的**反面**）：
 * ★ 本入口是为了治「19 个门在 CI 上一次都没被执行过」⇒ 若它**只**校验
 * ★ 「清单里的脚本都存在」（checkManifest 的方向），那么 ★★ **将来新增一道门
 * ★★ 而忘了登记进清单 ⇒ 没有任何东西会红 ⇒ 那道门重蹈本轮那 19 道的覆辙**。
 * ★★ 换句话说：★ 只做单向校验的清单门，★ **恰好保不住它自己存在的理由**。
 *
 * ★ 覆盖判定见 isCovered（精确命中 / 被更严的变体覆盖）。
 * ★ EXCLUDED 里列出的名字视为**显式裁决**（要求写了理由）⇒ 不判红。
 */
function checkCoverage() {
  const missing = []
  const excludedNames = new Set(EXCLUDED.map((e) => e.name))
  for (const name of Object.keys(PKG.scripts)) {
    const fam = familyOf(name)
    if (fam === null) continue
    if (excludedNames.has(name)) continue
    const list = fam === 'gate' ? GATES : SELFTESTS
    if (!isCovered(name, list)) {
      missing.push(
        `${fam}: ${name}（package.json 里有，但不在本入口的${fam === 'gate' ? '门' : '自证'}清单里，` +
          `也没在 EXCLUDED 里声明理由 ⇒ 它会像本轮那 19 道门一样「在 CI 上一次都没被执行过」）`,
      )
    }
  }
  return missing
}

// ─────────────────────────────────────────────────────────────
// 执行
// ─────────────────────────────────────────────────────────────

/**
 * 选定包管理器（★ 与 scripts/lib/node-pm.sh **同一契约**）。
 *
 * ★★★ **2026-10-31 12h 审计轮修（P2）**：★★★ ★ 原实现是
 * ★★★ `process.env.WEB_PM === 'npm' ? 'npm' : 'pnpm'` ⇒ ⇒ ★★ ⇒ **它与
 * ★★★ scripts/lib/node-pm.sh 的契约不一致**：★ 那边的契约是「WEB_PM 显式指定；
 * ★★★ 否则**有 pnpm 用 pnpm，没有就退 npm**」⇒ ⇒ ★★ ⇒ 而本入口**硬编码 pnpm**
 * ★★★ ⇒ ⇒ ⇒ ★★ ⇒ **机器上没装 pnpm 时 ⇒ 60 项全部 rc=1，而输出尾部是空的**
 * ★★★ ⇒ ⇒ ⇒ ★★ ⇒ 失败原因（ENOENT）被吞掉 ⇒ ⇒ ⇒ ★★ ⇒ 人只看到「全红」，
 * ★★★ 却看不出「是包管理器不在」。
 *
 * ★★★ **所以改成与 node-pm.sh 同一套判定**：★★★ ★ WEB_PM 显式指定（只接受
 * ★★★ pnpm / npm）⇒ ⇒ ★ 否则**先探测 pnpm 在不在**⇒ ⇒ ★ 都不在 ⇒ ★ npm。
 * ★★★ ★★ 那边的契约原文：「选定一个，跑它，原样透传退出码；不做跨包管理器回退」。
 */
function resolvePm() {
  const want = (process.env.WEB_PM || '').trim()
  if (want) {
    if (want !== 'pnpm' && want !== 'npm') {
      throw new Error(`WEB_PM 只能是 pnpm 或 npm，收到 '${want}'（★ 契约见 scripts/lib/node-pm.sh）`)
    }
    return want
  }
  const probe = spawnSync('pnpm', ['--version'], { stdio: 'ignore', encoding: 'utf8' })
  if (!probe.error && probe.status === 0) return 'pnpm'
  return 'npm'
}

function runOne(name, pm) {
  const r = spawnSync(pm, ['run', name], {
    cwd: WEB,
    stdio: ['ignore', 'pipe', 'pipe'],
    encoding: 'utf8',
    env: process.env,
  })
  // ★★ **spawn 本身失败**（★ ENOENT：包管理器不在 PATH）★ 与 **门跑出非零退出码**
  // ★★ 是两件事 ⇒ ⇒ ★★ 原实现把两者都压成 `rc = r.status ?? 1` ⇒ ⇒ ★★ ⇒
  // ★★ ⇒ 输出尾部为空 ⇒ ⇒ ⇒ ★★ ⇒ 「为什么红」被吞掉 ⇒ ⇒ ★★ ⇒ 现在把 error 原样带出来。
  if (r.error) {
    return { name, rc: 1, out: `★ 启动失败：${r.error.message}` }
  }
  return { name, rc: r.status ?? 1, out: (r.stdout || '') + (r.stderr || '') }
}

/**
 * ★★ 本入口的**自证**（★ 证明「本入口自己有牙」）★★
 *
 * ★★★ **2026-10-31 12h 审计轮新增**：★★★ ★ 本入口此前**没有任何自证** ⇒ ⇒ ★★ ⇒
 * ★★★ ⇒ 「familyOf 的口径悄悄变窄 ⇒ ⇒ 某道门掉出清单」这类退化**没有任何东西会红**
 * ★★★ ⇒ ⇒ ⇒ ★★ ⇒ 而那正是本轮实测到的 P1（★ 6 道 `*:runtime-check` 真门**既不在
 * ★★★ 清单、也不在 EXCLUDED** ⇒ ⇒ 它们**在 CI 上一次都没跑过**⇒ ⇒ ⇒ ★★ ⇒
 * ★★★ ⇒ 而本入口恰恰是治这个病的那个入口 ⇒ ⇒ ⇒ ★★ ⇒ **它自己必须有牙**。
 *
 * ★★ 断言清单（★ 每一条都要能指到具体行为）：★★
 *   ① `familyOf` 认得 `runtime-check`（★ 回归本轮那个 P1 的口径）
 *   ② `*:runtime-check:selftest` 仍然算**自证**（★ 优先级没被改坏）
 *   ③ 非门脚本（`web-gates:run`）不算门族
 *   ④ 每个 EXCLUDED 都写了理由（★ 排除不许静默）
 *   ⑤ 清单自身与反向覆盖检查此刻都为空（★ 门清单是自洽的）
 *   ⑥ **反向覆盖检查有牙**：★ 造一个「像门但不在清单里」的名字 ⇒ ⇒ 它必须被
 *      判为未覆盖 ⇒ ⇒ ⇒ ★ **这一条是本自证的关键** ⇒ ⇒ 若 familyOf 口径再变窄
 *      （★ 某道门掉出识别范围）⇒ ⇒ 本条就会红 ⇒ ⇒ ⇒ ★ ⇒ 退化会被当场抓住。
 */
function selftest() {
  const cases = []
  const expect = (name, got, want) => {
    const ok = got === want
    cases.push({ name, ok, got, want })
    return ok
  }

  // ① 口径：'runtime-check' 也算门族（本轮 P1 的回归护栏）。
  expect("familyOf('design-icon-size:runtime-check') === 'gate'",
    familyOf('design-icon-size:runtime-check'), 'gate')
  expect("familyOf('design-spacing-scale:runtime-check') === 'gate'",
    familyOf('design-spacing-scale:runtime-check'), 'gate')
  // ② 自证优先。
  expect("familyOf('design-icon-size:runtime-check:selftest') === 'selftest'",
    familyOf('design-icon-size:runtime-check:selftest'), 'selftest')
  // ③ 非门脚本不算门族。
  expect("familyOf('web-gates:run') === null", familyOf('web-gates:run'), null)
  // ③' ★ `typecheck` 的段名以 'check' 结尾 ⇒ ⇒ 落进门族 ⇒ ⇒ **必须被登记**
  //      （★ 登记在 EXCLUDED 里也算数 ⇒ ⇒ ★ 判据是「被收住」而不是「在 GATES 里」）。
  expect("familyOf('typecheck') === 'gate'", familyOf('typecheck'), 'gate')
  expect("'typecheck' 已被登记（GATES 或 EXCLUDED）",
    isCovered('typecheck', GATES) || EXCLUDED.some((e) => e.name === 'typecheck'), true)
  // ④ 排除项都写了理由。
  const noReason = EXCLUDED.filter((e) => !e.reason || !e.reason.trim()).map((e) => e.name)
  expect('EXCLUDED 每条都写了理由', noReason.length === 0, true)
  // ⑤ 清单自洽。
  expect('checkManifest() 为空', checkManifest().length, 0)
  expect('checkCoverage() 为空', checkCoverage().length, 0)
  // ⑥ ★★ 反向覆盖检查有牙（★ 这一条决定本自证本身有没有牙）★★
  const fakeGate = 'synthetic-family:runtime-check'
  const excludedNames = new Set(EXCLUDED.map((e) => e.name))
  const fakeFam = familyOf(fakeGate)
  const fakeCovered = excludedNames.has(fakeGate) || isCovered(fakeGate, GATES)
  expect('未登记的 runtime-check 门必须被判为未覆盖（★ 反向检查有牙）', fakeCovered, false)

  let failed = 0
  for (const c of cases) {
    if (!c.ok) failed++
    console.log(`  ${c.ok ? '✔' : '✘'} ${c.name}` + (c.ok ? '' : ` （实测 ${JSON.stringify(c.got)} ≠ 期望 ${JSON.stringify(c.want)}）`))
  }
  console.log(`\n自证：${cases.length - failed}/${cases.length} 通过`)
  if (failed > 0) process.exit(1)
}

function main() {
  const argv = process.argv.slice(2)

  // ★ 自证入口（★ 先于清单检查：★ 本入口的判据坏了，也得能跑出自证）。
  if (argv.includes('--selftest')) {
    console.log('[web-gates:selftest] 本入口的自证：')
    selftest()
    return
  }

  const missing = checkManifest()
  if (missing.length > 0) {
    console.error('✘ 门清单自身有问题（★ 本入口自己有牙 ⇒ 不必等 CI 才发现）：')
    for (const m of missing) console.error(`  ${m}`)
    process.exit(2)
  }

  // ★★ 反向覆盖检查（P1-1）：★ package.json 里每个门族脚本都必须在清单里
  // ★★ 或在 EXCLUDED 里带理由 ⇒ 否则它就是「CI 上一次都没跑过」的那道门。
  const uncovered = checkCoverage()
  if (uncovered.length > 0) {
    console.error('✘ 有门/自证脚本没进本入口的清单（★ 它们在 CI 上不会被执行）：')
    for (const m of uncovered) console.error(`  ${m}`)
    console.error('  ⇒ 修法：把它加进 GATES/SELFTESTS，或在 EXCLUDED 里写明排除理由。')
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

  const onlyIdx = argv.indexOf('--only')
  const only = onlyIdx >= 0 ? argv[onlyIdx + 1] : null
  // ★★ P1-2：--only 的值必须是 gates / selftests 之一。
  // ★★ 此前拼错（`--only gate` / `--only self`）⇒ plan 为空 ⇒ 打印
  // ★★ 「✔ 全部 0 项通过」并 exit 0 ⇒ **一次都没跑却报通过**（恒绿）。
  if (onlyIdx >= 0 && only !== 'gates' && only !== 'selftests') {
    console.error(`✘ --only 的取值只能是 gates 或 selftests（收到 ${JSON.stringify(only ?? null)}）`)
    process.exit(2)
  }
  const plan = []
  if (!only || only === 'gates') plan.push(...GATES.map((n) => [n, 'gate']))
  if (!only || only === 'selftests') plan.push(...SELFTESTS.map((n) => [n, 'selftest']))

  console.log(`[web-gates] 开始执行 ${plan.length} 项（门 ${GATES.length} + 自证 ${SELFTESTS.length}）`)

  // ★ 包管理器**只选一次**（★ 与 scripts/lib/node-pm.sh 同一契约 ⇒ ★ 显式 WEB_PM，
  // ★ 否则有 pnpm 用 pnpm、没有就退 npm）⇒ ⇒ ★ 挑一次、跑它、原样透传退出码。
  let pm
  try {
    pm = resolvePm()
  } catch (e) {
    console.error(`✘ ${e.message}`)
    process.exit(2)
  }
  console.log(`[web-gates] 包管理器 = ${pm}（★ 契约见 scripts/lib/node-pm.sh）`)

  const failed = []
  for (const [name, kind] of plan) {
    const r = runOne(name, pm)
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
