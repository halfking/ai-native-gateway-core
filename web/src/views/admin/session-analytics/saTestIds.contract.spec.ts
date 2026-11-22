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
const vueFiles = readVueFiles(SA_DIR)

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
})
