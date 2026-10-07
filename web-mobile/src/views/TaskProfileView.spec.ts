import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TaskProfileView from './TaskProfileView.vue'
import { setLocale } from '@/i18n'

/**
 * 回归护栏：这一页是批 98/99 两个 API 模块的**唯一入口**。
 *
 * 缺陷背景（2026-10-08）：批 98/99 把 `/api/admin/task-profile` 与
 * `/api/admin/task-profile/corrections/stats` 的 API 层做完了（128 + 214 条判据），
 * **但两个模块都没有被任何 UI 引用** —— 全仓 94 个 api 模块里有 32 个这种孤儿。
 * ⇒ API 层做完 ≠ 功能复制到移动端；用户点不到的东西不算数。
 *
 * 这里钉住：两个模块都真的被渲染（不是只挂一个）、可空字段不炸、
 * 未知档位不猜、单边失败不吞掉另一边。
 */

// ── 夹具：逐字照抄后端形状（taskprofile/types.go · registry.go · corrections.go） ──

/** registry.go:42 coding（tier-b / [tier-a, tier-c] / 0.75） */
const PROFILE_CODING = {
  task_type: 'coding',
  description: 'Feature implementation, API integration',
  preferred_tier: 'tier-b',
  fallback_tiers: ['tier-a', 'tier-c'],
  min_confidence: 0.75,
}

/** ★ registry 里**没有**的类型 ⇒ 后端自造档案，`description` 是空串（批 98 文件头 (12)） */
const PROFILE_UNKNOWN = {
  task_type: 'mystery-task',
  description: '',
  preferred_tier: 'tier-b',
  fallback_tiers: ['tier-a', 'tier-c'],
  min_confidence: 0.7,
}

function profileEnvelope() {
  return {
    registry_version: '2026.09.v3.1-defaults',
    schema_version: 1,
    task_types: ['coding', 'mystery-task'],
    profiles: [
      {
        ...PROFILE_CODING,
        suggestion: {
          task_type: 'coding',
          tier: 'tier-b',
          fallback_tiers: ['tier-a', 'tier-c'],
          min_confidence: 0.75,
          tier_source: 'registry',
        },
      },
      {
        ...PROFILE_UNKNOWN,
        suggestion: {
          task_type: 'mystery-task',
          tier: 'tier-b',
          fallback_tiers: ['tier-a', 'tier-c'],
          min_confidence: 0.7,
          tier_source: 'registry',
        },
      },
    ],
  }
}

/** corrections.go:28-39 —— ★ 这两键**键恒在、值可为 null** */
function correctionRow(over: Record<string, unknown> = {}) {
  return {
    id: 412,
    request_id: 'req-8f21c0ab',
    auto_task_type: 'coding',
    human_task_type: 'debugging',
    agrees: false,
    classifier_confidence: 0.93,
    profile: 'balanced',
    annotator: 'qa@local',
    reason: '实际是排错',
    created_at: '2026-10-08T09:12:33Z',
    ...over,
  }
}

function statsEnvelope(recent: unknown[]) {
  return {
    since: '2026-09-08T09:12:33Z',
    stats: { coding: { task_type: 'coding', total: 10, agrees: 6, corrected: 4, correction_rate: 0.4 } },
    suggestions: {
      coding: {
        task_type: 'coding',
        tier: 'tier-b',
        fallback_tiers: ['tier-a', 'tier-c'],
        min_confidence: 0.75,
        tier_source: 'registry',
      },
    },
    recent,
  }
}

const fetchTaskProfile = vi.fn()
const fetchTaskTypeCorrectionStats = vi.fn()

vi.mock('@/api/taskProfile', () => ({ fetchTaskProfile: (o?: unknown) => fetchTaskProfile(o) }))
vi.mock('@/api/taskTypeCorrectionStats', () => ({
  fetchTaskTypeCorrectionStats: (o?: unknown) => fetchTaskTypeCorrectionStats(o),
}))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

beforeEach(() => {
  // ★ 测试环境 navigator.language 不是 zh，词典默认会落到 en-US ⇒ 显式钉住中文侧
  setLocale('zh-CN')
  fetchTaskProfile.mockReset()
  fetchTaskTypeCorrectionStats.mockReset()
  fetchTaskProfile.mockResolvedValue(profileEnvelope())
  fetchTaskTypeCorrectionStats.mockResolvedValue(statsEnvelope([correctionRow()]))
})

async function mountView() {
  const w = mount(TaskProfileView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  return w
}

describe('任务档案视图 · 两个模块都被渲染', () => {
  it('★ 同时调用两个端点', async () => {
    await mountView()
    expect(fetchTaskProfile).toHaveBeenCalledTimes(1)
    expect(fetchTaskTypeCorrectionStats).toHaveBeenCalledTimes(1)
  })

  it('★ 渲染注册表版本与类型数', async () => {
    const w = await mountView()
    expect(w.text()).toContain('2026.09.v3.1-defaults')
  })

  it('★ 两个档案类型都渲染出来', async () => {
    const w = await mountView()
    expect(w.text()).toContain('coding')
    expect(w.text()).toContain('mystery-task')
  })

  it('★ 修正记录被渲染（第二个模块确实接上了）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('req-8f21c0ab')
    expect(w.text()).toContain('实际是排错')
  })

  it('★ 统计窗口起点被渲染', async () => {
    const w = await mountView()
    expect(w.text()).toContain('2026-09-08T09:12:33Z')
  })
})

describe('任务档案视图 · 空 description 是未知类型信号', () => {
  it('★ 空 description 渲染成未知类型提示而不是空白', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('不在注册表里")\n    </p>')
    expect(w.find('.tp__desc--unknown').exists()).toBe(true)
  })

  it('★ 已知类型不挂 unknown 类', async () => {
    const w = await mountView()
    expect(w.findAll('.tp__desc--unknown')).toHaveLength(1)
  })

  it('★ 已知类型的描述原样渲染', async () => {
    const w = await mountView()
    expect(w.text()).toContain('Feature implementation, API integration')
  })
})

describe('任务档案视图 · 可空字段不炸', () => {
  it('★ classifier_confidence 为 null 时页面仍渲染', async () => {
    fetchTaskTypeCorrectionStats.mockResolvedValue(
      statsEnvelope([correctionRow({ classifier_confidence: null })]),
    )
    const w = await mountView()
    expect(w.text()).toContain('req-8f21c0ab')
  })

  it('★ 两个可空键都为 null 时不把 null 渲染进 DOM', async () => {
    fetchTaskTypeCorrectionStats.mockResolvedValue(
      statsEnvelope([correctionRow({ classifier_confidence: null, profile: null })]),
    )
    const w = await mountView()
    expect(w.text()).not.toContain('null')
  })

  it('★ 两个可空键都有值时都渲染', async () => {
    const w = await mountView()
    expect(w.text()).toContain('balanced')
  })

  it('★ 只有 classifier_confidence 为 null 时 profile 仍渲染', async () => {
    fetchTaskTypeCorrectionStats.mockResolvedValue(
      statsEnvelope([correctionRow({ classifier_confidence: null, profile: 'balanced' })]),
    )
    const w = await mountView()
    expect(w.text()).toContain('balanced')
    expect(w.text()).not.toContain('null')
  })
})

describe('任务档案视图 · tier_source 枚举外的值不猜', () => {
  it('★ 三个已知取值各自有标签', async () => {
    for (const [source, label] of [
      ['registry', '注册表'],
      ['correction_escalation', '修正驱动升档'],
      ['confidence_escalation', '低置信度升档'],
    ] as const) {
      fetchTaskProfile.mockResolvedValue({
        ...profileEnvelope(),
        profiles: [
          {
            ...PROFILE_CODING,
            suggestion: { ...profileEnvelope().profiles[0]!.suggestion, tier_source: source },
          },
        ],
      })
      const w = await mountView()
      expect(w.text()).toContain(label)
    }
  })

  it('★ 未知取值原样透出（不猜、不吞）', async () => {
    fetchTaskProfile.mockResolvedValue({
      ...profileEnvelope(),
      profiles: [
        {
          ...PROFILE_CODING,
          suggestion: { ...profileEnvelope().profiles[0]!.suggestion, tier_source: 'tier_source_from_the_future' },
        },
      ],
    })
    const w = await mountView()
    expect(w.text()).toContain('tier_source_from_the_future')
  })
})

describe('任务档案视图 · 单边失败不吞掉另一边', () => {
  it('★ 档案失败但统计成功时，统计仍然渲染且不显示错误', async () => {
    fetchTaskProfile.mockRejectedValue(new Error('boom-profile'))
    const w = await mountView()
    expect(w.text()).toContain('req-8f21c0ab')
    expect(w.text()).not.toContain('boom-profile')
  })

  it('★ 统计失败但档案成功时，档案仍然渲染且不显示错误', async () => {
    fetchTaskTypeCorrectionStats.mockRejectedValue(new Error('boom-stats'))
    const w = await mountView()
    expect(w.text()).toContain('coding')
    expect(w.text()).not.toContain('boom-stats')
  })

  it('★ 两边都失败才显示错误', async () => {
    fetchTaskProfile.mockRejectedValue(new Error('boom-profile'))
    fetchTaskTypeCorrectionStats.mockRejectedValue(new Error('boom-stats'))
    const w = await mountView()
    expect(w.text()).toContain('boom-profile')
  })

  it('★ 失败时错误原文透出，不退化成空列表', async () => {
    fetchTaskProfile.mockRejectedValue(new Error('Database not available'))
    fetchTaskTypeCorrectionStats.mockRejectedValue(new Error('Database not available'))
    const w = await mountView()
    expect(w.text()).toContain('Database not available')
  })
})

describe('任务档案视图 · 筛选', () => {
  it('★ 输入类型名后只剩匹配的档案', async () => {
    const w = await mountView()
    const input = w.find('input.tp__input')
    await input.setValue('coding')
    expect(w.text()).toContain('coding')
    expect(w.text()).not.toContain('mystery-task')
  })

  it('★ 按描述筛选也生效', async () => {
    const w = await mountView()
    await w.find('input.tp__input').setValue('Feature implementation')
    expect(w.text()).toContain('coding')
    expect(w.text()).not.toContain('mystery-task')
  })

  it('★ 按档位筛选也生效', async () => {
    const w = await mountView()
    await w.find('input.tp__input').setValue('tier-b')
    expect(w.text()).toContain('coding')
  })

  it('★ 无匹配时给空态提示', async () => {
    const w = await mountView()
    await w.find('input.tp__input').setValue('zzz-不存在')
    expect(w.text()).toContain('没有匹配的类型')
  })

  it('★ 筛选只影响档案段，不影响修正记录段', async () => {
    const w = await mountView()
    await w.find('input.tp__input').setValue('coding')
    expect(w.text()).toContain('req-8f21c0ab')
  })
})