// saDeepLink.contract.spec.ts —— 会话分析族的深链契约（2026-11-23 审计轮 P1-3 / P2-2 / P2-5）
//
// ★ 三条缺陷同族：**跳过去的那条链没人消费** ⇒ 点击静默无效。
//
//	① P1-3：Top5「任务」行点击跳到 /admin/session-analytics/users?task=…
//	   而该页此前**连 useRoute 都没有** ⇒ ?task= 无人消费 ⇒ 点过去什么也没发生。
//	② P2-5：三个排行页写死 rangeFromSpanDays(7) ⇒ 从带 start_date/end_date 的
//	   链接进来时**日期被忽略**（用户点的区间与看到的不是同一区间）。
//	③ P2-2：Top5 四维并发加载**没有请求序号** ⇒ 旧响应覆盖新数据；且某个维度
//	   失败时被画成「无结果」（把失败伪装成空数据）。
//
// ★ 为什么用源码契约而不是 mount：本族视图依赖 i18n / router / element-plus，
//   mount 成本高；本门钉的是**代码形状**（query 读取 / 请求序号 / 失败呈现），
//   与同族做法一致（components/saTop5Providers.contract.spec.ts）。
//   ★ query 读取的**取值口径**由 saQueryParams.spec.ts 钉住（纯函数）。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

async function read(rel: string): Promise<string> {
  for (const c of CANDS(rel)) {
    try {
      return await readFile(c, 'utf8')
    } catch {
      /* next */
    }
  }
  throw new Error(`${rel} not found`)
}

/** 去掉注释，只留可执行代码。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
}

const USERS_VIEW = 'views/admin/session-analytics/SessionAnalyticsUsersView.vue'
const TOP5 = 'views/admin/session-analytics/components/SaTop5Rankings.vue'
const RANKING_VIEWS = [
  'views/admin/session-analytics/SessionAnalyticsUserRankingView.vue',
  'views/admin/session-analytics/SessionAnalyticsModelRankingView.vue',
  'views/admin/session-analytics/SessionAnalyticsAgentRankingView.vue',
]

describe('P1-3 ?task= 必须被消费（Top5 任务行点击不再静默无效）', () => {
  it('① users 页必须读 route.query.task（此前连 useRoute 都没有）', async () => {
    const src = await read(USERS_VIEW)
    expect(src).toContain('useRoute')
    expect(src).toContain('taskFilterFromQuery(route.query)')
    expect(src).toMatch(/const taskFilter = ref\(taskFilterFromQuery\(route\.query\)\)/)
  })

  it('② 任务过滤必须真的作用在 Top5 任务行上，且过滤可见、可清', async () => {
    const src = await read(USERS_VIEW)
    // 过滤：命中任务名才留行；无 task 参数 ⇒ 原样返回（行为与从前完全一致）
    expect(src).toMatch(/visibleTaskRows[\s\S]*?taskFilter\.value \? taskRows\.value\.filter\([\s\S]*?r\.task === taskFilter\.value[\s\S]*?: taskRows\.value/)
    // 过滤后的行喂给组件
    expect(src).toContain(':task-rows="visibleTaskRows"')
    // 可见（chip）+ 可清（同时摘掉 URL 里的 ?task=）
    expect(src).toContain('data-testid="sa-task-filter"')
    expect(src).toContain('data-testid="sa-task-filter-clear"')
    expect(src).toMatch(/function clearTaskFilter[\s\S]*?delete next\.task/)
  })

  it('③ Top5 任务行的跳转确实带 ?task=（链路另一端存在）', async () => {
    const src = await read(TOP5)
    expect(src).toMatch(/query:\s*\{\s*task,\s*start_date: props\.startDate, end_date: props\.endDate\s*\}/)
  })
})

describe('P2-2 Top5 四维并发：请求序号 + 失败维度显式呈现', () => {
  it('① 四维加载必须有请求序号，旧响应必须被丢弃', async () => {
    const code = stripComments(await read(USERS_VIEW))
    expect(code).toMatch(/let seriesToken = 0/)
    expect(code).toMatch(/const token = \+\+seriesToken/)
    // await 之后比对，不一致就整轮丢弃（不写任何状态）
    expect(code).toMatch(/if \(token !== seriesToken\) return/)
  })

  it('② 失败维度必须显式记账（不得与「无结果」混淆）', async () => {
    const code = stripComments(await read(USERS_VIEW))
    expect(code).toContain('top5DimensionErrors')
    expect(code).toMatch(/if \(models\.status === 'rejected'\) dimensionErrors\.models = surfaceError\(models\.reason\)/)
    expect(code).toMatch(/if \(providers\.status === 'rejected'\) dimensionErrors\.providers = surfaceError\(providers\.reason\)/)
    expect(code).toMatch(/if \(users\.status === 'rejected'\) dimensionErrors\.users = surfaceError\(users\.reason\)/)
    expect(code).toMatch(/if \(tasks\.status === 'rejected'\) dimensionErrors\.tasks = surfaceError\(tasks\.reason\)/)
    // 失败维度的行仍为空（没有旧数据可展示），但**必须同时**有失败文案
    // ⇒ 组件据此渲染失败横幅，而不是该维度的「无结果」空态。
    expect(code).toMatch(/modelRows\.value = models\.status === 'fulfilled' \? models\.value\.models \|\| \[\] : \[\]/)
  })

  it('③ 组件必须逐维度渲染「加载失败」，而不是该维度的空态', async () => {
    const src = await read(TOP5)
    for (const dim of ['models', 'providers', 'users', 'tasks']) {
      expect(src, `Top5 必须有 ${dim} 维度的失败出口`).toContain(`data-testid="sa-top5-${dim}-error"`)
    }
    expect(src).toContain('dimensionErrors')
  })
})

describe('P2-5 三个排行页必须读 URL 的 start_date / end_date', () => {
  for (const rel of RANKING_VIEWS) {
    it(`① ${rel.split('/').pop()}：优先读 URL，缺省回退 7 天窗口`, async () => {
      const src = await read(rel)
      expect(src).toContain('useRoute')
      expect(src).toContain('rangeFromQuery(route.query) ?? rangeFromSpanDays(7)')
      // 不再是写死的 7 天窗口
      expect(src).not.toMatch(/const range = ref<KxDateRange>\(rangeFromSpanDays\(7\)\)/)
    })
  }
})
