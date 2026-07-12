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

async function load() {
  const { default: PromptInjectionConfigPanel } = await import('./PromptInjectionConfigPanel.vue')
  const wrapper = mount(PromptInjectionConfigPanel, {
    global: { plugins: [i18n], stubs: { 'router-link': true } },
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

  it('renders 47-rule hint placeholder; not real data but visible', async () => {
    const wrapper = await load()
    // The component uses rules from API; assert both seeded rules are visible.
    expect(wrapper.findAll('.rule-card')).toHaveLength(2)
  })

  it('toggles a rule and calls toggleRule API', async () => {
    const wrapper = await load()
    // Drive the rule toggle through the script exposure to avoid DOM-order fragility
    // (the policy section also renders switch inputs).
    const vm = wrapper.vm as unknown as {
      rules: PromptInjectionRule[]
      onToggleRule: (rule: PromptInjectionRule, value: boolean) => Promise<void>
    }
    await vm.onToggleRule(vm.rules[0], false)
    expect(toggleRuleMock).toHaveBeenCalledWith(1, false)
  })

  it('persists policy changes via debounced PUT', async () => {
    const wrapper = await load()
    // Mutate policy directly via the script exposure (bypasses v-model / DOM ordering).
    const vm = wrapper.vm as unknown as {
      policy: PromptInjectionPolicy | null
      onToggleRule: (rule: PromptInjectionRule, value: boolean) => Promise<void>
    }
    expect(vm.policy).not.toBeNull()
    vm.policy!.enabled = false
    vm.policy!.detection_mode = 'enforce'
    // Wait for the debounced save
    await new Promise((r) => setTimeout(r, 700))
    await flushPromises()
    // updatePolicyMock may have been called by the initial load's policy watcher too;
    // assert the LATEST call carries the mutation.
    expect(updatePolicyMock).toHaveBeenCalled()
    const sent = updatePolicyMock.mock.calls.at(-1)![0]
    expect(sent.enabled).toBe(false)
    expect(sent.detection_mode).toBe('enforce')
  })

  it('navigates to full config page when "open full" is clicked', async () => {
    const wrapper = await load()
    const openBtn = wrapper.find('.action-bar .btn-primary')
    await openBtn.trigger('click')
    expect(pushMock).toHaveBeenCalledWith('/admin/prompt-injection')
  })

  it('filters rules by category', async () => {
    const wrapper = await load()
    const chips = wrapper.findAll('.filter-chips .chip-btn')
    expect(chips.length).toBeGreaterThanOrEqual(5)
    // Click "Custom only"
    const customChip = chips.find((c) => c.text().includes('自定义'))
    expect(customChip).toBeDefined()
    await customChip!.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.rule-card')).toHaveLength(1)
    expect(wrapper.text()).toContain('custom_x')
  })
})