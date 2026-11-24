// statsRangeTestIds.contract.spec.ts —— flow 18 的**跨文件契约钉测**
// （2026-11-24 统一轮交付）
//
// ★ 为什么要有这个钉测（与仓内 saTestIds.contract.spec.ts 同构）：
//   flow 18 的断言判据是 **data-testid**（结构判据，与界面语言解耦）。
//   这类判据天然有**双向漂移**风险：
//     ① flow 里写了 `id: kx-dr-preset-last7d`，前端却改了命名 ⇒ E2E 假红
//        （★ 是**量具错**，不是产品红 ⇒ 会被当成产品缺陷去查，浪费一轮）；
//     ② 前端加了新锚点，flow 忘了断 ⇒ 覆盖率悄悄退化，没人知道；
//     ③ 后来者又加回中文文案断言 ⇒ locale 依赖回流 ⇒ 换语种即假红。
//   ⇒ 把三条都钉住，让漂移在 CI 暴露，而不是在真机 E2E 时才发现。
//
// ★ 为什么**不用组件测试**：组件测试断的是「组件渲染出什么」，而 E2E 断的是
//   「DOM 里有没有这个选择器」——两者是不同的物；只有把 flow 文件与前端源码
//   **放在一起对账**，才抓得到跨文件漂移。
//
// ★ 为什么必须处理**动态 testid**：档位按钮的 testid 是模板串
//   （`:data-testid="`kx-dr-preset-${preset.id}`"`）⇒ 静态正则扫不到
//   ⇒ ⇒ 把 `${…}` 归一成 `*` 当作**模式**参与匹配。
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// cwd = web/（vitest root）⇒ 仓根 = web/../
const REPO = resolve(process.cwd(), '..')
const FLOW = join(REPO, 'maestro/flows/18-stats-range-and-scope-hint.yaml')

/**
 * ★ flow 18 覆盖到的前端文件清单。
 *   · 三处消费方（看板筛选条 / 会话统计面板 / 会话分析用户页）
 *   · 两个本轮新增的共享组件（StatsDateRangePicker / StatsScopeHint）
 *   · 日期控件本体（KxDateRangePicker —— 档位与自定义起止的锚都在它身上）
 *   · 看板页容器（DashboardViewV2 —— 提示符挂在它的刷新按钮旁）
 *   · 顶栏用户菜单（flow §0 的登出两行；同 saTestIds.contract.spec.ts 的理由：
 *     ★ 判据①若看不见 §0 的锚点 ⇒ 会误报「flow 断言了前端不存在的 testid」
 *     ⇒ ⇒ **门自己的覆盖缺口**（门看不见 ≠ 缺口不存在））
 */
const FRONTEND_FILES = [
  'web/src/components/ui/KxDateRangePicker.vue',
  'web/src/components/analytics/StatsDateRangePicker.vue',
  'web/src/components/analytics/StatsScopeHint.vue',
  'web/src/components/board/BoardFilterBar.vue',
  'web/src/components/SessionStatsPanel.vue',
  'web/src/views/DashboardViewV2.vue',
  'web/src/views/admin/session-analytics/SessionAnalyticsUsersView.vue',
  'web/src/components/shell/UserMenuDropdown.vue',
  // ★ 2026-11-25：flow 18 的登录段用 `login-modal-title` 作**条件分流锚点**
  //   ⇒ 它是 LoginModal.vue 里的 **HTML id**（不是 data-testid）
  //   ⇒ 登录弹窗的**出现与否**＝「要不要走 UI 登录」的判据（见 flow 头注）。
  'web/src/components/LoginModal.vue',
]

const flowText = readFileSync(FLOW, 'utf8')

/** 前端源码里出现的所有 data-testid（静态字面量 + 动态模板串 + 状态三元）。 */
const sourceTestIds = new Set<string>()
/** 动态模板归一后的**模式**（`${…}` → `*`）。 */
const sourceTestIdPatterns: string[] = []
/**
 * 前端源码里的 **HTML id**（`<h2 id="login-modal-title">`）。
 * ★ 为什么单列一栏（2026-11-25）：Maestro 的 `id:` 选择器**匹配 HTML id**
 *   （`data-testid` 是它的**优先**命中项，但不是唯一来源）⇒ flow 里的
 *   `id: "login-modal-title"` 指的是 HTML id ⇒ 本门若只扫 data-testid
 *   ⇒ 会把**真实存在**的锚点误报成「前端不存在」（量具口径过窄 ⇒ 假红）。
 *   ⇒ 判据口径：flow 的每个 `id:` 锚点，要么是 data-testid、要么是 HTML id，
 *     **两者都算「存在」** ⇒ 反向漂移保护不被削弱（改名/删除仍会红）。
 */
const sourceHtmlIds = new Set<string>()
for (const rel of FRONTEND_FILES) {
  const source = readFileSync(join(REPO, rel), 'utf8')
  // ① 静态字面量（前面不是 ':' ⇒ 不是绑定表达式）。
  for (const m of source.matchAll(/(?<!:)data-testid="([^"]+)"/g)) {
    sourceTestIds.add(m[1])
  }
  // ② 绑定表达式之一：模板串（`:data-testid="`kx-dr-preset-${preset.id}`"`）
  //    ⇒ 把 `${…}` 归一成 `*` 当作**模式**参与匹配。
  for (const m of source.matchAll(/:data-testid="`([^`]+)`"/g)) {
    sourceTestIdPatterns.push(m[1].replace(/\$\{[^}]+\}/g, '*'))
  }
  // ③ 绑定表达式之二：状态三元（`:data-testid="disabled ? 'a' : 'b'"`）
  //    ⇒ 里面的**每一个**引号字面量都是合法的 testid ⇒ 全部收进来。
  //    ★ 为什么要单列：触发器锚点随「可交互/不可交互」切换
  //      （2026-11-24 E2E 第二轮：加载期控件 disabled ⇒ 点击点不动 ⇒ 假红），
  //      两个值都必须在登记范围内，否则门会把「flow 用了可交互态锚点」误报成缺失。
  for (const m of source.matchAll(/:data-testid="([^"`]+)"/g)) {
    for (const lit of m[1].matchAll(/'([^']+)'|"([^"]+)"/g)) {
      sourceTestIds.add(lit[1] ?? lit[2])
    }
  }
  // ④ **HTML id**（★ 排除 `data-testid`（前缀是 `-`）与 `:id` 绑定（前缀是 `:`）★）。
  for (const m of source.matchAll(/(?<![\w:-])id="([^"]+)"/g)) {
    sourceHtmlIds.add(m[1])
  }
}

/** flow 里作为选择器出现的所有 id（id: "xxx"）。 */
const flowIds = new Set<string>()
for (const m of flowText.matchAll(/\bid:\s*"?([A-Za-z0-9_-]+)"?/g)) {
  flowIds.add(m[1])
}

/** flow 里仍是「文案断言」的条目（assertVisible: "…" 的字面量）。 */
const textAssertions = [...flowText.matchAll(/assertVisible:\s*"([^"]*)"/g)].map((m) => m[1])

/** 注释行不参与判据（注释里提到 text: 不算）。 */
const flowCodeOnly = flowText
  .split('\n')
  .filter((line) => !/^\s*#/.test(line))
  .join('\n')

/** 每个命令块里的 `text:` 选择器 + 该命令是否 optional。 */
const textSelectors = flowCodeOnly
  .split(/\n(?=\s*-\s+[A-Za-z])/)
  .map((block) => {
    const m = block.match(/\btext:\s*"([^"]*)"/)
    if (!m) return null
    return { value: m[1], optional: /\boptional:\s*true/.test(block), block: block.trim().slice(0, 80) }
  })
  .filter((x): x is { value: string; optional: boolean; block: string } => x !== null)

/** 判据：某个 flow id 是否在前端存在（静态命中或命中动态模式）。 */
function existsInFrontend(id: string): boolean {
  if (sourceTestIds.has(id)) return true
  // ★ HTML id 也算「存在」（Maestro 的 `id:` 选择器同样命中它）。
  if (sourceHtmlIds.has(id)) return true
  return sourceTestIdPatterns.some((p) => {
    const rx = new RegExp(`^${p.split('*').map((s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('[^ ]*')}$`)
    return rx.test(id)
  })
}

describe('统一轮 · flow 18 判据收敛契约', () => {
  it('① flow 里出现的每个 id 选择器都必须在前端源码里存在', () => {
    // 反向漂移（前端改名/删除 testid）在这里变红，而不是在真机 E2E 上假红。
    const missing = [...flowIds].filter((id) => !existsInFrontend(id)).sort()
    expect(missing, `flow 断言了前端不存在的锚点（data-testid 或 HTML id）：${missing.join(', ')}`).toEqual([])
    expect(flowIds.size).toBeGreaterThan(10) // 断言数量本身要有量，别悄悄退化
  })

  it('② flow 里零 `text:` 选择器（判据与文案解耦 ⇒ 换语种结果相同）', () => {
    const offenders = textSelectors.map((t) => t.block)
    expect(offenders, `flow 里仍有 text: 选择器（判据必须与文案解耦）：${offenders.join(' | ')}`).toEqual([])
    expect(
      flowIds.size,
      'flow 里一个 id: 选择器都没有 ⇒ 本判据扫的是空目标（门会恒绿）',
    ).toBeGreaterThan(0)
  })

  it('③ flow 里没有中文文案断言', () => {
    const cjk = textAssertions.filter((t) => /[一-鿿]/.test(t))
    expect(cjk, `flow 里仍有中文文案断言：${cjk.join(' | ')}`).toEqual([])
    expect(textAssertions.length).toEqual(0)
  })

  it('④ 五处消费方都必须引用**同一个**时间范围组件（防单边漂移）', () => {
    // ★ 本轮的交付口径是「结构同构」⇒ 五处 import 的是**同一个文件**，
    //   而不是「各处各写一份长得一样的」⇒ 这条判据把「结构同构」钉在源码层。
    //   五处 = 看板页筛选条 / 会话统计面板 / 会话分析用户页 / **运营面板**
    //        / **运营看板列表页**（★ 2026-11-25 并入统一轮）
    //   （★ 2026-11-25：运营面板并入共享组件 ⇒ 此前它是**第四套**预设
    //     （今天/近7/近30/近90、跨度上限 365 天）⇒ 与其余三页形态不同；
    //     同轮运营看板列表页（:dimension）也并入 ⇒ 此前它是**第四套**预设
    //     （今天/近7/近30/近90、自定义上限 365 天）＋ **URL start/end 深链**）。
    const timeRangeConsumers = [
      'web/src/components/board/BoardFilterBar.vue',
      'web/src/components/SessionStatsPanel.vue',
      'web/src/views/admin/session-analytics/SessionAnalyticsUsersView.vue',
      'web/src/views/BoardOperationalView.vue',
      'web/src/views/BoardOperationalListView.vue',
    ]
    for (const rel of timeRangeConsumers) {
      const source = readFileSync(join(REPO, rel), 'utf8')
      expect(source, `${rel} 没有引用共享组件 StatsDateRangePicker`).toMatch(
        /from\s+['"][^'"]*analytics\/StatsDateRangePicker\.vue['"]/,
      )
      // ★ 反向判据：**不许**直接用裸 KxDateRangePicker（那会是第六套形态）。
      //   （裸控件本身仍在共享组件内部使用 ⇒ 判据只查消费方，不查控件本体。）
      expect(
        source,
        `${rel} 直接用了裸 KxDateRangePicker ⇒ 它自己有一套档位 ⇒ 与其余页不同构`,
      ).not.toMatch(/from\s+['"][^'"]*ui\/KxDateRangePicker\.vue['"]/)
    }
    // 提示符只挂在**有统计面板**的两个页面上（看板页签头部 + 会话分析用户页）
    //   ⇒ 不要求看板筛选条也挂它（那块条子没有自己的「统计说明」可讲）。
    const scopeHintConsumers = [
      'web/src/views/DashboardViewV2.vue',
      'web/src/views/admin/session-analytics/SessionAnalyticsUsersView.vue',
    ]
    for (const rel of scopeHintConsumers) {
      const source = readFileSync(join(REPO, rel), 'utf8')
      expect(source, `${rel} 没有引用共享组件 StatsScopeHint`).toMatch(
        /from\s+['"][^'"]*analytics\/StatsScopeHint\.vue['"]/,
      )
    }
  })

  it('⑤ 关键锚点齐全（三页共享壳 / 档位 / 自定义起止 / 提示符）', () => {
    const required = [
      // 三页共用的同一个组件（共享封装的壳）
      'stats-date-range-picker',
      // 触发器 + 面板 + 自定义起止（证明 presets-only 未开）
      'kx-dr-trigger',
      'kx-dr-panel',
      'kx-dr-inputs',
      'kx-dr-input-start',
      'kx-dr-input-end',
      // 固定档位（date 精度 = 7 个）
      'kx-dr-preset-today',
      'kx-dr-preset-yesterday',
      'kx-dr-preset-last7d',
      'kx-dr-preset-last14d',
      'kx-dr-preset-last30d',
      'kx-dr-preset-thisMonth',
      'kx-dr-preset-lastMonth',
      // 提示符（点击才展开）
      'stats-scope-hint-label',
      'stats-scope-hint-body',
    ]
    for (const id of required) {
      expect(existsInFrontend(id), `前端缺锚点：${id}`).toBe(true)
      expect(flowIds.has(id), `flow 未断言该锚点：${id}`).toBe(true)
    }
  })

  it('⑥ 动态 testid 的归一模式能覆盖 flow 里用到的每个档位', () => {
    // ★ 档位 testid 是模板串 ⇒ 静态集合里没有 ⇒ 必须靠模式匹配 ⇒
    //   这条判据专门确认「模式归一这条径真的能用」（否则①会误报缺锚点）。
    expect(sourceTestIdPatterns.length).toBeGreaterThan(0)
    for (const id of [...flowIds].filter((x) => x.startsWith('kx-dr-preset-'))) {
      expect(existsInFrontend(id), `动态模式没覆盖住档位锚点：${id}`).toBe(true)
    }
  })

  it('⑦ 触发器锚点随「可交互」状态切换（⇒ E2E 能等到可点再点）', () => {
    // ★ 2026-11-24 E2E 第二轮（真机实测）：会话统计面板在加载期把日期控件置为
    //   disabled ⇒ Maestro 点它**面板不会开** ⇒ 假红（产品是好的）。
    //   ⇒ 锚点必须区分「可交互 / 不可交互」两个值，E2E 的
    //   `extendedWaitUntil: visible: id: kx-dr-trigger` 才会一直等到可点为止。
    const source = readFileSync(join(REPO, 'web/src/components/ui/KxDateRangePicker.vue'), 'utf8')
    expect(source, '触发器锚点必须区分「不可交互」态').toMatch(/kx-dr-trigger-disabled/)
    expect(source, '触发器锚点必须保留「可交互」态').toMatch(/'kx-dr-trigger'/)
    expect(flowIds.has('kx-dr-trigger'), 'flow 必须用「可交互」态锚点').toBe(true)
    expect(flowIds.has('kx-dr-trigger-disabled'), 'flow 不该断言「不可交互」态锚点').toBe(false)
  })
})
