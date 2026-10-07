import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import WorkTypesView from './WorkTypesView.vue'
import { setLocale } from '@/i18n'

/**
 * 工作类型配置面回归护栏（2026-10-08，第一百零三批）。
 *
 * 缺陷背景（读 `api/workTypes.ts` 文件头 + 复核 `admin/work_types.go` 得出）：
 *
 *  1. ★★★ **`model_routes` 的 `null` 与 `[]` 后端都产生不出来。**
 *     字段是 `json:"model_routes,omitempty"`，而 Go 的 `omitempty` 对 slice 的判定是
 *     `len() == 0` —— **非 nil 的空切片同样被省略**。两条装配路径都验证过：
 *       - `listWorkTypes`：`out[i].ModelRoutes = routeMap[key]`，而 `fetchRoutesForKeys`
 *         只在 `rows.Next()` 里 append，**没建过条目的 key 取出来是 nil slice**；
 *       - `getWorkType`：`fetchRoutes` 返回 `make([]modelRoute, 0)`，零行时是
 *         **非 nil 的空切片**，一样被省略。
 *     ⇒ 后端只有「键消失 / 非空数组」两态。
 *     ⇒ 第一版视图把 `null` 渲染成「配了但为 null」、把 `[]` 渲染成「空数组」，
 *       **凭空造出两种配置状态**；真出现时也不该说成正常状态，只能说响应异常。
 *
 *  2. ★★★ **`by_work_type` 为空是三义的**（`admin/work_types.go:298` 的
 *     `if err == nil { … }` 把 `h.db.Query` 的错误整个吞掉，响应仍是 200）：
 *     查询失败 / 真的没有启用的工作类型 / `h.db == nil` —— 三者响应同形。
 *     ⇒ 第一版视图在 `by_work_type` 为空时给**每一行**标「不在 by_work_type 里」，
 *       把「查不出来」说成了「这一行没有统计」—— 与批 101 的 `{}` 三合一同型。
 *
 *  3. ★★ **`count_24h` 是派生量**（`:415-419` `count := row.CountDirect;
 *     if count == 0 { count = row.CountL1 }`），而 `count_l1_proxy` 是该 L1 的
 *     **全局量**，同一 L1 下多行会重复 ⇒ **求和必然重复计数**。
 *
 *  4. ★★ **`system_prompt` / `acc_task_type` / `synced_from_acc_at` 是 `*T + omitempty`**
 *     ⇒ **键可能整个不存在**。渲染成「空串」会把「没配」与「查不出来」说成一件事。
 *
 *  5. ★ 三个 GET 用 `Promise.allSettled` 并行 ⇒ 单边失败不得吞掉另外两边的结果。
 */

// ── 夹具：逐字照抄 `admin/work_types.go:503-525` 的 json tag 形状 ───────────────

/** 有路由、有 prompt、有同步时间戳的启用项。 */
const CONFIG_A = {
  key: 'code_review',
  label: '代码评审',
  category: 'engineering',
  l1_task_type: 'code',
  default_profile: 'smart',
  tags: ['review', 'pr'],
  prompt_keywords: ['diff', 'patch'],
  system_prompt: '你是一名资深代码评审',
  acc_task_type: 'code',
  enabled: true,
  sort_order: 10,
  synced_from_acc_at: '2026-10-08T09:00:00Z',
  updated_at: '2026-10-08T09:00:00Z',
  model_routes: [
    { id: 1, canonical_name: 'claude-opus-5', weight: 1, min_score: 0.2, enabled: true, tier: 'primary', task_quality_score: 95 },
    { id: 2, canonical_name: 'gpt-5', weight: 0.6, min_score: 0.3, enabled: true, tier: 'fallback', task_quality_score: 80 },
  ],
}

/**
 * ★ `model_routes` **键整个不存在** —— 这才是后端「无路由」的真实形态。
 *   三个 omitempty 指针也都缺席（全部为 nil）。
 */
const CONFIG_B = {
  key: 'log_review',
  label: '日志审查',
  category: 'ops',
  l1_task_type: 'chat',
  default_profile: 'speed_first',
  tags: [],
  prompt_keywords: [],
  enabled: true,
  sort_order: 20,
  updated_at: '2026-10-08T09:00:00Z',
}

/** ★ 停用项：按 `:399 WHERE enabled = TRUE`，它**不该**出现在 by_work_type 里。 */
const CONFIG_E = {
  key: 'legacy_ops',
  label: '历史运维',
  category: 'ops',
  l1_task_type: 'agent',
  default_profile: 'cost_first',
  tags: ['legacy'],
  prompt_keywords: [],
  system_prompt: '旧流程',
  enabled: false,
  sort_order: 30,
  updated_at: '2026-10-08T09:00:00Z',
  model_routes: [
    { id: 3, canonical_name: 'qwen-max', weight: 1, min_score: 0.1, enabled: false, tier: 'secondary', task_quality_score: 60 },
  ],
}

/** ★★ 异常形态：`null` —— 后端产生不出来。 */
const CONFIG_C = { ...CONFIG_B, key: 'null_routes', label: 'null 路由', model_routes: null }

/** ★★ 异常形态：`[]` —— 后端同样产生不出来（omitempty 连空切片一起省）。 */
const CONFIG_D = { ...CONFIG_B, key: 'empty_routes', label: '空路由', model_routes: [] }

/** ★★ 两条派生格：一条 `count_direct > 0`，一条走 L1 兜底。 */
const STATS = {
  window_hours: 24,
  by_work_type: {
    code_review: {
      key: 'code_review', label: '代码评审', category: 'engineering', l1_task_type: 'code',
      count_24h: 7, count_direct: 7, count_l1_proxy: 12,
    },
    log_review: {
      key: 'log_review', label: '日志审查', category: 'ops', l1_task_type: 'chat',
      count_24h: 12, count_direct: 0, count_l1_proxy: 12,
    },
  },
  by_l1_task: { code: 7, chat: 12, __specified__: 3 },
  total_auto: 19,
  total_specified: 3,
  top_models: [
    { model: 'claude-opus-5', count: 9 },
    { model: 'gpt-5', count: 4 },
  ],
  sync_meta: { source: 'acc', last_synced_at: null, enabled_count: 2, route_count: 3, acc_configured: true },
}

/** ★ `items` 恒数组、最少 8 项（`:221-228` 无条件 seed canonical 八项）。 */
const L1_ITEMS = {
  items: [
    { key: 'chat', label: '对话', icon: '💬', count: 12 },
    { key: 'reasoning', label: '推理', icon: '🧠', count: 0 },
    { key: 'code', label: '代码', icon: '⌨️', count: 7 },
    { key: 'agent', label: '智能体', icon: '🤖', count: 0 },
    { key: 'creative', label: '创意', icon: '🎨', count: 0 },
    { key: 'long_context', label: '长上下文', icon: '📚', count: 0 },
    { key: 'vision', label: '视觉', icon: '👁️', count: 0 },
    { key: 'function_call', label: '函数调用', icon: '🔧', count: 0 },
    // ★ `:240-241` 新增项回落：icon 恒为 ◆，label 回落成 key 本身
    { key: 'db_tuning', label: 'db_tuning', icon: '◆', count: 0 },
  ],
}

const fConfigs = vi.fn()
const fStats = vi.fn()
const fL1 = vi.fn()

vi.mock('@/api/workTypes', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/workTypes')>()
  return {
    ...actual,
    fetchWorkTypes: (o?: unknown) => fConfigs(o),
    fetchWorkTypeStats: (o?: unknown) => fStats(o),
    fetchL1TaskTypes: (o?: unknown) => fL1(o),
  }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

beforeEach(() => {
  setLocale('zh-CN')
  fConfigs.mockReset()
  fStats.mockReset()
  fL1.mockReset()
  fConfigs.mockResolvedValue([CONFIG_A, CONFIG_B, CONFIG_E])
  fStats.mockResolvedValue(STATS)
  fL1.mockResolvedValue(L1_ITEMS)
})

async function mountView() {
  const w = mount(WorkTypesView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  return w
}

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 1 · model_routes 只有两态是真的', () => {
  it('键消失渲染成「未配置模型路由」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('未配置模型路由')
  })

  it('★ 键消失**不得**被说成响应异常（两个分支方向相反）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('响应异常')
  })

  it('非空数组渲染出 canonical_name', async () => {
    const w = await mountView()
    expect(w.text()).toContain('claude-opus-5 / gpt-5')
  })

  it('★ 有路由时**不得**出现「未配置模型路由」', async () => {
    fConfigs.mockResolvedValue([CONFIG_A])
    const w = await mountView()
    expect(w.text()).not.toContain('未配置模型路由')
  })

  it('★★ null 渲染成响应异常，而不是「配了但为 null」这种正常状态', async () => {
    fConfigs.mockResolvedValue([CONFIG_C])
    const w = await mountView()
    expect(w.text()).toContain('响应异常')
    expect(w.text()).not.toContain('未配置模型路由')
  })

  it('★★ 空数组同样渲染成响应异常（Go omitempty 连空切片一起省，后端产生不出来）', async () => {
    fConfigs.mockResolvedValue([CONFIG_D])
    const w = await mountView()
    expect(w.text()).toContain('响应异常')
    expect(w.text()).not.toContain('未配置模型路由')
  })

  it('★ 键缺失与异常态在同一页上并存时各自说各自的话', async () => {
    fConfigs.mockResolvedValue([CONFIG_A, CONFIG_B, CONFIG_C, CONFIG_D])
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('未配置模型路由')
    expect(text).toContain('响应异常')
    expect(text).toContain('claude-opus-5 / gpt-5')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 2 · count_24h 是派生量且不给合计', () => {
  it('★ count_direct > 0 时三个数字逐行给出', async () => {
    const w = await mountView()
    expect(w.text()).toContain('24 小时 7（直接 7 · L1 代理 12）')
  })

  it('★ count_direct 为 0 时 count_24h 落在 L1 代理上（派生格）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('24 小时 12（直接 0 · L1 代理 12）')
  })

  it('★★ 常驻披露「不提供合计」（count_l1_proxy 跨行重复，求和会重复计数）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不提供合计')
    expect(w.text()).toContain('求和会重复计数')
  })

  it('★ 响应里的 window_hours 被采纳而不是硬编码 24 之外的数', async () => {
    fStats.mockResolvedValue({ ...STATS, window_hours: 12 })
    const w = await mountView()
    expect(w.text()).toContain('近 12 小时统计')
  })

  it('★ 不在 by_work_type 里的行不渲染计数（没有可渲染的东西就不编）', async () => {
    const w = await mountView()
    // legacy_ops 停用 ⇒ 不在 by_work_type ⇒ 不该有它的 24 小时行
    expect(w.text()).not.toContain('24 小时 0（直接 0 · L1 代理 0）')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 3 · by_work_type 为空是三义的，必须说无法判定', () => {
  it('★★ by_work_type 为空时给出无法判定文案', async () => {
    fStats.mockResolvedValue({ ...STATS, by_work_type: {} })
    const w = await mountView()
    expect(w.text()).toContain('无法判定')
  })

  it('★★★ by_work_type 为空时**绝不**给每行标「不在 by_work_type 里」', async () => {
    fStats.mockResolvedValue({ ...STATS, by_work_type: {} })
    const w = await mountView()
    expect(w.text()).not.toContain('不在 by_work_type 里')
  })

  it('★ by_work_type 非空时缺的那一行**确实**被标出来（区分格方向相反）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不在 by_work_type 里')
  })

  it('★ by_work_type 非空时不再出现无法判定文案', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('无法判定')
  })

  it('★ stats 整体失败时不出现无法判定（那是没有数据，不是无法判定）', async () => {
    fStats.mockRejectedValue(new Error('403 forbidden'))
    const w = await mountView()
    expect(w.text()).not.toContain('by_work_type 为空')
    expect(w.text()).toContain('403 forbidden')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 4 · omitempty 键缺失不得渲染成空值', () => {
  it('★ 有 system_prompt 时给出提示', async () => {
    const w = await mountView()
    expect(w.text()).toContain('已设置 system_prompt')
  })

  it('★★ 键整个不存在时不出现该提示（不是「空串」）', async () => {
    fConfigs.mockResolvedValue([CONFIG_B])
    const w = await mountView()
    expect(w.text()).not.toContain('已设置 system_prompt')
    expect(w.text()).not.toContain('ACC 同步于')
  })

  it('★ 有 synced_from_acc_at 时把时间戳渲染出来', async () => {
    const w = await mountView()
    expect(w.text()).toContain('2026-10-08T09:00:00Z')
  })

  it('★★ 空 tags 数组不渲染成「标签」行', async () => {
    fConfigs.mockResolvedValue([CONFIG_B])
    const w = await mountView()
    expect(w.text()).not.toContain('标签')
  })

  it('★ 非空 tags 渲染出标签', async () => {
    const w = await mountView()
    expect(w.text()).toContain('review · pr')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 5 · 单边失败不吞掉另外两边', () => {
  it('★ configs 失败时 stats 与 L1 仍然渲染', async () => {
    fConfigs.mockRejectedValue(new Error('configs boom'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('configs boom')
    // ★ 断言「另外两边还在」，而不是笼统一句 error —— 三边全挂时这条也会红，
    //   所以它与下面「三条文案各自都在」构成一对。
    expect(text).toContain('近 24 小时统计')
    expect(text).toContain('db_tuning')
    expect(text).not.toContain('代码评审')
  })

  it('★ stats 失败时 configs 与 L1 仍然渲染', async () => {
    fStats.mockRejectedValue(new Error('stats boom'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('stats boom')
    expect(text).toContain('代码评审')
    expect(text).toContain('db_tuning')
    expect(text).not.toContain('近 24 小时统计')
  })

  it('★ l1 失败时 configs 与 stats 仍然渲染', async () => {
    fL1.mockRejectedValue(new Error('l1 boom'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('l1 boom')
    expect(text).toContain('代码评审')
    expect(text).toContain('claude-opus-5 / gpt-5')
    expect(text).not.toContain('L1 任务类型（')
  })

  it('★ 三个都失败时三条错误文案各自都在', async () => {
    fConfigs.mockRejectedValue(new Error('configs boom'))
    fStats.mockRejectedValue(new Error('stats boom'))
    fL1.mockRejectedValue(new Error('l1 boom'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('configs boom')
    expect(text).toContain('stats boom')
    expect(text).toContain('l1 boom')
  })

  it('★ 403 的原文被透出（superAdmin 档最常见的失败就是它）', async () => {
    fStats.mockRejectedValue(new Error('请求失败：403 Forbidden'))
    const w = await mountView()
    expect(w.text()).toContain('403 Forbidden')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('清单渲染与筛选', () => {
  it('L1 列表按响应顺序渲染，新增项也在内', async () => {
    const w = await mountView()
    expect(w.text()).toContain('L1 任务类型（9 项）')
    expect(w.text()).toContain('db_tuning')
  })

  it('top_models 为空数组时给空态文案', async () => {
    fStats.mockResolvedValue({ ...STATS, top_models: [] })
    const w = await mountView()
    expect(w.text()).toContain('没有 top_models')
  })

  it('top_models 非空时逐条给出模型与计数', async () => {
    const w = await mountView()
    expect(w.text()).toContain('claude-opus-5')
    expect(w.text()).toContain('gpt-5')
  })

  it('total_auto 与 total_specified 都渲染（后者桌面类型里没声明但后端一定返回）', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('自动识别')
    expect(text).toContain('显式指定')
  })

  it('★ 路由层级图例给出 CHECK 锁死的三个值', async () => {
    const w = await mountView()
    expect(w.text()).toContain('primary / secondary / fallback')
  })

  it('★ 筛选按 key 命中', async () => {
    const w = await mountView()
    await w.find('input.wt__input').setValue('legacy_ops')
    const text = w.text()
    expect(text).toContain('历史运维')
    expect(text).not.toContain('代码评审')
  })

  it('★ 筛选按 l1_task_type 命中', async () => {
    const w = await mountView()
    await w.find('input.wt__input').setValue('agent')
    const text = w.text()
    expect(text).toContain('历史运维')
    expect(text).not.toContain('代码评审')
  })

  it('★ 筛选无命中时给空态', async () => {
    const w = await mountView()
    await w.find('input.wt__input').setValue('zzz-no-such')
    expect(w.text()).toContain('没有匹配的工作类型')
  })

  it('空清单时给出空态而不是白屏', async () => {
    fConfigs.mockResolvedValue([])
    const w = await mountView()
    expect(w.text()).toContain('没有匹配的工作类型')
    expect(w.text()).toContain('工作类型（0 项）')
  })

  it('★ 停用项渲染为停用徽章', async () => {
    const w = await mountView()
    expect(w.text()).toContain('停用')
    expect(w.text()).toContain('启用')
  })
})