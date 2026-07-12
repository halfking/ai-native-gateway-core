// PromptInjectionConfigPanel.test.ts
//
// Unit tests for the embedded prompt-injection config panel.
// Mirrors the style of CompressionConfigPanel.test.ts:
//   - mock the api/promptInjection module
//   - render the component with a minimal i18n instance
//   - assert API calls + DOM state after user interaction
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { PromptInjectionRule, PromptInjectionPolicy } from '../api/promptInjection'

const getPolicyMock = vi.fn()
const listRulesMock = vi.fn()
const updatePolicyMock = vi.fn()
const toggleRuleMock = vi.fn()
const updateRuleMock = vi.fn()

vi.mock('../api/promptInjection', () => ({
  getPolicy: (...args: any[]) => getPolicyMock(...args),
  listRules: (...args: any[]) => listRulesMock(...args),
  updatePolicy: (...args: any[]) => updatePolicyMock(...args),
  toggleRule: (...args: any[]) => toggleRuleMock(...args),
  updateRule: (...args: any[]) => updateRuleMock(...args),
  CATEGORIES: [],
  getCategoryMeta: (raw: string) => ({ tagType: '', i18nKey: 'promptInjectionCategoryUnknown', zhFallback: raw }),
  getSeverityTagType: (s: number) => (s >= 9 ? 'danger' : s >= 7 ? 'warning' : s >= 5 ? 'info' : 'success'),
}))

const pushMock = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: pushMock }),
}))

const ElMessageMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }
vi.mock('element-plus', () => ({
  ElMessage: ElMessageMock,
}))

// Stub element-plus el-tag to silence component-resolution warnings in tests.
const globalStubs = { 'el-tag': { template: '<span class="el-tag-stub"><slot /></span>' } }

const messages = {
  'zh-CN': {
    common: { save: '保存' },
    sessions: {
      config: {
        loading: '加载中',
        tenantScope: '租户',
        promptInjectionOpenFull: '打开完整配置',
        promptInjectionSubtitle: '副标题',
        promptInjectionPolicyTitle: '检测策略',
        promptInjectionPolicyHint: '策略说明',
        promptInjectionFieldEnabled: '启用提示词注入检测',
        promptInjectionFieldEnabledHint: '总开关说明',
        promptInjectionFieldMode: '检测模式',
        promptInjectionFieldModeHint: '观察 vs 强制说明',
        promptInjectionFieldModeObserve: '观察模式',
        promptInjectionFieldModeEnforce: '强制模式',
        promptInjectionSectionLayers: '检测层级',
        promptInjectionFieldBasic: '基础规则',
        promptInjectionFieldBasicHint: '基础说明',
        promptInjectionFieldAdvanced: '高级规则',
        promptInjectionFieldAdvancedHint: '高级说明',
        promptInjectionFieldHeuristics: '启发式检测',
        promptInjectionFieldHeuristicsHint: '启发式说明',
        promptInjectionFieldLLM: 'LLM 检测',
        promptInjectionFieldLLMHint: 'LLM 说明',
        promptInjectionFieldCanary: 'Canary',
        promptInjectionFieldCanaryHint: 'Canary 说明',
        promptInjectionFieldVector: '向量相似度',
        promptInjectionFieldVectorHint: '向量说明',
        promptInjectionSectionThresholds: '分数阈值',
        promptInjectionFieldThresholdLog: '记录阈值',
        promptInjectionFieldThresholdWarn: '警告阈值',
        promptInjectionFieldThresholdSanitize: '清洗阈值',
        promptInjectionFieldThresholdBlock: '阻断阈值',
        promptInjectionFieldThresholdHint: '阈值说明',
        promptInjectionThresholdInvalid: '阈值无效：必须满足 记录 ≤ 警告 ≤ 清洗 ≤ 阻断',
        promptInjectionStatsSummary: '共 {total} 条规则，已启用 {enabled} 条',
        promptInjectionModuleDisabled: '模块未启用，配置不会生效',
        promptInjectionEmptyFiltered: '没有匹配的规则',
        promptInjectionRulesTitle: '检测规则（{count}）',
        promptInjectionRulesHint: '规则说明',
        promptInjectionRuleSystem: '系统',
        promptInjectionRuleCustom: '自定义',
        promptInjectionRuleSeverity: '严重度',
        promptInjectionRuleEnabled: '启用',
        promptInjectionRulePattern: '正则',
        promptInjectionRuleExamples: '关键词',
        promptInjectionRuleNoExamples: '无',
        promptInjectionRuleFilterAll: '全部',
        promptInjectionRuleFilterSystem: '系统',
        promptInjectionRuleFilterCustom: '自定义',
        promptInjectionRuleFilterEnabled: '启用',
        promptInjectionRuleFilterDisabled: '禁用',
        promptInjectionRuleSearchPlaceholder: '搜索',
        promptInjectionRuleSaveSuccess: '规则已更新',
        promptInjectionPolicySaveSuccess: '策略已保存',
        promptInjectionLoadError: '加载失败',
        promptInjectionSaveError: '保存失败',
        promptInjectionSectionAdvanced: '高级入口',
        promptInjectionLinkEngines: 'LLM 引擎',
        promptInjectionLinkEnginesHint: 'LLM 引擎说明',
        promptInjectionLinkCanary: 'Canary',
        promptInjectionLinkCanaryHint: 'Canary 说明',
        promptInjectionLinkMatrix: '矩阵',
        promptInjectionLinkMatrixHint: '矩阵说明',
        promptInjectionLinkStats: '统计',
        promptInjectionLinkStatsHint: '统计说明',
        promptInjectionCategoryRoleHijack: '角色劫持',
        promptInjectionCategoryInstructionOverride: '指令覆盖',
        promptInjectionCategoryInstructionLeak: '指令泄漏',
        promptInjectionCategoryJailbreak: '越狱',
        promptInjectionCategoryEncodingBypass: '编码绕过',
        promptInjectionCategoryInjectionMarker: '注入标记',
        promptInjectionCategoryMultiTurnAttack: '多轮攻击',
        promptInjectionCategoryResourceExhaustion: '资源耗尽',
        promptInjectionCategoryDataExfiltration: '数据窃取',
        promptInjectionCategorySocialEngineering: '社会工程',
        promptInjectionCategoryPromptLeaking: '提示词泄漏',
        promptInjectionCategoryPayloadSmuggling: 'Payload走私',
        promptInjectionCategoryUnicodeObfuscation: 'Unicode混淆',
        promptInjectionCategoryContextManipulation: '上下文操纵',
        promptInjectionCategoryToolAbuse: '工具滥用',
        promptInjectionCategoryLegacy: '兼容旧分类',
        promptInjectionCategoryUnknown: '其他',
      },
    },
  },
}

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages,
})

const samplePolicy: PromptInjectionPolicy = {
  id: 1,
  tenant_id: 't1',
  enabled: true,
  detection_mode: 'observe',
  enable_basic_rules: true,
  enable_advanced_rules: true,
  enable_heuristics: true,
  enable_ml_model: false,
  enable_llm_detection: true,
  enable_canary_detection: true,
  enable_vector_similarity: false,
  llm_engine_id: null,
  content_replacement: 'llm_rewrite',
  max_input_length: 50000,
  auto_learn_enabled: false,
  detection_timeout_ms: 5000,
  score_threshold_log: 3,
  score_threshold_warn: 6,
  score_threshold_sanitize: 8,
  score_threshold_block: 10,
  action_on_low_risk: 'log',
  action_on_medium_risk: 'warn',
  action_on_high_risk: 'block',
  whitelist_patterns: [],
  whitelist_users: [],
  notify_on_detection: false,
  notification_webhook: '',
  notification_email: '',
  total_detections: 0,
  total_blocks: 0,
  last_detection_at: null,
  created_at: '',
  updated_at: '',
}

const sampleRules: PromptInjectionRule[] = [
  {
    id: 1, rule_name: 'role_hijack_you_are_now', rule_type: 'basic',
    category: 'role_hijack', category_new: 'role_hijack',
    pattern: '(?i)you are now (a|an) admin',
    description: '尝试切换模型角色为特权用户',
    severity: 10, enabled: true, case_sensitive: false,
    is_system: true, action_override: '',
    tags: [], examples: ['you are now an admin'],
    created_at: '', updated_at: '',
  },
  {
    id: 2, rule_name: 'custom_x', rule_type: 'advanced',
    category: 'bypass', category_new: '',
    pattern: '(?i)foo bar',
    description: '自定义规则示例',
    severity: 5, enabled: false, case_sensitive: false,
    is_system: false, action_override: '',
    tags: [], examples: [],
    created_at: '', updated_at: '',
  },
]

async function load(props: Record<string, unknown> = {}) {
  const { default: PromptInjectionConfigPanel } = await import('./PromptInjectionConfigPanel.vue')
  const wrapper = mount(PromptInjectionConfigPanel, {
    global: { plugins: [i18n], stubs: { ...globalStubs, 'router-link': true } },
    props,
  })
  await flushPromises()
  return wrapper
}

describe('PromptInjectionConfigPanel', () => {
  beforeEach(() => {
    getPolicyMock.mockReset()
    listRulesMock.mockReset()
    updatePolicyMock.mockReset()
    toggleRuleMock.mockReset()
    updateRuleMock.mockReset()
    pushMock.mockReset()
    ElMessageMock.success.mockReset()
    ElMessageMock.error.mockReset()

    getPolicyMock.mockResolvedValue({ ...samplePolicy })
    listRulesMock.mockResolvedValue({ rules: sampleRules, count: sampleRules.length })
    updatePolicyMock.mockResolvedValue({ message: 'ok', policy_id: 1 })
    toggleRuleMock.mockResolvedValue({ message: 'ok' })
    updateRuleMock.mockResolvedValue({ message: 'ok' })
  })

  it('loads policy + rules and renders them', async () => {
    const wrapper = await load()
    expect(getPolicyMock).toHaveBeenCalled()
    expect(listRulesMock).toHaveBeenCalled()
    expect(wrapper.text()).toContain('role_hijack_you_are_now')
    expect(wrapper.text()).toContain('检测策略')
  })

  it('renders both seeded rules', async () => {
    const wrapper = await load()
    expect(wrapper.findAll('.rule-card')).toHaveLength(2)
  })

  it('does NOT auto-save on initial policy load', async () => {
    await load()
    // Initial load sets policy + rules. The watch must skip this assignment
    // to avoid an unnecessary PUT right after GET.
    await new Promise((r) => setTimeout(r, 700))
    expect(updatePolicyMock).not.toHaveBeenCalled()
  })

  it('toggles a rule and calls toggleRule API', async () => {
    const wrapper = await load()
    const vm = wrapper.vm as unknown as {
      rules: PromptInjectionRule[]
      onToggleRule: (rule: PromptInjectionRule, value: boolean) => Promise<void>
    }
    await vm.onToggleRule(vm.rules[0], false)
    expect(toggleRuleMock).toHaveBeenCalledWith(1, false)
  })

  it('reverts a rule toggle on API failure', async () => {
    const wrapper = await load()
    toggleRuleMock.mockRejectedValueOnce(new Error('boom'))
    const vm = wrapper.vm as unknown as {
      rules: PromptInjectionRule[]
      onToggleRule: (rule: PromptInjectionRule, value: boolean) => Promise<void>
    }
    const rule = vm.rules[0]
    const before = rule.enabled
    await vm.onToggleRule(rule, !before)
    expect(rule.enabled).toBe(before)
    expect(ElMessageMock.error).toHaveBeenCalledWith(expect.stringContaining('boom'))
  })

  it('persists policy changes via debounced PUT', async () => {
    const wrapper = await load()
    const vm = wrapper.vm as unknown as {
      policy: PromptInjectionPolicy | null
    }
    expect(vm.policy).not.toBeNull()
    vm.policy!.enabled = false
    vm.policy!.detection_mode = 'enforce'
    await new Promise((r) => setTimeout(r, 700))
    await flushPromises()
    expect(updatePolicyMock).toHaveBeenCalled()
    const sent = (updatePolicyMock.mock.calls as any[])[(updatePolicyMock.mock.calls as any[]).length - 1][0]
    expect(sent.enabled).toBe(false)
    expect(sent.detection_mode).toBe('enforce')
  })

  it('debounces severity updates per rule', async () => {
    const wrapper = await load()
    const vm = wrapper.vm as unknown as {
      rules: PromptInjectionRule[]
      scheduleSeveritySave: (rule: PromptInjectionRule, value: number) => void
    }
    vm.scheduleSeveritySave(vm.rules[0], 3)
    vm.scheduleSeveritySave(vm.rules[0], 5)
    vm.scheduleSeveritySave(vm.rules[0], 7)
    await new Promise((r) => setTimeout(r, 700))
    expect(updateRuleMock).toHaveBeenCalledTimes(1)
    expect(updateRuleMock).toHaveBeenCalledWith(1, { severity: 7 })
  })

  it('navigates to full config page when "open full" is clicked', async () => {
    const wrapper = await load()
    const openBtn = wrapper.find('.action-bar .btn-primary')
    await openBtn.trigger('click')
    expect(pushMock).toHaveBeenCalledWith('/admin/prompt-injection')
  })

  it('navigates to advanced tab via router.push on link click', async () => {
    const wrapper = await load()
    const cards = wrapper.findAll('.link-card')
    expect(cards.length).toBe(4)
    await cards[0].trigger('click')
    expect(pushMock).toHaveBeenCalledWith('/admin/prompt-injection?tab=engines')
  })

  it('filters rules by category (custom only)', async () => {
    const wrapper = await load()
    const chips = wrapper.findAll('.filter-chips .chip-btn')
    expect(chips.length).toBeGreaterThanOrEqual(5)
    const customChip = chips.find((c) => c.text().includes('自定义'))
    expect(customChip).toBeDefined()
    await customChip!.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.rule-card')).toHaveLength(1)
    expect(wrapper.text()).toContain('custom_x')
  })

  it('shows "no matching rules" when search returns nothing', async () => {
    const wrapper = await load()
    const search = wrapper.find('input[type=search]')
    await search.setValue('zzz_no_match_zzz')
    await flushPromises()
    expect(wrapper.text()).toContain('没有匹配的规则')
  })

  it('flags invalid thresholds', async () => {
    const wrapper = await load()
    const vm = wrapper.vm as unknown as { policy: PromptInjectionPolicy | null }
    // Force log=8 > warn=5 (out of order)
    vm.policy!.score_threshold_log = 8
    vm.policy!.score_threshold_warn = 5
    await flushPromises()
    expect(wrapper.text()).toContain('阈值无效')
  })

  it('shows module-disabled banner when moduleEnabled=false', async () => {
    const wrapper = await load({ moduleEnabled: false })
    expect(wrapper.text()).toContain('模块未启用')
    expect(wrapper.find('.banner-warn').exists()).toBe(true)
  })

  it('hides module-disabled banner by default (moduleEnabled=true)', async () => {
    const wrapper = await load()
    expect(wrapper.find('.banner-warn').exists()).toBe(false)
  })
})