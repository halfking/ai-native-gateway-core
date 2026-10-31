import { afterEach, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'
import MobileModelPicker from './MobileModelPicker.vue'
import { setLocale, t } from '@/i18n'
import { fetchAvailableModels } from '@/api/models'
vi.mock('@/api/models', () => ({ fetchAvailableModels: vi.fn() }))
afterEach(() => setLocale('zh-CN'))
it('default trigger and sheet titles update when the language changes', async () => {
  setLocale('zh-CN')
  const wrapper = mount(MobileModelPicker, { props: { modelValue: '' }, global: { stubs: { AppSheet: { props: ['title'], template: '<div>{{ title }}<slot /></div>' } } } })
  expect(wrapper.text()).toContain(t('modelPicker.title'))
  setLocale('en-US'); await nextTick()
  expect(wrapper.find('.model-picker-trigger').text()).toBe(t('modelPicker.trigger'))
  expect(wrapper.text()).toContain(t('modelPicker.title'))
  wrapper.unmount()
})

// 2026-10-28 回归：线上 popular 的真实形状是 Go popularModelEntry 对象数组
// （{canonical_name, display_name, source}），此前按 string[] 消费 ⇒ 列表整行
// 渲染成 JSON 字样（用户报「路由检查选模型是一列 JSON」），点选还会把对象
// 写进 v-model ⇒ 查询参数被污染。
it('popular 列表按 canonical_name 渲染、点选产出字符串而非对象', async () => {
  vi.mocked(fetchAvailableModels).mockResolvedValue({
    families: [{
      id: 'claude', display_name: 'Claude', vendor: 'Anthropic',
      versions: [{
        canonical_name: 'claude-fable-5-1', display_name: 'claude-fable-5-1',
        modality: 'text', context_window: 200000, parameters_b: null,
        provider_count: 3, featured: false, aliases: [],
      }],
    }],
    popular: [
      { canonical_name: 'claude-fable-5-1', display_name: 'claude-fable-5-1', source: 'policy' },
      { canonical_name: 'gpt-5-1', display_name: 'gpt-5-1', source: 'usage', count: 12 },
    ],
  } as never)

  const wrapper = mount(MobileModelPicker, {
    props: { modelValue: '' },
    global: { stubs: { AppSheet: { props: ['title'], template: '<div>{{ title }}<slot /></div>' } } },
  })
  await wrapper.find('.model-picker-trigger').trigger('click')
  await flushPromises()
  await nextTick()

  const text = wrapper.text()
  expect(text).toContain('claude-fable-5-1')
  expect(text).toContain('gpt-5-1')
  // 不得出现对象序列化出来的 JSON 字样。
  // ⚠️ 断言必须踩在**对象插值真正产出的形状**上：Vue 的 toDisplayString 对
  // 对象走 JSON.stringify(v, null, 2) ⇒ 产出的是 `{\n  "canonical_name": ...`，
  // **不含字面量 `{"`** ⇒ 早前那句 not.toContain('{"') 永远不会触发（无牙）。
  // 有牙的是带引号的键名（`"canonical_name"`）与键名裸串。
  expect(text).not.toContain('"canonical_name"')
  expect(text).not.toContain('canonical_name')
  expect(text).not.toContain('source')
  expect(text).not.toMatch(/\{\s*"/)

  // 点 popular 条目 ⇒ emit 的是字符串（canonical_name，不是对象）
  const popularItem = wrapper.findAll('.model-picker__item')
    .find((b) => b.text().includes('gpt-5-1'))
  expect(popularItem).toBeTruthy()
  await popularItem!.trigger('click')
  const emitted = wrapper.emitted('update:modelValue')
  expect(emitted).toBeTruthy()
  expect(emitted![0]).toEqual(['gpt-5-1'])
  wrapper.unmount()
})

// 2026-10-28：popular 与家族分组里都有同一个模型时，只应出现一次
// （与桌面 projectAvailableModels 的「一个模型只出现一次」合并语义一致）。
it('popular 与家族分组里的同一模型只出现一次', async () => {
  vi.mocked(fetchAvailableModels).mockResolvedValue({
    families: [{
      id: 'claude', display_name: 'Claude', vendor: 'Anthropic',
      versions: [{
        canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1',
        modality: 'text', context_window: 200000, parameters_b: null,
        provider_count: 3, featured: false, aliases: [],
      }],
    }],
    popular: [
      { canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1', source: 'policy' },
      { canonical_name: 'gpt-5-1', display_name: 'gpt-5-1', source: 'usage', count: 12 },
    ],
  } as never)

  const wrapper = mount(MobileModelPicker, {
    props: { modelValue: '' },
    global: { stubs: { AppSheet: { props: ['title'], template: '<div>{{ title }}<slot /></div>' } } },
  })
  await wrapper.find('.model-picker-trigger').trigger('click')
  await flushPromises()
  await nextTick()

  const items = wrapper.findAll('.model-picker__item')
  const texts = items.map((b) => b.text())
  const occurrences = texts.filter((t) => t.includes('claude-fable-5-1')).length
  expect(occurrences).toBe(1)
  // 友好名要显示出来（canonical 只在同名时才不重复显示）
  expect(wrapper.text()).toContain('Claude Fable 5.1')
  wrapper.unmount()
})

// 防御：老服务端若仍返回字符串数组，也不能崩。
it('popular 为旧形状（字符串数组）时同样正常渲染', async () => {
  vi.mocked(fetchAvailableModels).mockResolvedValue({
    families: [],
    popular: ['legacy-model'],
  } as never)

  const wrapper = mount(MobileModelPicker, {
    props: { modelValue: '' },
    global: { stubs: { AppSheet: { props: ['title'], template: '<div>{{ title }}<slot /></div>' } } },
  })
  await wrapper.find('.model-picker-trigger').trigger('click')
  await flushPromises()
  await nextTick()
  expect(wrapper.text()).toContain('legacy-model')
  wrapper.unmount()
})
