import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import AlertsView from './AlertsView.vue'

/**
 * 回归护栏：告警卡**不能**在「没有内容可展开」时仍然宣称可点。
 *
 * 缺陷背景（2026-10-05，模拟器上实测到的）：
 *   原来卡片无条件渲染成 `<button>` + `cursor:pointer`，而唯一受 `expanded`
 *   驱动的渲染是 `<pre v-if="expanded === key && a.last_response_preview">`。
 *   对没有 `last_response_preview` 的告警，实测点击后属性 / 类名 / 高度 /
 *   文字 / 子节点**全部零变化**（`changed:false`）—— 一张骗人的可点卡片。
 *
 * 这里钉三条：无预览 ⇒ div + 无 expandable 类；点它不动；有预览 ⇒ button 且能展开。
 * 只钉「无预览」一条不够 —— 修法很容易顺手把可展开的那类也弄坏。
 */
const ITEMS = [
  {
    credential_id: 9,
    raw_model_name: 'mimo-v2.5-asr',
    ts: 1,
    count: 25,
    error_kind: 'transient',
    last_response_preview: '', // ← 现场实测就是这种：点了什么都不发生
  },
  {
    credential_id: 24,
    raw_model_name: 'mimo-v2.5-asr',
    ts: 2,
    count: 3,
    error_kind: 'auth',
    last_response_preview: '{"error":{"message":"invalid api key"}}',
  },
]

vi.mock('@/api/alerts', () => ({
  fetchAlerts: vi.fn(async () => ({ data: ITEMS.map((i) => ({ ...i })) })),
}))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

async function mountView() {
  const w = mount(AlertsView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  return w
}

/**
 * 取第 i 张告警卡。
 *
 * `findAll(...)[i]` 在 noUncheckedIndexedAccess 下是 `DOMWrapper | undefined`，
 * 直接 `.element` / `.attributes()` 会编译不过（TS18048/TS2532）。
 * 这里收口成一个入口：**先断言数量够，再交出元素**。
 * 为什么不写 `[i]!`：那会把「卡少了」从一条清晰的断言失败
 * 变成运行时的 undefined 崩溃，报错位置指不到真正的原因。
 */
function cardAt(w: ReturnType<typeof mount>, i: number) {
  const cards = w.findAll('.alert-card')
  expect(cards.length).toBeGreaterThan(i)
  const el = cards[i]
  if (!el) throw new Error(`alert-card[${i}] 不存在（实际 ${cards.length} 张）`)
  return el
}

describe('AlertsView 告警卡的可点性', () => {
  beforeEach(() => { document.body.innerHTML = '' })

  it('没有响应预览的告警渲染成 div，且不带可点的样式类', async () => {
    const w = await mountView()
    const cards = w.findAll('.alert-card')
    expect(cards.length).toBe(2)

    const noPreview = cardAt(w, 0)
    expect(noPreview.element.tagName).toBe('DIV')
    expect(noPreview.classes()).not.toContain('alert-card--expandable')
    expect(noPreview.attributes('type')).toBeUndefined()
    expect(noPreview.attributes('aria-expanded')).toBeUndefined()
  })

  it('点没有预览的告警，整张卡零变化（钉住那个实测到的现象）', async () => {
    const w = await mountView()
    const card = cardAt(w, 0)
    const before = card.element.outerHTML

    await card.trigger('click')
    await flushPromises()

    const after = cardAt(w, 0)
    expect(after.element.outerHTML).toBe(before)
    expect(w.find('pre.alert-card__body').exists()).toBe(false)
  })

  it('有响应预览的告警仍然可点、可展开、可收起', async () => {
    const w = await mountView()
    const card = cardAt(w, 1)
    expect(card.element.tagName).toBe('BUTTON')
    expect(card.classes()).toContain('alert-card--expandable')
    expect(card.attributes('aria-expanded')).toBe('false')

    await card.trigger('click')
    await flushPromises()
    const pre = w.find('pre.alert-card__body')
    expect(pre.exists()).toBe(true)
    expect(pre.text()).toContain('invalid api key')
    expect(cardAt(w, 1).attributes('aria-expanded')).toBe('true')

    await cardAt(w, 1).trigger('click')
    await flushPromises()
    expect(w.find('pre.alert-card__body').exists()).toBe(false)
    expect(cardAt(w, 1).attributes('aria-expanded')).toBe('false')
  })
})
