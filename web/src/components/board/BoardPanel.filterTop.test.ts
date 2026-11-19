// BoardPanel.filterTop.test.ts —— 总请求数据块（英雄区）上方的筛选条（2026-11-19 二轮）
//
// ★ 背景：owner 图批定案——
//   ① 上一轮的纯文字说明行（「数据时间范围 …」）不要（图上划掉，批「这一行不要」）；
//   ② 把筛选条本体（时间范围 KxDateRangePicker + 粒度 + 刷新）**复制**到英雄区上方
//     （图上从底部筛选条画长箭头到顶部红框位置，批「复制到这个位置」）；
//   ③ 顶部这份与运维状态行**对齐高度**（批「对齐高度」，指向 ops-bar 行）。
//   下方（模型分布块上方）的筛选条保留 ⇒ 两份入口共享同一份 BoardTimeRange 状态，
//   「两个数据连动一致」由同源保证。
//
// ★ 本守卫钉三件事：位置（顶部条在英雄区之前）/ 双条同源（同一个 rangeValue 与
//   apply 通道）/ 纯文字行不得回魂（含 i18n 键）。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { ref, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import BoardPanel from './BoardPanel.vue'
import BoardFilterBar from './BoardFilterBar.vue'
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

const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })

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

describe('看板英雄区上方的筛选条（owner 图批二轮契约）', () => {
  it('① 渲染两份筛选条：第一份在英雄区（总请求数据块）上方', () => {
    const timeRange = ref<BoardTimeRange>({ preset: '7d', days: 7 })
    const w = mountPanel(timeRange)
    const bars = w.findAllComponents(BoardFilterBar)
    expect(bars.length).toBe(2)
    // 位置契约（DOM 位置对拍，不玩字符串搜索）：英雄区在第一份之后、第二份之前
    const hero = w.find('board-hero-row-stub')
    expect(hero.exists()).toBe(true)
    const heroAfterFirst = !!(bars[0].element.compareDocumentPosition(hero.element) & Node.DOCUMENT_POSITION_FOLLOWING)
    const heroBeforeSecond = !!(bars[1].element.compareDocumentPosition(hero.element) & Node.DOCUMENT_POSITION_PRECEDING)
    expect(heroAfterFirst).toBe(true)
    expect(heroBeforeSecond).toBe(true)
  })

  it('② 顶部那份是 compact（与运维状态行对齐高度），底部那份保持默认', () => {
    const timeRange = ref<BoardTimeRange>({ preset: 'today', days: 1 })
    const w = mountPanel(timeRange)
    const bars = w.findAllComponents(BoardFilterBar)
    expect(bars[0].props('compact')).toBe(true)
    expect(bars[1].props('compact')).toBe(false)
  })

  it('③ 连动：两份筛选条同一个 rangeValue / 同一个 apply 通道（改一处两处同步）', async () => {
    const timeRange = ref<BoardTimeRange>({ preset: 'today', days: 1 })
    const w = mountPanel(timeRange)
    const bars = w.findAllComponents(BoardFilterBar)
    const { startMs, endMs } = resolveBoardRangeMs(timeRange.value)
    const d = (ms: number) => new Date(ms).toISOString().slice(0, 10)
    const expected = { start: d(startMs), end: d(Math.max(startMs, endMs - 1)) }
    expect(bars[0].props('rangeValue')).toEqual(expected)
    expect(bars[1].props('rangeValue')).toEqual(expected)
    // 模拟顶部条 apply → setTimeRange → 两份的 rangeValue 同步变化
    bars[0].vm.$emit('apply-range', { start: '2026-11-01', end: '2026-11-03' })
    await nextTick()
    const bars2 = w.findAllComponents(BoardFilterBar)
    expect(bars2[0].props('rangeValue')).toEqual({ start: '2026-11-01', end: '2026-11-03' })
    expect(bars2[1].props('rangeValue')).toEqual({ start: '2026-11-01', end: '2026-11-03' })
  })

  it('④ 纯文字说明行不得回魂（owner 明确不要）', async () => {
    const src = await readSource()
    expect(src).not.toContain('board-panel__hero-range')
    expect(src).not.toContain('heroRangeDates')
    expect(src).not.toContain('heroRangeLabel')
    // i18n 键也要清干净（不留死键）
    for (const loc of ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']) {
      const localeSrc = await readFile(
        resolve(process.cwd(), `web/src/locales/${loc}/dashboard.ts`),
        'utf8',
      ).catch(() => readFile(resolve(process.cwd(), `src/locales/${loc}/dashboard.ts`), 'utf8'))
      expect(localeSrc).not.toContain('heroRangeLabel')
    }
  })
})
