#!/usr/bin/env node
/**
 * design-hero-metric-runtime-check.mjs
 * ─────────────────────────────────────────────────────────────────────────────
 * 「巨型数字 + 小标签 + 辅助统计 + 强调色」的 hero 指标模板（第 32 增量）
 *
 * 这道门治的是哪条明文
 * ────────────────────
 * impeccable `skill/reference/craft-floor.md` 的 "Refuse" 段（Page scaffolds 一栏）：
 *   - The hero-metric template: big number, small label, supporting stats, accent.
 *     <!-- rule:skill-ban-hero-metric -->
 * 仓内 `DESIGN.md` §6「卡片」把它写成硬禁令：
 *   - 不许用「巨型数字 + 小标签 + 辅助统计 + 强调色」当 hero 指标模板。
 *     指标要有语境：同比、口径、时间窗、异常指向，不是四个数字并排。
 *
 * ★★ 为什么这道门之前不存在、为什么第 30 轮那道门**抓不到**它
 * ──────────────────────────────────────────────────────────────────────────
 * 第 30 轮已经有「同尺寸同构的裸指标卡网格当页面骨架」的运行时尺
 * （design-metric-skeleton-runtime-check.mjs），但它的卡片判定
 * `isBareMetricCard` 里有两条**主动排除**：
 *   ① 「卡内恰好 2 个文本叶子（标签 + 值）—— 多一个叶子就说明卡里还有
 *       说明/口径/时间窗，那是「有语境」，不判红」；
 *   ② 「卡里没有 svg / canvas / progress / table」。
 * ⇒ ★★ 而 craft-floor 点名的 hero-metric 模板**恰恰由「辅助统计」构成**
 *   （big number + small label + supporting stats + accent）⇒
 *   ⇒ **模板的第三个要素正好落在第 30 轮门的排除项里** ⇒ 模板整族漏网。
 * ⇒ 这不是第 30 轮的错：那是它按 DESIGN.md「有语境不判红」的明文收窄，
 *   收窄对了自己的口径，只是**口径之间留了一条缝**。本轮把那条缝补上。
 *
 * ★★ 判据必须落在「渲染后的事实」上（★ 第 30/31 增量通式）
 * ──────────────────────────────────────────────────────────────────────────
 * 「这个数字是不是巨型」「这一格是不是被强调」都取决于**计算后的字号与颜色**，
 * 源码里写着 `<div class="stat-val">` 推不出任何一条。
 * ⇒ 与仓内既有运行时尺同族，做成【浏览器运行时尺】。
 *
 * ★★ 定稿判据（★ 经全量 108 页实测 + 目视评审双向核对后收窄）
 * ──────────────────────────────────────────────────────────────────────────
 *   ① **指标格（metric cell）** = 一个「表面」盒子，同时满足：
 *        a. **巨数字**：存在文本叶子，字号 >= 24px（仓内 `--kx-text-2xl`，
 *           即「巨数字」的规范档）**且内容含数字**；
 *        b. **小标签**：存在另一个文本叶子，字号 <= 13px；
 *        c. **不是商品卡**：格内**没有交互控件**（button / a[href] / [role=button]）
 *           —— 「立即购买」那类 CTA 说明它是**被售的商品**，不是指标；
 *        d. **不是药丸**（圆角 >= 高度的一半）、**不是隐藏子树**、
 *           **不是一个整段页面**（宽度 <= 400px：24px 的数字摆在 660px 宽的
 *           区块里是「页面章节」，不是「指标卡」）。
 *   ② **装饰性强调色** = 巨数字的**计算色**命中「装饰性主色集」
 *      {--accent, --accent-h, --kx-primary, --kx-primary-hover, --el-color-primary}
 *      的实测计算值（每通道容差 2）。
 *      ★ **语义色集 {--success, --warning, --danger} 不算强调色** ——
 *        那是**状态表达**（DESIGN.md §3 允许：软填充 + 文字颜色 + 文字本身），
 *        不是**装饰性强调**。★ 这一条是本轮**排除合法存量**的关键：
 *        控制台的抬头摘要条（总检测次数 / PII 命中 / 密钥命中 …）用语义色
 *        表达命中类型，目视复核认定它是**合法的存量**；
 *        而 `/pricing` 的七格摘要用**主色**染数字，目视复核认定它是**真违规**。
 *        ⇒ **两者的结构完全同形 ⇒ 唯一能分开它们的就是「这个色是不是装饰性主色」。**
 *   ③ **页面级硬闸**：同一父容器下 **>= 3 个**指标格，且其中 **>= 2 个**的
 *      巨数字带装饰性强调色 ⇒ 判红。
 *      ★ 为什么下限是 3 而不是 1：两格的摘要条不是「四个数字并排」；
 *        而「并排」这件事本身就是这条例子的要害（DESIGN.md：不是四个数字并排）。
 *
 * ★★ 三处「量具自己坏了」的自保（★ 第 31 增量的教训：0 命中有三种读法）
 * ──────────────────────────────────────────────────────────────────────────
 *   ① **强调色集合解析不出来 ⇒ 不许判绿**。
 *      `--accent: var(--kx-primary)` 是**变量链**，`getPropertyValue` 拿到的是
 *      " var(--kx-primary)" 而不是色值（★ 仓内第 22 轮的主色门就有这个疏漏：
 *      它只解析了字面量 `--kx-primary`，`--accent` 那一项其实是空的）。
 *      ⇒ 本门用**探针元素**实测解析：`color: rgb(1,2,3); color: var(--x)`，
 *      读计算色；若回落到哨兵色 ⇒ 该变量不存在 ⇒ 跳过。
 *      ⇒ 若最终「强调色集合为空」⇒ 门**判红并打出探针**
 *        （`HERO-METRIC-ACCENT-UNRESOLVED`），绝不把「没量到」写成「通过」。
 *   ② **色值比较用 chroma 而不是 HSL 饱和度**。
 *      HSL 饱和度对**深色**会放大：近黑石板色 #0f172a 的 HSL 饱和度是 0.47，
 *      而真正的蓝 #2563eb 是 0.86 —— 阈值卡在哪都会误判。
 *      ⇒ 本门用**集合精确匹配**（而不是任何阈值）⇒ 天然没有这个坑。
 *   ③ **只量「渲染出来的」**。本门测的是**当次数据**下渲染出来的页面：
 *      数据降级导致某块内容不渲染时，那一页读数是 0 —— 那是「没扫到」，
 *      不是「真没有」。★ 本门在输出里显式区分这两种状态。
 *
 * 用法
 * ────
 *   node scripts/design-hero-metric-runtime-check.mjs                    # 跑全部页面
 *   node scripts/design-hero-metric-runtime-check.mjs --selftest          # 判据自证（不需要浏览器）
 *   node scripts/design-hero-metric-runtime-check.mjs --pages=/,/pricing
 *   node scripts/design-hero-metric-runtime-check.mjs --json=out.json
 *   node scripts/design-hero-metric-runtime-check.mjs --update-baseline   # 人工降基线（只许降）
 *
 * 环境依赖
 * ────────
 *   需要本机有 Google Chrome（无头模式 + CDP）。
 *   没有 Chrome 时本工具**不产出任何判定** ⇒ 打印 SKIP 并说明「未测量」，
 *   绝不把「没量到」写成「通过」。
 */
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const BASELINE = join(ROOT, 'scripts', 'design-hero-metric-runtime-baseline.json')

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean)

const BASE = process.env.GATEWAY_WEB_ORIGIN || 'http://localhost:5200'

/**
 * 页面清单：**从 `src/router.ts` 自动派生**（全部静态路由）。
 * ★ 与第 30/31 轮同一口径：写死的清单会悄悄过期，新路由就无人看守。
 */
export const FALLBACK_PAGES = [
  '/',
  '/free-pool',
  '/model-pricing',
  '/providers',
  '/request-logs',
  '/keys',
  '/admin/proxy',
  '/models',
  '/routing-v2',
  '/admin/ops-overview',
  '/admin/board-operational',
  '/admin/data-lifecycle',
  '/system-monitor',
]

function derivePages() {
  const routerFile = join(ROOT, 'src', 'router.ts')
  if (!existsSync(routerFile)) return null
  try {
    const src = readFileSync(routerFile, 'utf8')
    const paths = [...src.matchAll(/path:\s*'([^']+)'/g)].map((m) => m[1])
    const uniq = [...new Set(paths)]
    return uniq.length ? uniq : null
  } catch {
    return null
  }
}

export const DEFAULT_PAGES = derivePages() ?? FALLBACK_PAGES

// ── tab 变体页面（第 36 增量新增）──────────────────────────────────────
//
// ★★ 这条缝的来历：★ 第 32 增量审计档 §7.2 写着「board 接口在本机是降级的 ⇒
//   这一排卡在本机**根本不渲染** ⇒ 运行时读数是 0，那是『没扫到』不是『真没有』」
//   ⇒ 于是那一轮只能靠**源码守卫**守住 `BoardHeroRow`。
// ★★ 而现在 board 接口已正常（HTTP 200、degraded:false）⇒ **运行时能看见了**
//   ⇒ 但**静态路由清单**里根本没有 `/dashboard?tab=board` ⇒ 门看不见它。
//   ⇒ ★★ 而看板 tab 正是产品的主屏（4 张 hero 卡 + 8 张 mini 卡）⇒ 覆盖缺口最疼的一页。
//
// ★ 为什么不「手写一个 tab 清单」：★ 手写的清单会腐化（视图加了新 tab ⇒ 门不知道）。
//   ⇒ 改成**从视图源码派生**：★ 每个视图用它自己声明 tab id 的那一行（marker），
//   门读那一行里的字符串字面量 ⇒ ★ 视图加 tab ⇒ 派生出的清单自动跟着长。
//   ⇒ ★ 派生失败（找不到 marker / 读不到文件）⇒ **problems 非空 ⇒ 门在测量前就判红**
//   ⇒ ★ 「量具坏了」不许静默通过。
export const TAB_PAGE_SOURCES = [
  {
    path: '/dashboard',
    source: 'src/views/DashboardView.vue',
    // ★ `export type DashboardTabId = 'board' | ... 'systemmonitor'` 里有**兼容别名**
    //   `systemmonitor`（已映射到 selfcheck）⇒ 用 `const VALID_TABS:` 那行（权威集合，4 个）。
    marker: 'const VALID_TABS:',
  },
  { path: '/models', source: 'src/views/ModelsView.vue', marker: 'type PageTab =' },
  {
    path: '/routing-v2/auto-ops',
    source: 'src/views/AutoRoutingOpsView.vue',
    marker: 'type OpsTab =',
  },
  {
    path: '/routing-v2/credentials',
    source: 'src/views/CredentialMonitorWithTabs.vue',
    marker: 'type ViewTab =',
  },
  {
    path: '/routing-v2',
    source: 'src/views/RoutingDashboardView.vue',
    // ★ 该视图的 tab 联合类型写在 `ref<...>` 的泛型里（不是独立的 type 行）
    //   ⇒ marker 钉到 `const activeTab = ref<` ⇒ 顺带避开同文件里 `nodeDrawerTab` 的抽屉 tab。
    marker: 'const activeTab = ref<',
  },
  {
    path: '/admin/prompt-injection',
    source: 'src/views/PromptInjectionSettingsView.vue',
    marker: 'const validTabs =',
  },
]

/**
 * 从视图源码里**抽出它自己声明的 tab id**（纯函数，可自证）。
 *
 * ★ 口径：找到**第一行**含 `marker` 的行 ⇒ 取该行里所有单引号字符串字面量 ⇒ 去重。
 * ★ 为什么要「第一行」：同一文件里可能有多个形状相似的声明
 *   （例：`DashboardView.vue` 的 type 联合里含**兼容别名** `systemmonitor`，
 *   而 `VALID_TABS` 数组里没有 ⇒ 用 marker 精确指到权威那一行）。
 * ★ 抽不出 ⇒ 返回 `null`（★ **失败必须显式**，不能返回空数组冒充「这个视图没有 tab」）。
 *
 * @param {string} sourceText 视图源码
 * @param {string} marker 声明所在行的锚点
 * @returns {string[] | null}
 */
export function extractTabIds(sourceText, marker) {
  const text = String(sourceText ?? '')
  const m = String(marker ?? '')
  if (!m) return null
  const line = text.split('\n').find((l) => l.includes(m))
  if (!line) return null
  const ids = [...line.matchAll(/'([^']+)'/g)].map((x) => x[1])
  const uniq = [...new Set(ids)]
  return uniq.length ? uniq : null
}

/**
 * 派生 tab 变体页面清单（★ 失败要显式 ⇒ `problems` 非空时门必须判红）。
 * @returns {{pages: string[], problems: string[]}}
 */
export function deriveTabPages() {
  const pages = []
  const problems = []
  for (const entry of TAB_PAGE_SOURCES) {
    const file = join(ROOT, entry.source)
    if (!existsSync(file)) {
      problems.push(`${entry.path}：读不到 ${entry.source}`)
      continue
    }
    let ids = null
    try {
      ids = extractTabIds(readFileSync(file, 'utf8'), entry.marker)
    } catch (e) {
      problems.push(`${entry.path}：读 ${entry.source} 失败（${String(e?.message || e)}）`)
      continue
    }
    if (!ids) {
      problems.push(`${entry.path}：在 ${entry.source} 里找不到 marker「${entry.marker}」`)
      continue
    }
    for (const id of ids) pages.push(`${entry.path}?tab=${id}`)
  }
  return { pages, problems }
}

const TAB_PAGES = deriveTabPages()
/** ★ 派生失败清单（非空 ⇒ 门在测量前判红；⇒ 「量具坏了」不许静默通过）。 */
export const TAB_PAGE_PROBLEMS = TAB_PAGES.problems
/** ★ 路由静态路径 + tab 变体。 */
export const ALL_PAGES = [...DEFAULT_PAGES, ...TAB_PAGES.pages]

/** 「巨数字」的下限：仓内 `--kx-text-2xl` = 24px。 */
export const BIG_VALUE_MIN_PX = 24
/**
 * 「小标签」的上限：仓内 stat 标签实际用到的档位有 11px（`--kx-text-2xs`）、
 * 12px（`--kx-text-xs`）、13px（`--kx-text-sm`）、**14px（`--kx-text-md`）**。
 * ★ 第一版写的是 13px ⇒ ★★★ **漏掉了 `--kx-text-md` 那一档** ⇒ ★★★ 而那正好是
 *   `OutputComplianceView.vue` 的 `.stat-label`（14px）⇒ ⇒ **那 6 张统计卡
 *   在第一版判据下根本不是「指标格」** ⇒ ⇒ **门对那一族形态没有牙**
 *   ⇒ ★ 这个疏漏是被**真仓变异臂的 M2 臂**抓出来的（★ 注入主色染色 ⇒ 门没红）。
 * ⇒ 修正为 14px。★ ★★ **放宽后的影响已离线复算过**：标签阈值 13 / 14 / 15px
 *   三档下，**「墙」的集合完全相同**（3 页 / 5 处，其中 4 处是商品卡、被 CTA 排除）
 *   ⇒ ⇒ **这次放宽不新增任何假红**，只是把「14px 标签」那一族纳入覆盖。
 */
export const SMALL_LABEL_MAX_PX = 14
/** 指标格的宽度上限：超过它就不是「卡片」，是页面章节。 */
export const MAX_CELL_WIDTH_PX = 400
/** 一行里至少几个指标格才谈得上「并排」。 */
export const MIN_CELLS_IN_ROW = 3
/** 一行里至少几个带装饰性强调色才判红。 */
export const MIN_ACCENT_CELLS = 2
/** 色值比较的每通道容差。 */
export const COLOR_TOLERANCE = 2

/**
 * ★★ 「不是业务页」的落点（第 36 增量新增）—— **没量到**不许读成**通过**。
 *
 * ★ 这条判据是被**本轮自己踩到**的：★ 本机 8782 后端处于「未激活」态 ⇒
 *   应用的 bootstrap 门把**每一个业务页**都重定向到 `/bootstrap` ⇒
 *   而 `/bootstrap` 上没有任何指标格 ⇒ 门读到「0 处墙」⇒ **判绿**
 *   ⇒ ★★★ **那不是「这一页干净」，那是「这一页压根没被量到」**。
 * ⇒ ⇒ 「0 命中」的第五种读法（本轮新发现的）：**整站被重定向走了**。
 *
 * ★ 口径：只有落到这些**非业务页**才算「被重定向走」；
 *   路由表里的**正常重定向**（`/catalog` → `/models?tab=catalog`、
 *   `/maas/account` → `/tenant/account` …）落到的是**另一个业务页** ⇒ 那是合法的跳转，
 *   量到的那一页确实是用户会看到的那一页 ⇒ 不判红（但把 `finalPath` 记进读数里）。
 */
export const NON_PAGE_PATHS = ['/bootstrap', '/login', '/forbidden', '/maintain']

/**
 * 这一页是不是「被重定向到非业务页」了（⇒ 没量到，不是通过）。
 * @param {string} requested 请求的页（含 query）
 * @param {string} finalPath 浏览器里的最终 pathname
 * @returns {boolean}
 */
export function redirectedAway(requested, finalPath) {
  const want = String(requested ?? '').split('?')[0]
  const got = String(finalPath ?? '')
  if (!got) return true
  if (got === want) return false
  return NON_PAGE_PATHS.some((p) => got === p || got.startsWith(`${p}/`))
}

/**
 * ★ 探针版本号（第 36 增量新增）。
 *
 * ★ 为什么要它：探针是**跑在浏览器里的一段字符串** ⇒ 自证**测不到它的 DOM 行为**
 *   ⇒ 只能靠「真仓变异臂」在真实浏览器里验。
 * ⇒ 但探针本身可以**自己声明版本** ⇒ 门读回版本号并与常量比对
 *   ⇒ ★ 改了探针却忘了改版本号 ⇒ 门立刻红（而不是「量具悄悄换了口径」）。
 */
export const PROBE_VERSION = 'hero-metric-probe/2026-10-28-36-2'

/**
 * 货币符号（商品标价 / 货币金额）。
 *
 * ★ 为什么它是一条排除项：hero-metric 是**指标**模板；商品卡上那个
 *   「¥29.00」是**被售商品的标价**，它被强调是商业表达，不是「拿数字当主角」。
 *   ⇒ 货币计价的数字不进指标格。
 * ★ 为什么要**两条**排除项（CTA + 货币）：CTA 那条是**视角依赖**的 ——
 *   `pricing-card` 的「立即购买」按钮带 `v-if="!isAdminTenantView"`，
 *   管理员视角下按钮不渲染 ⇒ 只靠 CTA 那条，管理员视角会把它误判成指标墙。
 *   ⇒ 货币那条与视角无关 ⇒ 两条并用，覆盖才稳。
 * ★ 已知代价：一条「以货币计量、且被主色染、且 ≥3 格并排」的指标墙会被漏掉。
 *   这是**判据宁可窄不可宽**的取舍（★ 本仓第 29~31 增量的一贯口径）。
 */
export const CURRENCY_PATTERN = /[¥$€£₤]/

/** 这个数字是不是货币计价。 */
export function isCurrencyValue(text) {
  return CURRENCY_PATTERN.test(String(text ?? ''))
}

/** 装饰性主色：这些 token 是「品牌主色」，用它染数字就是装饰性强调。 */
export const ACCENT_TOKEN_NAMES = [
  '--accent',
  '--accent-h',
  '--kx-primary',
  '--kx-primary-hover',
  '--el-color-primary',
]
/** 语义色：它们表达**状态**，不是装饰。命中它们**不算**强调色。 */
export const SEMANTIC_TOKEN_NAMES = ['--success', '--warning', '--danger']

/**
 * ★★ 主色的**淡染**（accent-soft 族）—— 第 36 增量新增。
 *
 * ★ 为什么单独一族：craft-floor 的 hero-metric 模板第 4 要素是「accent」，
 *   而 accent 落在**卡底**上时（淡蓝底）它就是「主色的淡染」⇒ 要能看见它。
 * ★ 为什么用 **token 精确匹配**而不是颜色空间阈值：★ 第 32 增量实测过
 *   「主色淡染 = 低饱和度」这条路是**错的**（`#0f172a` 饱和度 0.47 落在任何阈值之上），
 *   而本仓把淡染**表达成 token** ⇒ 直接比色值，既准又不碰阈值。
 * ★ 为什么**不把 `--bg-subtle` 列进来**：它是**通用**token（浅色模式下恰好别名到
 *   `--kx-primary-soft`，暗色模式下是 `--kx-sidebar`）⇒ 把它当「accent 族」会让暗色主题
 *   下的每个次级底色都变成 accent。★ 真正要抓的是**按名就是 accent-soft 的那几个**；
 *   而 `--bg-subtle` 的浅色模式取值**与 `--kx-primary-soft` 同值** ⇒ 靠**色值匹配**照样命中
 *   ⇒ ⇒ 两条路都通，且暗色模式不会误报。
 */
export const TINT_TOKEN_NAMES = ['--accent-soft', '--primary-soft', '--kx-primary-soft']

/**
 * ★ accent 可以落在哪些地方（第 36 增量）。
 *
 * ★ 第 32 增量把 accent 信号**收窄到「巨数字的计算色」**，理由写进了审计档 §3.3：
 *   「静态扫查全仓，容器级主色命中 236 处（每个主色按钮、每个选中 chip、每个激活 tab、
 *   每条进度条、每个图例点）⇒ 任何阈值都分不开『装饰性 tint』与『交互控件的正常主色』」。
 * ★★ **那个结论的分母是「全仓所有元素」** ⇒ 而本轮的分母是
 *   **「已经是指标格的容器」**（巨数字 ≥24px 含数字 + 小标签 ≤14px + 无 CTA + 宽度 ≤400px）
 *   ⇒ 实测 76 个指标格里容器级 accent 只有 **7 处** ⇒ 交互控件那 236 处**根本进不了这个分母**
 *   ⇒ ⇒ **不是推翻上一轮的结论，而是把它的口径换到一个更窄的分母上重新量**。
 */
export const ACCENT_LANDINGS = ['value', 'bg', 'border', 'shadow']

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ w: number, h: number, radius: number, cls: string,
 *             parentKey: string, leaves: {text: string, fs: number, color: string}[],
 *             hasInteractive: boolean, hidden: boolean }} CellDesc
 */

/**
 * 解析 rgb/rgba 字符串 → [r,g,b]；解析不出来返回 null。
 *
 * ★★ 第 36 增量补了 `color(srgb r g b)` 形态：★ Chrome 对 `color-mix()` /
 *   相对色语法给出的**计算值**就是这个形态（实测仓里有 3 个指标格的底色是它）
 *   ⇒ 旧版只认 `rgb()` ⇒ **那 3 个格子的底色对量具是隐形的**
 *   ⇒ ★ 这就是「『0 命中』的第三种读法：量具自己坏了」的一个真实实例。
 */
export function parseRgb(color) {
  const s = String(color ?? '').trim().toLowerCase()
  const m = s.match(/rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/)
  if (m) return [Number(m[1]), Number(m[2]), Number(m[3])]
  const c = s.match(/color\(\s*srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)/)
  if (c) return [Number(c[1]) * 255, Number(c[2]) * 255, Number(c[3]) * 255]
  return null
}

/** 两个颜色是否相同（每通道容差）。 */
export function sameColor(a, b, tolerance = COLOR_TOLERANCE) {
  const x = parseRgb(a)
  const y = parseRgb(b)
  if (!x || !y) return false
  return x.every((v, i) => Math.abs(v - y[i]) <= tolerance)
}

/**
 * 由「token 名 → 实测计算色」的映射构造「装饰性强调色集合」。
 *
 * ★ 语义色的 token 名会被**剔除**：即使某个语种/主题下 `--danger` 恰好等于
 *   `--accent` 的值，它表达的仍然是状态 ⇒ 不该被判成装饰性强调。
 *
 * @param {Record<string, string>} resolved token 名 → 计算色
 * @returns {{ accents: string[], unresolved: string[] }}
 */
export function buildAccentSet(resolved) {
  const map = resolved && typeof resolved === 'object' ? resolved : {}
  const accents = []
  const unresolved = []
  // ★ 去重：`--accent: var(--kx-primary)` 与 `--kx-primary` 解析出的是**同一个色**
  //   ⇒ 集合里留一份即可（这个集合是「颜色集合」，不是「token 清单」）。
  const seen = new Set()
  for (const name of ACCENT_TOKEN_NAMES) {
    const v = map[name]
    if (!v || !parseRgb(v)) {
      unresolved.push(name)
      continue
    }
    if (SEMANTIC_TOKEN_NAMES.includes(name)) continue
    const key = parseRgb(v).join(',')
    if (seen.has(key)) continue
    seen.add(key)
    accents.push(v)
  }
  return { accents, unresolved }
}

/**
 * 由「token 名 → 实测计算色」构造「主色淡染集合」（第 36 增量）。
 *
 * ★ 与 `buildAccentSet` 同构：按**色值**去重（三个 token 解析出同一个色 ⇒ 留一份）。
 * @param {Record<string, string>} resolved token 名 → 计算色
 * @returns {{ tints: string[], unresolved: string[] }}
 */
export function buildTintSet(resolved) {
  const map = resolved && typeof resolved === 'object' ? resolved : {}
  const tints = []
  const unresolved = []
  const seen = new Set()
  for (const name of TINT_TOKEN_NAMES) {
    const v = map[name]
    if (!v || !parseRgb(v)) {
      unresolved.push(name)
      continue
    }
    const key = parseRgb(v).join(',')
    if (seen.has(key)) continue
    seen.add(key)
    tints.push(v)
  }
  return { tints, unresolved }
}

/** 这个颜色是不是装饰性强调色（命中强调色集合，且**不**是语义色）。 */
export function isDecorativeAccent(color, accentColors, semanticColors = []) {
  if (!parseRgb(color)) return false
  const list = Array.isArray(accentColors) ? accentColors : []
  if (!list.length) return false
  // 语义色优先否决：状态色即使与主色同值也不当装饰。
  for (const s of Array.isArray(semanticColors) ? semanticColors : []) {
    if (sameColor(color, s)) return false
  }
  return list.some((a) => sameColor(color, a))
}

/**
 * 取「巨数字叶子」：字号最大的那个文本叶子（★ 第 36 增量抽成纯函数 ——
 * 同一段「取最大叶子」的逻辑在旧版里被抄了三遍，改一处漏两处是迟早的事）。
 */
export function bigLeaf(cell) {
  const leaves = Array.isArray(cell?.leaves) ? cell.leaves : []
  let big = null
  for (const l of leaves) {
    const fs = Number(l?.fs)
    if (!Number.isFinite(fs)) continue
    if (!big || fs > Number(big.fs)) big = l
  }
  return big
}

/** 取「小标签叶子」：字号最小且 ≤ 小标签上限的那个文本叶子。 */
export function smallLeaf(cell) {
  const leaves = Array.isArray(cell?.leaves) ? cell.leaves : []
  let small = null
  for (const l of leaves) {
    const fs = Number(l?.fs)
    if (!Number.isFinite(fs) || fs > SMALL_LABEL_MAX_PX) continue
    if (!small || fs < Number(small.fs)) small = l
  }
  return small
}

/**
 * 判「这一格是不是指标格」（巨数字 + 小标签）。
 *
 * @param {CellDesc} cell
 * @returns {boolean}
 */
/** ★ 这一页量到了几个「指标格」（★ 纯函数 ⇒ CLI 与自证同源）。 */
export function metricCellCountOf(probe) {
  const cells = Array.isArray(probe?.cells) ? probe.cells : []
  return cells.filter((c) => isMetricCell(c)).length
}

/**
 * ★★ 第 36 增量：**门的输出也是门的契约** —— 分母提示必须**按分母分支**。
 *
 * ★★ 本轮当场抓到的自伤：★ 原实现**无条件**挂了一句「（分母为 0 ⇒ 这一类页面本轮没量到东西）」
 *   ⇒ ★★ 而那次运行的分母明明是 **4** ⇒ ★★ 输出在说一件**假事**（读者会以为「分母为 0」）
 *   ⇒ ★★ 这比假红更坏：★ **假红只是吵，假输出是骗**（★ 读数被当成「没量到」⇒ 结论反向）。
 * ⇒ ★★ 修法：★ 分母 > 0 ⇒ 只报数；★ 分母 = 0 ⇒ **必须**说「没量到指标格」。
 * ⇒ ★★ 纯函数 ⇒ CLI 与自证**同源**（★ 不用工具自己的成功日志当证据）。
 */
export function metricDenomNote(total) {
  const n = Number(total) || 0
  if (n <= 0) {
    return '  ⓘ 指标格分母：本次量到 0 个 ⇒ 这一类页面本轮**没量到指标格**（★ 不许读成「通过」）'
  }
  return `  ⓘ 指标格分母：本次量到 ${n} 个`
}

/**
 * ★★ 第 36 增量：**「0 个指标格 ⇒ 重探一次」的可注入实现**（★ 纯逻辑 ⇒ 可自证）。
 *
 * ★ 起因（本轮实测）：★ 全量跑读到 `/admin/data-lifecycle` 的 `metricCellCount = 0`，
 *   而**手动复现**（同一份代码、同一后端）读到 **4** ⇒ ★★ 差别是**那一次的数据还没到**
 *   （★ `data.columnar` 没来 ⇒ 区块没渲染）⇒ ★★ 而稳定判据只管「有没有变化」
 *   ⇒ ★★★ **⇒ 「0 个指标格」不等于「这一页没有指标格」**。
 *
 * ★★ 为什么必须**可注入**而不是把 `await sleep(1500)` 写死在测量循环里：
 *   ★★ 若这一段**没有接线级自证**，则「判据写错了」与「压根没接上」在读数上**长得一样**
 *   ⇒ ★★ **假绿**（★ 「没量到」被读成「通过」）⇒ ⇒ 所以：★ 抽成可注入函数（★ 自证用假探针喂）
 *   + ★ 读数带 `reprobeCount`（★ 真实跑里「0 格」的页必然是 1 ⇒ 没接上立刻暴露）。
 *
 * @param {() => Promise<object>} probeFn 取一次探针结果（**已解析的对象**，不是 JSON 串）
 * @param {(ms:number)=>Promise<void>} sleepFn
 * @param {object|undefined} firstProbe 已知的那一次探针结果（★ 可省一次探测）
 * @param {number} reprobeDelayMs 重探前的等待
 * @returns {Promise<{probe:object, reprobeCount:number, firstCellCount:number}>}
 */
export async function probeWithReprobe(probeFn, sleepFn, firstProbe = undefined, reprobeDelayMs = 1500) {
  const first = firstProbe === undefined ? await probeFn() : firstProbe
  const firstCellCount = metricCellCountOf(first)
  if (firstCellCount !== 0) {
    return { probe: first, reprobeCount: 0, firstCellCount }
  }
  await sleepFn(reprobeDelayMs)
  const second = await probeFn()
  return { probe: second, reprobeCount: 1, firstCellCount }
}

export function isMetricCell(cell) {
  if (!cell || typeof cell !== 'object') return false
  // 隐藏子树：用户看不到的格子不是指标墙。
  if (cell.hidden) return false
  // ★ 第 36 增量：**格子自己就是控件** ⇒ 它是按钮/chip，不是指标卡。
  //   （旧版只查「格子里有没有控件」，漏了「这格本身是个控件」这一形态。）
  if (cell.isControl) return false
  // 商品卡 / 操作卡：有 CTA ⇒ 它卖东西或给人点，不是指标。
  if (cell.hasInteractive) return false
  const leaves = Array.isArray(cell.leaves) ? cell.leaves : []
  if (leaves.length < 2) return false
  const w = Number(cell.w)
  const h = Number(cell.h)
  // 一个整段页面不是「指标卡」。
  if (!Number.isFinite(w) || w <= 0 || w > MAX_CELL_WIDTH_PX) return false
  // 药丸是 chip，不是指标格。
  const radius = Number(cell.radius)
  if (Number.isFinite(radius) && Number.isFinite(h) && h > 0 && radius >= h / 2) return false
  const big = bigLeaf(cell)
  if (!big) return false
  const bigFs = Number(big.fs)
  if (bigFs < BIG_VALUE_MIN_PX) return false
  // 巨数字必须真的是数：标题、句子都不是指标值。
  if (!/[0-9]/.test(String(big.text ?? ''))) return false
  // 货币计价的是商品标价 / 货币金额，不是 hero 指标。
  if (isCurrencyValue(big.text)) return false
  // 小标签：另有一个明显更小的文本叶子（★ 相对判据：必须**小于**巨数字字号）。
  const hasSmall = leaves.some(
    (l) =>
      l !== big &&
      Number(l?.fs) <= SMALL_LABEL_MAX_PX &&
      Number(l?.fs) < bigFs &&
      String(l?.text ?? '').trim() !== '',
  )
  if (!hasSmall) return false
  return true
}

/**
 * ★★ 这一格的「accent 落在哪儿」（第 36 增量新增的核心判据）。
 *
 * ★ 返回值是**落点清单**（`['value']` / `['bg']` / `['border']` / `['shadow']`），
 *   而不是布尔 ⇒ 门报出来的时候能说清「是卡底染了主色」还是「数字染了主色」。
 *
 * @param {CellDesc} cell
 * @param {string[]} accentColors 装饰性主色集合
 * @param {string[]} semanticColors 语义色集合（状态色，命中即否决）
 * @param {string[]} tintColors 主色淡染集合（accent-soft 族）
 * @returns {string[]}
 */
export function accentLandings(cell, accentColors, semanticColors = [], tintColors = []) {
  const out = []
  if (!isMetricCell(cell)) return out
  const accents = Array.isArray(accentColors) ? accentColors : []
  const tints = Array.isArray(tintColors) ? tintColors : []
  if (!accents.length && !tints.length) return out
  // ① 数字本身被主色染
  const big = bigLeaf(cell)
  if (big && isDecorativeAccent(big.color, accents, semanticColors)) out.push('value')
  // ② 卡底是主色的淡染（accent-soft 族）
  const bg = String(cell.bg ?? '')
  if (bg && tints.some((t) => sameColor(bg, t))) out.push('bg')
  // ③ 卡底描边命中主色（高亮卡的「主色描边」）
  for (const b of Array.isArray(cell.border) ? cell.border : []) {
    if (isDecorativeAccent(b, accents, semanticColors)) {
      out.push('border')
      break
    }
  }
  // ④ 阴影里含主色（高亮卡的「主色内发光」）
  const shadow = String(cell.shadow ?? '')
  const parts = shadow.match(/rgba?\([^)]*\)|color\([^)]*\)/g)
  if (parts && parts.some((x) => isDecorativeAccent(x, accents, semanticColors))) out.push('shadow')
  return out
}

/**
 * 找出「带装饰性强调色」的指标格。
 *
 * ★ 第 36 增量：accent 的**落点**从「数字文本色」扩到「数字 / 卡底淡染 / 卡底描边 / 阴影」。
 * ★ 新参数一律**追加在末尾**（★ 追加而不是插在中间：插在中间会让旧调用点把
 *   `semanticColors` 悄悄当成 `tintColors` ⇒ 一个「看起来能跑」的静默错绑）。
 */
export function isAccentCell(cell, accentColors, semanticColors = [], tintColors = []) {
  return accentLandings(cell, accentColors, semanticColors, tintColors).length > 0
}

/**
 * 找「并排的装饰性指标墙」。
 *
 * @param {CellDesc[]} cells
 * @param {string[]} accentColors
 * @param {string[]} semanticColors
 * @param {string[]} tintColors
 * @returns {{parentKey: string, cells: number, accentCells: number,
 *            classes: string[], labels: string[], values: string[],
 *            landings: string[]}[]}
 */
export function findAccentWalls(cells, accentColors, semanticColors = [], tintColors = []) {
  const list = Array.isArray(cells) ? cells : []
  if (!Array.isArray(accentColors) || !accentColors.length) return []
  const byParent = new Map()
  for (const c of list) {
    if (!isMetricCell(c)) continue
    const k = String(c.parentKey ?? '')
    if (!byParent.has(k)) byParent.set(k, [])
    byParent.get(k).push(c)
  }
  const out = []
  for (const [parentKey, group] of byParent) {
    if (group.length < MIN_CELLS_IN_ROW) continue
    const accents = group.filter((c) => isAccentCell(c, accentColors, semanticColors, tintColors))
    if (accents.length < MIN_ACCENT_CELLS) continue
    out.push({
      parentKey,
      cells: group.length,
      accentCells: accents.length,
      classes: [...new Set(group.map((c) => String(c.cls ?? '')))],
      labels: group.map((c) => String(smallLeaf(c)?.text ?? '').trim()),
      values: accents.map((c) => String(bigLeaf(c)?.text ?? '').trim()),
      // ★ 第 36 增量：把「accent 落在哪儿」一起报出来（门输出也是门的契约）。
      landings: [...new Set(accents.flatMap((c) => accentLandings(c, accentColors, semanticColors, tintColors)))],
    })
  }
  return out
}

/**
 * 页面级判定：这页是不是「拿装饰性指标墙当了页面骨架」。
 *
 * @param {walls} findAccentWalls 的结果
 * @returns {{ walls: number, cells: number, isViolation: boolean }}
 */
export function judgePageHeroMetric(walls) {
  const list = Array.isArray(walls) ? walls : []
  const cells = list.reduce((n, w) => n + (Number(w?.accentCells) || 0), 0)
  return {
    walls: list.length,
    cells,
    isViolation: list.length > 0,
  }
}

/**
 * ★ 把「一页的读数」组装成**一个对象**（纯函数）。
 *
 * ★★ 本轮修掉的一个真 bug 就出在这里：
 *   第一版 `runChrome` 产出的是 `walls: <数字>`（墙的**数量**），
 *   而 `collectHardGateViolations` 吃的是 `p.walls`（墙的**数组**）
 *   ⇒ `for (const w of p.walls ?? [])` 拿到数字 ⇒ **TypeError: number is not iterable**
 *   ⇒ ★★ **而且它只在「真的量到违规」时才崩** ⇒ ⇒ **之前那次全绿跑根本没走到上报路径**
 *   ⇒ ★★ 「门绿」原来是「门没走到那段代码」，不是「门量过了、判过了」。
 *   ⇒ 而自证里喂的是数组（`walls: [{...}]`）⇒ **断言恒真**：
 *     ★ 判据函数的自证喂的形状，与产出方实际产出的形状不一致 ⇒ 自证就测不到真正的接线。
 * ⇒ ⇒ 修法：产出与消费**都走这一个纯函数**，自证也走它 ⇒ 形状不一致就再也发生不了。
 *
 * @param {string} page
 * @param {CellDesc[]} cells
 * @param {string[]} accentColors
 * @param {string[]} semanticColors
 * @param {string[]} tintColors
 */
export function buildPageReading(page, cells, accentColors, semanticColors = [], tintColors = [], extra = {}) {
  const walls = findAccentWalls(cells, accentColors, semanticColors, tintColors)
  const verdict = judgePageHeroMetric(walls)
  const list = Array.isArray(cells) ? cells : []
  return {
    page,
    totalCells: list.length,
    // ★★ 第 36 增量新增：**这一页量到几个指标格**（★ 不是探针盒数，是过了 `isMetricCell` 的数）。
    //   ★ 为什么要有它：门报「0 处墙」时，**看不出分母是几** ⇒
    //   「这一页干净」与「这一页一个指标格都没量到」在读数上长得一模一样
    //   ⇒ ★★ 这正是本轮实测踩到的假绿形态（★ 修法把格子从分母里抹掉 ⇒ 读数 0 ⇒ 判绿）。
    //   ⇒ 带上分母，「0 处墙」才有解释力。
    metricCellCount: list.filter((c) => isMetricCell(c)).length,
    // ★★ 第 36 增量新增：**这一页可见文本有多长**（★ 「没量到」与「量到 0」要分得开）。
    //   ★ 判据收窄的教训见 §4.4：★ 探针的候选规则是「有底色或有四边描边」⇒
    //   ★ 一个**真的空状态页**（内容是「暂无迁移记录」，文本在，但没有任何卡片状盒子）
    //   ★★ 也会 `totalCells === 0` ⇒ ★★ 只看 `totalCells` 会把它判红 ⇒ **假红**。
    //   ⇒ 所以「没量到」必须看**文本**：★ 页面连可见文本都没有 ⇒ 那才是真的没渲染出来。
    bodyTextLen: Number(extra?.bodyTextLen ?? 0),
    elementCount: Number(extra?.elementCount ?? 0),
    // ★★ 第 36 增量：**这一页重探过几次**（★ 接线自证字段）。
    //   ★ 为什么要有它：★ 「0 格 ⇒ 重探一次」这段若**没接上**，读数上**看不出任何异常**
    //   （分母偏小、没有报错）⇒ ★★ 假绿 ⇒ ⇒ 所以读数必须**自带「我重探过」这个事实**：
    //   ★ 真实跑里「0 格」的页必然是 1 ⇒ ★ 字段恒为 0 ⇒ 说明这段没接上。
    reprobeCount: Number(extra?.reprobeCount ?? 0),
    walls, // ★ 数组（与 collectHardGateViolations 同形）
    wallCount: verdict.walls,
    accentCells: verdict.cells,
    isViolation: verdict.isViolation,
  }
}

/**
 * ★ 硬闸收集（退出码的唯一依据）。
 * @param {pages} [{page, isViolation, walls:[...]}]
 */
export function collectHardGateViolations(pages) {
  const list = Array.isArray(pages) ? pages : []
  const out = []
  for (const p of list) {
    if (!p || p.isViolation !== true) continue
    for (const w of p.walls ?? []) out.push({ page: p.page, ...w })
  }
  return out
}

/**
 * ★★ 门这一次到底算不算失败（第 36 增量：把失败数抽成纯函数 ⇒ CLI 与自证同源）。
 *
 * ★★ **本轮修掉的一个真洞**：★ 旧版 `failed` 只数
 *   「硬闸 + 棘轮 + unresolved + 基线缺页 + 基线过期」
 *   ⇒ ★ **报错的页（`report.errors`）根本没被算进去** ⇒ ⇒ ★★★ **那一页压根没量到，
 *   而门却判绿** ⇒ ★★ 「没量到」被读成了「通过」（★ 与「被重定向走」同族）。
 *   ⇒ ★ 实测：`/customer/site` 与 `/customer/activate` 两页探针报错 ⇒ 旧版照样绿。
 * ⇒ ⇒ 报错页现在**计入失败数**，并在输出里点名。
 *
 * @param {{pages?: any[], errors?: any[]}} report
 * @param {any[]} hard 硬闸命中
 * @param {any[]} ratchet 棘轮命中
 * @param {any[]} unresolvedPages 没量到的页
 * @param {{missing: string[], stale: boolean}} cover 基线覆盖
 * @returns {{total: number, kinds: Record<string, number>}}
 */
export function collectGateFailures(report, hard, ratchet, unresolvedPages, cover) {
  const kinds = {
    hard: (Array.isArray(hard) ? hard : []).length,
    ratchet: (Array.isArray(ratchet) ? ratchet : []).length,
    unresolved: (Array.isArray(unresolvedPages) ? unresolvedPages : []).length,
    baselineMissing: (cover?.missing ?? []).length,
    baselineStale: cover?.stale ? 1 : 0,
    // ★★ 第 36 增量新增：**报错的页 = 没量到 ⇒ 计入失败**。
    pageErrors: (Array.isArray(report?.errors) ? report.errors : []).length,
    // ★★ 第 36 增量新增：**页面连可见文本都没有 ⇒ 没渲染出来 ⇒ 计入失败**。
    //   ★ 判据：`totalCells === 0` **且** `bodyTextLen === 0`
    //   ⇒ ★★★ 为什么不能只看 `totalCells`：★ 探针的候选规则是「有底色 或 四边描边」
    //   ⇒ ★★ 一个**真的空状态页**（★ 本轮实测 `/admin/data-lifecycle/migration-state`
    //   ⇒ 内容是「存储迁移状态 / 空闲 / 刷新 / 暂无迁移记录」，★ **文本在，但没有任何
    //   卡片状盒子** ⇒ `totalCells === 0`）★★ 会被判红 ⇒ **假红** ⇒ ★ 收窄。
    //   ⇒ ★ 排除 `unresolved` 的页（那几类已经单列过，避免重复计数）。
    emptyPage: (Array.isArray(report?.pages) ? report.pages : []).filter(
      (p) => Number(p?.totalCells) === 0 && Number(p?.bodyTextLen ?? 0) === 0 && p?.unresolved !== true,
    ).length,
  }
  const total = Object.values(kinds).reduce((a, b) => a + b, 0)
  return { total, kinds }
}

/**
 * ★★ 这次导航失败了吗（第 36 增量）？
 *
 * ★ `Page.navigate` 在失败时**不抛异常**，而是带着 `errorText` 返回
 *   （★ 例：`net::ERR_CONNECTION_REFUSED` —— dev server 挂了）。
 * ⇒ 旧版不看返回值 ⇒ ★ **页面停在上一页** ⇒ 探针在**上一页的 DOM** 上跑
 *   ⇒ ⇒ 读数是「别页的」，而门照样判绿 ⇒ ★★ 本轮实测踩到过（dev server 崩了）。
 * @param {{errorText?: string}|null|undefined} nav
 * @returns {boolean}
 */
export function navigationFailed(nav) {
  return !!(nav && typeof nav === 'object' && nav.errorText)
}

/** 逐页读数 vs 基线：只许降不许升。 */
export function compareToBaseline(readings, baseline) {
  const base = baseline && typeof baseline === 'object' ? baseline.pages ?? {} : {}
  const list = Array.isArray(readings) ? readings : []
  const violations = []
  for (const r of list) {
    const prev = Number(base[r.page]?.wallCount)
    if (!Number.isFinite(prev)) continue
    const now = Number(r.wallCount)
    if (now > prev) {
      violations.push({ page: r.page, previous: prev, current: now, kind: 'ratchet-up' })
    }
  }
  return violations
}

/** 基线可用吗？（缺页 / 结构不对 ⇒ 不可用 ⇒ 测量之前就判红） */
export function baselineUsable(baseline) {
  if (!baseline || typeof baseline !== 'object') return false
  const pages = baseline.pages
  if (!pages || typeof pages !== 'object') return false
  const keys = Object.keys(pages)
  if (!keys.length) return false
  for (const k of keys) {
    const v = pages[k]
    if (!v || typeof v !== 'object') return false
    if (!Number.isFinite(Number(v.wallCount))) return false
  }
  return true
}

/**
 * ★★ 「基线没了 ⇒ 必红」这条**接线**本身也要能被自证覆盖（★ 本轮 §8.3(a) 的同源教训：
 * ★   自证只喂纯函数 ⇒ 断言恒真 ⇒ 测不到真正的接线）。
 * ★ 这里把 CLI 的判定抽成纯函数：产出方（自证）与消费方（CLI）走同一份。
 */
export function baselineGateDecision(baseline) {
  if (baselineUsable(baseline)) return { red: false, probe: null }
  return { red: true, probe: 'HERO-METRIC-BASELINE-UNUSABLE' }
}

/**
 * ★★ 写回时追加的那条 history 记录（★ 抽成纯函数 ⇒ 与自证同源）。
 *
 * ★★ **为什么要抽**：★ 本轮实测到 M6 臂跑完，基线里多出一条 `"pages": 1`
 *   —— 而 history 里另两条的 `pages` 说的是「**基线覆盖多少页**」（108）。
 *   ⇒ ★★ **同一个字段名在同一个数组里有两种含义** ⇒ 子集跑一次就把基线「写成只剩 1 页」的观感。
 *   ⇒ ★★ 修法：**`pages` 只表示「基线覆盖的页数」**（与另两条同义），
 *   ★ 「本次实测了多少页」另起字段 `measuredPages` ⇒ ★ 两个含义各归各的字段。
 */
export function buildBaselineHistoryEntry(baselinePageCount, measuredPages) {
  const total = Number(baselinePageCount) || 0
  const measured = Number(measuredPages) || 0
  return {
    at: '2026-10-28',
    change: '基线更新（只许降）',
    pages: total,
    measuredPages: measured,
    note:
      measured < total
        ? `写回值 = min(旧值, 实测值)；★ 本次只实测了 ${measured} 页 / 基线共 ${total} 页` +
          `⇒ 其余 ${total - measured} 页**未参与写回**（它们的值原样保留）。` +
          `★ 本条**不声称**原因（★ 「只实测了部分页」既可能是主动只跑了一部分，` +
          `也可能是**有几页探针报错** ⇒ ★ 原因去看 CLI 输出）。`
        : '写回值 = min(旧值, 实测值)',
  }
}

/** 读数页集合与基线页集合是否一致（页面清单变了 ⇒ 基线过期）。 */
export function pagesMatchBaseline(readings, baseline) {
  const base = baseline && typeof baseline === 'object' ? baseline.pages ?? {} : {}
  const a = Object.keys(base).sort()
  const b = [...new Set((Array.isArray(readings) ? readings : []).map((r) => r.page))].sort()
  if (a.length !== b.length) return false
  return a.every((v, i) => v === b[i])
}

/**
 * 本次量到的页面是否**被基线覆盖**（★ 区分「子集运行」与「基线真过期」）。
 *
 * ★ 与 `pagesMatchBaseline` 的区别：
 *   `pagesMatchBaseline` 要求**完全一致** ⇒ 任何 `--pages=` 子集运行都会被判红。
 *   但子集运行是**诊断手段**（只想看一页为什么红）⇒ 它不该被当成「基线过期」。
 *   ⇒ 本函数区分三种情形：
 *     ① measured ⊆ baseline（且不是全量）⇒ **子集运行**，棘轮覆盖不全但不算过期；
 *     ② measured = baseline 的页数但集合不同 ⇒ **基线真过期**（路由变了）⇒ 判红；
 *     ③ measured 里有 baseline 没有的页 ⇒ **基线缺页** ⇒ 判红。
 *
 * @param {Array<{page: string}>} readings
 * @param {any} baseline
 * @returns {{ missing: string[], isSubset: boolean, covered: boolean, stale: boolean }}
 */
export function pagesCoveredByBaseline(readings, baseline) {
  const base = baseline && typeof baseline === 'object' ? baseline.pages ?? {} : {}
  const baseKeys = Object.keys(base)
  const measured = [...new Set((Array.isArray(readings) ? readings : []).map((r) => r.page))]
  const missing = measured.filter((p) => !Object.prototype.hasOwnProperty.call(base, p))
  const isSubset = measured.length < baseKeys.length
  const sameCountDifferentSet = measured.length === baseKeys.length && !pagesMatchBaseline(readings, baseline)
  return {
    missing,
    isSubset,
    covered: missing.length === 0,
    stale: sameCountDifferentSet,
  }
}

export function readBaseline() {
  if (!existsSync(BASELINE)) return null
  try {
    return JSON.parse(readFileSync(BASELINE, 'utf8'))
  } catch {
    return null
  }
}

// ── 页内探针（在浏览器上下文里跑）────────────────────────────────────────
const PROBE = `(() => {
  const num = (v) => Math.round((parseFloat(v) || 0) * 10) / 10
  const isTransparent = (c) => !c || c === 'transparent' || /^rgba\\(0,\\s*0,\\s*0,\\s*0\\)$/.test(c)
  const inHidden = (el) => {
    let n = el
    while (n && n.nodeType === 1 && n !== document.documentElement) {
      const cs = getComputedStyle(n)
      if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return true
      n = n.parentElement
    }
    return false
  }
  const parentIds = new WeakMap()
  let nextId = 0
  const pid = (el) => {
    const p = el.parentElement
    if (!p) return 'ROOT'
    if (!parentIds.has(p)) parentIds.set(p, 'P' + (nextId++))
    return parentIds.get(p)
  }
  // ★★ 用**探针元素**解析 token：var() 链在 getPropertyValue 里拿不到色值。
  //   先落一个哨兵色，再声明 var(--x)；若计算色仍是哨兵 ⇒ 该变量不存在。
  // ★ 第 36 增量：**document.body 可能是 null**（★ 实测：/customer/site 与
  //   /customer/activate 这两页在导航后 3.2s 那一刻 body 还没建出来
  //   ⇒ 旧版直接往 body 上 appendChild 崩了 ⇒ **那一页根本没量到**
  //   ⇒ 而旧版**又没把报错页算进失败数** ⇒ ★★ 「没量到」被读成了「通过」。
  //   ⇒ 修法两条：① 挂载点退化到 documentElement；② 报错页计入退出码。
  const resolveToken = (name) => {
    const host = document.body || document.documentElement
    if (!host) return null
    const probe = document.createElement('span')
    probe.style.position = 'absolute'
    probe.style.left = '-9999px'
    probe.style.top = '0'
    probe.style.color = 'rgb(1, 2, 3)'
    probe.style.color = 'var(' + name + ')'
    host.appendChild(probe)
    const v = getComputedStyle(probe).color
    probe.remove()
    if (!v || v === 'rgb(1, 2, 3)') return null
    return v
  }
  const tokens = {}
  for (const n of ${JSON.stringify([...ACCENT_TOKEN_NAMES, ...SEMANTIC_TOKEN_NAMES, ...TINT_TOKEN_NAMES])}) {
    const v = resolveToken(n)
    if (v) tokens[n] = v
  }
  const CONTROL_SEL = 'button, .el-button, a[href], [role="button"], input, select, textarea'
  const candidates = [...document.querySelectorAll('body *')].filter((el) => {
    if (el.children.length > 40) return false
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.display === 'contents') return false
    const hasBg = cs.backgroundColor && !isTransparent(cs.backgroundColor)
    const side = (s) => num(cs['border' + s + 'Width']) >= 1 && !isTransparent(cs['border' + s + 'Color'])
    return hasBg || (side('Top') && side('Bottom') && side('Left') && side('Right'))
  })
  const cells = []
  for (const el of candidates) {
    const leaves = []
    let own = ''
    for (const n of el.childNodes) if (n.nodeType === 3) own += n.nodeValue || ''
    if (own.trim()) {
      const cs = getComputedStyle(el)
      leaves.push({ text: own.trim().replace(/\\s+/g, ' ').slice(0, 90), fs: num(cs.fontSize), color: cs.color })
    }
    // ★★ 第 36 增量：**不再只收「叶子元素」的 innerText**，而是收**所有后代自身的直接文本**。
    //   ★ 旧口径的盲区：值为「文本 + <small>单位</small>」的容器（mini-card__value）
    //     自己有子元素 ⇒ 不是叶子 ⇒ 它的**值整个看不见** ⇒ 只剩下标签与单位
    //     ⇒ 量具看到的是「标签是巨数字」，量到的根本不是那个值。
    for (const n of el.querySelectorAll('*')) {
      let ownText = ''
      for (const c of n.childNodes) if (c.nodeType === 3) ownText += c.nodeValue || ''
      const t = ownText.trim()
      if (!t) continue
      const cs = getComputedStyle(n)
      leaves.push({ text: t.replace(/\\s+/g, ' ').slice(0, 90), fs: num(cs.fontSize), color: cs.color })
    }
    if (!leaves.length) continue
    const r = el.getBoundingClientRect()
    if (r.width <= 0 || r.height <= 0) continue
    if (inHidden(el)) continue
    const cs = getComputedStyle(el)
    cells.push({
      w: num(r.width), h: num(r.height),
      radius: num(cs.borderTopLeftRadius),
      cls: (el.getAttribute('class') || el.tagName).slice(0, 100),
      parentKey: pid(el),
      leaves,
      hasInteractive: !!el.querySelector(CONTROL_SEL),
      // ★ 第 36 增量：格**自己**就是控件（按钮 / chip）⇒ 它不是指标卡。
      isControl: typeof el.matches === 'function' && el.matches(CONTROL_SEL),
      hidden: false,
      // ★ 第 36 增量：容器上 accent 的三个落点（底色 / 描边 / 阴影）。
      bg: cs.backgroundColor,
      border: [cs.borderTopColor, cs.borderRightColor, cs.borderBottomColor, cs.borderLeftColor],
      shadow: cs.boxShadow || '',
    })
  }
  return JSON.stringify({ cells, tokens, finalPath: location.pathname, probeVersion: ${JSON.stringify(PROBE_VERSION)}, bodyTextLen: document.body ? (document.body.innerText || '').trim().length : 0, elementCount: document.body ? document.body.querySelectorAll('*').length : 0 })
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'hero-metric-runtime-'))
  // ★★ 第 36 增量：**独立进程组**（★ detached）⇒ 结束时能整组回收。
  //   ★ 背景：★ 旧版只 `chrome.kill()` 杀父进程 ⇒ Chrome 的 zygote / renderer 子进程
  //   **成孤儿继续跑** ⇒ ★★ 实测跑完 99 次门之后，机器上积了 **99 个 profile / 63 个孤儿进程**
  //   ⇒ ★★★ **负载被自己抬到 13~38** ⇒ ★★ 页面在固定的 3.2s 等待窗口里加载不完
  //   ⇒ ★★ 探针量到 **0 个候选格** ⇒ 「0 处墙」⇒ ★★★ **门把自己的噪声当成了「干净」**
  //   ⇒ ★★ 这是**尺自己把自己变成噪声源** ⇒ 必须自己负责回收。
  const chrome = spawn(CHROME_CANDIDATES[0], [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--hide-scrollbars',
    '--window-size=1440,1400',
    'about:blank',
  ], { stdio: 'ignore', detached: true })

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/json/version`)).ok) break
    } catch {
      /* 等 */
    }
    await sleep(250)
  }
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
  const target = list.find((t) => t.type === 'page')
  if (!target) throw new Error('找不到 page target')

  const ws = new WebSocket(target.webSocketDebuggerUrl)
  await new Promise((res, rej) => {
    ws.addEventListener('open', res, { once: true })
    ws.addEventListener('error', rej, { once: true })
  })
  let seq = 0
  const pending = new Map()
  ws.addEventListener('message', (ev) => {
    const msg = JSON.parse(ev.data)
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id)
      pending.delete(msg.id)
      msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result)
    }
  })
  const cdp = (method, params = {}) => {
    const id = ++seq
    ws.send(JSON.stringify({ id, method, params }))
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject })
      setTimeout(() => {
        if (pending.has(id)) {
          pending.delete(id)
          reject(new Error(`CDP 超时: ${method}`))
        }
      }, 60000)
    })
  }
  const evaluate = async (expression) => {
    const r = await cdp('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
      userGesture: true,
    })
    if (r.exceptionDetails) {
      throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text)
    }
    return r.result.value
  }
  await cdp('Page.enable')
  await cdp('Runtime.enable')

  const report = { pages: [], errors: [], tokens: null }
  for (const p of pages) {
    try {
      const nav = await cdp('Page.navigate', { url: BASE + p })
      // ★★ 第 36 增量：**导航失败必须显式判红**。
      // ★ 旧版不看 `Page.navigate` 的返回值 ⇒ ★ dev server 挂掉时 navigate 会
      //   带着 `errorText` 返回（不抛异常）⇒ ★ **页面停在上一页**
      //   ⇒ ⇒ 探针在**上一页的 DOM** 上跑 ⇒ ★★ 读数是「别页的」，而门照样判绿
      //   ⇒ ⇒ ★★ 本轮实测：dev server 崩了 ⇒ 臂的还原态读数 rc=0（假绿）。
      if (navigationFailed(nav)) throw new Error(`导航失败：${nav.errorText}`)
      // ★★ 第 36 增量：**死等 3.2s 不够**（★ 实测机器过载时页面在这个窗口里加载不完
      //   ⇒ 探针量到 0 个候选格 ⇒ 门判绿 ⇒ 假绿）。
      //   ⇒ 修法：★ 保留 3.2s 作为**下限**，★ 再加一道**稳定判据**：
      //   ★★ 「连续两次采样（格数 + 可见文本长度）相同」⇒ 认为页面已稳定 ⇒ 最多再等 8s。
      //   ⇒ ★★ 数据晚到时，签名会变（文本长度变化）⇒ 继续等 ⇒ **不会量到半成品页面**。
      await sleep(3200)
      const probeFn = async () => JSON.parse(await evaluate(PROBE))
      let probe = null
      let prevSig = null
      let stable = 0
      const settleDeadline = Date.now() + 8000
      while (true) {
        probe = await probeFn()
        const sig = `${(probe.cells ?? []).length}|${probe.bodyTextLen ?? 0}`
        if (sig === prevSig) {
          stable++
          if (stable >= 1) break
        } else {
          stable = 0
          prevSig = sig
        }
        if (Date.now() >= settleDeadline) break
        await sleep(400)
      }
      // ★★★ 第 36 增量：**一个指标格都没量到 ⇒ 可能是「数据晚到」⇒ 重探一次。**
      //   ★ 起因（本轮实测）：★ 全量跑读到 `/admin/data-lifecycle` 的
      //   `metricCellCount = 0`，而**手动复现**（同一份代码、同一后端）读到 **4** ⇒
      //   ★★ 差别是**那一次的数据还没到**（★ `data.columnar` 没来 ⇒ 区块没渲染）
      //   ⇒ ★★ 而稳定判据只管「有没有变化」（★ 页面有别的卡 ⇒ 签名稳定 ⇒ 提前放行）
      //   ⇒ ★★★ **⇒ 「0 个指标格」不等于「这一页没有指标格」**
      //   ⇒ ⇒ **⇒ 修法：★ 若本页指标格数为 0 ⇒ 等 1.5s 再探一次；★ 若第二次仍是 0 ⇒ 接受**
      //   ⇒ ★★ 代价：只有「0 格」的页多花 1.5s（★ 本轮 137 页里 112 页是 0 格 ⇒ +2.8 min）。
      //   ★★★ **判据与接线都在 `probeWithReprobe` 里**（★ 可注入 ⇒ 自证能覆盖「接线」这一层；
      //   ★★ 否则「判据写错」与「压根没接上」在读数上**长得一样** ⇒ 假绿）。
      const rr = await probeWithReprobe(probeFn, sleep, probe)
      probe = rr.probe
      const reprobeCount = rr.reprobeCount
      // ★ 探针版本不符 ⇒ **量具不是这一把** ⇒ 那一页「没量到」，不是「真没有」。
      if (probe.probeVersion !== PROBE_VERSION) {
        report.pages.push({
          ...buildPageReading(p, [], [], [], [], { reprobeCount }),
          unresolved: true,
          unresolvedTokens: [`probeVersion:${probe.probeVersion ?? '缺失'}`],
        })
        continue
      }
      const tokens = probe.tokens ?? {}
      if (!report.tokens) report.tokens = tokens
      const finalPath = probe.finalPath ?? ''
      // ★★ 第 36 增量：这一页被**重定向到非业务页**（激活向导 / 登录页）⇒
      //   **没量到**，不是「真没有」⇒ 记成 unresolved ⇒ 门判红。
      if (redirectedAway(p, finalPath)) {
        report.pages.push({
          ...buildPageReading(p, [], [], [], [], { reprobeCount }),
          unresolved: true,
          unresolvedTokens: [`HERO-METRIC-REDIRECTED:${finalPath || '未知'}`],
          finalPath,
        })
        continue
      }
      const { accents, unresolved } = buildAccentSet(tokens)
      const semanticColors = SEMANTIC_TOKEN_NAMES.map((n) => tokens[n]).filter(Boolean)
      // ★ 第 36 增量：主色**淡染**集合（accent-soft 族）。
      const { tints } = buildTintSet(tokens)
      // ★ 强调色集合解析不出来 ⇒ 那一页「没量到」，不是「真没有」。
      if (!accents.length) {
        report.pages.push({
          ...buildPageReading(p, [], [], [], [], { reprobeCount }),
          unresolved: true,
          unresolvedTokens: unresolved,
          finalPath,
        })
        continue
      }
      report.pages.push({
        ...buildPageReading(p, probe.cells ?? [], accents, semanticColors, tints, {
          bodyTextLen: probe.bodyTextLen,
          elementCount: probe.elementCount,
          reprobeCount,
        }),
        unresolved: false,
        finalPath,
      })
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
    }
  }
  ws.close()
  // ★★ 第 36 增量：**整组回收**（★ 否则子进程成孤儿 ⇒ 跑几十次就把机器压垮）。
  try {
    process.kill(-chrome.pid, 'SIGKILL')
  } catch {
    try { chrome.kill('SIGKILL') } catch { /* 已经退出 */ }
  }
  // ★ 回收 profile 目录（★ 每次跑留一个 ⇒ 实测积到 99 个）。
  try { rmSync(profile, { recursive: true, force: true }) } catch { /* 已被回收 */ }
  return report
}

// ── 判据自证 ────────────────────────────────────────────────────────────────
//
// ★ 必须**双向**：会判红 + 不误判各半 —— 否则恒绿门与恒红门同样没用。

export async function runRuntimeSelftest() {
  const cases = []
  const eq = (name, actual, expected) => {
    const ok = JSON.stringify(actual) === JSON.stringify(expected)
    cases.push({ name, ok, msg: `期望 ${JSON.stringify(expected)}，实测 ${JSON.stringify(actual)}` })
  }

  const ACCENT = 'rgb(30, 79, 214)'
  const ACCENT2 = 'rgb(91, 140, 255)'
  const DANGER = 'rgb(220, 38, 38)'
  const WARNING = 'rgb(217, 119, 6)'
  const TEXT = 'rgb(15, 23, 42)'
  const MUTED = 'rgb(100, 116, 139)'
  const ACCENTS = [ACCENT, ACCENT2]
  const SEMANTICS = [DANGER, WARNING]

  // 造一格
  const cell = (o = {}) => ({
    w: 120, h: 80, radius: 8, cls: 'stat-card', parentKey: 'P1',
    leaves: [
      { text: '总请求', fs: 12, color: MUTED },
      { text: '1,234', fs: 24, color: TEXT },
    ],
    hasInteractive: false, hidden: false,
    // ★ 第 36 增量：容器级 accent 的三个落点（默认中性 ⇒ 不影响旧断言）。
    isControl: false,
    bg: 'rgb(255, 255, 255)',
    border: ['rgb(220, 227, 238)', 'rgb(220, 227, 238)', 'rgb(220, 227, 238)', 'rgb(220, 227, 238)'],
    shadow: 'none',
    ...o,
  })
  const accentCell = (o = {}) => cell({
    leaves: [
      { text: '总 Offer', fs: 12, color: MUTED },
      { text: '15', fs: 24, color: ACCENT },
    ],
    ...o,
  })

  // ── 判据：颜色解析 ──
  eq('S-HM1 解析 rgb', parseRgb('rgb(30, 79, 214)'), [30, 79, 214])
  eq('S-HM2 解析失败返回 null', parseRgb('nope'), null)
  eq('S-HM3 同色（含容差）', sameColor('rgb(30,79,214)', 'rgb(31,80,215)'), true)
  eq('S-HM4 异色', sameColor('rgb(30,79,214)', 'rgb(30,79,240)'), false)

  // ── 判据：强调色集合构造 ──
  eq('S-HM5 强调色集合只收主色',
    buildAccentSet({ '--accent': ACCENT, '--kx-primary': ACCENT, '--success': 'rgb(22,163,74)' }).accents,
    [ACCENT])
  eq('S-HM6 解析不出来的 token 记进 unresolved',
    buildAccentSet({ '--accent': 'var(--kx-primary)' }).unresolved, ['--accent', '--accent-h', '--kx-primary', '--kx-primary-hover', '--el-color-primary'])
  eq('S-HM7 主色命中装饰强调', isDecorativeAccent(ACCENT, ACCENTS, SEMANTICS), true)
  eq('S-HM8 语义色不算装饰强调', isDecorativeAccent(DANGER, ACCENTS, SEMANTICS), false)
  eq('S-HM9 普通文字不算装饰强调', isDecorativeAccent(TEXT, ACCENTS, SEMANTICS), false)
  eq('S-HM10 强调色集合为空 ⇒ 一律不算',
    isDecorativeAccent(ACCENT, [], SEMANTICS), false)

  // ── 判据：指标格 ──
  eq('S-HM11 巨数字+小标签 ⇒ 是指标格', isMetricCell(cell()), true)
  eq('S-HM12 隐藏子树 ⇒ 不是', isMetricCell(cell({ hidden: true })), false)
  eq('S-HM13 有 CTA ⇒ 不是（商品卡）', isMetricCell(cell({ hasInteractive: true })), false)
  eq('S-HM14 药丸 ⇒ 不是', isMetricCell(cell({ radius: 60, h: 100 })), false)
  eq('S-HM15 超宽区块 ⇒ 不是', isMetricCell(cell({ w: 700 })), false)
  eq('S-HM16 数字不够大（20px）⇒ 不是',
    isMetricCell(cell({ leaves: [{ text: '总请求', fs: 12, color: MUTED }, { text: '12', fs: 20, color: TEXT }] })), false)
  eq('S-HM17 巨数字不是数 ⇒ 不是',
    isMetricCell(cell({ leaves: [{ text: '总请求', fs: 12, color: MUTED }, { text: '很多', fs: 24, color: TEXT }] })), false)
  eq('S-HM18 没有小标签 ⇒ 不是',
    isMetricCell(cell({ leaves: [{ text: '1,234', fs: 24, color: TEXT }] })), false)
  eq('S-HM19 叶子不足两个 ⇒ 不是', isMetricCell(cell({ leaves: [] })), false)
  eq('S-HM19b 货币计价（¥）⇒ 不是（商品标价不是指标）',
    isMetricCell(cell({ leaves: [{ text: '基础版', fs: 14, color: MUTED }, { text: '¥29.00', fs: 24, color: ACCENT }] })), false)
  eq('S-HM19c 货币计价（$）⇒ 不是',
    isMetricCell(cell({ leaves: [{ text: '总费用', fs: 12, color: MUTED }, { text: '$0.00', fs: 24, color: ACCENT }] })), false)
  eq('S-HM19d 货币符号识别', isCurrencyValue('¥29.00'), true)
  eq('S-HM19e 非货币数字', isCurrencyValue('100%'), false)
  eq('S-HM19f 14px 的标签也是「小标签」（--kx-text-md 档）',
    isMetricCell(cell({ leaves: [{ text: 'PII 命中', fs: 14, color: MUTED }, { text: '0', fs: 24, color: ACCENT }] })), true)
  eq('S-HM19g 标签不小于巨数字 ⇒ 不是（小标签必须更小）',
    isMetricCell(cell({ leaves: [{ text: 'PII 命中', fs: 24, color: MUTED }, { text: '0', fs: 24, color: ACCENT }] })), false)

  // ── 判据：并排 ──
  const three = [
    accentCell({ parentKey: 'P1', cls: 'stat-card a' }),
    accentCell({ parentKey: 'P1', cls: 'stat-card b' }),
    accentCell({ parentKey: 'P1', cls: 'stat-card c' }),
  ]
  eq('S-HM20 三格主色并排 ⇒ 1 面墙',
    findAccentWalls(three, ACCENTS, SEMANTICS).length, 1)
  eq('S-HM21 两格并排 ⇒ 不算墙（不是「并排」）',
    findAccentWalls(three.slice(0, 2), ACCENTS, SEMANTICS).length, 0)
  eq('S-HM22 三格但只有 1 格主色 ⇒ 不算墙',
    findAccentWalls([
      accentCell({ parentKey: 'P1' }),
      cell({ parentKey: 'P1' }),
      cell({ parentKey: 'P1' }),
    ], ACCENTS, SEMANTICS).length, 0)
  eq('S-HM23 三格语义色 ⇒ 不算墙（抬头摘要条是合法存量）',
    findAccentWalls([
      cell({ parentKey: 'P1', leaves: [{ text: '命中', fs: 12, color: MUTED }, { text: '0', fs: 24, color: DANGER }] }),
      cell({ parentKey: 'P1', leaves: [{ text: '命中', fs: 12, color: MUTED }, { text: '0', fs: 24, color: DANGER }] }),
      cell({ parentKey: 'P1', leaves: [{ text: '命中', fs: 12, color: MUTED }, { text: '0', fs: 24, color: WARNING }] }),
    ], ACCENTS, SEMANTICS).length, 0)
  eq('S-HM24 不同父容器 ⇒ 不算并排',
    findAccentWalls([
      accentCell({ parentKey: 'P1' }),
      accentCell({ parentKey: 'P2' }),
      accentCell({ parentKey: 'P3' }),
    ], ACCENTS, SEMANTICS).length, 0)
  eq('S-HM25 强调色集合为空 ⇒ 一面墙都找不到',
    findAccentWalls(three, [], SEMANTICS).length, 0)
  eq('S-HM26 三格两格主色 ⇒ 算墙',
    findAccentWalls([
      accentCell({ parentKey: 'P1' }),
      accentCell({ parentKey: 'P1' }),
      cell({ parentKey: 'P1' }),
    ], ACCENTS, SEMANTICS).length, 1)

  // ── 页面级 ──
  eq('S-HM27 有墙 ⇒ 判红', judgePageHeroMetric([{ accentCells: 2 }]).isViolation, true)
  eq('S-HM28 无墙 ⇒ 判绿', judgePageHeroMetric([]).isViolation, false)
  eq('S-HM29 硬闸只收 isViolation=true 的页',
    collectHardGateViolations([
      { page: '/a', isViolation: true, walls: [{ parentKey: 'P1' }] },
      { page: '/b', isViolation: false, walls: [{ parentKey: 'P1' }] },
    ]).length, 1)

  // ── 棘轮 ──
  const base = { pages: { '/a': { wallCount: 1 }, '/b': { wallCount: 0 } } }
  eq('S-HM30 读数升高 ⇒ 棘轮红',
    compareToBaseline([{ page: '/a', wallCount: 2 }], base).length, 1)
  eq('S-HM31 读数下降 ⇒ 棘轮绿',
    compareToBaseline([{ page: '/a', wallCount: 0 }], base).length, 0)
  eq('S-HM32 读数持平 ⇒ 棘轮绿',
    compareToBaseline([{ page: '/a', wallCount: 1 }], base).length, 0)
  eq('S-HM33 基线缺该页 ⇒ 不判（新增页不算涨）',
    compareToBaseline([{ page: '/c', wallCount: 5 }], base).length, 0)
  eq('S-HM34 基线可用', baselineUsable(base), true)
  eq('S-HM35 基线空 ⇒ 不可用', baselineUsable({ pages: {} }), false)
  eq('S-HM36 基线缺页 ⇒ 不可用', baselineUsable(null), false)
  eq('S-HM37 页集合不一致 ⇒ 不可用',
    pagesMatchBaseline([{ page: '/a' }], base), false)
  eq('S-HM38 页集合一致 ⇒ 可用',
    pagesMatchBaseline([{ page: '/a' }, { page: '/b' }], base), true)

  // ── 基线覆盖：子集运行 vs 基线真过期 ──
  eq('S-HM39 子集运行 ⇒ covered 但 isSubset',
    { covered: pagesCoveredByBaseline([{ page: '/a' }], base).covered, isSubset: pagesCoveredByBaseline([{ page: '/a' }], base).isSubset },
    { covered: true, isSubset: true })
  eq('S-HM40 量到的页基线里没有 ⇒ 不 covered',
    pagesCoveredByBaseline([{ page: '/c' }], base).covered, false)
  eq('S-HM41 页数相同但集合不同 ⇒ stale',
    pagesCoveredByBaseline([{ page: '/a' }, { page: '/z' }], base).stale, true)
  eq('S-HM42 全量且集合一致 ⇒ 不 stale 不 subset',
    { stale: pagesCoveredByBaseline([{ page: '/a' }, { page: '/b' }], base).stale, isSubset: pagesCoveredByBaseline([{ page: '/a' }, { page: '/b' }], base).isSubset },
    { stale: false, isSubset: false })

  // ── 形状契约（★ 本轮真 bug 的专测）─────────────────────────────────────
  // ★ 第一版：产出方给 `walls: <数字>`，消费方要 `walls: <数组>`
  //   ⇒ 量到违规时 `for (const w of p.walls ?? [])` 直接 TypeError
  //   ⇒ 而自证喂的是数组 ⇒ **断言恒真**，测不到真正的接线。
  // ⇒ 这一条**从产出方出发**造读数，再喂给消费方 ⇒ 形状不一致就当场红。
  {
    const cells = [
      accentCell({ parentKey: 'P1' }),
      accentCell({ parentKey: 'P1' }),
      accentCell({ parentKey: 'P1' }),
    ]
    const reading = buildPageReading('/x', cells, ACCENTS, SEMANTICS)
    let crashed = false
    let found = -1
    try {
      found = collectHardGateViolations([reading]).length
    } catch {
      crashed = true
    }
    eq('S-HM43 ★ 产出方的读数能被消费方直接吃下（形状契约）', crashed ? 'crash' : found, 1)
    eq('S-HM44 读数里的 walls 是数组、wallCount 是数字',
      [Array.isArray(reading.walls), typeof reading.wallCount], [true, 'number'])
  }

  // ── M2 臂那个形态（★ 14px 标签 + 主色巨数字）必须能被检出 ──────────────
  {
    const cells = [
      accentCell({ parentKey: 'P1', leaves: [{ text: 'PII 命中', fs: 14, color: MUTED }, { text: '0', fs: 24, color: ACCENT }] }),
      accentCell({ parentKey: 'P1', leaves: [{ text: '密钥命中', fs: 14, color: MUTED }, { text: '0', fs: 24, color: ACCENT }] }),
      accentCell({ parentKey: 'P1', leaves: [{ text: '毒性命中', fs: 14, color: MUTED }, { text: '0', fs: 24, color: ACCENT }] }),
    ]
    const r = buildPageReading('/m2', cells, ACCENTS, SEMANTICS)
    eq('S-HM45 ★ 14px 标签 + 主色巨数字的并排 ⇒ 判红（M2 臂那个形态）',
      [r.isViolation, collectHardGateViolations([r]).length], [true, 1])
  }

  // ── 「基线没了 ⇒ 必红」这条**接线**也要被自证盖住 ──────────────────────
  // ★ 与 §8.3(a) 同源：★ 只测 `baselineUsable` 这个谓词 ⇒ 测不到「CLI 到底判没判红」。
  // ★ 这里测的是**判定结果 + 探针 token**（★ 探针是 go 掏空门认的红信号 ⇒ token 不能变）。
  eq('S-HM51 ★ 基线可用 ⇒ 不判红',
    baselineGateDecision(base).red, false)
  eq('S-HM52 ★ 基线空 ⇒ 判红 + 探针',
    baselineGateDecision({ pages: {} }),
    { red: true, probe: 'HERO-METRIC-BASELINE-UNUSABLE' })
  eq('S-HM53 ★ 基线文件缺失（null）⇒ 判红 + 同一个探针',
    baselineGateDecision(null).probe, 'HERO-METRIC-BASELINE-UNUSABLE')
  eq('S-HM54 ★ 判红的出口码必须是 1（★ 「量具自己坏了」不许伪装成「没有违规」）',
    (baselineGateDecision(null).red ? 1 : 0), 1)

  // ── 写回基线时追加的 history 记录：★ `pages` 只能有一种含义 ─────────────
  // ★ M6 臂实测出来的坑：★ 旧写法把「本次实测了几页」写进 `pages`
  //   ⇒ 而另两条的 `pages` 说的是「基线覆盖多少页」⇒ 同名不同形。
  {
    const full = buildBaselineHistoryEntry(108, 108)
    eq('S-HM55 ★ 全量写回：pages = 基线总页数',
      full.pages, 108)
    eq('S-HM56 ★ 全量写回：measuredPages = 实测页数',
      full.measuredPages, 108)
    const sub = buildBaselineHistoryEntry(108, 1)
    eq('S-HM57 ★ 子集写回：pages 仍是基线总页数（★ 绝不写成实测页数）',
      sub.pages, 108)
    eq('S-HM58 ★ 子集写回：measuredPages 单独记实测页数',
      sub.measuredPages, 1)
    eq('S-HM59 ★ 子集写回的 note 必须点明「其余页未参与写回」',
      /未参与写回/.test(sub.note), true)
  }

  // ── 第 36 增量：色值解析补 `color(srgb …)` ──────────────────────────────
  // ★ Chrome 对 color-mix / 相对色给出的计算值就是这个形态 ⇒ 旧版解析不出来
  //   ⇒ 那类颜色对量具是**隐形的**（★「0 命中」的第三种读法：量具自己坏了）。
  eq('S-HM60 ★ 解析 color(srgb …)',
    parseRgb('color(srgb 0.955882 0.965490 0.991961)').map((v) => Math.round(v)),
    [244, 246, 253])
  eq('S-HM61 ★ color(srgb) 与同值 rgb() 判为同色',
    sameColor('color(srgb 0.917647 0.941176 1.000000)', 'rgb(234, 240, 255)'), true)

  // ── 第 36 增量：主色淡染集合 ─────────────────────────────────────────────
  const TINT = 'rgb(234, 240, 255)'
  eq('S-HM62 ★ 淡染集合按色值去重（三个 token 同值 ⇒ 留一份）',
    buildTintSet({ '--accent-soft': TINT, '--primary-soft': TINT, '--kx-primary-soft': TINT }).tints,
    [TINT])
  eq('S-HM63 ★ 淡染 token 解析不出来 ⇒ 记进 unresolved',
    buildTintSet({ '--accent-soft': 'var(--x)' }).unresolved,
    ['--accent-soft', '--primary-soft', '--kx-primary-soft'])

  // ── 第 36 增量：accent 的四个落点 ───────────────────────────────────────
  eq('S-HM64 ★ 落点：数字被主色染',
    accentLandings(accentCell(), ACCENTS, SEMANTICS, []), ['value'])
  eq('S-HM65 ★ 落点：卡底是主色淡染',
    accentLandings(cell({ bg: TINT }), ACCENTS, SEMANTICS, [TINT]), ['bg'])
  eq('S-HM66 ★ 落点：卡底描边命中主色',
    accentLandings(cell({ border: [ACCENT, 'rgb(220,227,238)', 'rgb(220,227,238)', 'rgb(220,227,238)'] }),
      ACCENTS, SEMANTICS, []), ['border'])
  eq('S-HM67 ★ 落点：阴影含主色',
    accentLandings(cell({ shadow: '0 0 12px rgba(30, 79, 214, 0.35)' }), ACCENTS, SEMANTICS, []), ['shadow'])
  eq('S-HM68 ★ 不是指标格 ⇒ 没有任何落点',
    accentLandings(cell({ leaves: [{ text: '1,234', fs: 24, color: ACCENT }] }), ACCENTS, SEMANTICS, [TINT]), [])
  eq('S-HM69 ★ 语义色底 ⇒ 不算 accent（状态优先）',
    accentLandings(cell({ bg: DANGER }), ACCENTS, SEMANTICS, [TINT]), [])
  eq('S-HM70 ★ 格子本身就是控件 ⇒ 不是指标格',
    isMetricCell(cell({ isControl: true })), false)
  eq('S-HM71 ★ 三格全是淡染底 ⇒ 1 面墙（容器 accent 能被抓到）',
    findAccentWalls([
      cell({ parentKey: 'P1', bg: TINT }),
      cell({ parentKey: 'P1', bg: TINT }),
      cell({ parentKey: 'P1', bg: TINT }),
    ], ACCENTS, SEMANTICS, [TINT]).length, 1)
  eq('S-HM72 ★ 三格里只有 1 格淡染 ⇒ 不算墙（单点强调合法）',
    findAccentWalls([
      cell({ parentKey: 'P1', bg: TINT }),
      cell({ parentKey: 'P1' }),
      cell({ parentKey: 'P1' }),
    ], ACCENTS, SEMANTICS, [TINT]).length, 0)
  eq('S-HM73 ★ 没有淡染集合 ⇒ 淡染底不算落点（量具口径要显式）',
    accentLandings(cell({ bg: TINT }), ACCENTS, SEMANTICS, []), [])
  eq('S-HM74 ★ 巨数字叶子取字号最大者',
    bigLeaf(cell({ leaves: [{ text: '标签', fs: 12, color: MUTED }, { text: '42', fs: 24, color: TEXT }] })).text, '42')
  eq('S-HM75 ★ 小标签叶子取字号最小且 ≤ 上限者',
    smallLeaf(cell({ leaves: [{ text: '标签', fs: 12, color: MUTED }, { text: '口径说明', fs: 11, color: MUTED }] })).text, '口径说明')

  // ── 第 36 增量：读数里要带出「accent 落在哪儿」（门输出也是门的契约）──────
  {
    const cells = [
      cell({ parentKey: 'P1', bg: TINT }),
      cell({ parentKey: 'P1', bg: TINT }),
      cell({ parentKey: 'P1', bg: TINT }),
    ]
    const r = buildPageReading('/bg', cells, ACCENTS, SEMANTICS, [TINT])
    eq('S-HM76 ★ 读数里的 walls 带 landings（接线：不是只测谓词）',
      r.walls[0]?.landings, ['bg'])
    eq('S-HM77 ★ 淡染底的那面墙确实判红',
      [r.isViolation, collectHardGateViolations([r]).length], [true, 1])
  }

  // ── 第 36 增量：tab 变体页面派生 ────────────────────────────────────────
  eq('S-HM78 ★ 抽 tab id：数组声明',
    extractTabIds("const VALID_TABS: X[] = ['board', 'stream']", 'const VALID_TABS:'),
    ['board', 'stream'])
  eq('S-HM79 ★ 抽 tab id：联合类型声明',
    extractTabIds("type PageTab = 'canonical' | 'catalog'", 'type PageTab ='),
    ['canonical', 'catalog'])
  eq('S-HM80 ★ 抽 tab id：ref 泛型里的联合类型（并去重）',
    extractTabIds("const activeTab = ref<'a' | 'b'>('a')", 'const activeTab = ref<'),
    ['a', 'b'])
  eq('S-HM81 ★ 抽不到 ⇒ 返回 null（★ 不许返回空数组冒充「这个视图没有 tab」）',
    extractTabIds('const x = 1', 'const VALID_TABS:'), null)
  eq('S-HM82 ★ 真仓里 6 个 tab 视图的声明全部抽得到（★ 接线自证）',
    TAB_PAGE_SOURCES.map((e) => extractTabIds(readFileSync(join(ROOT, e.source), 'utf8'), e.marker) !== null)
      .filter((x) => !x).length, 0)
  eq('S-HM83 ★ 真仓里 tab 派生没有失败项（★ 失败 ⇒ 门必须判红）',
    TAB_PAGE_PROBLEMS.length, 0)
  eq('S-HM84 ★ 全量页面 = 静态路由 + tab 变体（且 tab 变体非空）',
    [ALL_PAGES.length > DEFAULT_PAGES.length, ALL_PAGES.length - DEFAULT_PAGES.length],
    [true, 29])

  // ── 第 36 增量：探针版本（★ 量具换了口径要能被发现）─────────────────────
  eq('S-HM85 ★ 探针源码里带着版本号（改探针就要改版本号）',
    PROBE.includes(JSON.stringify(PROBE_VERSION)), true)

  // ── 第 36 增量：「被重定向到非业务页」= 没量到，不是通过 ────────────────
  // ★ 这条判据是本轮**自己踩到**才写出来的：后端未激活 ⇒ 整站被 bootstrap 门
  //   重定向 ⇒ 那一页没有指标格 ⇒ 门读到「0 处墙」⇒ **判绿**（假绿）。
  eq('S-HM86 ★ 落到激活向导 ⇒ 判成「被重定向走」',
    redirectedAway('/admin/data-lifecycle', '/bootstrap'), true)
  eq('S-HM87 ★ 落到登录页 ⇒ 判成「被重定向走」',
    redirectedAway('/admin/data-lifecycle', '/login'), true)
  eq('S-HM88 ★ 留在原页 ⇒ 不算被重定向',
    redirectedAway('/admin/data-lifecycle', '/admin/data-lifecycle'), false)
  eq('S-HM89 ★ 路由表里的正常跳转（/catalog → /models?tab=catalog）⇒ 不算被重定向',
    redirectedAway('/catalog', '/models'), false)
  eq('S-HM90 ★ 带 query 的请求页比对时只取 path',
    redirectedAway('/dashboard?tab=board', '/dashboard'), false)
  eq('S-HM91 ★ 请求页本身就是 /bootstrap ⇒ 不算被重定向',
    redirectedAway('/bootstrap', '/bootstrap'), false)
  eq('S-HM92 ★ finalPath 读不到 ⇒ 判成「没量到」（★ 不许默认当成通过）',
    redirectedAway('/admin/data-lifecycle', ''), true)

  // ── 第 36 增量：报错的页 = 没量到 ⇒ 计入失败数 ──────────────────────────
  {
    const clean = collectGateFailures({ pages: [], errors: [] }, [], [], [], { missing: [], stale: false })
    eq('S-HM93 ★ 干净的一次运行 ⇒ 失败数 0',
      [clean.total, clean.kinds.pageErrors], [0, 0])
    const withErr = collectGateFailures(
      { pages: [], errors: [{ page: '/customer/site', error: 'TypeError' }] },
      [], [], [], { missing: [], stale: false },
    )
    eq('S-HM94 ★ ★ 有一页探针报错 ⇒ 失败数 ≥ 1（「没量到」不许读成「通过」）',
      withErr.total, 1)
    eq('S-HM95 ★ 失败数按类别分账（★ 报错页单独一类）',
      withErr.kinds.pageErrors, 1)
  }
  // ── 第 36 增量：history 的 note 不许断言它不知道的原因 ──────────────────
  {
    const sub = buildBaselineHistoryEntry(137, 135)
    eq('S-HM96 ★ history note 不得断言「子集运行」（★ 子集与报错会长得一样）',
      /子集运行/.test(sub.note), false)
    eq('S-HM97 ★ history note 要说清「哪几页没参与写回」',
      /未参与写回/.test(sub.note), true)
  }
  // ── 第 36 增量：导航失败 ⇒ 那一页没量到 ⇒ 必须判红 ──────────────────────
  {
    eq('S-HM98 ★ 导航带 errorText ⇒ 判成失败（★ dev server 挂了不许读到上一页的 DOM）',
      navigationFailed({ errorText: 'net::ERR_CONNECTION_REFUSED' }), true)
    eq('S-HM99 ★ 正常导航 ⇒ 不算失败',
      navigationFailed({ frameId: 'F1', loaderId: 'L1' }), false)
    eq('S-HM100 ★ 导航返回空 ⇒ 不算失败（★ 没有证据就不判失败）',
      navigationFailed(null), false)
  }

  // ── 第 36 增量：读数要带**分母**（★ 「0 处墙」必须有解释力）────────────────
  {
    const reading = buildPageReading('/x', [cell(), cell({ w: 900 })], ACCENTS, SEMANTICS, [])
    eq('S-HM101 ★ 读数带 metricCellCount（★ 过了 isMetricCell 的格数）',
      reading.metricCellCount, 1)
    eq('S-HM102 ★ metricCellCount ≠ totalCells（★ 探针盒数 ≠ 指标格数）',
      [reading.totalCells, reading.metricCellCount], [2, 1])
    eq('S-HM103 ★ 空数组 ⇒ metricCellCount 为 0（★ 断言不许恒真）',
      buildPageReading('/x', [], ACCENTS, SEMANTICS, []).metricCellCount, 0)
  }
  // ── 第 36 增量：没渲染出来 ⇒ 没量到 ⇒ 计入失败（★ 判据要分清「没量到」与「量到 0」）──
  {
    const base = { pages: [{ page: '/a', totalCells: 0, bodyTextLen: 0, unresolved: false }] }
    eq('S-HM104 ★ totalCells=0 且**连文本都没有** ⇒ 计入 emptyPage（★ 没渲染出来不许读成「通过」）',
      collectGateFailures(base, [], [], [], {}).kinds.emptyPage, 1)
    eq('S-HM105 ★ totalCells>0 ⇒ 不计入 emptyPage',
      collectGateFailures({ pages: [{ page: '/a', totalCells: 3, bodyTextLen: 10, unresolved: false }] }, [], [], [], {}).kinds.emptyPage, 0)
    eq('S-HM106 ★ unresolved 的页不重复计入 emptyPage（★ 那几类已单列）',
      collectGateFailures({ pages: [{ page: '/a', totalCells: 0, bodyTextLen: 0, unresolved: true }] }, [], [], [], {}).kinds.emptyPage, 0)
    eq('S-HM107 ★ emptyPage 计入总失败数',
      collectGateFailures(base, [], [], [], {}).total, 1)
    // ★★★ 假红防护：★ 本轮实测 `/admin/data-lifecycle/migration-state` 是**真的空状态页**
    //   （内容「暂无迁移记录」，★ 文本在、但没有任何卡片状盒子 ⇒ totalCells=0）
    //   ⇒ ★★ 只看 totalCells 会把它判红 ⇒ **假红** ⇒ 判据必须同时看文本。
    eq('S-HM108 ★★ 假红防护：totalCells=0 但**文本在**（★ 真空状态页）⇒ 不判红',
      collectGateFailures({ pages: [{ page: '/a', totalCells: 0, bodyTextLen: 8, unresolved: false }] }, [], [], [], {}).kinds.emptyPage, 0)
    eq('S-HM109 ★ 读数带 bodyTextLen（★ 「没量到」与「量到 0」要分得开）',
      buildPageReading('/x', [], ACCENTS, SEMANTICS, [], { bodyTextLen: 12 }).bodyTextLen, 12)
    eq('S-HM110 ★ 没传 bodyTextLen ⇒ 记 0（★ 断言不许恒真）',
      buildPageReading('/x', [], ACCENTS, SEMANTICS, []).bodyTextLen, 0)
    // ★★ 第 36 增量：**门的输出也是门的契约** —— 分母提示必须**按分母分支**。
    // ★★ 本轮当场抓到的自伤：原实现无条件挂「（分母为 0 ⇒ …）」⇒ 而那次分母明明是 4
    // ⇒ ★★ 输出在说假事 ⇒ ★★ 比假红更坏（假红只是吵，假输出是骗）。
    eq('S-HM111 ★ 分母 > 0 ⇒ 输出带格数，且**不带**「没量到」提示',
      metricDenomNote(4).includes('4 个') && !metricDenomNote(4).includes('没量到'), true)
    eq('S-HM112 ★ 分母 = 0 ⇒ 输出必须说「没量到指标格」（★ 不许读成「通过」）',
      metricDenomNote(0).includes('没量到指标格'), true)
    eq('S-HM113 ★★ 分母 > 0 ⇒ 输出**不得**出现「分母为 0」（★ 无条件挂提示 = 输出说谎）',
      metricDenomNote(4).includes('分母为 0'), false)
  }

  // ── 第 36 增量：「0 格 ⇒ 重探一次」的**接线**也要被自证盖住 ─────────────────
  // ★★ 为什么单列一块：★ 若这段没接上，★ 读数上**看不出任何异常**（分母偏小、不报错）
  // ⇒ ★★ 假绿 ⇒ ⇒ 所以判据（会不会重探）与接线（有没有重探）**都要**被断言。
  {
    const probeWith = (cs) => ({ cells: cs, bodyTextLen: 10 })
    const mkProbe = () => {
      let calls = 0
      let slept = 0
      return {
        get calls() { return calls },
        get slept() { return slept },
        fn: async () => { calls++; return probeWith([]) },
        fnWithCells: async () => { calls++; return probeWith([cell()]) },
        sleep: async (ms) => { slept += ms },
      }
    }

    // ① 第一次就量到指标格 ⇒ **不重探**（★ 探针只调 1 次、没睡）
    {
      const h = mkProbe()
      const r = await probeWithReprobe(h.fnWithCells, h.sleep)
      eq('S-HM114 ★ 第一次就量到指标格 ⇒ 不重探（★ 探针只调 1 次、没睡）',
        [r.reprobeCount, h.calls, h.slept], [0, 1, 0])
    }
    // ② 第一次 0 格 ⇒ 重探一次（★ 探针调 2 次、睡 1500ms、**结果取第二次**）
    {
      let calls = 0
      let slept = 0
      const r = await probeWithReprobe(async () => {
        calls++
        return calls === 1 ? probeWith([]) : probeWith([cell()])
      }, async (ms) => { slept += ms })
      eq('S-HM115 ★ 第一次 0 格 ⇒ 重探一次（★ 探针调 2 次、睡 1500ms、结果取第二次）',
        [r.reprobeCount, calls, slept, metricCellCountOf(r.probe)], [1, 2, 1500, 1])
    }
    // ③ 两次都 0 格 ⇒ 接受第二次（★ 仍记 reprobeCount=1，★ 不许悄悄抹掉）
    {
      let calls = 0
      const r = await probeWithReprobe(async () => { calls++; return probeWith([]) }, async () => {})
      eq('S-HM116 ★ 两次都 0 格 ⇒ 接受（★ 仍记 reprobeCount=1，不许悄悄抹掉）',
        [r.reprobeCount, calls, metricCellCountOf(r.probe)], [1, 2, 0])
    }
    // ④ 已知 firstProbe ⇒ 不多探一次（★ 稳定判据的最后一次采样可以直接复用）
    {
      let calls = 0
      const r = await probeWithReprobe(async () => { calls++; return probeWith([cell()]) }, async () => {}, probeWith([]))
      eq('S-HM117 ★ 传了 firstProbe ⇒ 只探 1 次（★ 不浪费一次探测）',
        [r.reprobeCount, calls, metricCellCountOf(r.probe)], [1, 1, 1])
    }
    // ⑤ 读数带 reprobeCount（★ 没接上时这个字段恒 0 ⇒ 一眼看出）
    eq('S-HM118 ★ 读数带 reprobeCount（★ 没接上时这个字段恒 0 ⇒ 一眼看出）',
      [buildPageReading('/x', [], ACCENTS, SEMANTICS, [], { reprobeCount: 1 }).reprobeCount,
        buildPageReading('/x', [], ACCENTS, SEMANTICS, []).reprobeCount], [1, 0])
  }

  const failed = cases.filter((x) => !x.ok)
  for (const x of cases) console.log(`  ${x.ok ? '✓' : '✘'} ${x.name}${x.ok ? '' : ' —— ' + x.msg}`)
  console.log(
    `\n自证 ${cases.length - failed.length}/${cases.length} ${failed.length ? '—— 有断言恒真/恒假' : '通过 —— 量具有牙。'}`,
  )
  return failed.length === 0
}

// ── CLI ─────────────────────────────────────────────────────────────────────

export async function main(argv = process.argv.slice(2)) {
  if (argv.includes('--selftest')) {
    const ok = await runRuntimeSelftest()
    process.exitCode = ok ? 0 : 1
    return
  }

  const pagesArg = argv.find((a) => a.startsWith('--pages='))
  const jsonArg = argv.find((a) => a.startsWith('--json='))
  const updateBaseline = argv.includes('--update-baseline')
  const pages = pagesArg ? pagesArg.slice(8).split(',').filter(Boolean) : ALL_PAGES

  // ★★ 第 36 增量：tab 变体页面**派生失败** ⇒ 测量前就判红。
  //   ★ 理由与基线不可用同源：「尺没了 / 尺坏了」这件事本身就已经是红的，
  //   再去跑一遍浏览器只是白花时间；而**更糟**的是它会「少测几页却显示通过」。
  // ★ ★ 子集运行（`--pages=`）**同样**要判红：★ 派生失败是【量具坏了】，
  //   与「这次只跑哪几页」无关 ⇒ ★ 旧写法把 `--pages=` 当成豁免 ⇒ 是真洞。
  if (TAB_PAGE_PROBLEMS.length) {
    console.log('✘ HERO-METRIC-TAB-PAGES-UNRESOLVED —— tab 变体页面派生失败，未测量。')
    for (const p of TAB_PAGE_PROBLEMS) console.log(`  · ${p}`)
    console.log('  ★ 判定不依赖测量 ⇒ 本次**没有**跑浏览器（清单不全 ⇒ 量到的不是全量）。')
    process.exitCode = 1
    return
  }

  if (!existsSync(CHROME_CANDIDATES[0])) {
    console.log('SKIP —— 本机没有 Chrome，未测量。（「没量到」不等于「通过」）')
    process.exitCode = 1
    return
  }

  const baseline = readBaseline()
  // ★ 基线不可用 ⇒ **测量之前**就判红（★ 与第 30 轮门同口径）：
  //   「棘轮的尺没了」这件事本身就已经是红的，再去跑一遍 108 页浏览器只是白花时间。
  // ★ 判定走纯函数 `baselineGateDecision` ⇒ 与自证同源（★ 别让 CLI 自己写一份）。
  const gate = baselineGateDecision(baseline)
  if (gate.red) {
    console.log(`✘ ${gate.probe} —— 基线缺失或结构不对，未测量。`)
    console.log(`  期望：${BASELINE}`)
    console.log('  ★ 判定不依赖测量 ⇒ 本次**没有**跑浏览器（基线没了，测了也无处判）。')
    process.exitCode = 1
    return
  }

  const port = 9340 + Math.floor(Math.random() * 40)
  const report = await runChrome(port, pages)

  const readings = report.pages.map((p) => ({
    page: p.page,
    wallCount: p.wallCount,
    accentCells: p.accentCells,
    // ★★ 第 36 增量：读数带**分母**（这一页量到几个指标格）⇒ 「0 处墙」才有解释力。
    metricCellCount: p.metricCellCount,
    totalCells: p.totalCells,
    bodyTextLen: p.bodyTextLen,
    isViolation: p.isViolation,
    unresolved: p.unresolved,
    finalPath: p.finalPath,
  }))

  const hard = collectHardGateViolations(report.pages)
  const ratchet = compareToBaseline(readings, baseline)
  // ★ 页集覆盖：区分「子集运行（诊断）」与「基线真过期（路由变了）」。
  const cover = pagesCoveredByBaseline(readings, baseline)

  // ★★ 第 36 增量：**读数自带「量的是哪个服务」**（★ 门输出也是门的契约）。
  //   ★★ 本轮踩过的坑：★ 门的 BASE 默认是 `http://localhost:5200`，★ 而本轮被测的树
  //   跑在 5202 ⇒ ★★ 臂没显式设 origin ⇒ **量的是另一棵树** ⇒ 读数「看起来对、其实不对」。
  console.log(`HERO-METRIC · 页面 ${report.pages.length} · 装饰性指标墙 ${hard.length} 处 · origin ${BASE}`)
  // ★★ 第 36 增量：报出**分母** —— 本次一共量到多少个指标格。
  //   ★ 没有分母，「0 处墙」既可能是「这一页干净」，也可能是「这一页一个指标格都没量到」
  //   ⇒ ★★ 门输出也是门的契约 ⇒ 分母必须一起报出来。
  const metricCellsTotal = report.pages.reduce((a, p) => a + (Number(p.metricCellCount) || 0), 0)
  console.log(metricDenomNote(metricCellsTotal))
  for (const v of hard) {
    // ★ 第 36 增量：门输出也是门的契约 ⇒ 报出**accent 落在哪儿**（数字 / 卡底 / 描边 / 阴影）。
    const pageCells = report.pages.find((p) => p.page === v.page)
    const denom = pageCells ? ` · 本页指标格 ${pageCells.metricCellCount}` : ''
    console.log(
      `  ✘ ${v.page} · ${v.accentCells}/${v.cells} 格染主色 · ${v.classes.join(' / ')} · 值 ${v.values.join(' ')}` +
        ` · 落点 ${(v.landings ?? []).join('+') || '未记录'}${denom}`,
    )
  }
  for (const v of ratchet) {
    console.log(`  ✘ 棘轮：${v.page} 由 ${v.previous} 升到 ${v.current}`)
  }
  const unresolvedPages = report.pages.filter((p) => p.unresolved)
  for (const p of unresolvedPages) {
    const redirected = (p.unresolvedTokens ?? []).find((t) => String(t).startsWith('HERO-METRIC-REDIRECTED:'))
    if (redirected) {
      // ★ 第 36 增量：★ 「没量到」必须**说成没量到**，不能读成「通过」。
      console.log(
        `  ✘ HERO-METRIC-REDIRECTED · ${p.page} · 落到了 ${String(redirected).split(':').slice(1).join(':')} ⇒ **没量到这一页**（不是「通过」）`,
      )
      continue
    }
    console.log(`  ✘ HERO-METRIC-ACCENT-UNRESOLVED · ${p.page} · 解析不出：${p.unresolvedTokens.join(' ')}`)
  }
  if (cover.isSubset) {
    console.log(
      `  ⓘ 本次是**子集运行**（量了 ${readings.length} 页 / 基线 ${Object.keys(baseline.pages).length} 页）` +
        `⇒ 棘轮只覆盖被量到的页，未量到的页本轮**未测量**（不是「通过」）。`,
    )
  }
  if (cover.missing.length) {
    console.log(`  ✘ HERO-METRIC-BASELINE-MISSING —— 基线里没有这些页：${cover.missing.join(' ')}`)
  }
  if (cover.stale) {
    console.log('  ✘ HERO-METRIC-BASELINE-STALE —— 页数相同但集合不同（路由变了？）⇒ 基线过期。')
  }
  if (report.errors.length) {
    for (const e of report.errors) console.log(`  ! ${e.page} · ${e.error}`)
  }

  if (updateBaseline) {
    // ★ 写回值 = min(旧值, 实测值) ⇒ 重建基线也放松不了棘轮（★ 第 31 增量实测到的坑）。
    const next = { ...baseline, pages: { ...baseline.pages } }
    for (const r of readings) {
      const prev = Number(baseline.pages[r.page]?.wallCount)
      const now = Number(r.wallCount)
      next.pages[r.page] = {
        ...(baseline.pages[r.page] ?? {}),
        wallCount: Number.isFinite(prev) ? Math.min(prev, now) : now,
      }
    }
    next.history = [
      ...(Array.isArray(baseline.history) ? baseline.history : []),
      buildBaselineHistoryEntry(Object.keys(next.pages).length, readings.length),
    ]
    writeFileSync(BASELINE, `${JSON.stringify(next, null, 2)}\n`)
    console.log(`基线已更新 → ${BASELINE}`)
  }

  if (jsonArg) {
    // ★ `--json=` 是 7 个字符（--json=）⇒ slice(7)；写成 slice(6) 会把 '=' 留在路径里
    //   ⇒ 写文件时 ENOENT（★ 本轮真跑时踩到：门跑完了却因为写读数炸掉）。
    writeFileSync(jsonArg.slice(7), JSON.stringify({ ...report, readings }, null, 1))
    console.log(`读数已写出 → ${jsonArg.slice(7)}`)
  }

  // ★ 第 36 增量：失败数走纯函数 `collectGateFailures`（★ CLI 与自证同源）
  //   ⇒ ★★ **报错的页也计入失败**（「没量到」不许读成「通过」）。
  const failures = collectGateFailures(report, hard, ratchet, unresolvedPages, cover)
  const failed = failures.total
  if (failures.kinds.pageErrors) {
    console.log(
      `  ✘ HERO-METRIC-PAGE-ERRORS · ${failures.kinds.pageErrors} 页探针报错 ⇒ **那几页没量到**（不是「通过」）`,
    )
  }
  if (failures.kinds.emptyPage) {
    const emptyPages = report.pages
      .filter((p) => Number(p.totalCells) === 0 && Number(p.bodyTextLen ?? 0) === 0 && p.unresolved !== true)
      .map((p) => p.page)
    console.log(
      `  ✘ HERO-METRIC-EMPTY-PAGE · ${failures.kinds.emptyPage} 页**连可见文本都没有** ⇒ **那几页没渲染出来，没量到**（不是「通过」）`,
    )
    for (const p of emptyPages) console.log(`    · ${p}`)
  }
  console.log(failed ? `\n✘ ${failed} 处待处理` : '\n✔ 通过')
  process.exitCode = failed ? 1 : 0
}

// ★ 只有直接跑本文件才执行 CLI（import 连带跑全量浏览器 ⇒ 模块没法被别的尺复用）。
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main()
}
