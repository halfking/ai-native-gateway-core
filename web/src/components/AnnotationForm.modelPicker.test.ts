// AnnotationForm.modelPicker.test.ts — 2026-10-03 模型选择换装 ModelPicker 守卫。
//
// 本轮（auto-ops 标注工作台）把「理想模型」字段从原生 <select> 下拉换成站内
// 标准控件 ModelPicker（厂商分组选卡）。本测试锁定三点契约，防回归退回下拉：
//   1. 模型字段渲染 .mp-trigger（ModelPicker 触发器），不存在原生
//      select#anno-model / 值为模型的 <select>；
//   2. 打开选卡弹层 → 点模型卡 → 弹层关闭、触发器显示所选值；
//   3. 所选模型进入 submit 载荷；清空模型后提交被必填校验拦下。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it, vi } from 'vitest'
import annotation from '../locales/zh-CN/annotation'
import type { FirstTurnSample } from '../api/annotations'

vi.mock('../api', () => ({
  // ModelPicker 挂载即取目录；给一个可控 fixture 供选卡。
  getAvailableModels: vi.fn().mockResolvedValue({
    popular: [
      { canonical_name: 'glm-5.2', display_name: 'GLM-5.2', source: 'usage', count: 12 },
      { canonical_name: 'kimi-k3', display_name: 'Kimi-K3', source: 'usage', count: 3 },
    ],
    families: [
      {
        id: 'zhipu',
        display_name: '智谱',
        vendor: '智谱',
        versions: [
          {
            canonical_name: 'glm-5.2',
            display_name: 'GLM-5.2',
            modality: 'text',
            context_window: 128000,
            parameters_b: null,
            aliases: [],
            raw_names: [],
            provider_count: 1,
            featured: true,
            tags: [],
          },
        ],
      },
    ],
    unmapped: [],
    total_raw: 1,
  }),
}))

import AnnotationForm from './AnnotationForm.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { annotation } },
})

const sample = {
  session_id: 'sess-1',
  request_id: 'req-1',
  ts: '2026-10-02T10:00:00+08:00',
  title: '写一段排序',
  client: 'cli',
  task_type: 'code',
  chosen_model: 'glm-5.2',
  confidence: 0.42,
  status_code: 200,
  success: true,
  latency_ms: 800,
  total_turns: 1,
} as FirstTurnSample

function mountForm() {
  return mount(AnnotationForm, {
    props: {
      sample,
      defaultAnnotator: 'alice',
      taskTypes: [{ key: 'code', label: '💻 代码' }],
    },
    global: { plugins: [i18n] },
  })
}

async function openPickerAndPick(wrapper: ReturnType<typeof mountForm>, name: string) {
  const trigger = wrapper.find('.model-picker .mp-trigger')
  ;(trigger.element as HTMLElement).click()
  await vi.waitFor(() => {
    expect(document.querySelector('.mp-dialog')).toBeTruthy()
  })
  const card = [...document.querySelectorAll('.mp-dialog .mp-model')].find(
    (b) => (b.textContent || '').toLowerCase().includes(name.toLowerCase()),
  )
  expect(card, `model card ${name} in picker dialog`).toBeTruthy()
  ;(card as HTMLElement).click()
  await vi.waitFor(() => {
    expect(document.querySelector('.mp-dialog')).toBeFalsy()
  })
  await wrapper.vm.$nextTick()
}

describe('AnnotationForm 模型选择 = ModelPicker（非下拉）', () => {
  it('模型字段渲染 ModelPicker 触发器，不存在原生 select#anno-model', () => {
    const wrapper = mountForm()
    expect(wrapper.find('.model-picker .mp-trigger').exists()).toBe(true)
    // 旧控件是 <select id="anno-model">；换装后不得回归
    expect(wrapper.find('select#anno-model').exists()).toBe(false)
    // 表单里仅剩任务类型/原因两个语义 select，模型不再是 select
    const selects = wrapper.findAll('select')
    expect(selects.length).toBe(2)
  })

  it('预填 auto 决策的 chosen_model 显示在触发器上', () => {
    const wrapper = mountForm()
    const text = wrapper.find('.model-picker .mp-trigger').text()
    expect(text).toContain('glm-5.2')
  })

  it('选卡弹层点选模型 → 关闭弹层、触发器更新、进入 submit 载荷', async () => {
    const wrapper = mountForm()
    await openPickerAndPick(wrapper, 'kimi-k3')
    expect(wrapper.find('.model-picker .mp-trigger').text()).toContain('kimi-k3')

    const annotatorInput = wrapper.find('#annotator')
    await annotatorInput.setValue('alice')
    await wrapper.find('button.btn-primary').trigger('click')
    const events = wrapper.emitted('submit') as unknown[][] | undefined
    expect(events).toBeTruthy()
    const payload = events![0][0] as { model: string; task_type: string }
    expect(payload.model).toBe('kimi-k3')
    expect(payload.task_type).toBe('code')
  })

  it('模型为空时（触发器 × 清空）提交按钮禁用、不产生 submit 事件', async () => {
    const wrapper = mountForm()
    // 清空预填（触发器上的 × 是真实按钮，走 clearSingle）
    const clearBtn = wrapper.find('.model-picker .mp-clear')
    expect(clearBtn.exists()).toBe(true)
    await clearBtn.trigger('click')
    expect(wrapper.find('.model-picker .mp-trigger').text()).not.toContain('glm-5.2')

    const annotatorInput = wrapper.find('#annotator')
    await annotatorInput.setValue('alice')
    // isValid 由 model 参与把关：模型空 → 按钮禁用，无 submit 可发
    const submitBtn = wrapper.find('button.btn-primary')
    expect((submitBtn.attributes('disabled') !== undefined) || (submitBtn.element as HTMLButtonElement).disabled).toBe(true)
    await submitBtn.trigger('click')
    expect(wrapper.emitted('submit')).toBeUndefined()
  })
})
