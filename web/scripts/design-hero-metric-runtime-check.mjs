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
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
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

// ── 纯函数：描述符 → 判定 ─────────────────────────────────────────────────
//
// ★ 判定吃**描述符**（纯数据），不碰 DOM ⇒ 自证不需要浏览器，
//   且浏览器探针只负责「把 DOM 翻译成描述符」，两层口径不会各说各话。

/**
 * @typedef {{ w: number, h: number, radius: number, cls: string,
 *             parentKey: string, leaves: {text: string, fs: number, color: string}[],
 *             hasInteractive: boolean, hidden: boolean }} CellDesc
 */

/** 解析 rgb/rgba 字符串 → [r,g,b]；解析不出来返回 null。 */
export function parseRgb(color) {
  const s = String(color ?? '').trim().toLowerCase()
  const m = s.match(/rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/)
  if (!m) return null
  return [Number(m[1]), Number(m[2]), Number(m[3])]
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
 * 判「这一格是不是指标格」（巨数字 + 小标签）。
 *
 * @param {CellDesc} cell
 * @returns {boolean}
 */
export function isMetricCell(cell) {
  if (!cell || typeof cell !== 'object') return false
  // 隐藏子树：用户看不到的格子不是指标墙。
  if (cell.hidden) return false
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
  let big = null
  for (const l of leaves) {
    const fs = Number(l?.fs)
    if (!Number.isFinite(fs)) continue
    if (!big || fs > Number(big.fs)) big = l
  }
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

/** 找出「被装饰性强调色染了巨数字」的那些指标格。 */
export function isAccentCell(cell, accentColors, semanticColors = []) {
  if (!isMetricCell(cell)) return false
  const leaves = Array.isArray(cell.leaves) ? cell.leaves : []
  let big = null
  for (const l of leaves) {
    const fs = Number(l?.fs)
    if (!Number.isFinite(fs)) continue
    if (!big || fs > Number(big.fs)) big = l
  }
  if (!big) return false
  return isDecorativeAccent(big.color, accentColors, semanticColors)
}

/**
 * 找「并排的装饰性指标墙」。
 *
 * @param {CellDesc[]} cells
 * @param {string[]} accentColors
 * @param {string[]} semanticColors
 * @returns {{parentKey: string, cells: number, accentCells: number,
 *            classes: string[], labels: string[], values: string[]}[]}
 */
export function findAccentWalls(cells, accentColors, semanticColors = []) {
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
    const accents = group.filter((c) => isAccentCell(c, accentColors, semanticColors))
    if (accents.length < MIN_ACCENT_CELLS) continue
    out.push({
      parentKey,
      cells: group.length,
      accentCells: accents.length,
      classes: [...new Set(group.map((c) => String(c.cls ?? '')))],
      labels: group.map((c) => {
        const leaves = Array.isArray(c.leaves) ? c.leaves : []
        let small = null
        for (const l of leaves) {
          const fs = Number(l?.fs)
          if (!Number.isFinite(fs) || fs > SMALL_LABEL_MAX_PX) continue
          if (!small || fs < Number(small.fs)) small = l
        }
        return String(small?.text ?? '').trim()
      }),
      values: accents.map((c) => {
        const leaves = Array.isArray(c.leaves) ? c.leaves : []
        let big = null
        for (const l of leaves) {
          const fs = Number(l?.fs)
          if (!Number.isFinite(fs)) continue
          if (!big || fs > Number(big.fs)) big = l
        }
        return String(big?.text ?? '').trim()
      }),
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
 */
export function buildPageReading(page, cells, accentColors, semanticColors = []) {
  const walls = findAccentWalls(cells, accentColors, semanticColors)
  const verdict = judgePageHeroMetric(walls)
  return {
    page,
    totalCells: (Array.isArray(cells) ? cells : []).length,
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
        ? `写回值 = min(旧值, 实测值)；★ 本次只实测了 ${measured} 页（子集运行）` +
          `⇒ 其余 ${total - measured} 页**未参与写回**（它们的值原样保留）。`
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
  const resolveToken = (name) => {
    const probe = document.createElement('span')
    probe.style.position = 'absolute'
    probe.style.left = '-9999px'
    probe.style.top = '0'
    probe.style.color = 'rgb(1, 2, 3)'
    probe.style.color = 'var(' + name + ')'
    document.body.appendChild(probe)
    const v = getComputedStyle(probe).color
    probe.remove()
    if (!v || v === 'rgb(1, 2, 3)') return null
    return v
  }
  const tokens = {}
  for (const n of ${JSON.stringify([...ACCENT_TOKEN_NAMES, ...SEMANTIC_TOKEN_NAMES])}) {
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
    for (const n of el.querySelectorAll('*')) {
      if (n.children.length > 0) continue
      const t = (n.innerText || n.textContent || '').trim()
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
      hidden: false,
    })
  }
  return JSON.stringify({ cells, tokens })
})()`

async function runChrome(port, pages) {
  const profile = mkdtempSync(join(tmpdir(), 'hero-metric-runtime-'))
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
  ], { stdio: 'ignore' })

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
      await cdp('Page.navigate', { url: BASE + p })
      await sleep(3200)
      const probe = JSON.parse(await evaluate(PROBE))
      const tokens = probe.tokens ?? {}
      if (!report.tokens) report.tokens = tokens
      const { accents, unresolved } = buildAccentSet(tokens)
      const semanticColors = SEMANTIC_TOKEN_NAMES.map((n) => tokens[n]).filter(Boolean)
      // ★ 强调色集合解析不出来 ⇒ 那一页「没量到」，不是「真没有」。
      if (!accents.length) {
        report.pages.push({
          ...buildPageReading(p, [], [], []),
          unresolved: true,
          unresolvedTokens: unresolved,
        })
        continue
      }
      const walls = findAccentWalls(probe.cells ?? [], accents, semanticColors)
      const verdict = judgePageHeroMetric(walls)
      report.pages.push({
        ...buildPageReading(p, probe.cells ?? [], accents, semanticColors),
        unresolved: false,
      })
    } catch (e) {
      report.errors.push({ page: p, error: String(e?.message || e) })
    }
  }
  ws.close()
  chrome.kill()
  return report
}

// ── 判据自证 ────────────────────────────────────────────────────────────────
//
// ★ 必须**双向**：会判红 + 不误判各半 —— 否则恒绿门与恒红门同样没用。

export function runRuntimeSelftest() {
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
    const ok = runRuntimeSelftest()
    process.exitCode = ok ? 0 : 1
    return
  }

  const pagesArg = argv.find((a) => a.startsWith('--pages='))
  const jsonArg = argv.find((a) => a.startsWith('--json='))
  const updateBaseline = argv.includes('--update-baseline')
  const pages = pagesArg ? pagesArg.slice(8).split(',').filter(Boolean) : DEFAULT_PAGES

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
    isViolation: p.isViolation,
    unresolved: p.unresolved,
  }))

  const hard = collectHardGateViolations(report.pages)
  const ratchet = compareToBaseline(readings, baseline)
  // ★ 页集覆盖：区分「子集运行（诊断）」与「基线真过期（路由变了）」。
  const cover = pagesCoveredByBaseline(readings, baseline)

  console.log(`HERO-METRIC · 页面 ${report.pages.length} · 装饰性指标墙 ${hard.length} 处`)
  for (const v of hard) {
    console.log(
      `  ✘ ${v.page} · ${v.accentCells}/${v.cells} 格染主色 · ${v.classes.join(' / ')} · 值 ${v.values.join(' ')}`,
    )
  }
  for (const v of ratchet) {
    console.log(`  ✘ 棘轮：${v.page} 由 ${v.previous} 升到 ${v.current}`)
  }
  const unresolvedPages = report.pages.filter((p) => p.unresolved)
  for (const p of unresolvedPages) {
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

  const failed =
    hard.length + ratchet.length + unresolvedPages.length + cover.missing.length + (cover.stale ? 1 : 0)
  console.log(failed ? `\n✘ ${failed} 处待处理` : '\n✔ 通过')
  process.exitCode = failed ? 1 : 0
}

// ★ 只有直接跑本文件才执行 CLI（import 连带跑全量浏览器 ⇒ 模块没法被别的尺复用）。
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main()
}
