// StatsRow.test.ts — 栅格容器列数配置与容器查询源码断言。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import StatsRow from './StatsRow.vue'
import StatCard from './StatCard.vue'
import { BREAKPOINTS } from '../../config/breakpoints'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/StatsRow.vue'), 'utf8')

function mountRow(props: Record<string, unknown> = {}) {
  return mount(StatsRow, {
    props,
    slots: {
      default: [
        '<StatCard label="A" value="1" />',
        '<StatCard label="B" value="2" />',
        '<StatCard label="C" value="3" />',
      ].join(''),
    },
    global: { components: { StatCard } },
  })
}

describe('StatsRow', () => {
  it('渲染插槽内的 StatCard 为栅格项', () => {
    const w = mountRow()
    expect(w.findAll('.app-stat-card').length).toBe(3)
    expect(w.find('.stats-row').exists()).toBe(true)
  })

  it('cols prop 映射为各档位 CSS 变量（默认 2/2/4）', () => {
    const w = mountRow()
    const style = w.find('.stats-row-outer').attributes('style') ?? ''
    expect(style).toContain('--sr-cols-mobile: 2')
    expect(style).toContain('--sr-cols-tablet: 2')
    expect(style).toContain('--sr-cols-desktop: 4')

    const w2 = mountRow({ cols: { mobile: 1, tablet: 3, desktop: 6 } })
    const style2 = w2.find('.stats-row-outer').attributes('style') ?? ''
    expect(style2).toContain('--sr-cols-mobile: 1')
    expect(style2).toContain('--sr-cols-tablet: 3')
    expect(style2).toContain('--sr-cols-desktop: 6')
  })

  it('响应式源码断言：容器查询切换列数，切换点对齐 breakpoints.ts（768/1024）', () => {
    expect(BREAKPOINTS.tablet).toBe(768)
    expect(BREAKPOINTS.desktop).toBe(1024)
    expect(source).toMatch(/@container \(min-width: 768px\)/)
    expect(source).toMatch(/@container \(min-width: 1024px\)/)
    expect(source).toMatch(/repeat\(var\(--sr-cols-mobile, 2\), minmax\(0, 1fr\)\)/)
    expect(source).toContain('container-type: inline-size')
  })
})
