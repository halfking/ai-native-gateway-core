import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import SaTrendChart from './SaTrendChart.vue'

/**
 * 趋势图组件的契约测试。
 *
 * ★ 为什么钉「polyline 的 points 字符串」而不是像素：
 *   坐标映射是这个组件的全部业务逻辑（值域 → 画布坐标），
 *   点位算错 ⇒ 图画出来是错的，但页面不会报错 ⇒ 只有断言数值才打得住。
 */

describe('SaTrendChart', () => {
  it('空序列渲染空态而不是一张空图', () => {
    const w = mount(SaTrendChart, {
      props: { points: [], emptyText: 'no data' },
    })
    expect(w.find('svg').exists()).toBe(false)
    expect(w.text()).toContain('no data')
  })

  it('单点时落在画布中点（不除零、不越界）', () => {
    const w = mount(SaTrendChart, {
      props: { points: [{ label: '2026-11-01', value: 5 }] },
    })
    const poly = w.find('polyline')
    expect(poly.exists()).toBe(true)
    const points = poly.attributes('points') ?? ''
    const [x, y] = points.split(',')
    expect(Number.isFinite(Number(x))).toBe(true)
    expect(Number.isFinite(Number(y))).toBe(true)
    // 中点 = 左边距 52 + 内容宽度 / 2
    expect(Number(x)).toBeCloseTo(52 + (720 - 52 - 14) / 2, 1)
  })

  it('多点时首尾分别落在左右边界', () => {
    const w = mount(SaTrendChart, {
      props: {
        points: [
          { label: '2026-11-01', value: 0 },
          { label: '2026-11-02', value: 5 },
          { label: '2026-11-03', value: 10 },
        ],
      },
    })
    const pts = (w.find('polyline').attributes('points') ?? '').split(' ')
    expect(pts).toHaveLength(3)
    const firstX = Number(pts[0].split(',')[0])
    const lastX = Number(pts[2].split(',')[0])
    expect(firstX).toBeCloseTo(52, 1)
    expect(lastX).toBeCloseTo(720 - 14, 1)
  })

  it('值越大，纵坐标越小（Y 轴向上）', () => {
    const w = mount(SaTrendChart, {
      props: {
        points: [
          { label: 'a', value: 1 },
          { label: 'b', value: 100 },
        ],
      },
    })
    const pts = (w.find('polyline').attributes('points') ?? '').split(' ')
    const yLow = Number(pts[0].split(',')[1])
    const yHigh = Number(pts[1].split(',')[1])
    expect(yHigh).toBeLessThan(yLow)
  })

  it('全零序列不会产生 NaN 坐标（除零防护）', () => {
    const w = mount(SaTrendChart, {
      props: {
        points: [
          { label: 'a', value: 0 },
          { label: 'b', value: 0 },
        ],
      },
    })
    const pts = (w.find('polyline').attributes('points') ?? '').split(' ')
    for (const p of pts) {
      const [x, y] = p.split(',')
      expect(Number.isFinite(Number(x))).toBe(true)
      expect(Number.isFinite(Number(y))).toBe(true)
    }
  })

  it('负值被夹到基线（不画到画布外）', () => {
    const w = mount(SaTrendChart, {
      props: {
        points: [
          { label: 'a', value: -50 },
          { label: 'b', value: 10 },
        ],
      },
    })
    const pts = (w.find('polyline').attributes('points') ?? '').split(' ')
    const y = Number(pts[0].split(',')[1])
    expect(Number.isFinite(y)).toBe(true)
    expect(y).toBeLessThanOrEqual(180)
  })

  it('日期标签出现在 X 轴上', () => {
    const w = mount(SaTrendChart, {
      props: {
        points: [
          { label: '2026-11-01', value: 1 },
          { label: '2026-11-05', value: 2 },
        ],
      },
    })
    expect(w.text()).toContain('2026-11-01')
    expect(w.text()).toContain('2026-11-05')
  })
})
