import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import RoutingPolicyView from './RoutingPolicyView.vue'
import { setLocale } from '@/i18n'

/**
 * 回归护栏：**三处「同形」绝不能被渲染成确定结论**。
 *
 * 缺陷背景（2026-10-08，读 `api/routingPolicy.ts` 文件头得出）：
 *  1. `policy` 返 `{}` 是**三合一**（没有这一行 / 查询失败 / 文本为空，
 *     `routing.go:2533`）⇒ `fetchRoutingPolicy` 给 `null`。
 *     ⇒ 渲染成「策略未配置」就是**凭空造出一个结论**。
 *  2. `scoring-weights` 的默认值在查询失败、解析失败、缺键三种情况下产出同一份，
 *     响应里**没有任何标记** ⇒ 不披露 note，用户会把兜底值当成线上真值。
 *  3. `featured_models === []` 时「没配」与「查不出来」同形（`:2594-2597`）。
 *
 * 外加两条：`standardized_name` 恒等于 `name`（`:4081-4082`），
 * 而 `policy.featured_models`（可为 null 的原始列）与
 * `featured.featured_models`（COALESCE 过的生效列表）**不是同一个东西**。
 */

/** routing.go:2533 的 `{}` ⇒ 模块层给 null。 */
const POLICY_ROW = {
  id: 1,
  tenant_id: 'default',
  weights_json: {},
  sticky_ttl_seconds: 1800,
  local_bonus: 0,
  updated_at: '2026-10-08T09:00:00Z',
  transient_fail_threshold: 3,
  notes: null,
  algorithm_version: null,
  retry_per_credential: null,
  tier_fallback_max: null,
  slot_soft_limit_ratio: null,
  slot_hard_limit_ratio: null,
  slot_wait_max_ms: null,
  circuit_open_seconds: null,
  circuit_failure_threshold: null,
  circuit_max_open_seconds: null,
  featured_models: null,
  stats_window_minutes: null,
  stats_update_interval_seconds: null,
  scoring_weights_json: null,
}

const WEIGHTS = {
  price: 0.4,
  session_load: 0.2,
  failure_penalty: 0.25,
  default_price_cny: 1,
  default_price_usd: 0.14,
  display_only: true,
  note: 'these weights only affect previews, not live routing',
}

const fPolicy = vi.fn()
const fFeatured = vi.fn()
const fWeights = vi.fn()
const fFeaturedModels = vi.fn()

vi.mock('@/api/routingPolicy', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/routingPolicy')>()
  return {
    ...actual,
    fetchRoutingPolicy: (o?: unknown) => fPolicy(o),
    fetchRoutingFeatured: (o?: unknown) => fFeatured(o),
    fetchRoutingScoringWeights: (o?: unknown) => fWeights(o),
    fetchRoutingFeaturedModels: (o?: unknown) => fFeaturedModels(o),
  }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

beforeEach(() => {
  setLocale('zh-CN')
  fPolicy.mockReset()
  fFeatured.mockReset()
  fWeights.mockReset()
  fFeaturedModels.mockReset()
  fPolicy.mockResolvedValue(null)
  fFeatured.mockResolvedValue({ featured_models: [] })
  fWeights.mockResolvedValue(WEIGHTS)
  fFeaturedModels.mockResolvedValue({ models: [] })
})

async function mountView() {
  const w = mount(RoutingPolicyView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  return w
}

describe('路由策略视图 · policy 的三合一语义', () => {
  it('★ 空对象时渲染「无法判定」而不是「未配置」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('无法判定')
  })

  it('★ ★绝不出现「未配置」三个字', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('未配置')
  })

  it('★ 有策略行时不再显示「无法判定」', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW)
    const w = await mountView()
    expect(w.text()).not.toContain('无法判定：后端')
    expect(w.text()).toContain('default')
  })

  it('★ 有策略行时展示租户与更新时间', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW)
    const w = await mountView()
    expect(w.text()).toContain('2026-10-08T09:00:00Z')
  })
})

describe('路由策略视图 · null 与 0 必须显示成不同的东西', () => {
  it('★ 可空列渲染成「（空）」', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW)
    const w = await mountView()
    expect(w.text()).toContain('（空）')
  })

  it('★★ NOT NULL 列的 0 渲染成 0 而不是「（空）」', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW) // local_bonus 就是 0
    const w = await mountView()
    // ★ 模板对两个 NOT NULL 列用的是 i18n 标签（不是原始字段名），断言必须按标签找
    const rows = w.findAll('.rp__kvRow')
    const localBonus = rows.find((r) => r.text().includes('本地加权'))
    expect(localBonus).toBeDefined()
    expect(localBonus?.text()).toContain('0')
    expect(localBonus?.text()).not.toContain('（空）')
  })

  it('★★ NOT NULL 列的 1800 正常渲染', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW)
    const w = await mountView()
    const rows = w.findAll('.rp__kvRow')
    const ttl = rows.find((r) => r.text().includes('粘性 TTL'))
    expect(ttl).toBeDefined()
    expect(ttl?.text()).toContain('1800')
  })

  it('★ 可空列用的是原始字段名（与 NOT NULL 列的标签化处理不同）', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW)
    const w = await mountView()
    const rows = w.findAll('.rp__kvRow')
    const row = rows.find((r) => r.text().includes('algorithm_version'))
    expect(row).toBeDefined()
    expect(row?.text()).toContain('（空）')
  })

  it('★ null 单元格挂上区分类', async () => {
    fPolicy.mockResolvedValue(POLICY_ROW)
    const w = await mountView()
    expect(w.findAll('.rp__kv--null').length).toBeGreaterThan(0)
  })
})

describe('路由策略视图 · 评分权重的兜底必须披露', () => {
  it('★ 兜底告警常驻显示', async () => {
    const w = await mountView()
    expect(w.text()).toContain('都会回同一份默认值')
  })

  it('★ 后端 note 原文被展示出来', async () => {
    const w = await mountView()
    expect(w.text()).toContain('not live routing')
  })

  it('★ 五个保证键全部渲染', async () => {
    const w = await mountView()
    for (const k of ['price', 'session_load', 'failure_penalty', 'default_price_cny', 'default_price_usd']) {
      expect(w.text()).toContain(k)
    }
  })

  it('★ 权重的值渲染正确', async () => {
    const w = await mountView()
    expect(w.text()).toContain('0.25')
  })

  it('★ 权重取数失败时也保留告警（不能因为没数据就撤掉披露）', async () => {
    fWeights.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    expect(w.text()).toContain('都会回同一份默认值')
  })
})

describe('路由策略视图 · 两个「精选」不是同一个东西', () => {
  it('★ 提示语点明两者不同', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不是同一个东西')
  })

  it('★ 生效列表为空时渲染告警而不是普通空态', async () => {
    fFeatured.mockResolvedValue({ featured_models: [] })
    const w = await mountView()
    expect(w.text()).toContain('查不出来')
  })

  it('★ 生效列表有值时逐项渲染', async () => {
    fFeatured.mockResolvedValue({ featured_models: ['gpt-4o', 'o3'] })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
    expect(w.text()).toContain('o3')
  })

  it('★ featured 取数失败时渲染「无法判定」', async () => {
    fFeatured.mockRejectedValue(new Error('boom-featured'))
    const w = await mountView()
    expect(w.text()).toContain('无法判定')
  })

  it('★★ 用量榜与生效列表同时存在时两段都渲染', async () => {
    fFeatured.mockResolvedValue({ featured_models: ['gpt-4o'] })
    fFeaturedModels.mockResolvedValue({
      models: [{ name: 'claude-sonnet', standardized_name: 'claude-sonnet', count: 12, source: 'usage' }],
    })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
    expect(w.text()).toContain('claude-sonnet')
  })

  it('★★★ standardized_name 不单独渲染（恒等于 name，渲染两遍会被读成两种东西）', async () => {
    fFeaturedModels.mockResolvedValue({
      models: [{ name: 'claude-sonnet', standardized_name: 'claude-sonnet', count: 12, source: 'usage' }],
    })
    const w = await mountView()
    const occurrences = w.text().split('claude-sonnet').length - 1
    expect(occurrences).toBe(1)
  })

  it('★ 用量榜的 source 与 count 都渲染', async () => {
    fFeaturedModels.mockResolvedValue({
      models: [{ name: 'm1', standardized_name: 'm1', count: 7, source: 'policy' }],
    })
    const w = await mountView()
    expect(w.text()).toContain('policy')
    expect(w.text()).toContain('7')
  })
})

describe('路由策略视图 · 单边失败不吞掉另一边', () => {
  it('★ policy 失败时其余三段仍然渲染', async () => {
    fPolicy.mockRejectedValue(new Error('boom-policy'))
    const w = await mountView()
    expect(w.text()).toContain('not live routing')
    expect(w.text()).toContain('boom-policy')
  })

  it('★ featured-models 失败时 featured 段仍然渲染', async () => {
    fFeaturedModels.mockRejectedValue(new Error('boom-fm'))
    const w = await mountView()
    expect(w.text()).toContain('boom-fm')
    expect(w.findAll('.rp__panel').length).toBeGreaterThanOrEqual(3)
  })

  it('★ 失败原文透出而不是统一的泛化文案', async () => {
    fPolicy.mockRejectedValue(new Error('仅限超级管理员'))
    const w = await mountView()
    expect(w.text()).toContain('仅限超级管理员')
  })
})