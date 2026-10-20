// PromptInjectionSettingsView.test.ts
//
// ★ 这道门存在的理由（2026-10-20 审计轮）：
//
//   1) **PUT /rules/{id} 过去是「全量覆盖」** —— 后端把 body 解码成值类型结构体
//      后无条件 SET 八列，于是**任何部分载荷都会把未传的字段清零**。
//      现存爆炸点不是理论上的：PromptInjectionConfigPanel 的严重度滑杆调
//      updateRule(id, { severity })，对自定义规则会把 pattern/description 清空、
//      enabled 置 false；系统规则则整条 404。
//      后端已改成 PATCH 语义（指针字段 + 动态 SET），本门钉住**前端只发变化的字段**。
//
//   2) **规则详情入口此前完全不存在** —— rules tab 里没有任何查看/编辑的入口，
//      唯一的行内写操作是 enabled 开关，唯一另一个动作是删除。
//      需求是「点击检查规则详情并修改保存」。
//
// 判据纪律：本门驱动**被测组件本身**（不是复刻一份逻辑），
// 且判据是**正向**的（「详情按钮存在」「保存体只含变化字段」），
// 不是「没坏」式的否定断言 —— 后者在「干脆没做」面前永远是绿的。
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, type PropType } from 'vue'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const mocks = vi.hoisted(() => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  listRules: vi.fn(),
  getRule: vi.fn(),
  createRule: vi.fn(),
  deleteRule: vi.fn(),
  toggleRule: vi.fn(),
  updateRule: vi.fn(),
  listEngines: vi.fn(),
  getEngine: vi.fn(),
  createEngine: vi.fn(),
  updateEngine: vi.fn(),
  removeEngine: vi.fn(),
  listCanaryTokens: vi.fn(),
  createCanaryToken: vi.fn(),
  updateCanaryToken: vi.fn(),
  removeCanaryToken: vi.fn(),
  getSeverityMatrix: vi.fn(),
  updateSeverityMatrix: vi.fn(),
  listStats: vi.fn(),
  listDetections: vi.fn(),
  listModels: vi.fn(),
}))

vi.mock('../api/promptInjection', () => ({
  getPolicy: mocks.getPolicy,
  updatePolicy: mocks.updatePolicy,
  listRules: mocks.listRules,
  getRule: mocks.getRule,
  createRule: mocks.createRule,
  deleteRule: mocks.deleteRule,
  toggleRule: mocks.toggleRule,
  updateRule: mocks.updateRule,
  listEngines: mocks.listEngines,
  getEngine: mocks.getEngine,
  createEngine: mocks.createEngine,
  updateEngine: mocks.updateEngine,
  removeEngine: mocks.removeEngine,
  listCanaryTokens: mocks.listCanaryTokens,
  createCanaryToken: mocks.createCanaryToken,
  updateCanaryToken: mocks.updateCanaryToken,
  removeCanaryToken: mocks.removeCanaryToken,
  getSeverityMatrix: mocks.getSeverityMatrix,
  updateSeverityMatrix: mocks.updateSeverityMatrix,
  listStats: mocks.listStats,
  listDetections: mocks.listDetections,
  CATEGORIES: [],
  ACTION_LABELS: { block: '阻断', log: '记录' },
  getCategoryMeta: (raw: string) => ({ tagType: '', i18nKey: 'catUnknown', zhFallback: raw }),
  getSeverityTagType: () => 'info',
}))

vi.mock('../api/models', () => ({ listModels: mocks.listModels }))

const replaceMock = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: replaceMock, push: vi.fn() }),
  useRoute: () => ({ query: { tab: 'rules' } }),
}))

// element-plus 只替换 ElMessage：视图**显式具名导入**了 ElTabs/ElTable/…
// （PromptInjectionSettingsView.vue:953），整体 mock 会让那些具名导入变成
// "No ElTabs export is defined on the mock" ⇒ 渲染直接炸。
vi.mock('element-plus', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ElMessage: mocks.ElMessage,
}))

vi.mock('../composables/useConfirmDialog', () => ({ confirmDialog: vi.fn(async () => true) }))

import PromptInjectionSettingsView from './PromptInjectionSettingsView.vue'

const customRule = {
  id: 41,
  rule_name: 'my-rule',
  rule_type: 'basic',
  category: 'role_hijack',
  category_new: 'role_hijack',
  pattern: 'ignore previous instructions',
  description: 'custom rule',
  severity: 6,
  enabled: true,
  case_sensitive: false,
  is_system: false,
  action_override: '',
  tags: ['ops'],
  examples: ['example one'],
  created_at: '2026-10-01 00:00:00',
  updated_at: '2026-10-02 00:00:00',
}

const systemRule = { ...customRule, id: 7, rule_name: 'system-rule', is_system: true }

// ── element-plus stubs ──────────────────────────────────────────────────────
// el-table 的行数据要送进 el-table-column 的 #default 作用域槽；这里用
// provide/inject 把「当前行」传给列组件，列再以 { row } 喂给作用域槽。
const ElTableRowProvider = defineComponent({
  props: {
    row: { type: Object as PropType<Record<string, unknown>>, default: () => ({}) },
  },
  provide() {
    return { tableRow: this.row }
  },
  template: '<div class="el-table-row-provider"><slot /></div>',
})

const ElTableStub = {
  props: ['data'],
  components: { 'el-table-row-provider': ElTableRowProvider },
  template:
    '<div class="el-table-stub">' +
    '<div v-for="row in data" :key="row.id" class="el-table-row">' +
    '<el-table-row-provider :row="row"><slot /></el-table-row-provider>' +
    '</div></div>',
}

const ElTableColumnStub = {
  inject: { tableRow: { default: null } },
  template: '<div class="el-table-col-stub"><slot :row="tableRow" /></div>',
}

const modelStub = (tag: string) => ({
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `<${tag} class="el-input-stub" :value="modelValue" @input="$emit('update:modelValue', $event.target.value)" />`,
})

const stubs = {
  'el-table': ElTableStub,
  'el-table-column': ElTableColumnStub,
  'el-tabs': { template: '<div class="el-tabs-stub"><slot /></div>' },
  'el-tab-pane': { template: '<div class="el-tab-pane-stub"><slot /></div>' },
  'el-card': { template: '<div class="el-card-stub"><slot name="header" /><slot /></div>' },
  'el-button': {
    emits: ['click'],
    template: '<button class="el-button-stub" @click="$emit(\'click\')"><slot /></button>',
  },
  'el-tag': { template: '<span class="el-tag-stub"><slot /></span>' },
  'el-icon': { template: '<span class="el-icon-stub"><slot /></span>' },
  'el-form': { template: '<div class="el-form-stub"><slot /></div>' },
  'el-form-item': { template: '<div class="el-form-item-stub"><slot /></div>' },
  'el-input': modelStub('input'),
  'el-input-number': modelStub('input'),
  'el-select': {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    template:
      '<select class="el-select-stub" :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>',
  },
  'el-option': { props: ['label', 'value'], template: '<option :value="value">{{ label }}</option>' },
  'el-slider': {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    template:
      '<input class="el-slider-stub" type="range" min="1" max="10" :value="modelValue" @input="$emit(\'update:modelValue\', Number($event.target.value))" />',
  },
  'el-switch': {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    template:
      '<input class="el-switch-stub" type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" />',
  },
  'el-alert': { template: '<div class="el-alert-stub"><slot /></div>' },
  'el-divider': { template: '<hr class="el-divider-stub" />' },
  'el-row': { template: '<div class="el-row-stub"><slot /></div>' },
  'el-col': { template: '<div class="el-col-stub"><slot /></div>' },
  'el-pagination': { template: '<div class="el-pagination-stub" />' },
  'el-statistic': { template: '<div class="el-statistic-stub"><slot /></div>' },
  'el-descriptions': { template: '<div class="el-descriptions-stub"><slot /></div>' },
  'el-descriptions-item': { props: ['label'], template: '<div class="el-descriptions-item-stub">{{ label }}<slot /></div>' },
  AppModal: {
    props: ['modelValue', 'title'],
    template:
      '<div v-if="modelValue" class="app-modal-stub"><div class="app-modal-title">{{ title }}</div><slot /><div class="app-modal-footer"><slot name="footer" /></div></div>',
  },
  PromptInjectionConfigPanel: true,
  EmptyState: { template: '<div class="empty-state-stub"><slot /></div>' },
}

const messages = {
  sessions: {
    promptInjectionFull: {
      loading: '加载中…',
      viewDetail: '查看详情',
      ruleDetailTitle: '规则详情',
      fieldActionOverride: '动作覆盖',
      fieldTags: '标签',
      fieldExamples: '示例',
      fieldUpdatedAt: '更新时间',
      systemRuleReadOnly: '系统规则为只读，仅可启用或停用',
      actionOverrideInherit: '继承策略',
      tagsPlaceholder: '用逗号分隔',
      examplesPlaceholder: '每行一个示例',
      ruleDetailNoChange: '规则没有改动',
      save: '保存',
      cancel: '取消',
      colRuleName: '规则名称',
      colCategory: '风险类别',
      colSeverity: '严重度',
      colDescription: '描述',
      colType: '类型',
      colPattern: '正则',
      colCaseSensitive: '大小写敏感',
      colEnabled: '启用',
      colActions: '操作',
      colTime: '时间',
      delete: '删除',
      rulesTitle: '检测规则',
      addRule: '添加规则',
      rulesEmpty: '暂无规则',
      searchPlaceholder: '搜索',
      systemRule: '系统规则',
      customRule: '自定义规则',
      lockedHint: '系统规则不可删除',
      ruleUpdated: '规则已更新',
      ruleCreated: '规则已创建',
      ruleDeleted: '规则已删除',
      loadRulesFailed: '加载规则失败：{msg}',
      saveFailed: '保存失败：{msg}',
      updateFailed: '更新失败：{msg}',
      createFailed: '创建失败：{msg}',
      deleteFailed: '删除失败：{msg}',
      confirmDelete: '确认删除？',
      confirmTitle: '确认',
      cat: { catUnknown: '未知' },
    },
  },
}

let wrapper: ReturnType<typeof mount> | undefined

async function open(rules: unknown[] = [customRule]) {
  mocks.listRules.mockResolvedValue({ rules, count: rules.length })
  wrapper = mount(PromptInjectionSettingsView, {
    global: {
      plugins: [
        createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': messages } }),
      ],
      stubs,
    },
  })
  await flushPromises()
  return wrapper
}

// 规则详情弹层自己的「保存」按钮。
// ⚠ 视图有 7 个 tab，每个 tab 都可能有「保存」按钮（严重度矩阵就有），
//   DOM 顺序里**第一个**「保存」不是规则详情这个 —— 必须限定在弹层页脚里找。
function modalSaveButton(w: ReturnType<typeof mount>) {
  const btn = w.find('.app-modal-footer').findAll('button').find((b) => b.text() === '保存')
  if (!btn) throw new Error('规则详情弹层里没有「保存」按钮')
  return btn
}

// 找到规则表里那一行的「查看详情」按钮。
function detailButton(w: ReturnType<typeof mount>) {
  const btn = w.findAll('button').find((b) => b.text().includes('查看详情'))
  if (!btn) throw new Error('规则表里没有「查看详情」按钮')
  return btn
}

describe('提示词注入规则详情（查看 / 编辑 / 保存）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getPolicy.mockResolvedValue({})
    mocks.listRules.mockResolvedValue({ rules: [customRule], count: 1 })
    mocks.listEngines.mockResolvedValue({ engines: [] })
    mocks.listCanaryTokens.mockResolvedValue({ tokens: [] })
    mocks.getSeverityMatrix.mockResolvedValue({ matrix: [] })
    mocks.listStats.mockResolvedValue({})
    mocks.listDetections.mockResolvedValue({ detections: [] })
    mocks.listModels.mockResolvedValue({ items: [] })
    mocks.getRule.mockResolvedValue({ ...customRule })
    mocks.updateRule.mockResolvedValue({ message: 'Rule updated' })
    mocks.toggleRule.mockResolvedValue({ message: 'Rule toggled' })
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
  })

  it('规则表每行都有「查看详情」入口（此前该页没有任何详情入口）', async () => {
    const w = await open()
    expect(w.findAll('button').filter((b) => b.text().includes('查看详情')).length).toBe(1)
  })

  it('点击「查看详情」按 id 重新读取该条规则（而不是渲染列表里的旧行）', async () => {
    const w = await open()
    mocks.getRule.mockResolvedValue({ ...customRule, description: 'fresh from server' })
    await detailButton(w).trigger('click')
    await flushPromises()

    expect(mocks.getRule).toHaveBeenCalledWith(41)
    expect(w.find('.app-modal-title').text()).toBe('规则详情')
    expect(w.html()).toContain('fresh from server')
  })

  it('★ 保存只发送「变化的字段」—— 回归锁：全量覆盖会把未传字段清零', async () => {
    const w = await open()
    await detailButton(w).trigger('click')
    await flushPromises()

    // 只改严重度，其余字段原样不动。
    const slider = w.find('input.el-slider-stub')
    await slider.setValue('9')
    await flushPromises()

    const saveBtn = modalSaveButton(w)
    await saveBtn.trigger('click')
    await flushPromises()

    expect(mocks.updateRule).toHaveBeenCalledTimes(1)
    const [id, patch] = mocks.updateRule.mock.calls[0]
    expect(id).toBe(41)
    // ★ 关键断言：只含变化的 severity。
    //   旧实现（后端全量覆盖）下，这里必须回传全部八列才不至于清零；
    //   一旦有人改回「回传整行」，本断言立刻变红。
    expect(patch).toEqual({ severity: 9 })
  })

  it('改动 pattern 时，保存体里出现 pattern，且不含其它字段', async () => {
    const w = await open()
    await detailButton(w).trigger('click')
    await flushPromises()

    const pattern = w.find('.app-modal-stub').find('input.el-input-stub')
    await pattern.setValue('new pattern here')
    await flushPromises()

    const saveBtn = modalSaveButton(w)
    await saveBtn.trigger('click')
    await flushPromises()

    expect(mocks.updateRule.mock.calls[0][1]).toEqual({ pattern: 'new pattern here' })
  })

  it('没有改动时不发 PUT（避免无谓写库）', async () => {
    const w = await open()
    await detailButton(w).trigger('click')
    await flushPromises()

    const saveBtn = modalSaveButton(w)
    await saveBtn.trigger('click')
    await flushPromises()

    expect(mocks.updateRule).not.toHaveBeenCalled()
    expect(mocks.ElMessage.info).toHaveBeenCalled()
  })

  it('系统规则：详情只读，没有保存按钮（后端对 is_system 的 PUT 返回 404）', async () => {
    const w = await open([systemRule])
    mocks.getRule.mockResolvedValue({ ...systemRule })
    await detailButton(w).trigger('click')
    await flushPromises()

    expect(w.html()).toContain('系统规则为只读')
    const saveBtn = w.find('.app-modal-footer').findAll('button').find((b) => b.text() === '保存')
    expect(saveBtn).toBeFalsy()
  })

  it('详情读取失败时提示错误并关闭弹层（不留下半开的空壳）', async () => {
    const w = await open()
    mocks.getRule.mockRejectedValue(new Error('boom'))
    await detailButton(w).trigger('click')
    await flushPromises()

    expect(mocks.ElMessage.error).toHaveBeenCalled()
    expect(w.find('.app-modal-stub').exists()).toBe(false)
  })
})
