// DataTable.test.ts — loading/空态/插槽透传（包裹模式，方案 §4.5.5 姿势 1）。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import DataTable from './DataTable.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/DataTable.vue'), 'utf8')

const tableSlot = '<table class="demo-table"><thead><tr><th>A</th></tr></thead><tbody><tr><td>1</td></tr></tbody></table>'

function mountTable(props: Record<string, unknown> = {}) {
  return mount(DataTable, {
    props,
    slots: { default: tableSlot },
  })
}

describe('DataTable', () => {
  it('默认渲染横向滚动容器并透传表格插槽', () => {
    const w = mountTable()
    expect(w.find('.app-data-table__scroller--scroll').exists()).toBe(true)
    expect(w.find('.demo-table').exists()).toBe(true)
    expect(w.text()).toContain('1')
  })

  it('loading 时以 AppSpinner 替换表格区域', () => {
    const w = mountTable({ loading: true })
    expect(w.find('.app-spinner').exists()).toBe(true)
    expect(w.find('.demo-table').exists()).toBe(false)
  })

  it('empty 时以 EmptyState 替换表格区域并显示文案', () => {
    const w = mountTable({ empty: true, emptyText: '暂无数据' })
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.text()).toContain('暂无数据')
    expect(w.find('.demo-table').exists()).toBe(false)
    // loading 优先于 empty
    const w2 = mountTable({ loading: true, empty: true, emptyText: 'x' })
    expect(w2.find('.app-spinner').exists()).toBe(true)
  })

  it('scrollable=false 时不启用滚动容器与 min-width', () => {
    const w = mountTable({ scrollable: false })
    expect(w.find('.app-data-table__scroller--scroll').exists()).toBe(false)
    expect(w.find('.demo-table').exists()).toBe(true)
  })

  it('minWidth 映射为 CSS 变量（源码断言 :slotted 列宽保护）', () => {
    const w = mountTable({ minWidth: '960px' })
    const style = w.find('.app-data-table__scroller').attributes('style') ?? ''
    expect(style).toContain('--dt-min-width: 960px')
    expect(source).toContain(':slotted(table)')
    expect(source).toContain('overflow-x: auto')
  })
})
