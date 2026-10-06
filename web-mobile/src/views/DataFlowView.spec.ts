import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataFlowView from './DataFlowView.vue'
import {
  fetchDataLifecycleStats,
  fetchDataLifecycleMetrics,
  fetchLifecycleJobs,
  fetchBlobTop,
  LIFECYCLE_JOBS_HISTORY_LIMIT,
} from '@/api/dataLifecycleStats'
import { setLocale, locale } from '@/i18n'
import type { DataLifecycleStats } from '@/api/dataLifecycleStats'

/**
 * DataFlowView 的不变量（2026-10-08，第六十七批）。
 *
 * ★ 钉住的九处「不能都渲染成同一个东西」（全部来自第六十六批的后端取证）：
 *   ① ★★★★ **`metrics` 是全表口径**（data_lifecycle_metrics.go:63 无 WHERE，
 *      注册却是 admin(...)）⇒ 必须挂「整表」横幅。
 *   ② ★★★★ **`stats.total_rows` 租户口径 / `total_size_bytes` 全表口径**
 *      （data_lifecycle.go:75-76）⇒ 两行各挂标签，不能并排成「本租户 X 行 / Y 字节」。
 *   ③ ★★★★ **四段可为 null** ⇒ 显示「查不出来」，不是「0 行」。
 *   ④ ★★★★ **by_tenant / growth_trend 的空数组是二义的**（查询失败非致命）
 *      ⇒ 措辞不能说「无数据」。
 *   ⑤ ★★★ **`growth_trend` 新的一天在前** ⇒ 列表要反转成时间正序。
 *   ⑥ ★★★ **`jobs` 的 limit 后端硬编码 50** ⇒ 页面**不给条数选择器**。
 *   ⑦ ★★★ **`blobs/top` 的 total_bytes 是 N 行合计** ⇒ 必须带 N。
 *   ⑧ ★★★★ **`last_cleanup_at`/`last_archive_at` 恒不存在**
 *      ⇒ 说「本端点不提供」，**绝不能**说「从未清理过」。
 *   ⑨ ★★★ **段/租户 size_bytes 是摊派估算** ⇒ 带「摊派估算」字样。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/dataLifecycleStats', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dataLifecycleStats')>()
  return {
    ...actual,
    fetchDataLifecycleStats: vi.fn(),
    fetchDataLifecycleMetrics: vi.fn(),
    fetchLifecycleJobs: vi.fn(),
    fetchBlobTop: vi.fn(),
  }
})

const statsMock = fetchDataLifecycleStats as unknown as ReturnType<typeof vi.fn>
const metricsMock = fetchDataLifecycleMetrics as unknown as ReturnType<typeof vi.fn>
const jobsMock = fetchLifecycleJobs as unknown as ReturnType<typeof vi.fn>
const blobsMock = fetchBlobTop as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

beforeEach(() => {
  statsMock.mockReset()
  metricsMock.mockReset()
  jobsMock.mockReset()
  blobsMock.mockReset()
  statsMock.mockResolvedValue(statsFixture())
  metricsMock.mockResolvedValue(metricsFixture())
  jobsMock.mockResolvedValue({ running: [], history: [] })
  blobsMock.mockResolvedValue(blobTopFixture())
  setLocale('zh-CN')
})

afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  vi.restoreAllMocks()
})

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(DataFlowView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

/** 0=stats 1=metrics 2=jobs 3=blobs（与模板里的 section 顺序一致）。 */
function sectionOf(w: W, index: number) {
  return w.findAll('.df__section')[index]!
}
function loadBtn(w: W, n: number) {
  return w.findAll('.df__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

/**
 * `<dl>` 里按 `<dt>` 精确文本取对应 `<dd>`。
 *
 * ★ 一区里可能有**多个** `.df__kv`（metrics 区就有「总量」与「维护」两个 dl），
 *   所以要在全部 dl 里找，不能只取第一个 —— 只取第一个会静默漏掉后半段。
 */
function ddByLabel(w: W, index: number, label: string): string {
  const dls = sectionOf(w, index).findAll('.df__kv')
  if (dls.length === 0) throw new Error(`第 ${index} 区没有 .df__kv`)
  for (const dl of dls) {
    const children = dl.element.children
    for (let i = 0; i < children.length - 1; i += 1) {
      if (children[i]!.tagName === 'DT' && children[i]!.textContent === label) {
        return children[i + 1]!.textContent ?? ''
      }
    }
  }
  throw new Error(`未找到标签为「${label}」的行（本区共 ${dls.length} 个 dl）`)
}

/* ── 夹具：逐字照抄后端 struct 与 SQL 语义 ─────────────────────────── */

// 返回类型显式标注：夹具里 cold_data 是 null，不标注的话 TS 会把它推成
// `null` 字面量类型，后面「补上非 null 段」的用例就赋不进去了。
function statsFixture(): DataLifecycleStats {
  return {
    total_rows: 1000,
    total_size_bytes: 1073741824,
    total_size_human: '1024 MB',
    hot_data: { rows: 500, size_bytes: 536870912, size_human: '512 MB', days: 7, percent_of_total: 50 },
    warm_data: { rows: 300, size_bytes: 322122547, size_human: '307 MB', days: 23, percent_of_total: 30 },
    // ★ cold_data 被 warnRowSkip 跳过 ⇒ 整段是 null
    cold_data: null,
    expired_data: { rows: 50, size_bytes: 53687091, size_human: '51 MB', days: 999, percent_of_total: 5 },
    by_tenant: [{ tenant_id: 'acme', rows: 700, size_bytes: 751619276, size_human: '717 MB' }],
    // ★ 后端 ORDER BY day DESC ⇒ 新的一天在前
    growth_trend: [
      { date: '2026-10-08', requests: 120, compressed: 60, compression_rate: 50 },
      { date: '2026-10-07', requests: 90, compressed: 45, compression_rate: 50 },
    ],
  }
}

/** ★ metrics 永远没有 last_cleanup_at / last_archive_at（后端从不赋值）。 */
function metricsFixture() {
  return {
    total_rows: 50000,
    total_size_bytes: 21474836480,
    hot_data_rows: 20000,
    hot_data_size_bytes: 8589934592,
    warm_data_rows: 15000,
    warm_data_size_bytes: 6442450944,
    cold_data_rows: 10000,
    cold_data_size_bytes: 4294967296,
    expired_data_rows: 5000,
    expired_data_size_bytes: 2147483648,
  }
}

function blobTopFixture() {
  return {
    rows: [
      {
        request_id: 'req-0001', session_key: 'sess-a', tenant_id: 'acme',
        occurred_at: '2026-10-08T01:30:00Z',
        request_body_bytes: 120000, outbound_body_bytes: 80000,
        total_bytes: 200000, total_human: '195 KB', model: 'claude-opus-5',
      },
      {
        // ★ session_key/tenant_id 是 COALESCE 兜底的空串；model 因 omitempty 键不存在
        request_id: 'req-0002', session_key: '', tenant_id: '',
        occurred_at: '2026-10-08T00:10:00Z',
        request_body_bytes: 50000, outbound_body_bytes: 5000,
        total_bytes: 55000, total_human: '54 KB',
      },
    ],
    total_bytes: 255000,
    total_human: '249 KB',
    collected_at: '2026-10-08T01:30:00.123456789Z',
  }
}

/* ═══════════════════════════════════════════════════════════════════════
 * ①② 分段统计：两行口径不同，必须各挂标签
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DataFlowView / 分段统计', () => {
  it('点击加载后才拉 stats（不是进页面就拉）', async () => {
    const w = await mountView()
    expect(statsMock).not.toHaveBeenCalled()
    await clickLoad(w, 0)
    expect(statsMock).toHaveBeenCalledTimes(1)
  })

  it('② total_rows 行带「租户口径」标签', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 0, '记录总数（租户口径）')).toContain('租户口径')
  })

  it('② total_size_bytes 行带「全表口径」标签（pg_total_relation_size 不过滤租户）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 0, '表物理大小（全表口径）')).toContain('全表口径')
  })

  it('② 两种口径不同这件事必须明写出来，不能让两行看起来同源', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.df__note').text()).toContain('口径不同')
  })

  it('★ stats 报错时把上一份数据清掉（不能留旧值冒充新值）', async () => {
    const w = await mountView()
    // 先成功一次 ⇒ 页面已经有旧数据
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.df__kv').exists()).toBe(true)

    // 再让它失败 ⇒ 旧数据必须消失，否则用户会把上一份读数当成这次的
    statsMock.mockRejectedValue(new Error('boom'))
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.df__msg--err').exists()).toBe(true)
    expect(sectionOf(w, 0).find('.df__kv').exists()).toBe(false)
  })

  it('stats 首次加载即失败时显示错误（负控）', async () => {
    statsMock.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.df__msg--err').exists()).toBe(true)
  })

  it('stats 403 显示无权限而不是后端原文', async () => {
    statsMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).find('.df__msg--err').text()).toContain('没有权限')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③④⑨ 分段：null / 摊派估算 / 二义空数组
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DataFlowView / ③ 四段为 null 显示「查不出来」', () => {
  it('③ null 段渲染成「这一段查不出来」，绝不能是「0 行」', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const rows = sectionOf(w, 0).findAll('.df__row')
    const cold = rows.find((r) => r.find('.df__rowLabel').text() === '冷数据')!
    expect(cold.find('.df__rowMain--unknown').exists()).toBe(true)
    expect(cold.text()).toContain('这一段查不出来')
    expect(cold.text()).not.toContain('0 行')
  })

  it('③ 非 null 段不会误挂「查不出来」的 class', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const rows = sectionOf(w, 0).findAll('.df__row')
    const hot = rows.find((r) => r.find('.df__rowLabel').text() === '热数据')!
    expect(hot.find('.df__rowMain--unknown').exists()).toBe(false)
    expect(hot.text()).toContain('500')
  })

  it('③ 有段缺失时给出说明，并说明占比合计算不出来', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('有分段查不出来')
    expect(sectionOf(w, 0).text()).toContain('占比合计算不出来')
  })

  it('③ 四段齐全时占比合计照常给出（负控）', async () => {
    const f = statsFixture()
    f.cold_data = { rows: 150, size_bytes: 1, size_human: '1 kB', days: 60, percent_of_total: 15 }
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('四段占比合计 100.0%')
  })

  it('★ days 标注写死的 7/23/60/999，而不是区间上界（expired 显 999）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const rows = sectionOf(w, 0).findAll('.df__row')
    const expired = rows.find((r) => r.find('.df__rowLabel').text() === '过期数据')!
    expect(expired.text()).toContain('口径 999 天')
  })

  it('⑨ 段与租户的 size 都标「摊派估算」（不是实测占用）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const text = sectionOf(w, 0).text()
    expect(text).toContain('摊派估算 512 MB')
    expect(text).toContain('摊派估算 717 MB')
  })

  it('★ 四段之和超过总量时提示「边界重复计数」，并说清不是数据出错', async () => {
    const f = statsFixture()
    // ⚠️ 必须先把 cold_data 补成非 null：`segmentRowsDisagree` 在有段缺失时
    //   直接返回 false（缺段不代表多算了），不补齐就测不到这条判据。
    f.cold_data = { rows: 150, size_bytes: 1, size_human: '1 kB', days: 60, percent_of_total: 15 }
    f.total_rows = 999
    f.hot_data!.rows = 2000 // 2000+300+150+50 = 2500 > 999
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('四段行数加起来超过了总量')
    expect(sectionOf(w, 0).text()).toContain('不是数据出错')
  })

  it('★ total_rows=0 时占比说「没有意义」，不渲染 0%', async () => {
    const f = statsFixture()
    f.total_rows = 0
    // 这三段在夹具里确定非 null（cold_data 才是 null），用 `!` 收窄
    for (const k of ['hot_data', 'warm_data', 'expired_data'] as const) {
      f[k]!.percent_of_total = 0
    }
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    const hot = sectionOf(w, 0).findAll('.df__row').find((r) => r.find('.df__rowLabel').text() === '热数据')!
    expect(hot.text()).toContain('没有意义')
    expect(hot.text()).not.toContain('0.0%')
  })

  it('④ by_tenant 为空数组 ⇒ 措辞是「没有可展示的记录」且点明可能是查询被跳过', async () => {
    const f = statsFixture()
    f.by_tenant = []
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('也可能是这一段查询被跳过了')
    expect(sectionOf(w, 0).text()).not.toContain('暂无数据')
  })

  it('④ by_tenant 非空时不挂二义措辞（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).not.toContain('也可能是这一段查询被跳过了')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑤ growth_trend 反转
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DataFlowView / ⑤ 趋势必须反成时间正序', () => {
  it('后端新的一天在前 ⇒ 渲染出来最早的一天在最前', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const labels = sectionOf(w, 0)
      .findAll('.df__row')
      .map((r) => r.find('.df__rowLabel').text())
    expect(labels.indexOf('2026-10-07')).toBeLessThan(labels.indexOf('2026-10-08'))
  })

  it('★ 反转若被去掉，最新的一天会排在最前（这正是要防的）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const labels = sectionOf(w, 0)
      .findAll('.df__row')
      .map((r) => r.find('.df__rowLabel').text())
    expect(labels.indexOf('2026-10-08')).toBeGreaterThan(labels.indexOf('2026-10-07'))
  })

  it('趋势区说明了「已改成时间正序」', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('已改成时间正序')
  })

  it('★ compression_rate 被后端夹到 100 时显示「已顶到 100%」而不是 100%', async () => {
    const f = statsFixture()
    f.growth_trend = [{ date: '2026-10-08', requests: 10, compressed: 14, compression_rate: 100 }]
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('已顶到 100%')
  })

  it('普通压缩率照常显示百分数（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    const d = sectionOf(w, 0).findAll('.df__row').find((r) => r.find('.df__rowLabel').text() === '2026-10-07')!
    expect(d.text()).toContain('50.0%')
  })

  it('requests=0 的那天说「无请求」，不渲染 0%', async () => {
    const f = statsFixture()
    f.growth_trend = [{ date: '2026-10-08', requests: 0, compressed: 0, compression_rate: 0 }]
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('当日无请求')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ①⑧ metrics：全表口径横幅 + 清理时间「本端点不提供」
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DataFlowView / ①⑧ metrics', () => {
  it('① metrics 区挂「整表」横幅，说明任何角色看到的都不是本租户', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).find('.df__banner').text()).toContain('整张表')
  })

  it('① stats 区**不能**出现同一块横幅（否则两区会被当成同口径）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    await clickLoad(w, 1)
    expect(sectionOf(w, 0).find('.df__banner').exists()).toBe(false)
  })

  it('⑧ 清理时间显示「本端点不提供」，绝不显示「从未清理」', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(ddByLabel(w, 1, '上次清理')).toContain('本端点不提供')
    expect(ddByLabel(w, 1, '上次归档')).toContain('本端点不提供')
    // ⚠️ 只对**值单元格**断言：下方说明文案里出现「从未清理」是**反例语境**
    //   （“不能说从未清理”），拿整区文本做 not.toContain 会误判。
    expect(ddByLabel(w, 1, '上次清理')).not.toContain('从未清理')
    expect(ddByLabel(w, 1, '上次归档')).not.toContain('从未清理')
  })

  it('⑧ 并解释「不能说从未清理」的理由（键恒不存在 ≠ 没发生过）', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('无论清理或归档是否真的跑过')
  })

  it('⑧ 若后端将来真填上时间，就照实显示而不是还说「不提供」', async () => {
    metricsMock.mockResolvedValue({ ...metricsFixture(), last_cleanup_at: '2026-10-08T01:00:00Z' })
    const w = await mountView()
    await clickLoad(w, 1)
    const cell = ddByLabel(w, 1, '上次清理')
    expect(cell).toContain('2026-10-08T01:00:00Z')
    expect(cell).not.toContain('本端点不提供')
  })

  it('metrics 四段行数渲染出来', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const rows = sectionOf(w, 1).findAll('.df__row')
    const hot = rows.find((r) => r.find('.df__rowLabel').text() === '热数据')!
    expect(hot.text()).toContain('20,000')
  })

  it('metrics 四段之和超过总量时给出说明（30 天边界重复计数）', async () => {
    metricsMock.mockResolvedValue({ ...metricsFixture(), total_rows: 49999 })
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('四段行数之和超过总量')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑥ jobs：不给条数选择器 + 三态互斥
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DataFlowView / ⑥ 异步任务', () => {
  it('⑥ 页面没有条数选择器（后端硬编码 50，给了会让人以为生效）', async () => {
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).find('select').exists()).toBe(false)
    expect(sectionOf(w, 2).find('input').exists()).toBe(false)
  })

  it('⑥ 明写上限 50 是后端写死的', async () => {
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain(`历史最多保留 ${LIFECYCLE_JOBS_HISTORY_LIMIT} 条`)
    expect(sectionOf(w, 2).text()).toContain('传了也不会生效')
  })

  it('fetchLifecycleJobs 不带任何 query（页面上没有可传的条数）', async () => {
    const w = await mountView()
    await clickLoad(w, 2)
    expect(jobsMock).toHaveBeenCalledTimes(1)
    expect(jobsMock.mock.calls[0]![0]).toBeUndefined()
  })

  it('运行中任务没有 finished_at ⇒ 标「进行中」而不是空白', async () => {
    jobsMock.mockResolvedValue({
      running: [{
        run_id: 'job-1', op: 'promote_hot', status: 'running',
        progress: { done: 120, total: 600, percent: 20, batches: 6 },
        duration_ms: 0,
      }],
      history: [],
    })
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('进行中')
    expect(sectionOf(w, 2).text()).toContain('120 / 600')
  })

  it('★ progress.total=0 ⇒ 进度显示「未知」，不是「已处理 0 / 0」', async () => {
    jobsMock.mockResolvedValue({
      running: [{
        run_id: 'job-1', op: 'vacuum', status: 'running',
        progress: { done: 0, total: 0, percent: 0, batches: 0 },
        duration_ms: 0,
      }],
      history: [],
    })
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('进度未知')
    expect(sectionOf(w, 2).text()).not.toContain('0 / 0')
  })

  it('失败任务带出后端的 error 原文，并计入失败计数', async () => {
    jobsMock.mockResolvedValue({
      running: [],
      history: [
        { run_id: 'j1', op: 'vacuum', status: 'failed', error: 'panic: boom', finished_at: '2026-10-07T10:00:42Z', duration_ms: 42000 },
        { run_id: 'j2', op: 'reindex', status: 'succeeded', finished_at: '2026-10-07T11:00:00Z', duration_ms: 900 },
      ],
    })
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('panic: boom')
    expect(sectionOf(w, 2).text()).toContain('其中 1 条失败')
  })

  it('running: null（后端 nil 切片）不炸，显示「当前没有进行中的任务」', async () => {
    jobsMock.mockResolvedValue({ running: null, history: [] })
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('当前没有进行中的任务')
  })

  it('两个列表都空时不报错', async () => {
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('没有历史记录')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑦ blobs：N 行合计 + 空串/缺键的兜底措辞
 * ═══════════════════════════════════════════════════════════════════════ */

describe('DataFlowView / ⑦ 大字段 Top-N', () => {
  it('⑦ total 措辞必须带 N 并声明不是全表总量', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    const banner = sectionOf(w, 3).find('.df__banner').text()
    expect(banner).toContain('2 行')
    expect(banner).toContain('249 KB')
    expect(banner).toContain('不是全表总量')
  })

  it('★ 取满 limit 时提示可能还有更大的没进榜', async () => {
    const f = blobTopFixture()
    f.rows = Array.from({ length: 20 }, (_, i) => ({ ...f.rows[0]!, request_id: `r${i}` }))
    blobsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 3)
    expect(sectionOf(w, 3).text()).toContain('可能还有更大的没进榜')
  })

  it('未取满时不挂截断提示（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    expect(sectionOf(w, 3).text()).not.toContain('可能还有更大的没进榜')
  })

  it('★ model 缺键 ⇒ 显示「模型未知」，不显示 undefined', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    const row = sectionOf(w, 3).findAll('.df__row').find((r) => r.text().includes('req-0002'))!
    expect(row.text()).toContain('模型未知')
    expect(row.text()).not.toContain('undefined')
  })

  it('★ session_key/tenant_id 是空串 ⇒ 显示「无会话」「无租户」', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    const row = sectionOf(w, 3).findAll('.df__row').find((r) => r.text().includes('req-0002'))!
    expect(row.text()).toContain('无会话')
    expect(row.text()).toContain('无租户')
  })

  it('非空时原样透传（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    const row = sectionOf(w, 3).findAll('.df__row').find((r) => r.text().includes('req-0001'))!
    expect(row.text()).toContain('sess-a')
    expect(row.text()).toContain('claude-opus-5')
    expect(row.text()).not.toContain('模型未知')
  })

  it('★ 两个 body 分量都是 0 ⇒ 显示「没有关联的请求/响应体」', async () => {
    const f = blobTopFixture()
    f.rows.push({
      request_id: 'req-0003', session_key: 'sess-b', tenant_id: 'acme',
      occurred_at: '2026-10-07T23:00:00Z',
      request_body_bytes: 0, outbound_body_bytes: 0,
      total_bytes: 0, total_human: '0 B',
    })
    blobsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 3)
    const row = sectionOf(w, 3).findAll('.df__row').find((r) => r.text().includes('req-0003'))!
    expect(row.text()).toContain('没有关联的请求/响应体')
  })

  it('条数选择只给后端认可的合法值，且改值会重新拉取', async () => {
    const w = await mountView()
    await clickLoad(w, 3)
    const select = sectionOf(w, 3).find('select')
    expect(select.exists()).toBe(true)
    const values = select.findAll('option').map((o) => Number(o.element.value))
    expect(values).toEqual([20, 50, 100, 200])
    await select.setValue('50')
    await flushPromises()
    await flushPromises()
    expect(blobsMock.mock.calls[1]![0]).toEqual({ limit: 50 })
  })

  it('rows 为空 ⇒ 明确说没查到，不显示 undefined', async () => {
    blobsMock.mockResolvedValue({ ...blobTopFixture(), rows: [], total_bytes: 0, total_human: '0 B' })
    const w = await mountView()
    await clickLoad(w, 3)
    expect(sectionOf(w, 3).text()).toContain('没有查到带大字段的记录')
  })
})