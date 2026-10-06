import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RouteFlowView from './RouteFlowView.vue'
import { fetchRouteFlow } from '@/api/autoRouteMatrix'
import { setLocale, locale } from '@/i18n'

/**
 * RouteFlowView 的四条不变量（2026-10-07）。
 *
 * 1. ★ **边上必须回显来源任务**（`links[].task_type`）。
 *    L2→L3 的链路按 (任务, 模型) 聚合，不带任务的话
 *    「代码生成 → gpt-4o → openai」和「翻译 → gpt-4o → openai」会长得一样。
 *
 * 2. ★ **占比的分母是全图总量**，不是某层的和 ——
 *    用后者会让每一层都显示 100%。
 *
 * 3. ★ **分母为 0 显示「未知」而不是 0%**（0% 意味着「全部被筛掉」）。
 *
 * 4. ★ 末层节点没有出边是**正常的**，必须说成「终点层」而不是空/缺数据。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/autoRouteMatrix', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/autoRouteMatrix')>()
  return { ...actual, fetchRouteFlow: vi.fn() };
});

const flowMock = fetchRouteFlow as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RouteFlowView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function chip(w: ReturnType<typeof mount>, label: string) {
  return w.findAll('button').find((b) => b.text() === label)
}

/**
 * ★ 按**自身名字**定位条目，不能用 `item.text().includes(name)`。
 *
 * 第二个 `.fl__item` 是 task:translation，它渲染出的边目标写着
 * `→gpt-4o`；第三项（模型 gpt-4o 本身）又是另一个元素。
 * 用子树文本做 includes，第二个元素**也**命中「gpt-4o」——
 * 于是读到的是 translation 那条边的占比（41.7%），不是模型那条（50.0%）。
 *
 * 判据量的东西必须和它声称量的东西**同一个元素**。
 * 同族：整页 `w.text()` 断言会命中筛选 chip（§11.43 修过一次）。
 */
function itemNamed(w: ReturnType<typeof mount>, name: string) {
  return w
    .findAll('.fl__item')
    .find((e) => e.find('.fl__item-name').text() === name)!
}

/**
 * ★ 同一个模型 + 同一个供应商，被**两个不同任务**打进来。
 * 这是判据 1 的关键样本：两条边长得极像，只有 task_type 不同。
 */
const FLOW = {
  nodes: [
    { id: 'task:code_generation', label: 'code_generation', layer: 0 },
    { id: 'task:translation', label: 'translation', layer: 0 },
    { id: 'model:gpt-4o', label: 'gpt-4o', layer: 1 },
    { id: 'model:claude-sonnet-5', label: 'claude-sonnet-5', layer: 1 },
    { id: 'prov:openai', label: 'openai', layer: 2 },
  ],
  // ★★ links 刻意**乱序**，且**必须有一个节点带多条出边**。
  //
  //   踩坑记录：这组样本原先每个任务节点只有 1 条出边，
  //   而「去掉排序」是对**单个节点的出边数组**排序 —— 1 元素数组排序
  //   恒为空操作，判据永远绿。
  //   ⇒ 属于「变异没转红」的第 ② 类：**前提失效**（样本没走到被测分支）。
  //   真实桑基里「一个任务分流到多个模型」是最常见的情形，
  //   所以这不该靠脑补补上，而是必须放进样本。
  links: [
    // task:code_generation 有 2 条出边，**小的那条在前**
    { source: 'task:code_generation', target: 'model:claude-sonnet-5', value: 40, task_type: 'code_generation' },
    { source: 'task:translation', target: 'model:gpt-4o', value: 20, task_type: 'translation' },
    { source: 'task:code_generation', target: 'model:gpt-4o', value: 100, task_type: 'code_generation' },
    { source: 'model:gpt-4o', target: 'prov:openai', value: 120, task_type: 'code_generation' },
  ],
  meta: { window: '7d' },
}
// 全图总量 = 40 + 20 + 100 + 120 = 280

const EMPTY = { nodes: [], links: [], meta: { window: '7d' } }

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  flowMock.mockResolvedValue(FLOW)
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
  it('挂载即请求一次，默认 7d', async () => {
    await mountView()
    expect(flowMock).toHaveBeenCalledTimes(1)
    expect(flowMock.mock.calls[0]![0]).toEqual({ window: '7d' })
  })

  it('切窗口重新请求', async () => {
    const w = await mountView()
    await chip(w, '24 小时')!.trigger('click')
    await flushPromises()
    expect(flowMock.mock.calls[1]![0]).toEqual({ window: '24h' })
  })

  it('403 ⇒ 报「仅超管」', async () => {
    flowMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('仅超管')
  })

  it('links 为空 ⇒ 空态', async () => {
    flowMock.mockResolvedValue(EMPTY)
    const w = await mountView()
    expect(w.text()).toContain('没有流量记录')
  })

  it('★ links 为 null 也不崩（后端 make(...,0) 不会给 null，但要兜）', async () => {
    flowMock.mockResolvedValue({ nodes: null, links: null, meta: { window: '7d' } })
    const w = await mountView()
    expect(w.text()).toContain('没有流量记录')
  })
})

describe('★ 判据 1：边上必须回显来源任务', () => {
  // ★ 这条样本的价值：两条边连的是**同一个** model: 目标，
  //   不带 task_type 时它们在页面上会完全一样。
  it('★ 同一目标的两条边显示不同任务', async () => {
    const w = await mountView()
    const items = w.findAll('.fl__item')
    const first = items[0]! // task:code_generation
    const second = items[1]! // task:translation
    expect(first.find('.fl__edge-task').text()).toBe('code_generation')
    expect(second.find('.fl__edge-task').text()).toBe('translation')
  })

  it('★ 边的目标显示节点 label 而不是带前缀的 id', async () => {
    const w = await mountView()
    const target = w.findAll('.fl__edge-target')[0]!
    expect(target.text()).toBe('gpt-4o')
    expect(w.text()).not.toContain('model:gpt-4o')
  })

  // ★ 断言必须钉**渲染顺序**，而不是「第一项的值是 100」。
  //   样本乱序 ⇒ 渲染成 100 在前才说明排过序；
  //   若只断言「某处有个 100」，去掉排序照样全绿。
  it('★ 同一节点的多条出边按流量降序（样本 40 在前 100 在后，去掉排序就露馅）', async () => {
    const w = await mountView()
    const code = itemNamed(w, 'code_generation')
    expect(code.findAll('.fl__edge-value').map((e) => e.text())).toEqual(['100', '40'])
  })

  it('★ 降序后目标顺序也随之变化（大流量指到 gpt-4o）', async () => {
    const w = await mountView()
    const code = itemNamed(w, 'code_generation')
    expect(code.findAll('.fl__edge-target').map((e) => e.text())).toEqual(['gpt-4o', 'claude-sonnet-5'])
  })
})

describe('★ 判据 2：占比分母是全图总量', () => {
  // 全图总量 = 280
  //   ⇒ code→gpt-4o 那条 100/280 = 35.7%
  // 若分母误用「源节点出边之和」(40+100=140)，会显示 71.4% —— 那是错的。
  it('★ 占比按全图总量算，不是按该节点出边和', async () => {
    const w = await mountView()
    const code = itemNamed(w, 'code_generation')
    // 该节点出边和 = 140，用它当分母第一条会显示 71.4%
    expect(code.findAll('.fl__edge-share')[0]!.text()).toBe('35.7%')
  })

  it('第二层那条（120/280 = 42.9%）也对得上全图分母', async () => {
    const w = await mountView()
    const modelItem = itemNamed(w, 'gpt-4o')
    expect(modelItem.find('.fl__edge-share').text()).toBe('42.9%')
  })

  it('页首显示全图总量 280', async () => {
    const w = await mountView()
    expect(w.text()).toContain('280')
  })
})

describe('★ 判据 3：分母为 0 显示「未知」而不是 0%', () => {
  it('全图总量为 0 ⇒ 占比显示未知', async () => {
    flowMock.mockResolvedValue({
      nodes: [
        { id: 'task:a', label: 'a', layer: 0 },
        { id: 'model:b', label: 'b', layer: 1 },
      ],
      links: [{ source: 'task:a', target: 'model:b', value: 0, task_type: 'a' }],
      meta: { window: '7d' },
    })
    const w = await mountView()
    expect(w.text()).toContain('未知')
    expect(w.findAll('.fl__edge-share')[0]!.text()).not.toBe('0.0%')
  })
})

describe('★ 判据 4：末层无出边是「终点层」不是缺数据', () => {
  it('供应商节点显示「终点层，无下游」', async () => {
    const w = await mountView()
    const prov = itemNamed(w, 'openai')
    expect(prov.text()).toContain('终点层')
  })

  it('★ 中间层节点**不**显示「终点层」（有出边就是有下游）', async () => {
    const w = await mountView()
    const model = itemNamed(w, 'gpt-4o')
    expect(model.text()).not.toContain('终点层')
  })

  it('三层标题都在（任务/模型/供应商）', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('任务类型')
    expect(text).toContain('模型')
    expect(text).toContain('供应商')
  })
})
