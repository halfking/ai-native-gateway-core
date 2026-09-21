// ModelPicker.test.ts — R48 §七 + R46 #10 收尾：[value] 守卫 + multi 吞值场景。
// ModelPicker 接受 modelValue: string | string[]。守卫必须在 type 与
// mode 不一致时（如 multi 模式收到 string、single 模式收到 string[]、
// 数组含非字符串）通过 emit 回吐正确形态，避免上游路由长期使用错误数据。
//
// Vue emit 形参包成外层数组：`events = [[arg1, arg2, ...]]` → 单一
// 参数时 lastCall[0] = arg1。

import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import ModelPicker from './ModelPicker.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      dashboard: {
        modelsSelectedCount: '已选 {n} 个',
      },
    },
  },
})

// props 强制类型收窄到组件 props 形态（而非 Record<string, unknown>），
// 否则 vue-tsc 会报 Property 'modelValue' is missing（mode 等 optional 字段
// 仍是组件 props 真实类型）。
type MPProps = {
  modelValue: string | string[]
  mode?: 'single' | 'multi'
}

function mountModelPicker(props: MPProps) {
  return mount(ModelPicker, {
    props: props as never,
    global: {
      plugins: [i18n],
    },
  })
}

function lastArg(wrapper: ReturnType<typeof mountModelPicker>): unknown {
  const events = wrapper.emitted('update:modelValue') as unknown[][][] | undefined
  if (!events || events.length === 0) return undefined
  // events = [ [arg1, arg2, ...] ] —— 取最近一次 emit 的第一个参数
  const args = events[events.length - 1]
  return args[0]
}

describe('ModelPicker [value] 守卫 (R46 #10 收尾 / R48 §七)', () => {
  it('multi 模式收到 string → safeValue computed 强制空数组形态', async () => {
    // 关键合约：safeValue 把 string 强制成 string[]，渲染层不吞值。
    // 即使 watcher 路径被现有 props 跳过（见 Vue watch deep 行为），
    // safeValue computed 仍然把数据形态纠正为下游可消费的 string[]。
    const wrapper = mountModelPicker({
      modelValue: 'gpt-4',
      mode: 'multi',
    })
    await new Promise((r) => setTimeout(r, 0))
    // 触发 watch：把 props 改成不同的 string，强制深度比较不命中 → emit
    await wrapper.setProps({ modelValue: 'claude' })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeTruthy()
    // 不变量：纠正后 lastArg 不是 'claude'（被吃掉），是 string[]
    const args = lastArg(wrapper)
    expect(args).not.toBe('claude')
    wrapper.unmount()
  })

  it('multi 模式收到包含非字符串的数组 → 过滤非字符串后 emit', async () => {
    const wrapper = mountModelPicker({
      modelValue: ['gpt-4', null, 'claude', undefined, 42] as unknown as string[],
      mode: 'multi',
    })
    await wrapper.setProps({
      modelValue: ['gpt-4', null, 'claude'] as unknown as string[],
    })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeTruthy()
    expect(lastArg(wrapper)).toEqual(['gpt-4', 'claude'])
    wrapper.unmount()
  })

  it('single 模式收到非空字符串数组 → emit 取首元素', async () => {
    const wrapper = mountModelPicker({
      modelValue: ['claude-opus', 'gpt-5'],
      mode: 'single',
    })
    await wrapper.setProps({ modelValue: ['claude-opus', 'gpt-5'] })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeTruthy()
    expect(lastArg(wrapper)).toBe('claude-opus')
    wrapper.unmount()
  })

  it('single 模式收到空数组 → emit 空字符串', async () => {
    const wrapper = mountModelPicker({
      modelValue: [],
      mode: 'single',
    })
    await wrapper.setProps({ modelValue: [] })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeTruthy()
    expect(lastArg(wrapper)).toBe('')
    wrapper.unmount()
  })

  it('正常 string/single 不触发冗余 emit', async () => {
    const wrapper = mountModelPicker({
      modelValue: 'gpt-4',
      mode: 'single',
    })
    await wrapper.setProps({ modelValue: 'gpt-4' })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeFalsy()
    wrapper.unmount()
  })

  it('多候选字符串数组 + multi 模式（合法形态）不触发 emit', async () => {
    const wrapper = mountModelPicker({
      modelValue: ['gpt-4', 'claude'],
      mode: 'multi',
    })
    await wrapper.setProps({ modelValue: ['gpt-4', 'claude'] })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeFalsy()
    wrapper.unmount()
  })

  it('multi 模式收到合法空数组 → 不触发 emit', async () => {
    const wrapper = mountModelPicker({
      modelValue: [],
      mode: 'multi',
    })
    await wrapper.setProps({ modelValue: [] })
    await new Promise((r) => setTimeout(r, 0))
    expect(wrapper.emitted('update:modelValue')).toBeFalsy()
    wrapper.unmount()
  })
})
