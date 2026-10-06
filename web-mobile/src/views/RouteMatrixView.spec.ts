import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RouteMatrixView from './RouteMatrixView.vue'
import { fetchRouteMatrix } from '@/api/autoRouteMatrix'
import { setLocale, locale } from '@/i18n'

/**
 * RouteMatrixView 的四条不变量（2026-10-07）。
 *
 * 1. ★★ **矩阵单元 0 的歧义必须被显示出来，不能靠猜。**
 *    `handleMatrix` 的缺格由 Go 零值填充 ⇒ 0 可能是「无记录」也可能是
 *    「真 0（全部失败）」。两者运营含义相反。
 *    判据：count 与 success_rate 两份响应下，0 的**视觉 class 必须不同**，
 *    且非 count 时必须挂着说明。
 *
 * 2. ★★ **p95 的口径提示是恒定的**，不是「7d 才提示」。
 *    因为响应里没有字段能说明本次走 MV 还是 base view。
 *
 * 3. ★ `__specified__` 合成键不得原样渲染。
 *
 * 4. ★ 行列方向（行=模型、列=任务）必须说在文案上，不能只靠排版。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/autoRouteMatrix', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/autoRouteMatrix')>()
  return { ...actual, fetchRouteMatrix: vi.fn() };
});

const matrixMock = fetchRouteMatrix as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RouteMatrixView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function chip(w: ReturnType<typeof mount>, label: string) {
  return w.findAll('button').find((b) => b.text() === label)
}

/** count：gpt-4o 在 summarization 上有 30 条，claude 在两个任务上都是 0。 */
const COUNT_MATRIX = {
  rows: ['gpt-4o', 'claude-sonnet-5'],
  cols: ['code_generation', 'summarization'],
  cells: [
    [120, 30],
    [0, 0],
  ],
  meta: { window: '7d', metric: 'count', row: 'task_type', row_aliases: null },
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  matrixMock.mockResolvedValue(COUNT_MATRIX)
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

describe('入口与筛选', () => {
  it('挂载即请求一次，默认 count + task_type + 7d', async () => {
    await mountView()
    expect(matrixMock).toHaveBeenCalledTimes(1)
    expect(matrixMock.mock.calls[0]![0]).toEqual({ window: '7d', row: 'task_type', metric: 'count' })
  })

  it('★ 换指标要重新请求，且参数跟着变', async () => {
    const w = await mountView()
    await chip(w, '成功率')!.trigger('click')
    await flushPromises()
    expect(matrixMock.mock.calls[1]![0]).toMatchObject({ metric: 'success_rate' })
  })

  it('换行维度重新请求', async () => {
    const w = await mountView()
    await chip(w, '按工作类型')!.trigger('click')
    await flushPromises()
    expect(matrixMock.mock.calls[1]![0]).toMatchObject({ row: 'work_type' })
  })

  it('403 ⇒ 报「仅超管」', async () => {
    matrixMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('仅超管')
  })

  it('空矩阵 ⇒ 空态而不是空白表', async () => {
    matrixMock.mockResolvedValue({ rows: [], cols: [], cells: [], meta: { ...COUNT_MATRIX.meta } })
    const w = await mountView()
    expect(w.text()).toContain('没有可聚合的路由记录')
  })
})

describe('★★ 判据 1：0 的歧义必须可见', () => {
  // ★ count 下 0 可判定为「无记录」⇒ 给一个明确的弱化 class
  it('count：0 是「无记录」样式（mx__cell--empty）', async () => {
    const w = await mountView()
    const cells = w.findAll('.mx__cell')
    // 矩阵 2×2 = 4 格，第 3、4 格是 0
    expect(cells[2]!.classes()).toContain('mx__cell--empty')
    expect(cells[3]!.classes()).toContain('mx__cell--empty')
  })

  // ★ success_rate 的 0 可能是「全部失败」⇒ 不能给「无记录」的定论
  // ★★ 判据前置必须**点 chip**切指标，不能只改 mock 响应里的 meta.metric：
  //   组件判 0 歧义看的是自己的 `metric` ref（用户选的那个），
  //   不是响应里的 meta。改 mock 只会让「前置」和「被测」错位，
  //   然后判据要么恒绿要么报一个与真实原因无关的失败（第八次踩这条）。
  it('★ success_rate：0 是更弱的 mx__cell--faint（不宣称「无记录」）', async () => {
    const w = await mountView()
    await chip(w, '成功率')!.trigger('click')
    await flushPromises()
    const cells = w.findAll('.mx__cell')
    expect(cells[2]!.classes()).toContain('mx__cell--faint')
    expect(cells[2]!.classes()).not.toContain('mx__cell--empty')
  })

  // ★ 反向锁定：两种指标的 0 视觉必须不同，否则「弱化」没做到位
  it('★ 同一份 cells，count 与 success_rate 下 0 的 class 不同', async () => {
    const w1 = await mountView()
    const underCount = w1.findAll('.mx__cell')[2]!.classes().join(' ')
    const w2 = await mountView()
    await chip(w2, '成功率')!.trigger('click')
    await flushPromises()
    const underRate = w2.findAll('.mx__cell')[2]!.classes().join(' ')
    expect(underCount).not.toBe(underRate)
  })

  it('非 count 指标挂 0 歧义说明，且说明里给「切到请求数」的可执行指引', async () => {
    const w = await mountView()
    await chip(w, '成功率')!.trigger('click')
    await flushPromises()
    const text = w.text()
    expect(text).toContain('也可能是真实值 0')
    expect(text).toContain('请求数')
  })

  // ★ 鼠标 title 也要带同样的保留措辞（桌面端悬停时看到的不能比移动端更笃定）
  it('★ 单元格 title 对 0 用保留措辞', async () => {
    const w = await mountView()
    await chip(w, '成功率')!.trigger('click')
    await flushPromises()
    const cell = w.findAll('.mx__cell')[2]!
    expect(cell.attributes('title')).toContain('可能是没有记录')
  })
})

describe('★ 判据 2：p95 口径提示恒定', () => {
  it('选中 p95 ⇒ 挂口径说明', async () => {
    const w = await mountView()
    await chip(w, 'P95 延时')!.trigger('click')
    await flushPromises()
    const text = w.text()
    expect(text).toContain('P95 口径')
    expect(text).toContain('加权的平均')
  })

  // ★ 这是本轮最容易漏的一条：若只在 7d 时提示，24h 那次（真 p95）
  //   就不显示说明 —— 而那条同样不该被当成可比数字。
  it('★ 切到 24h 窗口后 p95 提示**仍在**（不因窗口变化消失）', async () => {
    const w = await mountView()
    await chip(w, 'P95 延时')!.trigger('click')
    await flushPromises()
    await chip(w, '24 小时')!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('P95 口径')
  })

  it('非 p95 指标不挂 p95 说明', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('P95 口径')
  })
})

describe('★ 判据 3：__specified__ 合成键', () => {
  it('★ 表头显示「指定模型」而不是 __specified__', async () => {
    matrixMock.mockResolvedValue({
      rows: ['gpt-4o'],
      cols: ['__specified__', 'unknown'],
      cells: [[10, 5]],
      meta: { ...COUNT_MATRIX.meta },
    })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('指定模型')
    expect(text).toContain('未分类')
    expect(text).not.toContain('__specified__')
  })
})

describe('判据 4：行列方向与别名', () => {
  it('★ 表头写明「行 = 模型」「列 = 任务」', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('行 = 模型')
    expect(text).toContain('列 = 任务')
  })

  it('行头渲染的是 meta.rows（模型名），列头是 meta.cols（任务名）', async () => {
    const w = await mountView()
    const rowheads = w.findAll('.mx__rowhead').map((e) => e.text())
    const colheads = w.findAll('.mx__colhead').map((e) => e.text())
    expect(rowheads[0]).toContain('gpt-4o')
    expect(colheads[0]).toContain('code_generation')
  })

  it('模型归并了别名 ⇒ 行头显示 +N', async () => {
    matrixMock.mockResolvedValue({
      ...COUNT_MATRIX,
      rows: ['gpt-4o'],
      cols: ['code_generation'],
      cells: [[120]],
      meta: { ...COUNT_MATRIX.meta, row_aliases: { 'gpt-4o': ['gpt-4o-0613', 'gpt-4o-latest'] } },
    })
    const w = await mountView()
    expect(w.find('.mx__alias').exists()).toBe(true)
  })

  it('只有一个别名（无归并）⇒ 不显示 +N', async () => {
    matrixMock.mockResolvedValue({
      ...COUNT_MATRIX,
      meta: { ...COUNT_MATRIX.meta, row_aliases: { 'gpt-4o': ['gpt-4o'] } },
    })
    const w = await mountView()
    expect(w.find('.mx__alias').exists()).toBe(false)
  })
})
