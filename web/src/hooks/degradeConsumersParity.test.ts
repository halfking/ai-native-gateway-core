// degradeConsumersParity.test.ts —— 后端自报降级的端点，前端必须真的有消费者
//
// ## 这条门在挡什么
//
// 2026-10-03 完成度审计时发现：T1 点名的 5 个文件里
// **4 个的降级标记到了消费者，只有 `usage_trend_series.go` 没有**。
// 它的两个端点恒发 `degraded`/`missing_view`（T1 第一批就加了），
// `api/usage.ts` 的类型也声明了，但 `BoardUsageTrendSection.vue`
// 只读 `resp.series ?? []` —— 降级时空序列被当成「零用量」渲染。
//
// **它为什么躲过了所有既有判据**：
//   · 服务端是对的（有标记、恒发、无 omitempty）
//   · 前端类型是对的（字段已声明）
//   · 组件不报错、图表照常渲染
//   · 契约测试只检查「载荷带标记」，不检查「有人读」
//
// 从**任何一侧**看都自洽。这是 §19「产出正确但没到消费者」的最后一环，
// 而那一环此前只在我手工核对维度名时才偶然撞见过。
//
// ## 判据的做法
//
// 列出「后端已自报降级」的前端数据入口，逐个断言：
// **至少有一个非测试文件读取它的 degraded 字段**。
//
// 为什么用「端点清单」而不是解析全部代码：消费形态多种多样
// （`resp.series ?? []` + `resp.degraded`、`mergeDegradation(payload)`、
// `unwrapDegradedList(...)`），逐个用正则找会让门比被测代码更脆。
// 清单里每条都写了「谁在消费」，清单与现实不符时门会红
// （下面那条「消费点必须真实存在」就是为此）。
//
// ## 反向对照
//
// · 把某个消费点的 `degraded` 读法改掉（使其不再读）→ 该条红
// · 把一个消费点文件改名 → 「消费点必须存在」红

import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// web/src/hooks → 上溯**一级** = web/src。
// ⚠ 层级是实测出来的，不是数出来的：候选 0/1/2/3 级里只有 1 级的
// 解析结果下存在 `api/usage.ts`。前两版分别写成 3 级和 2 级，
// 都让 6 个消费点「不存在」、两条一起红 —— 症状是「全红」，
// 但原因只是路径层级。**判据里的相对路径要用探针确认，不要靠数目录。**
const SRC = join(import.meta.dirname, '..')

/**
 * 每个条目 = 「后端已自报降级」的数据入口 + 它的消费点。
 *
 * `degradedIn` 是消费点里必须出现的字面量之一——通常就是字段名本身。
 * 列多个是因为不同入口的消费写法不同（有的读 `.degraded`，
 * 有的整体交给 `mergeDegradation`）。
 */
const DEGRADED_ENDPOINTS: { name: string; consumers: string[]; degradedIn: string }[] = [
  {
    // T1 点名文件之一：usage_trend_series.go 的两个端点
    name: 'GET /api/admin/usage/trend-series (admin/usage_trend_series.go)',
    consumers: ['components/board/BoardUsageTrendSection.vue'],
    degradedIn: 'resp.degraded',
  },
  {
    name: 'GET /api/admin/usage/trend-models (admin/usage_trend_series.go)',
    consumers: ['components/board/BoardUsageTrendSection.vue', 'views/admin/UsageTrendExplorer.vue'],
    degradedIn: 'degraded',
  },
  {
    name: 'GET /api/usage/hot-keys (admin/usage.go 的 DegradedListResponse)',
    consumers: ['api/usage.ts'],
    degradedIn: 'unwrapDegradedList',
  },
  {
    name: 'GET /api/usage/by-model (admin/usage.go)',
    consumers: ['api/usage.ts'],
    degradedIn: 'unwrapDegradedList',
  },
  {
    name: 'GET /api/usage/by-provider (admin/usage.go)',
    consumers: ['api/usage.ts', 'components/ProviderUsageExplorer.vue'],
    degradedIn: 'degraded',
  },
  {
    // T1 点名文件之一：usage_credits.go 的积分降级（经 summary 载荷）
    name: 'credits 降级 (admin/usage_credits.go → summary.credits_missing_view)',
    consumers: ['api/board.ts', 'components/board/BoardHeroRow.vue'],
    degradedIn: 'credits_missing_view',
  },
  {
    // 看板饼图（第七轮引入的逐维度账本）
    name: 'board pies 逐维度降级 (admin/dashboard_board_fallback.go)',
    consumers: ['api/board.ts'],
    degradedIn: 'degraded_pies',
  },
  {
    name: '整屏汇总降级 (admin/dashboard_board_queries.go 的 degraded_summary)',
    consumers: ['api/board.ts', 'components/board/BoardHeroRow.vue'],
    degradedIn: 'degraded_summary',
  },
]

describe('后端自报降级 ⇒ 前端有真实消费者', () => {
  it('每个端点的每个消费点都必须真实存在', () => {
    const missing: string[] = []
    for (const ep of DEGRADED_ENDPOINTS) {
      for (const c of ep.consumers) {
        if (!existsSync(join(SRC, c))) missing.push(`${ep.name} → ${c}`)
      }
    }
    expect(
      missing,
      `这些消费点文件不存在 —— 清单与现实不符，请更新清单或恢复文件。\n${missing.join('\n')}`,
    ).toEqual([])
  })

  it('每个端点都至少有一个消费点真的读了 degraded 字段', () => {
    const silent: string[] = []
    for (const ep of DEGRADED_ENDPOINTS) {
      const read = ep.consumers.some((c) => {
        const f = join(SRC, c)
        if (!existsSync(f)) return false
        return readFileSync(f, 'utf8').includes(ep.degradedIn)
      })
      if (!read) silent.push(`${ep.name}（查了 ${ep.consumers.join(', ')}，都没读到 "${ep.degradedIn}"）`)
    }
    expect(
      silent,
      [
        '这些端点后端已经自报降级，但前端没有任何地方读那个字段 ——',
        '载荷里带着标记，却没到消费者。降级时页面会把「没算出来」显示成「零/空」。',
        ...silent,
      ].join('\n'),
    ).toEqual([])
  })

  it('消费点清单不是空的（防把门写成恒绿）', () => {
    expect(DEGRADED_ENDPOINTS.length).toBeGreaterThanOrEqual(8)
  })
})
