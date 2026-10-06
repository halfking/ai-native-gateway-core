import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CompliancePolicyView from './CompliancePolicyView.vue'
import { fetchCompliancePolicy, fetchComplianceKeywords } from '@/api/outputCompliance'
import { setLocale, locale } from '@/i18n'

/**
 * CompliancePolicyView 的不变量（2026-10-07）。
 *
 * 1. ★★★★★★ **合成默认策略必须被识别并标出**（后端 ErrNoRows 时返回默认值，不是 404）；
 * 2. ★★★★ 阈值与采样率是 0..1 ⇒ 必须换算成百分比；
 * 3. ★★★★★ keywords 的 500 **绝不能**渲染成「没有配置违禁词」；
 * 4. ★★ `llm_engine_id` 键一定存在但值可能为 null ⇒ 显示「未指定」；
 * 5. ★★ 开关的「关」要看得见（不能只列开的）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/outputCompliance', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/outputCompliance')>()
  return { ...actual, fetchCompliancePolicy: vi.fn(), fetchComplianceKeywords: vi.fn() }
})

const policyMock = fetchCompliancePolicy as unknown as ReturnType<typeof vi.fn>
const kwMock = fetchComplianceKeywords as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(CompliancePolicyView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** 后端 `defaultOutputCompliancePolicy` 的形状：id=0、时间空、policy_name='default'。 */
function syntheticDefault() {
  return {
    id: 0,
    policy_name: 'default',
    enabled: true,
    enforcement_mode: 'observe',
    llm_engine_id: null,
    check_pii: true,
    check_toxicity: true,
    check_bias: false,
    check_hallucination: false,
    check_secrets: true,
    check_internal_ip: true,
    check_jailbreak_response: false,
    check_instruction_injection_response: false,
    pii_threshold: 0.7,
    toxicity_threshold: 0.7,
    bias_threshold: 0.6,
    hallucination_threshold: 0.7,
    secrets_threshold: 0.7,
    internal_ip_threshold: 0.7,
    auto_redact: true,
    redact_email: true,
    redact_phone: true,
    redact_id_card: true,
    redact_credit_card: true,
    redact_bank_card: false,
    redact_jwt: true,
    redact_password: true,
    toxic_replacement: '[内容已过滤]',
    block_message: '响应因合规策略被阻断',
    strict_mode: false,
    whitelist_keywords: [],
    realtime_alert_enabled: false,
    alert_threshold_severity: 7,
    alert_aggregation_window_minutes: 5,
    sampling_rate: 1,
    auto_review_queue_enabled: false,
    feedback_loop_enabled: false,
    skill_generation_enabled: false,
    auto_threshold_tuning_enabled: false,
    retention_days: 90,
    total_detections: 0,
    total_blocks: 0,
    last_detection_at: null,
    created_at: '',
    updated_at: '',
  }
}

function storedPolicy(over: Record<string, unknown> = {}) {
  return { ...syntheticDefault(), id: 7, policy_name: 'prod', enforcement_mode: 'enforce', created_at: '2026-01-01T00:00:00Z', ...over }
}

function kw(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    keyword: '内部代号',
    category: 'custom',
    severity: 8,
    action: 'block',
    enabled: true,
    description: '产品名',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  policyMock.mockResolvedValue(storedPolicy())
  kwMock.mockResolvedValue({ keywords: [kw()] })
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('入口', () => {
  it('★★★ 挂载即请求 policy 与 keywords（互不依赖）', async () => {
    await mountView()
    expect(policyMock).toHaveBeenCalledTimes(1)
    expect(kwMock).toHaveBeenCalledTimes(1)
    expect(kwMock.mock.calls[0]![0]).toEqual({ category: undefined })
  })

  it('★ 真实策略行显示 policy_name 与 enforcement_mode', async () => {
    const w = await mountView()
    expect(w.text()).toContain('prod')
    expect(w.text()).toContain('enforce')
  })
})

describe('★★★★★★ 判据 1：合成默认策略', () => {
  it('★★★★★★ id=0 ⇒ 明说「数据库里没有策略行，这是内置默认」', async () => {
    policyMock.mockResolvedValue(syntheticDefault())
    const w = await mountView()
    expect(w.text()).toContain('数据库里没有这个租户的策略行')
    expect(w.text()).toContain('不是你们的配置')
  })

  it('★★★★★★ 合成时**不**显示「这条是数据库里存的」', async () => {
    policyMock.mockResolvedValue(syntheticDefault())
    const w = await mountView()
    expect(w.text()).not.toContain('数据库里存着的策略记录')
  })

  it('★★★ 真实行 ⇒ 显示「这条是存着的」且无警告', async () => {
    const w = await mountView()
    expect(w.text()).toContain('这一条是数据库里存着的策略记录')
    expect(w.text()).not.toContain('不是你们的配置')
  })

  it('★★★ 合成默认的字段值仍照实渲染（observe / 阈值 0.7）', async () => {
    policyMock.mockResolvedValue(syntheticDefault())
    const w = await mountView()
    expect(w.text()).toContain('observe')
    expect(w.text()).toContain('70.00%')
  })
})

describe('★★★★ 判据 2：阈值 0..1 ⇒ 百分比', () => {
  it('★★★★ 六个阈值 + 采样率都换算成百分比', async () => {
    const w = await mountView()
    // pii/toxicity/secrets/internal_ip/hallucination = 0.70，bias = 0.60，采样率 = 1.00
    const hits = w.text().split('70.00%').length - 1
    expect(hits).toBe(5)
    expect(w.text()).toContain('60.00%')
    expect(w.text()).toContain('100.00%')
  })

  it('★★★ 明说「原始值是 0 到 1，这里已换算成百分比」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('原始值是 0 到 1 之间的小数')
    expect(w.text()).toContain('换算成百分比')
  })

  it('★ 页面**不**出现裸的 0.7（说明确实换算了）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('0.7')
    expect(w.text()).not.toContain('700')
  })
})

describe('★★★★★ 判据 3：keywords 的 500 不许变空态', () => {
  it('★★★★★ 500 ⇒ 显示错误 + 成因，**不**显示「没有配置违禁词」', async () => {
    kwMock.mockRejectedValueOnce(new Error('Failed to scan keyword'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to scan keyword')
    expect(w.text()).not.toContain('没有配置自定义违禁词')
    expect(w.text()).toContain('只要有一行备注是空值，整个词库接口就会失败')
  })

  it('★★ policy 失败不影响词库', async () => {
    policyMock.mockRejectedValueOnce(new Error('Failed to get policy'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to get policy')
    expect(w.text()).toContain('内部代号')
  })

  it('★★ keywords 失败也不影响策略', async () => {
    kwMock.mockRejectedValueOnce(new Error('kw boom'))
    const w = await mountView()
    expect(w.text()).toContain('kw boom')
    expect(w.text()).toContain('prod')
  })

  it('★ 真的空词库 ⇒ 空态文案', async () => {
    kwMock.mockResolvedValue({ keywords: [] })
    const w = await mountView()
    expect(w.text()).toContain('没有配置自定义违禁词')
  })
})

describe('★★ 判据 4/5：字段降级与开关', () => {
  it('★★ llm_engine_id = null ⇒ 「未指定」（不是 0、不是空）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('未指定')
    expect(w.text()).not.toContain('undefined')
  })

  it('★★ llm_engine_id 有值 ⇒ 显示该值', async () => {
    policyMock.mockResolvedValue(storedPolicy({ llm_engine_id: 42 }))
    const w = await mountView()
    expect(w.text()).toContain('42')
  })

  it('★★★ 关闭的检查项**也要出现**（灰的），不是只列开着的', async () => {
    const w = await mountView()
    // 默认策略里 check_bias / check_hallucination / check_jailbreak_response / check_instruction_injection 是 false
    const off = w.findAll('.cp__flag--off').map((e) => e.text())
    expect(off).toContain('偏见')
    expect(off).toContain('幻觉')
    expect(off).toContain('越狱应答')
    expect(off).toContain('指令注入应答')
    // 脱敏侧：redact_bank_card 是 false
    expect(off).toContain('银行卡')
  })

  it('★★ 开启的检查项不在 off 列表里', async () => {
    const w = await mountView()
    const off = w.findAll('.cp__flag--off').map((e) => e.text())
    expect(off).not.toContain('隐私')
    expect(off).not.toContain('密钥')
  })

  it('★★ 白名单为空 ⇒ 不渲染白名单说明', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('白名单词')
  })

  it('★ 白名单非空 ⇒ 显示条数', async () => {
    policyMock.mockResolvedValue(storedPolicy({ whitelist_keywords: ['a', 'b'] }))
    const w = await mountView()
    expect(w.text()).toContain('白名单词 2 个')
  })
})

describe('词库行渲染', () => {
  it('★★★ 显示词、类别、严重度、动作', async () => {
    const w = await mountView()
    expect(w.text()).toContain('内部代号')
    expect(w.text()).toContain('类别 custom')
    expect(w.text()).toContain('严重度 8')
    expect(w.text()).toContain('block')
  })

  it('★★ enabled=false ⇒ 打「已停用」标签', async () => {
    kwMock.mockResolvedValue({ keywords: [kw({ enabled: false })] })
    const w = await mountView()
    expect(w.findAll('.cp__off').length).toBe(1)
  })

  it('★★ 开启时无「已停用」标签（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.findAll('.cp__off')).toHaveLength(0)
  })

  it('★★ description 缺失 ⇒ 不渲染备注行（不是空白）', async () => {
    const r = kw()
    delete (r as Record<string, unknown>).description
    kwMock.mockResolvedValue({ keywords: [r] })
    const w = await mountView()
    expect(w.text()).not.toContain('产品名')
    expect(w.text()).not.toContain('undefined')
  })

  it('★★★ 填类别提交 ⇒ 带 category 重查；清除后回到无过滤', async () => {
    const w = await mountView()
    await w.find('input').setValue('  custom  ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(kwMock.mock.calls[1]![0]).toEqual({ category: 'custom' })
    await w.findAll('.cp__btn').find((b) => b.text() === '清除类别筛选')!.trigger('click')
    await flushPromises()
    expect((w.find('input').element as HTMLInputElement).value).toBe('')
    expect(kwMock.mock.calls[2]![0]).toEqual({ category: undefined })
  })
})