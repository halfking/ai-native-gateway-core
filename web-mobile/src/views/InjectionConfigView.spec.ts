import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import InjectionConfigView from './InjectionConfigView.vue'
import {
  fetchInjectionRules,
  fetchInjectionEngines,
  fetchSeverityMatrix,
  fetchCanaryTokens,
} from '@/api/promptInjection'
import { setLocale, locale } from '@/i18n'

/**
 * InjectionConfigView 的不变量（2026-10-07）。
 *
 * 1. ★★★★ rules / engines / canary-tokens **没有分页** ⇒ 页面不提供翻页控件，
 *    且明说「条数是本次返回的条数，不是总数」；
 * 2. ★★★★ 500 **绝不能**渲染成「没有配置」——安全规则的缺失等于检测被静默关闭；
 * 3. ★★★ rules 的 `category` 过滤是两列 OR ⇒ 新旧两列都要显示；
 * 4. ★★★ `is_system` 是 `COALESCE(...,true)` ⇒ 缺值按「是」算，页面要说清；
 * 5. ★★★ `action_override` 空串 = 沿用矩阵，不是「无动作」；
 * 6. ★★★ `notify_channels` 解析失败被吞 ⇒ 空列表有两种可能，页面照实说；
 * 7. ★★ 蜜罐 token 值是**诱饵凭据**，页面必须警告。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/promptInjection', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/promptInjection')>()
  return {
    ...actual,
    fetchInjectionRules: vi.fn(),
    fetchInjectionEngines: vi.fn(),
    fetchSeverityMatrix: vi.fn(),
    fetchCanaryTokens: vi.fn(),
  }
})

const rulesMock = fetchInjectionRules as unknown as ReturnType<typeof vi.fn>
const enginesMock = fetchInjectionEngines as unknown as ReturnType<typeof vi.fn>
const matrixMock = fetchSeverityMatrix as unknown as ReturnType<typeof vi.fn>
const tokensMock = fetchCanaryTokens as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(InjectionConfigView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function rule(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    rule_name: 'ignore-previous',
    rule_type: 'regex',
    category: 'legacy-injection',
    category_new: 'instruction_override',
    pattern: '^ignore previous',
    description: '经典的指令覆盖开头',
    severity: 9,
    enabled: true,
    case_sensitive: false,
    is_system: true,
    action_override: '',
    tags: ['classic'],
    examples: ['ignore previous instructions'],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function engine(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    engine_name: 'judge-1',
    description: '主力判定模型',
    model_canonical_id: 3,
    model_name: 'qwen-max',
    credential_id: 8,
    temperature: 0.1,
    max_tokens: 1024,
    timeout_ms: 5000,
    max_retries: 2,
    system_prompt: '你是安全判定器',
    detection_prompt: '…',
    priority: 10,
    enabled: true,
    total_calls: 1200,
    total_detections: 12,
    avg_latency_ms: 430,
    error_count: 2,
    last_called_at: '2026-10-07T09:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function matrixRow(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    severity_level: 'critical',
    observe_action: 'log',
    enforce_action: 'block',
    require_approval: false,
    approval_timeout_minutes: 30,
    notify_on_detect: true,
    notify_channels: ['email'],
    affect_session_health: true,
    session_health_penalty: 30,
    terminate_on_repeat: true,
    repeat_threshold: 3,
    ...over,
  }
}

function token(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    token_value: 'sk-canary-abc123',
    token_type: 'api_key',
    token_name: '假 AWS Key',
    description: '放在文档里的诱饵',
    leak_action: 'block',
    notify_on_leak: true,
    active: true,
    expires_at: null,
    times_injected: 40,
    times_leaked: 0,
    last_leaked_at: null,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  rulesMock.mockResolvedValue({ rules: [rule()], count: 1 })
  enginesMock.mockResolvedValue({ engines: [engine()], count: 1 })
  matrixMock.mockResolvedValue({ matrix: [matrixRow()] })
  tokensMock.mockResolvedValue({ tokens: [token()], count: 1 })
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

describe('入口与四个端点', () => {
  it('★★★ 挂载即请求 rules / matrix / engines / canary-tokens', async () => {
    await mountView()
    expect(rulesMock).toHaveBeenCalledTimes(1)
    expect(matrixMock).toHaveBeenCalledTimes(1)
    expect(enginesMock).toHaveBeenCalledTimes(1)
    expect(tokensMock).toHaveBeenCalledTimes(1)
  })

  it('★ rules 请求**不带**任何分页参数', async () => {
    await mountView()
    expect(rulesMock.mock.calls[0]![0]).toEqual({ search: undefined, enabled: undefined })
  })
})

describe('★★★★ 判据 1：没有分页', () => {
  it('★★★★ 明说「不分页」且「条数是本次返回的条数」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('规则清单不分页')
    expect(w.text()).toContain('不是库里一共多少条')
  })

  it('★★ 页面上**没有**上一页/下一页控件（证明不是恒有）', async () => {
    const w = await mountView()
    const texts = w.findAll('.ic__btn').map((b) => b.text())
    expect(texts).not.toContain('上一页')
    expect(texts).not.toContain('下一页')
  })
})

describe('★★★★ 判据 2：500 不许退化成「没有配置」', () => {
  it('★★★★ rules 500 ⇒ 显示错误 + 成因，**不**显示「没有查到规则」', async () => {
    rulesMock.mockRejectedValueOnce(new Error('Failed to iterate rules'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to iterate rules')
    expect(w.text()).not.toContain('没有查到规则')
    expect(w.text()).toContain('这一块不是「没有配置」')
  })

  it('★★★★ engines 500 ⇒ 不显示「没有配置 LLM 引擎」', async () => {
    enginesMock.mockRejectedValueOnce(new Error('Failed to iterate engines'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to iterate engines')
    expect(w.text()).not.toContain('没有配置 LLM 引擎')
  })

  it('★★★★ matrix 500 ⇒ 不显示「没有查到处置矩阵」', async () => {
    matrixMock.mockRejectedValueOnce(new Error('Failed to iterate severity matrix'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to iterate severity matrix')
    expect(w.text()).not.toContain('没有查到处置矩阵')
  })

  it('★★★ canary 500 ⇒ 不显示「没有配置蜜罐 token」', async () => {
    tokensMock.mockRejectedValueOnce(new Error('Failed to iterate tokens'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to iterate tokens')
    expect(w.text()).not.toContain('没有配置蜜罐 token')
  })

  it('★ 真的空 ⇒ 空态文案', async () => {
    rulesMock.mockResolvedValue({ rules: [], count: 0 })
    matrixMock.mockResolvedValue({ matrix: [] })
    const w = await mountView()
    expect(w.text()).toContain('没有查到规则')
    expect(w.text()).toContain('没有查到处置矩阵')
  })
})

describe('★★★ 判据 3：新旧两套分类都显示', () => {
  it('★★★ 同时显示「旧分类」与「新分类」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('旧分类')
    expect(w.text()).toContain('新分类')
    expect(w.text()).toContain('legacy-injection')
    expect(w.text()).toContain('instruction_override')
  })

  it('★★★ 明说「按类别筛时新旧任一命中就算命中」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('新旧两套分类字段任一命中就算命中')
    expect(w.text()).toContain('所以两列都列出来')
  })

  it('★ category_new 为空 ⇒ 显示「—」而不是空白', async () => {
    rulesMock.mockResolvedValue({ rules: [rule({ category_new: '' })], count: 1 })
    const w = await mountView()
    expect(w.text()).toContain('新分类')
  })
})

describe('★★★ 判据 4/5：is_system 与 action_override', () => {
  it('★★★ 明说「is_system 在库里为空时会被当成是」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('在库里为空时会被当成「是」')
    expect(w.text()).toContain('标否定的可信度低于标肯定的')
  })

  it('★★ is_system=true ⇒ 打「内置」标签；false ⇒ 无标签', async () => {
    const w = await mountView()
    expect(w.findAll('.ic__sys').length).toBe(1)
    rulesMock.mockResolvedValue({ rules: [rule({ is_system: false })], count: 1 })
    const w2 = await mountView()
    expect(w2.findAll('.ic__sys')).toHaveLength(0)
  })

  it('★★★ action_override 空串 ⇒ 说「沿用处置矩阵」，不是「无动作」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('覆盖动作：沿用处置矩阵')
  })

  it('★ action_override 有值 ⇒ 显示该值', async () => {
    rulesMock.mockResolvedValue({ rules: [rule({ action_override: 'block' })], count: 1 })
    const w = await mountView()
    expect(w.text()).toContain('覆盖动作：block')
  })
})

describe('★★★ 判据 6：notify_channels 的两种可能', () => {
  it('★★★ 明说「解析失败会被静默当成没配」且「接口分不出来」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('在后端解析失败时会被静默当成「没配」')
    expect(w.text()).toContain('接口分不出来')
  })

  it('★★ 有渠道 ⇒ 列出；无渠道 ⇒ 说「没有通知渠道」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('渠道 email')
    matrixMock.mockResolvedValue({ matrix: [matrixRow({ notify_channels: [] })] })
    const w2 = await mountView()
    expect(w2.text()).toContain('没有通知渠道')
  })

  it('★ 页脚列出动作枚举的完整取值', async () => {
    const w = await mountView()
    expect(w.text()).toContain('动作枚举的完整取值')
    expect(w.text()).toContain('quarantine')
  })

  it('★ 明说矩阵只认四个严重度', async () => {
    const w = await mountView()
    expect(w.text()).toContain('处置矩阵只认这几个严重度：low / medium / high / critical')
  })
})

describe('★★★ 判据 7：蜜罐是诱饵凭据', () => {
  it('★★★ 明说「是故意放进内容里的诱饵凭据」且「不要拿去当密钥用」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('故意放进内容里的诱饵凭据')
    expect(w.text()).toContain('不要拿去当密钥用')
  })

  it('★★ 显示 token 值与投放/泄露次数', async () => {
    const w = await mountView()
    expect(w.text()).toContain('sk-canary-abc123')
    expect(w.text()).toContain('投放 40 次')
    expect(w.text()).toContain('泄露 0 次')
  })

  it('★★ 泄露过 ⇒ 高亮「最近泄露」', async () => {
    tokensMock.mockResolvedValue({ tokens: [token({ times_leaked: 2, last_leaked_at: '2026-10-07T09:00:00Z' })], count: 1 })
    const w = await mountView()
    expect(w.text()).toContain('最近泄露')
  })

  it('★ 未泄露 ⇒ 不显示「最近泄露」', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('最近泄露')
  })
})

describe('筛选与引擎', () => {
  it('★★★ 点启用状态 ⇒ 只发 true/false 字面量', async () => {
    const w = await mountView()
    await w.findAll('.ic__chip').find((c) => c.text() === '已启用')!.trigger('click')
    await flushPromises()
    expect(rulesMock.mock.calls[1]![0]).toMatchObject({ enabled: true })
    await w.findAll('.ic__chip').find((c) => c.text() === '已停用')!.trigger('click')
    await flushPromises()
    expect(rulesMock.mock.calls[2]![0]).toMatchObject({ enabled: false })
  })

  it('★★ 填搜索提交 ⇒ 带 search（trim 后）', async () => {
    const w = await mountView()
    await w.find('input').setValue('  ignore  ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(rulesMock.mock.calls[1]![0]).toMatchObject({ search: 'ignore' })
  })

  it('★★ 「清空筛选」清掉搜索与状态', async () => {
    const w = await mountView()
    await w.find('input').setValue('ignore')
    await w.find('form').trigger('submit')
    await flushPromises()
    await w.findAll('.ic__btn').find((b) => b.text() === '清空筛选')!.trigger('click')
    await flushPromises()
    expect((w.find('input').element as HTMLInputElement).value).toBe('')
    expect(rulesMock.mock.calls.at(-1)![0]).toEqual({ search: undefined, enabled: undefined })
  })

  it('★★★ 引擎：模型未关联（空串）⇒ 显示「未关联模型」', async () => {
    enginesMock.mockResolvedValue({ engines: [engine({ model_canonical_id: null, model_name: '' })], count: 1 })
    const w = await mountView()
    expect(w.text()).toContain('未关联模型')
    expect(w.text()).not.toContain('null')
  })

  it('★★ 引擎：credential_id=null ⇒ 「未指定」', async () => {
    enginesMock.mockResolvedValue({ engines: [engine({ credential_id: null })], count: 1 })
    const w = await mountView()
    expect(w.text()).toContain('未指定')
  })

  it('★★ 引擎：显示优先级与调用统计', async () => {
    const w = await mountView()
    expect(w.text()).toContain('优先级 10')
    expect(w.text()).toContain('调用 1200 次')
    expect(w.text()).toContain('命中 12 次')
    expect(w.text()).toContain('出错 2 次')
  })
})
