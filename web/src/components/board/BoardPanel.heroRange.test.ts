// BoardPanel.heroRange.test.ts —— 总请求数据块（英雄区）上方的数据时间范围（2026-11-19 轮）
//
// ★ 为什么需要这道测试：
//   owner 需求是「总请求数据块上面加当前数据时间范围，与模型分布块上方的筛选条一致且联动」。
//   此前一轮改动落在了 BoardOperationalView（/admin/board-operational，另一个页面），
//   /dashboard?tab=board 一直没生效 —— 本文件把「改对了地方 + 两处同源联动」钉成契约，
//   防止下一次又在错误的组件上加提示。
//
// ★ 联动的实现事实（本测试要钉住的结构）：
//   BoardPanel 用同一个 `rangeValue` computed（BoardTimeRange → resolveBoardRangeMs → 日历日）
//   同时喂给：① 英雄区上方说明行（本文件的对象）；② BoardFilterBar 的 KxDateRangePicker
//   （模型分布块上方那条）。同源 ⇒ 改一处两处同步，不存在第三份状态。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { ref, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import BoardPanel from './BoardPanel.vue'
import { resolveBoardRangeMs, type BoardTimeRange } from '../../utils/boardTimeRange'

const SRC_CANDIDATES = [
  resolve(process.cwd(), 'web/src/components/board/BoardPanel.vue'),
  resolve(process.cwd(), 'src/components/board/BoardPanel.vue'),
]

async function readSource(): Promise<string> {
  for (const p of SRC_CANDIDATES) {
    try {
      return await readFile(p, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error('BoardPanel.vue not found')
}

function utcDate(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}

/** 与组件内 rangeValue 同一套解析，取「当前范围应显示的日期对」。 */
function expectedDates(range: BoardTimeRange): { start: string; end: string } {
  const { startMs, endMs } = resolveBoardRangeMs(range)
  return { start: utcDate(startMs), end: utcDate(Math.max(startMs, endMs - 1)) }
}

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      dashboard: {
        board: {
          heroRangeLabel: '数据时间范围',
          rangeCustom: '{start} 至 {end}',
        },
      },
    },
  },
})

function mountPanel(timeRange: ReturnType<typeof ref<BoardTimeRange>>) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div/>' } }] })
  return mount(BoardPanel, {
    shallow: true,
    global: {
      plugins: [i18n, router],
      provide: {
        dashboardBoard: {
          board: ref(null),
          days: ref(7),
          timeRange,
          loading: ref(false),
          load: async () => {},
          setTimeRange: (next: BoardTimeRange) => {
            timeRange.value = next
          },
          startAutoRefresh: async () => {},
        },
      },
    },
  })
}

describe('看板总请求数据块上方的时间范围（与筛选条同源联动）', () => {
  it('① 渲染在英雄区上方，显示当前范围的日历日', async () => {
    const timeRange = ref<BoardTimeRange>({ preset: '7d', days: 7 })
    const w = mountPanel(timeRange)
    const caption = w.find('.board-panel__hero-range')
    expect(caption.exists()).toBe(true)
    const exp = expectedDates(timeRange.value)
    expect(caption.text()).toContain('数据时间范围')
    expect(caption.text()).toContain(`${exp.start} 至 ${exp.end}`)
    // 位置契约：说明行在英雄区（总请求数据块）之前，而不是别处。
    const html = w.element.innerHTML
    expect(html.indexOf('board-panel__hero-range')).toBeGreaterThanOrEqual(0)
    expect(html.indexOf('board-panel__hero-range')).toBeLessThan(html.indexOf('board-hero-row-stub'))
  })

  it('② 联动：时间范围变化（筛选条 apply）后说明行同步更新', async () => {
    const timeRange = ref<BoardTimeRange>({ preset: 'today', days: 1 })
    const w = mountPanel(timeRange)
    const before = expectedDates(timeRange.value)
    expect(w.find('.board-panel__hero-range').text()).toContain(`${before.start} 至 ${before.end}`)
    // 模拟 BoardFilterBar apply-range → setTimeRange → timeRange 状态变化
    timeRange.value = { preset: 'custom', days: 3, start: '2026-11-01', end: '2026-11-03' }
    await nextTick()
    expect(w.find('.board-panel__hero-range').text()).toContain('2026-11-01 至 2026-11-03')
  })

  it('③ 同源契约：说明行与筛选条 KxDateRangePicker 消费同一个 rangeValue', async () => {
    const src = await readSource()
    // 说明行用 rangeValue 渲染日期
    expect(src).toContain('data-testid="board-hero-range"')
    expect(src).toMatch(/rangeCustom', \{ start: rangeValue\.start, end: rangeValue\.end \}/)
    // 筛选条（模型分布块上方）拿到的也是同一个 rangeValue
    expect(src).toContain(':range-value="rangeValue"')
  })
})
