// saTestIds.contract.spec.ts —— L13 判据收敛的**跨文件契约钉测**
//
// 背景：16-session-analytics-system.yaml 的断言判据在 2026-11-23 从
// 「中文文案」改成「data-testid」。这类改动天然有**双向漂移**风险：
//   ① flow 里写了 id:，前端却没这个 testid ⇒ E2E 假红（量具错，不是产品红）；
//   ② 前端删/改了 testid，flow 还在断它 ⇒ 同上；
//   ③ 后来者又加回中文文案断言 ⇒ locale 依赖回流，换语种即假红。
// ⇒ 本钉测把三条都钉住，让漂移在 CI 就暴露，而不是在真机 E2E 时才发现。
//
// ★ 为什么不用组件测试：组件测试断的是「组件渲染出什么」，而 E2E 断的是
//   「DOM 里有没有这个选择器」。两者是不同的物——只有把 flow 文件与前端
//   源码**放在一起对账**，才抓得到跨文件漂移。
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// cwd = web/（vitest root）⇒ 仓根 = web/../
const REPO = resolve(process.cwd(), '..')
const FLOW = join(REPO, 'maestro/flows/16-session-analytics-system.yaml')
const SA_DIR = join(REPO, 'web/src/views/admin/session-analytics')

/** 本 flow 覆盖的 5 个页面组件（含 overview 的整合落点 users 页）。 */
const PAGE_COMPONENTS = [
  'SessionAnalyticsUsersView.vue',
  'SessionAnalyticsUserRankingView.vue',
  'SessionAnalyticsUserDetailView.vue',
  'SessionAnalyticsAgentRankingView.vue',
  'SessionAnalyticsModelRankingView.vue',
]

/**
 * ★★ 2026-11-23（L17）：flow 的 §0 会点**顶栏用户菜单**（显示名 / 登出）⇒
 *   ⇒ 这两个 `id:` 的锚点在**会话分析视图之外**（`web/src/components/shell/`）。
 *   ⇒ 若不把它们纳入扫描范围 ⇒ ★ **判据①会误报「flow 断言了前端不存在的 testid」**
 *   ⇒ ⇒ ★ **门自己的覆盖缺口**（L13 轮就栽过一次：门看不见 ≠ 缺口不存在）。
 *   ⇒ ★ 归入本钉测的扫描范围 ⇒ 门就既看得见「锚点缺失」，也看得见「命名不合规」。
 */
const EXTRA_COMPONENT_FILES = [
  join(REPO, 'web/src/components/shell/UserMenuDropdown.vue'),
]

function readVueFiles(dir: string): { file: string; source: string }[] {
  const out: { file: string; source: string }[] = []
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) {
      out.push(...readVueFiles(full))
    } else if (name.endsWith('.vue')) {
      out.push({ file: name, source: readFileSync(full, 'utf8') })
    }
  }
  return out
}

const flowText = readFileSync(FLOW, 'utf8')
const vueFiles = [
  ...readVueFiles(SA_DIR),
  ...EXTRA_COMPONENT_FILES.map((full) => ({
    file: full.split('/').pop() as string,
    source: readFileSync(full, 'utf8'),
  })),
]

/** 前端源码里出现的所有 data-testid 值。 */
const sourceTestIds = new Set<string>()
for (const { source } of vueFiles) {
  for (const m of source.matchAll(/data-testid="([^"]+)"/g)) {
    sourceTestIds.add(m[1])
  }
}

/** flow 里作为选择器出现的所有 id（id: "xxx"）。 */
const flowIds = new Set<string>()
for (const m of flowText.matchAll(/\bid:\s*"?([A-Za-z0-9_-]+)"?/g)) {
  flowIds.add(m[1])
}

/** flow 里仍是「文案断言」的条目（assertVisible: "…" 的字面量）。 */
const textAssertions = [...flowText.matchAll(/assertVisible:\s*"([^"]*)"/g)].map((m) => m[1])

// ★★★ 2026-11-23（L13 收口轮）新增：**任何命令**上的 `text:` 选择器。
//
// ★ 为什么必须补这条（原口径有洞）：原判据②只扫 `assertVisible: "…"` 这一种**顶层字面量**形式
//   ⇒ ★ **完全看不见 `tapOn: text:`** ⇒ ⇒ 两处 tab 交互的中文文案依赖
//   （`text: ".*模型用量.*"` / `text: ".*API Key用量.*"`）**一直没被门看见**。
//   ⇒ ★★ 这就是「门自己的覆盖缺口」：**门绿 ≠ 没缺口**，只说明缺口不在门的射程里。
//   ⇒ ⇒ 必须把「文案依赖」的口径扩到**选择器本身**，而不是某一种命令的写法。
//
// ★ 为什么区分 optional：★ **非 optional** 的中文 `text:` 会「换语种即假红」（响亮的错）；
//   而 **optional** 的中文 `text:` 只会 WARNED（找不到就跳过）⇒ 不影响判定
//   ⇒ ⇒ 口径＝「**非 optional 的命令上不得出现中文 `text:` 选择器**」。
//   ⇒ ★★ **2026-11-23（L17）**：§0 登出那两行已改成 `id:` ⇒ **flow 里零 `text:`**
//   ⇒ ⇒ 口径**升级**为「**任何** `text:` 选择器都不许（连 optional、连英文）」
//   ⇒ ⇒ ★★ **判据与量具自检一起升级**（★ 旧自检是「必须还剩着中文 text 选择器」
//   ⇒ 口径一升级它就会报红 ⇒ ★ **一个随口径过期而误导的自检，本身就是要修的缺陷**）。
//
// ★ 为什么用块级解析而不是 YAML 解析：`web/` 没有 yaml/js-yaml 依赖（实测 `require('yaml')` 抛错）
//   ⇒ ⇒ 不能给测试引入新依赖（否则 CI 镜像要装包）⇒ 用「按 `- 命令:` 切块」的近似解析。
const flowCodeOnly = flowText
  // 整行注释（行首空白 + #）不参与判据 ⇒ 注释里提到 text: 不算
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

describe('L13 · flow 16 判据收敛契约', () => {
  it('① flow 里出现的每个 id 选择器都必须在前端源码里存在', () => {
    // 反向漂移（前端改名/删除 testid）在这里变红，而不是在真机 E2E 上假红。
    const missing = [...flowIds].filter((id) => !sourceTestIds.has(id)).sort()
    expect(missing, `flow 断言了前端不存在的 testid：${missing.join(', ')}`).toEqual([])
    expect(flowIds.size).toBeGreaterThan(20) // 断言数量本身要有量，别悄悄退化
  })

  it('② flow 里不再有中文文案断言（判据与文案解耦）', () => {
    // 文案断言 = locale 依赖的源头。保留一条会换语种即假红。
    const cjk = textAssertions.filter((t) => /[一-鿿]/.test(t))
    expect(cjk, `flow 里仍有中文文案断言：${cjk.join(' | ')}`).toEqual([])
    expect(textAssertions.length).toEqual(0)
  })

  it('③ 5 个页面组件各自带页面级 testid（sa-page-*）', () => {
    for (const name of PAGE_COMPONENTS) {
      const file = vueFiles.find((f) => f.file === name)
      expect(file, `组件不存在：${name}`).toBeTruthy()
      expect(file!.source, `${name} 缺页面级 data-testid`).toMatch(/data-testid="sa-page-[a-z0-9-]+"/)
    }
  })

  it('④ sa-* testid 命名规范（小写 + 短横线，无中文/无驼峰）', () => {
    const bad = [...sourceTestIds].filter((id) => id.startsWith('sa-')).filter((id) => !/^sa-[a-z0-9]+(-[a-z0-9]+)*$/.test(id))
    expect(bad, `命名不合规：${bad.join(', ')}`).toEqual([])
  })

  it('⑤ flow 覆盖的 testid 在前端存在，且关键区块都有锚点', () => {
    // 每个页面至少要有：页面 / 区间 / 刷新 / 数据表 四类锚点——
    // 少了任何一类，flow 就有一段只能靠文案断（= 回到 L13 之前的状态）。
    const required = [
      'sa-page-users',
      'sa-page-user-ranking',
      'sa-page-user-detail',
      'sa-page-agent-ranking',
      'sa-page-model-ranking',
      'sa-range-label',
      'sa-refresh-btn',
      'sa-users-table',
      'sa-user-ranking-table',
      'sa-user-models-table',
      'sa-user-apikeys-table',
      'sa-agent-ranking-table',
      'sa-model-ranking-table',
    ]
    for (const id of required) {
      expect(sourceTestIds.has(id), `前端缺锚点：${id}`).toBe(true)
      expect(flowIds.has(id), `flow 未断言该锚点：${id}`).toBe(true)
    }
  })

  it('⑥ ★★ flow 里**不得有任何** `text:` 选择器（L13 补轮 + ★ L17 收口：连 optional 也不许）', () => {
    // ★ 本条是 L13 补轮新增：**原判据②只扫 `assertVisible: "…"``，看不见 `tapOn: text:`**
    //   ⇒ 两处 tab 交互的中文文案依赖一直隐形。
    // ★ L13 当时的口径是「**非 optional** 的命令上不得有中文 text 选择器」，
    //   并**故意**保留了 §0 登出那两处 optional 的中文 text（当作「判据有射程」的自检）。
    // ★★ **2026-11-23（L17）**：★ 那两处已改成 `id:`（`app-user-menu-name` /
    //   `app-user-menu-logout`）⇒ ⇒ **flow 里一个 `text:` 选择器都不剩**
    //   ⇒ ⇒ ★ 原口径连同它的自检一起过期（自检会因「射程为零」而报红 ⇒ 那是个**误导性的红**）。
    // ⇒ ⇒ ★ 口径升级为**更强**的形态：**任何** `text:` 选择器都不许（含 optional、
    //   含英文）⇒ ★ flow 的判据**全部**与文案解耦 ⇒ ★ 换语种 ⇒ 0 WARNED。
    //   ⇒ ★ 仍保留**射程自检**，但换成正确的问题：「判据扫的目标是不是空的？」
    //     ⇒ flow 的 `id:` 选择器集合必须非空（否则连 flow 都没在用选择器判据）。
    const offenders = textSelectors.map((t) => t.block)
    expect(offenders, `flow 里仍有 text: 选择器（判据必须与文案解耦）：${offenders.join(' | ')}`).toEqual([])
    expect(
      flowIds.size,
      'flow 里一个 id: 选择器都没有 ⇒ 本判据扫的是空目标（门会恒绿）',
    ).toBeGreaterThan(0)
  })
})
