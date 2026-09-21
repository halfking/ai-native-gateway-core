// PaginationBar.test.ts — 渲染信息、事件转发、边界禁用与页大小切换。
// i18n 使用真实 zh-CN common.pagination 词条（含新增 pageOf/perPage），
// 断言插值输出而非 key 回退。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import PaginationBar from './PaginationBar.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/PaginationBar.vue'), 'utf8')

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: {
        pagination: {
          total: '共 {n} 条',
          pageOf: '第 {page} / {pages} 页',
          perPage: '每页',
          previous: '上一页',
          next: '下一页',
        },
      },
    },
  },
})

function mountBar(props: Record<string, unknown> = {}) {
  return mount(PaginationBar, {
    props: {
      page: 2,
      pageSize: 50,
      total: 120,
      pageSizes: [50, 100, 200, 500],
      ...props,
    },
    global: { plugins: [i18n] },
  })
}

describe('PaginationBar', () => {
  it('renders interpolated total and page-of-pages info', () => {
    const w = mountBar({ page: 2, pageSize: 50, total: 120 })
    expect(w.find('.pagination-info').text()).toContain('共 120 条')
    expect(w.find('.pagination-info').text()).toContain('第 2 / 3 页')
  })

  it('disables prev on the first page and next on the last page', () => {
    const first = mountBar({ page: 1 })
    expect(first.findAll('button')[0].attributes('disabled')).toBeDefined()
    expect(first.findAll('button')[1].attributes('disabled')).toBeUndefined()

    const last = mountBar({ page: 3 })
    expect(last.findAll('button')[0].attributes('disabled')).toBeUndefined()
    expect(last.findAll('button')[1].attributes('disabled')).toBeDefined()
  })

  it('emits prev/next on button clicks', async () => {
    const w = mountBar({ page: 2 })
    const [prevBtn, nextBtn] = w.findAll('button')
    await prevBtn.trigger('click')
    await nextBtn.trigger('click')
    expect(w.emitted('prev')).toHaveLength(1)
    expect(w.emitted('next')).toHaveLength(1)
  })

  it('renders page-size options and emits change-size with the numeric value', async () => {
    const w = mountBar()
    const select = w.find('select')
    const options = select.findAll('option')
    expect(options.map((o) => o.element.value)).toEqual(['50', '100', '200', '500'])
    await select.setValue('200')
    const events = w.emitted('change-size')
    expect(events).toHaveLength(1)
    expect(events![0]).toEqual([200])
  })

  it('hides the page-size select when pageSizes is empty', () => {
    const w = mountBar({ pageSizes: [] })
    expect(w.find('select').exists()).toBe(false)
    expect(w.find('.page-size-label').exists()).toBe(false)
  })

  it('formats large totals with locale separators without breaking the message', () => {
    const w = mountBar({ total: 1234567 })
    expect(w.find('.pagination-info').text()).toContain('1,234,567')
  })

  it('keeps the mobile breakpoint on the standard 768px (responsive source assertion)', () => {
    expect(source).toMatch(/@media \(max-width: 768px\)/)
    // 720px 是本组件替换掉的 RequestLogsView 碎片段点，不允许回流
    expect(source).not.toMatch(/@media \(max-width: 720px\)/)
    const mobileBlock = source.split('@media (max-width: 768px)')[1] ?? ''
    expect(mobileBlock).toContain('flex-wrap: wrap')
  })
})
