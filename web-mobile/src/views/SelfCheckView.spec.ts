import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import SelfCheckView from './SelfCheckView.vue'
import { useAuthStore } from '@/stores/auth'
import { setLocale } from '@/i18n'
import {
  fetchSelfCheckRuns,
  fetchSelfCheckSettings,
  fetchSelfCheckTriggerAvailability,
  fetchSelfCheckStats,
  fetchSelfCheckModels,
} from '@/api/selfCheck'

/**
 * 系统自检面回归护栏（2026-10-08，第一百零六批）。
 *
 * ★★ 本批第一件事是**门控**，因为 `self_check_runs` 表
 *    `tenant_id text NOT NULL DEFAULT 'default'`（`01-schema.sql` 建表第 19 行）
 *    **就在表里**，而六个 handler **一个都不引用**：
 *      `handleListRuns` `WHERE 1=1` + 仅 model/status（`:126-150`）
 *      `handleGetRun`  `WHERE id=$1`（`:223`）
 *      `handleStats`  三处 `WHERE started_at >= $1`
 *      `handleModels` `GROUP BY model_name`
 *    注册全是 `admin(...)` ⇒ **tenant_admin 能读所有租户的自检记录**。
 *    ⇒ 本仓**第四次**「不隔离 + admin 档」；与前三次不同的是
 *      **列就在表里，只是查询从不引用它** —— 不是「表没有租户概念」。
 *
 * 判据钉七组不变量：
 *  ① ★★★ **`probe_system` 的绿灯可能是查询失败**（`:941`/`:949` 两个查询的错
 *      都被 `_ =` 丢弃 ⇒ 全零 ⇒ `queue_ready_unclaimable==0 && last_activity==nil`
 *      ⇒ `healthy=true`）。而这个区块存在的理由（`:926-931`）恰恰是
 *      「页面绿灯但探测管线已死」（glm-5.2 事故）—— **它的查询失败恰好产出绿灯**。
 *  ② ★★ `summary` 失败时是全零 + `success_rate: 0.0` ⇒ 与「全失败」同值。
 *  ③ ★★ `error_breakdown` / `trend` 失败时是 `[]` ⇒ 与「没有记录」同形。
 *  ④ ★★ `success + partial + failed` **可能小于** `total_runs`（status 五值，
 *      统计只 FILTER 三个）⇒ 拿三项相加当分母会算错。
 *  ⑤ ★★ `range` 回显的是**请求原值**不是生效窗口；未知值静默落 24h。
 *  ⑥ ★★★ `upstream_latency_ms` 是 **`int` + omitempty** ⇒ **0 毫秒是键缺失**。
 *      ★ 对照第一百零五批的 `*int`（那里 0 会**出现**）—— 同家族、**相反方向**。
 *      ⚠️ 第一版多写了一条「后端若发 0 也要显示 0」的用例，**已删**：
 *      `!== undefined` 与 `!` 只在 `upstream_latency_ms === 0` 上不同，
 *      而那个值被 `omitempty` 吃掉、**后端产生不出来**
 *      ⇒ 那条用例测的是一个虚构输入，属恒真判据。契约改由 `latencyText` 的注释承担。
 *  ⑦ ★ `credential_id` 不是 DB 列，是从 `model_name` 推导的。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/selfCheck', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/selfCheck')>()
  return {
    ...actual,
    fetchSelfCheckRuns: vi.fn(),
    fetchSelfCheckSettings: vi.fn(),
    fetchSelfCheckTriggerAvailability: vi.fn(),
    fetchSelfCheckStats: vi.fn(),
    fetchSelfCheckModels: vi.fn(),
  }
})

const mRuns = fetchSelfCheckRuns as unknown as ReturnType<typeof vi.fn>
const mSettings = fetchSelfCheckSettings as unknown as ReturnType<typeof vi.fn>
const mTrigger = fetchSelfCheckTriggerAvailability as unknown as ReturnType<typeof vi.fn>
const mStats = fetchSelfCheckStats as unknown as ReturnType<typeof vi.fn>
const mModels = fetchSelfCheckModels as unknown as ReturnType<typeof vi.fn>

// ── 夹具 ──────────────────────────────────────────────────────────────────

/** ★★ `probe_system` 两个查询都失败时的形状 —— 全零，后端会算出 healthy=true。 */
const PROBE_UNQUERIED = {
  queue_ready: 0,
  queue_ready_unclaimable: 0,
  queue_running: 0,
  due_states: 0,
  queue_last_activity_at: null,
  last_probe_attempt_at: null,
  executing: false,
  healthy: true,
}

/** 真正活着的探测管线：刚跑过、在跑。 */
const PROBE_ALIVE = {
  queue_ready: 3,
  queue_ready_unclaimable: 0,
  queue_running: 1,
  due_states: 2,
  queue_last_activity_at: '2026-10-08T00:00:00Z',
  last_probe_attempt_at: '2026-10-08T00:00:00Z',
  executing: true,
  healthy: true,
}

const STATS_OK = {
  range: '24h',
  summary: { total_runs: 10, success_runs: 6, partial_runs: 1, failed_runs: 2, success_rate: 0.6 },
  by_model: [],
  error_breakdown: [{ error_type: 'timeout', count: 2 }],
  trend: [{ timestamp: '2026-10-08T00:00:00Z', success_rate: 0.6, total: 10 }],
  probe_system: PROBE_ALIVE,
}

/** ★★ summary 失败：全零 + success_rate 0.0，HTTP 200。 */
const STATS_ZERO = { ...STATS_OK, summary: { total_runs: 0, success_runs: 0, partial_runs: 0, failed_runs: 0, success_rate: 0 }, error_breakdown: [], trend: [] }

// ★★ 「三项合计 9 < total 10」这一格**就是 STATS_OK 本身** ——
//   第一版我另建了一个与之逐字相同的 STATS_INFLIGHT，纯属重复夹具。

/** ★★ `range=xyz` ⇒ 拿到 24h 数据、标着 xyz。 */
const STATS_BAD_RANGE = { ...STATS_OK, range: 'xyz' }

const RUN_NO_LATENCY = {
  id: 901, model_name: 'cred-42', started_at: '2026-10-08T00:00:00Z',
  duration_ms: 1200, status: 'success', rounds_total: 3, rounds_success: 3,
  had_tool_call: false, total_tokens: 100, avg_latency_ms: 400,
  upstream_tested: true, credential_id: 42,
}

/** ★★★ 同上，但 `upstream_latency_ms` 键**不存在** —— 那是 0 毫秒的编码。 */
const RUN_ZERO_LATENCY = { ...RUN_NO_LATENCY, id: 902, upstream_latency_ms: 55 }

/** ★ `model_name` 不是 cred-<正整数> 形态 ⇒ 后端不会写 credential_id。 */
const RUN_NO_CRED = {
  ...RUN_NO_LATENCY, id: 903, model_name: 'glm-5.2',
  credential_id: undefined, upstream_tested: false,
}

/** ★ 写操作造成的漂移：回写的 credential_id 与 model_name 推导值不符。 */
const RUN_DRIFT = { ...RUN_NO_LATENCY, id: 904, credential_id: 99 }

const RUNS_OK = { items: [RUN_NO_LATENCY], total: 1 }

const MODELS_RESIDUAL = { models: [{ model_name: 'glm-5.2', total: 5, success: 3, failed: 1 }] }
const MODELS_TIGHT = { models: [{ model_name: 'glm-5.2', total: 4, success: 3, failed: 1 }] }

let mountedList: Array<{ unmount(): void }> = []

async function mountView(role = 'super_admin') {
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.userInfo = { username: 'u', role } as never
  const w = mount(SelfCheckView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

/** 按段标题找到那一节的 wrapper（判据锚在**标题文案**上，不锚 class）。 */
function sectionByTitle(w: W, title: string) {
  return w.findAll('.sc__sec').find((s) => s.text().includes(title))!
}
function sectionTitles(w: W): string[] {
  return w.findAll('.sc__sec').map((s) => s.find('.sc__h').text())
}

beforeEach(() => {
  setLocale('zh-CN')
  for (const w of mountedList) {
    try { w.unmount() } catch { /* ignore */ }
  }
  mountedList = []
  mRuns.mockReset()
  mSettings.mockReset()
  mTrigger.mockReset()
  mStats.mockReset()
  mModels.mockReset()
  mRuns.mockResolvedValue(RUNS_OK)
  mStats.mockResolvedValue(STATS_OK)
  mModels.mockResolvedValue(MODELS_TIGHT)
  mSettings.mockResolvedValue({
    enabled: true, normal_interval_seconds: 3600, fault_interval_seconds: 300,
    model_source: 'featured', max_models: 3, max_tokens_per_run: 2000,
    featured_model_ids: ['glm-5.2'], updated_at: '2026-10-08T00:00:00Z',
  })
  mTrigger.mockResolvedValue({ available: true, new_probe_mode: true, mode: 'probe_queue' })
})

/** 点某一段的加载按钮（按标题定位，不按序号 —— 段数会随门控变化）。 */
async function clickLoad(w: W, title: string) {
  const sec = sectionByTitle(w, title)
  await sec.find('.sc__btn').trigger('click')
  await flushPromises()
  await flushPromises()
}

// ═══════════════════════════════════════════════════════════════════════════
describe('① 门控 · 跨租户三段只对 super_admin', () => {
  it('★★★ tenant_admin 看不到「运行记录」这一段', async () => {
    const w = await mountView('tenant_admin')
    expect(sectionTitles(w)).not.toContain('运行记录')
  })

  it('★★★ tenant_admin 看不到「统计」与「模型分布」两段', async () => {
    const w = await mountView('tenant_admin')
    const titles = sectionTitles(w)
    expect(titles).not.toContain('统计')
    expect(titles).not.toContain('模型分布')
  })

  it('★★★ tenant_admin 页面上根本没有这三段的加载按钮（取数边界就在这里）', async () => {
    const w = await mountView('tenant_admin')
    // ★ 第一版写的是「挂载后 mock 一次都没被调用」—— 那是**恒真**的：
    //   本组件**不自动取数**，只挂载不点 ⇒ 有没有取数屏障都绿。
    //   （批 104/105 各撞过一次，这是第三次同族。）已换成**结构性**断言：
    //   非超管只有 2 个加载按钮，超管有 5 个 —— 两侧必须不等。
    expect(w.findAll('.sc__btn').length).toBe(2)
    const su = await mountView('super_admin')
    expect(su.findAll('.sc__btn').length).toBe(5)
  })

  it('★★ 设置与触发可用性两段对 tenant_admin **照常可见**（不得一锅端）', async () => {
    const w = await mountView('tenant_admin')
    const titles = sectionTitles(w)
    expect(titles).toContain('设置')
    expect(titles).toContain('触发可用性')
  })

  it('★★ tenant_admin 能加载设置与触发可用性', async () => {
    const w = await mountView('tenant_admin')
    await clickLoad(w, '设置')
    expect(mSettings).toHaveBeenCalledTimes(1)
    await clickLoad(w, '触发可用性')
    expect(mTrigger).toHaveBeenCalledTimes(1)
  })

  it('★★ super_admin 五段全在', async () => {
    const w = await mountView('super_admin')
    const titles = sectionTitles(w)
    for (const t of ['运行记录', '统计', '模型分布', '设置', '触发可用性']) expect(titles).toContain(t)
  })

  it('★ role 为空（未 hydrate）按最严处理', async () => {
    const w = await mountView('')
    const titles = sectionTitles(w)
    expect(titles).not.toContain('运行记录')
    expect(titles).toContain('设置')
  })

  it('★ 跨租户披露是常驻的：三段都必须在加载前就写明', async () => {
    const w = await mountView('super_admin')
    for (const t of ['运行记录', '统计', '模型分布']) {
      expect(sectionByTitle(w, t).text()).toContain('跨租户')
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('② probe_system · 绿灯可能是查询失败', () => {
  it('★★★ 两个探测查询都失败时告警（不得渲染成健康）', async () => {
    mStats.mockResolvedValue({ ...STATS_OK, probe_system: PROBE_UNQUERIED })
    const w = await mountView()
    await clickLoad(w, '统计')
    const txt = sectionByTitle(w, '统计').text()
    expect(txt).toContain('探测查询都失败时会算出「健康」')
    expect(txt).toContain('探测管线可能已经死了')
  })

  it('★★★ 正常在跑的探测管线**不**报这条告警（区分格方向相反）', async () => {
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).not.toContain('探测管线可能已经死了')
  })

  it('★ 正控：告警那句话确实会被渲染（否则上面两条都是空的）', async () => {
    mStats.mockResolvedValue({ ...STATS_OK, probe_system: PROBE_UNQUERIED })
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).toContain('探测查询都失败时会算出「健康」')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('③ summary 的二义与三项合计', () => {
  it('★★ 总数为 0 时说明成功率无意义', async () => {
    mStats.mockResolvedValue(STATS_ZERO)
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).toContain('这个比值此刻无意义')
  })

  it('★★ 总数非 0 时**不**说无意义', async () => {
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).not.toContain('这个比值此刻无意义')
  })

  it('★★ 三项合计与总数都渲染出来（供人自己对）', async () => {
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).toContain('总数 10（成功 6 / 部分 1 / 失败 2，三项合计 9）')
  })

  it('★ 三项合计等于总数时不报「分母含在途」（区分格方向相反）', async () => {
    mStats.mockResolvedValue({
      ...STATS_OK,
      summary: { total_runs: 9, success_runs: 6, partial_runs: 1, failed_runs: 2, success_rate: 0.667 },
    })
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).not.toContain('分母里含 running 与 retrying')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('④ 空数组的二义', () => {
  it('★★ error_breakdown 为空时说「与真的没有同形」', async () => {
    mStats.mockResolvedValue(STATS_ZERO)
    const w = await mountView()
    await clickLoad(w, '统计')
    const txt = sectionByTitle(w, '统计').text()
    expect(txt).toContain('查询失败时也是空数组')
    expect(txt).toContain('没有趋势点')
  })

  it('★ 两个区块都有数据时不出现那两句', async () => {
    const w = await mountView()
    await clickLoad(w, '统计')
    const txt = sectionByTitle(w, '统计').text()
    expect(txt).not.toContain('查询失败时也是空数组')
    expect(txt).not.toContain('没有趋势点')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('⑤ range 回显不是生效窗口', () => {
  it('★★ 回显未知 range 时告警并给出本地复刻的生效窗口', async () => {
    mStats.mockResolvedValue(STATS_BAD_RANGE)
    const w = await mountView()
    await clickLoad(w, '统计')
    const txt = sectionByTitle(w, '统计').text()
    expect(txt).toContain('回显的 range 不是受支持的取值')
    expect(txt).toContain('实际窗口按本地复刻是 24h')
  })

  it('★ 回显合法 range 时不告警', async () => {
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(sectionByTitle(w, '统计').text()).not.toContain('回显的 range 不是受支持的取值')
  })

  it('★ 只发合法 range（四个白名单值）', async () => {
    const w = await mountView()
    await clickLoad(w, '统计')
    expect(mStats).toHaveBeenCalledWith('24h')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('⑥ upstream_latency_ms 的 0 是键缺失', () => {
  it('★★★ result_empty（tested 但结果为空）也**要**渲染延迟行', async () => {
    // ★★ 第一版按 outcome 联合里的 'tested' 判断，而函数**从不返回它** ⇒
    //   这一整类渲染不出延迟行。本条盯住那个死成员。
    mRuns.mockResolvedValue({ items: [RUN_NO_LATENCY], total: 1 })
    const w = await mountView()
    await clickLoad(w, '运行记录')
    const txt = sectionByTitle(w, '运行记录').text()
    expect(txt).toContain('已测上游（结果为空）')
    expect(txt).toContain('未记录上游延迟')
  })

  it('★★ upstream_tested=false 时不渲染延迟行', async () => {
    mRuns.mockResolvedValue({ items: [RUN_NO_CRED], total: 1 })
    const w = await mountView()
    await clickLoad(w, '运行记录')
    const txt = sectionByTitle(w, '运行记录').text()
    expect(txt).toContain('未测上游')
    expect(txt).not.toContain('上游延迟')
  })

  it('★★ 键缺失时显示「未记录」', async () => {
    mRuns.mockResolvedValue({ items: [RUN_NO_LATENCY], total: 1 })
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(sectionByTitle(w, '运行记录').text()).toContain('未记录上游延迟')
  })

  it('★★ 键存在时显示数值（区分格方向相反）', async () => {
    mRuns.mockResolvedValue({ items: [RUN_ZERO_LATENCY], total: 1 })
    const w = await mountView()
    await clickLoad(w, '运行记录')
    const txt = sectionByTitle(w, '运行记录').text()
    expect(txt).toContain('上游延迟 55 ms')
    expect(txt).not.toContain('未记录上游延迟')
  })

})

// ═══════════════════════════════════════════════════════════════════════════
describe('⑦ credential_id 是推导值', () => {
  it('★ cred-<正整数> 形态时渲染推导结果', async () => {
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(sectionByTitle(w, '运行记录').text()).toContain('cred-42 → 42')
  })

  it('★★ 非该形态时说明后端不会写这个字段', async () => {
    mRuns.mockResolvedValue({ items: [RUN_NO_CRED], total: 1 })
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(sectionByTitle(w, '运行记录').text()).toContain('后端不会给这个 run 写 credential_id')
  })

  it('★★ 回写值与推导值不符时告警（写操作漂移）', async () => {
    mRuns.mockResolvedValue({ items: [RUN_DRIFT], total: 1 })
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(sectionByTitle(w, '运行记录').text()).toContain('credential_id 与 model_name 推导值不一致')
  })

  it('★ 没有漂移时不告警', async () => {
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(sectionByTitle(w, '运行记录').text()).not.toContain('推导值不一致')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('模型分布 · 没有 partial 计数且是全时段', () => {
  it('★ 残差不为零时告警', async () => {
    mModels.mockResolvedValue(MODELS_RESIDUAL)
    const w = await mountView()
    await clickLoad(w, '模型分布')
    expect(sectionByTitle(w, '模型分布').text()).toContain('没有 partial 计数')
  })

  it('★ 残差为零时不告警', async () => {
    const w = await mountView()
    await clickLoad(w, '模型分布')
    expect(sectionByTitle(w, '模型分布').text()).not.toContain('没有 partial 计数')
  })

  it('★ 常驻披露「全时段，与 stats 口径不可比」', async () => {
    const w = await mountView()
    await clickLoad(w, '模型分布')
    expect(sectionByTitle(w, '模型分布').text()).toContain('与上面 stats 的窗口口径不可直接比')
  })

  it('★ 三个计数都渲染', async () => {
    const w = await mountView()
    await clickLoad(w, '模型分布')
    expect(sectionByTitle(w, '模型分布').text()).toContain('总数 4 / 成功 3 / 失败 1')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('取数纪律', () => {
  it('★ 挂载不自动取数（五段各自一次请求）', async () => {
    await mountView()
    expect(mRuns).not.toHaveBeenCalled()
    expect(mStats).not.toHaveBeenCalled()
    expect(mSettings).not.toHaveBeenCalled()
  })

  it('★★ 失败时清空该段，不保留上一次的成功结果', async () => {
    mRuns.mockResolvedValue(RUNS_OK)
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(sectionByTitle(w, '运行记录').text()).toContain('cred-42')
    mRuns.mockRejectedValue(new Error('boom-runs'))
    await clickLoad(w, '运行记录')
    const txt = sectionByTitle(w, '运行记录').text()
    expect(txt).toContain('boom-runs')
    expect(txt).not.toContain('cred-42')
  })

  it('★ 触发不可用时说清原因码', async () => {
    mTrigger.mockResolvedValue({
      available: false, new_probe_mode: false,
      error_code: 'self_check.trigger.no_probe_path', reason: 'no probe path',
    })
    const w = await mountView()
    await clickLoad(w, '触发可用性')
    const txt = sectionByTitle(w, '触发可用性').text()
    expect(txt).toContain('self_check.trigger.no_probe_path')
    expect(txt).toContain('no probe path')
  })

  it('★ 触发可用时不出现原因行', async () => {
    const w = await mountView()
    await clickLoad(w, '触发可用性')
    expect(sectionByTitle(w, '触发可用性').text()).not.toContain('不可触发原因')
  })

  it('★ status 筛选按精确值发（空表示不带这个参数）', async () => {
    const w = await mountView()
    await clickLoad(w, '运行记录')
    expect(mRuns).toHaveBeenCalledWith({ limit: 50 })
    const btn = sectionByTitle(w, '运行记录').findAll('.sc__chip-btn').find((b) => b.text() === '失败')!
    await btn.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(mRuns).toHaveBeenLastCalledWith({ limit: 50, status: 'failed' })
  })
})