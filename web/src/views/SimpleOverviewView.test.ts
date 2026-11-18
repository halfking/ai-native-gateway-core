import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readSource(rel: string): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web', rel),
    resolve(process.cwd(), rel),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error(`${rel} not found`)
}

describe('simple mode overview view contract', () => {
  it('is the simple mode home and the old access-list view is gone', async () => {
    const home = await readSource('src/views/HomeView.vue')
    expect(home).toContain("import SimpleOverviewView from './SimpleOverviewView.vue'")
    expect(home).toContain('<SimpleOverviewView v-if="isLoggedIn && isSimple" />')
    expect(home).not.toContain('SimpleAccessListView')
    await expect(readSource('src/views/SimpleAccessListView.vue')).rejects.toThrow()
  })

  it('answers the three beginner questions in one screen', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    // ① 能不能用 → 状态条
    expect(src).toContain('data-testid="overview-status"')
    expect(src).toContain('status-dot')
    // ② 今天跑了多少 → 统计卡
    expect(src).toContain('data-testid="overview-stats"')
    expect(src).toContain('<StatCard')
    expect(src).toContain('<StatsRow')
    // ③ 该做什么 → 常用操作
    expect(src).toContain('data-testid="overview-quick-actions"')
    // ④ 我的客户端 / 上游
    expect(src).toContain('data-testid="overview-access-list"')
    expect(src).toContain('data-testid="overview-provider-health"')
  })

  it('loads four sources instead of fanning out one request per key', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('fetchDashboardBoard')
    expect(src).toContain('getProviders()')
    expect(src).toContain('getKeys()')
    // ★ 2026-11-18：趋势卡加了一路 days=7（与专家面看板 7d 预设同端点同口径）
    expect(src).toContain("fetchDashboardBoard({ days: 7 })")
    // ★ 旧实现对每个密钥发一次 /api/usage/{id}/models（线上 208 个密钥 ⇒ 208 个请求）
    expect(src).not.toContain('getKeyUsageByModel')
    expect(src).not.toMatch(/keys\.value\.map\(/)
  })

  // ── 2026-11-18：趋势卡与「消耗情况」契约 ──────────────────────────────────
  //
  // ★ owner 报障「关心的是整体状况、消耗情况」—— 只有今日一个口径看不出走向。
  //   修法＝与专家面看板 days=7 同端点，15min 桶在前端按 UTC 日聚合。
  it('★ 近 7 日消耗卡必须存在且降级诚实（零线 ≠ 没流量被读成零消耗）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('data-testid="overview-trend"')
    // 聚合/补零/截窗全部在纯函数层（可单测），视图不现算
    expect(src).toContain('aggregateTrendByDay')
    expect(src).toContain('fillMissingDays')
    expect(src).toContain('lastDailyPoints')
    // 降级路径：请求失败 / degraded_trends / summary 降级 三者都要拦
    expect(src).toContain('board7Failed')
    expect(src).toContain('degraded_trends')
    expect(src).toContain('data-testid="overview-trend-unavailable"')
    // 复用专家面看板的指标文案键（不新增一份同义词）
    expect(src).toContain("t('dashboard.board.trendRequests')")
    expect(src).toContain("t('dashboard.board.trendTokens')")
    expect(src).toContain("t('dashboard.board.trendCost')")
  })

  // ── 2026-11-18：系统状态（运维 chips）与 issue 行内动作 ────────────────────
  it('★ 系统状态复用专家面 BoardOpsBar（同一口径同一交互，不另造一套）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('BoardOpsBar')
    expect(src).toContain('data-testid="overview-ops"')
    expect(src).toContain("router.push('/dashboard?tab=selfcheck')")
  })

  it('★ 每条 issue 的「去处理」落点都必须有真实路由（死链 = 点了就空白）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    const routerSrc = await readSource('src/router.ts')

    const registered = new Set<string>()
    for (const m of routerSrc.matchAll(/path:\s*'([^']+)'/g)) registered.add(m[1])

    const targets = new Set<string>()
    // ① router.push / go 的直接字面量（原有口径）
    for (const m of src.matchAll(/router\.push\('([^']+)'\)|go\('([^']+)'\)/g)) {
      const hit = m[1] ?? m[2]
      if (hit) targets.add(hit)
    }
    // ② ISSUE_ACTION_ROUTES 映射表里的字面量（2026-11-18：issue 行内动作）
    const mapBlock = src.slice(src.indexOf('ISSUE_ACTION_ROUTES'), src.indexOf('function issueRoute'))
    for (const m of mapBlock.matchAll(/'\/[^']+'/g)) targets.add(m[0].slice(1, -1))

    expect(targets.size, '总览页应至少有一个跳转落点').toBeGreaterThan(0)

    // 带 query 的落点（如 /dashboard?tab=selfcheck）按 path 段判注册
    const dead = [...targets].filter((t) => !registered.has(t.split('?')[0]))
    expect(
      dead,
      `总览页跳到了未注册的路由（点了就是空白页）：\n${dead.join('\n')}`,
    ).toEqual([])
  })

  it('★ 成功率卡必须带失败笔数语境（比率没有分母 = 初级用户读不懂）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('statFailures')
    expect(src).toContain('stat.key === \'successRate\' && stat.failures')
    // 明细来源与专家面英雄区同口径（pies 优先、summary 兜底）
    expect(src).toContain('errorsPie')
    const model = await readSource('src/composables/simpleOverviewModel.ts')
    expect(model).toContain('deriveFailureCount')
  })

  it('★ 最常用模型必须带今日花费列（消耗情况：模型排行不能只有请求数）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('model-cost')
    expect(src).toContain('fmtUsd(m.cost_usd)')
    expect(src).toContain('topModelsHint')
  })

  it('degrades each source independently', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('Promise.allSettled')
    expect(src).toContain('boardFailed')
    expect(src).toContain('providersFailed')
    expect(src).toContain('keysFailed')
    // 权限不足与服务异常分开说
    expect(src).toContain('providersDenied')
    expect(src).toContain('providerDenied')
    // 原始 error.message 不直接上屏
    expect(src).toContain('surfaceError')
    expect(src).not.toMatch(/error\.value = e instanceof Error \? e\.message/)
  })

  it('keeps the quick action zone within the action density budget', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    const block = src.slice(src.indexOf('data-testid="overview-quick-actions"'))
    const zone = block.slice(0, block.indexOf('</section>'))
    const buttons = zone.match(/<button/g)?.length ?? 0
    // design-action-density: 一个动作区最多 4 个可见动作
    expect(buttons).toBeGreaterThan(0)
    expect(buttons).toBeLessThanOrEqual(4)
  })

  it('never hardcodes Chinese in the view', async () => {
    // ★ 用**官方计数器**判，不自己重写一遍规则（同一口径只能有一份实现）：
    //   hardcodedCjk.countFile 会按 parser 剥掉 HTML 注释与脚本注释，
    //   自己写正则必然与门禁口径漂移（这正是本仓吃过亏的地方）。
    const { countFile } = await import('../i18n/hardcodedCjk')
    const abs = resolve(process.cwd(), 'web/src/views/SimpleOverviewView.vue')
    const fallback = resolve(process.cwd(), 'src/views/SimpleOverviewView.vue')
    const count = await (async () => {
      try {
        return countFile(abs)
      } catch {
        return countFile(fallback)
      }
    })()
    expect(count).toBe(0)
  })

  it('keeps the folded inactive keys visible with a count', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('data-testid="overview-access-fold"')
    expect(src).toContain('inactiveRows.length')
    expect(src).toContain('count-badge')
  })

  // ── 2026-11-17：抓「接入清单太长」的那条 ──────────────────────────────────
  //
  // ★ 事故：接入清单把**在用密钥整表铺开**（线上 118 行在用 + 91 行折叠）⇒
  //   表格比页面还长，「初级用户一眼看明白」在这一块直接失败。
  // ⇒ 改成「最近在用」+「用得最多」两个**分页**列表，每页 10 条。
  it('★ 接入清单必须是两个分页列表（整表铺开 = 一眼看不明白）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('data-testid="overview-client-recent"')
    expect(src).toContain('data-testid="overview-client-busiest"')
    expect(src).toContain('CLIENT_PAGE_SIZE = 10')
    // 分页栏走仓库既有资产，不自己造轮子
    expect(src).toContain('PaginationBar')
    // ★ 整表铺开不许回来（这两个 v-for 就是原来那两处）
    expect(src).not.toContain('v-for="r in activeRows"')
    expect(src).not.toContain('v-for="r in inactiveRows"')
    // 排序与分页都在纯函数里（可单测），视图只画表
    expect(src).toContain('sortClientRows')
    expect(src).toContain('paginateClientRows')
  })

  // ── 2026-11-17：抓「上游健康明细被静默截断」的那条 ──────────────────────
  //
  // ★ 事故：上游健康明细按严重度排序、只留前 12 行 ⇒ 被切掉的几乎全是「可用」的
  //   （线上实测：徽标写「19 个上游可用」，列表里只有 3 行标着「可用」）。
  //   截断本身没披露 ⇒ 用户看到 19 与 3 对不上，只能判定「这页数据坏了」。
  //   ★ 与本仓 11-16 审计轮的 F3（模型候选静默截断 ⇒ 上限自带披露）同型。
  it('★ 上游健康明细被截断时必须披露（不披露 = 用户以为数据对不上）', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('providerHealth.hidden')
    expect(src).toContain('data-testid="overview-provider-truncated"')
    expect(src).toContain('simpleMode.overview.providerMore')
    // ★ 明细截断数必须由模型层算出来（不能在页面里现算一个跟列表口径不一致的数）
    const model = await readSource('src/composables/simpleOverviewModel.ts')
    expect(model).toMatch(/hidden:\s*providers\.length\s*-\s*visibleRows\.length/)
  })
})

describe('simple mode providers view contract', () => {
  it('uses localizable strings for every referenced common key', async () => {
    const src = await readSource('src/views/SimpleProvidersView.vue')
    expect(src).toContain("t('common.status.healthy')")
    expect(src).toContain("t('common.status.warning')")
    expect(src).toContain("t('common.status.unknown')")
    expect(src).toContain("t('common.credentials')")
    expect(src).toContain("t('common.autoFilled')")
    expect(src).toContain("t('common.protocol')")
    // 不再有硬编码中文标签
    expect(src).not.toContain('主凭据')
    expect(src).toContain("t('simpleMode.providers.primaryCredentialLabel'")
  })

  it('routes error copy through the localization exit', async () => {
    const src = await readSource('src/views/SimpleProvidersView.vue')
    expect(src).toContain('surfaceError')
    expect(src).not.toMatch(/ElMessage\.error\(msg\)/)
  })
})

describe('simple mode keys view contract', () => {
  it('sends the user back to a route that exists', async () => {
    const src = await readSource('src/views/SimpleKeysView.vue')
    // '/simple/access-list' 从来没有注册过 ⇒ 「返回」落到空路由
    expect(src).not.toContain("router.push('/simple/access-list')")
    expect(src).toContain("router.push('/dashboard')")
  })
})
