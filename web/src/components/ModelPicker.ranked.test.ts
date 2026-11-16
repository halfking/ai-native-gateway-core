// ModelPicker.ranked.test.ts —— 置顶分组「本区间有请求的模型」（2026-11-16 审计轮）。
//
// 为什么要有这一段：模型目录（/api/routing/available-models）描述的是**能路由
// 的模型**，而对账页关心的是**这个区间真跑过的模型**——两者的差集正是审计要抓
// 的东西（配了却没流量、目录里没有却有流量的 raw 名）。所以 ModelPicker 支持在
// 目录之上挂一段「有请求的」清单，**按请求数降序置顶**。
//
// 判据纪律：门必须双向——① 有 requestRanked 时该段必须出现在目录**之前**；
// ② 没给 requestRanked / 没给标题时该段**不得**出现（否则路由页凭空多一段）。
//
// ⚠ 弹层是 Teleport 到 body 的 ⇒ 断言必须查 document.body，wrapper.findAll
// 看不到（这是本文件第一版的真实失败点，不是断言写错）。

import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ModelPicker from './ModelPicker.vue'

const getAvailableModelsMock = vi.fn()
vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getAvailableModels: (...args: unknown[]) => getAvailableModelsMock(...args),
  }
})

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      dashboard: { modelsSelectedCount: '已选 {n} 个' },
      common: {
        button: { more: '更多', back: '返回', cancel: '取消', confirm: '确定' },
        close: '关闭',
        feedback: { loading: '加载中…' },
      },
    },
  },
})

function catalog() {
  return {
    families: [
      {
        vendor: 'OpenAI',
        display_name: 'Openai Gpt',
        versions: [
          { canonical_name: 'gpt-5.6', display_name: 'gpt-5.6', aliases: [], raw_names: ['gpt-5.6'] },
        ],
      },
    ],
    popular: [{ canonical_name: 'gpt-5.6', display_name: 'gpt-5.6' }],
    total_raw: 1,
    unmapped: [],
  }
}

function mountPicker(props: Record<string, unknown>) {
  return mount(ModelPicker, {
    props: { modelValue: '', ...props } as never,
    // Teleport **不**打桩：弹层要真的渲染到 document.body，断言才查得到。
    global: { plugins: [i18n] },
  })
}

async function openDialog(wrapper: ReturnType<typeof mountPicker>) {
  await wrapper.find('.mp-trigger').trigger('click')
  await new Promise((r) => setTimeout(r, 0))
}

/** 弹层（Teleport）里的分段。 */
function sections(): HTMLElement[] {
  return Array.from(document.body.querySelectorAll<HTMLElement>('.mp-section'))
}

function sectionByTitle(title: string): HTMLElement | undefined {
  return sections().find((s) => (s.querySelector('.mp-section-title')?.textContent ?? '').includes(title))
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ModelPicker 置顶分组「本区间有请求的模型」', () => {
  it('有 requestRanked 时该段出现在模型目录之前，并带请求数', async () => {
    getAvailableModelsMock.mockResolvedValue(catalog())
    const wrapper = mountPicker({
      requestRanked: [
        { name: 'MiniMax-M2.5', requests: 25300 },
        { name: 'glm-5.2', requests: 1200 },
      ],
      requestRankedTitle: '本区间有请求的模型',
    })
    await openDialog(wrapper)

    const ranked = sectionByTitle('本区间有请求的模型')
    expect(ranked, '置顶分组没渲染').toBeTruthy()
    const popular = sectionByTitle('热门模型')
    expect(popular, '热门模型段没渲染（目录数据面没加载）').toBeTruthy()

    const order = sections()
    expect(order.indexOf(ranked!)).toBeLessThan(order.indexOf(popular!))
    expect(ranked!.textContent).toContain('25300')
    expect(ranked!.textContent).toContain('MiniMax-M2.5')
  })

  it('点置顶分组里的模型 → emit 该模型名（对账页据此筛 raw 名）', async () => {
    getAvailableModelsMock.mockResolvedValue(catalog())
    const wrapper = mountPicker({
      requestRanked: [{ name: 'MiniMax-M2.5', requests: 25300 }],
      requestRankedTitle: '本区间有请求的模型',
    })
    await openDialog(wrapper)

    const ranked = sectionByTitle('本区间有请求的模型')!
    const btn = Array.from(ranked.querySelectorAll<HTMLButtonElement>('button.mp-model'))
      .find((b) => (b.textContent ?? '').includes('MiniMax-M2.5'))
    expect(btn, '置顶分组里没找到该模型按钮').toBeTruthy()
    btn!.click()
    await new Promise((r) => setTimeout(r, 0))

    const events = wrapper.emitted('update:modelValue') as unknown[][][]
    expect(events.length).toBeGreaterThan(0)
    expect(events[events.length - 1][0]).toBe('MiniMax-M2.5')
  })

  it('没给 requestRanked / 没给标题 → 不渲染该段（路由页不受影响）', async () => {
    getAvailableModelsMock.mockResolvedValue(catalog())
    const wrapper = mountPicker({})
    await openDialog(wrapper)

    expect(document.body.querySelector('.mp-section--ranked')).toBeNull()
    expect(sections().length).toBeGreaterThan(0) // 目录本身照常渲染
  })

  it('超过预览条数 → 「更多…」打开全量图层', async () => {
    getAvailableModelsMock.mockResolvedValue(catalog())
    const ranked = Array.from({ length: 30 }, (_, i) => ({
      name: `m-${i}`,
      requests: 1000 - i,
    }))
    const wrapper = mountPicker({
      requestRanked: ranked,
      requestRankedTitle: '本区间有请求的模型',
      requestRankedLimit: 5,
    })
    await openDialog(wrapper)

    const section = sectionByTitle('本区间有请求的模型')!
    const buttons = Array.from(section.querySelectorAll<HTMLButtonElement>('button.mp-model'))
    expect(buttons.length).toBe(6) // 5 条预览 + 1 个「更多…」
    const more = buttons.find((b) => (b.textContent ?? '').includes('更多'))
    expect(more, '没有「更多…」按钮').toBeTruthy()
    more!.click()
    await new Promise((r) => setTimeout(r, 0))

    const dialogs = Array.from(document.body.querySelectorAll<HTMLElement>('.mp-dialog--vendor'))
    const last = dialogs[dialogs.length - 1]
    expect(last, '全量图层没打开').toBeTruthy()
    expect(last.querySelectorAll('button.mp-model').length).toBe(30)
  })
})
