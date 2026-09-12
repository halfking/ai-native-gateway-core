// PageHeader.test.ts — 标题/副标题渲染、插槽分发与响应式源码断言。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import PageHeader from './PageHeader.vue'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/PageHeader.vue'), 'utf8')

describe('PageHeader', () => {
  it('renders the title as an h2 with page-header visual metrics', () => {
    const w = mount(PageHeader, { props: { title: '请求日志' } })
    const h2 = w.find('h2.app-page-header__title')
    expect(h2.exists()).toBe(true)
    expect(h2.text()).toBe('请求日志')
  })

  it('renders the subtitle only when provided', () => {
    const withSub = mount(PageHeader, { props: { title: 't', subtitle: 's' } })
    expect(withSub.find('.app-page-header__subtitle').text()).toBe('s')

    const withoutSub = mount(PageHeader, { props: { title: 't' } })
    expect(withoutSub.find('.app-page-header__subtitle').exists()).toBe(false)
  })

  it('projects the actions slot into the right-side container', () => {
    const w = mount(PageHeader, {
      props: { title: 't' },
      slots: { actions: '<button class="btn btn-primary">签发</button>' },
    })
    const actions = w.find('.app-page-header__actions')
    expect(actions.exists()).toBe(true)
    expect(actions.find('button.btn-primary').exists()).toBe(true)
  })

  it('omits the actions container when no actions slot is given', () => {
    const w = mount(PageHeader, { props: { title: 't' } })
    expect(w.find('.app-page-header__actions').exists()).toBe(false)
  })

  it('projects leading slot next to the title and default slot below it', () => {
    const w = mount(PageHeader, {
      props: { title: 't' },
      slots: {
        leading: '<button class="page-back">← 返回</button>',
        default: '<div class="tenant-badge">admin</div>',
      },
    })
    expect(w.find('.app-page-header__main .page-back').exists()).toBe(true)
    expect(w.find('.app-page-header__below .tenant-badge').exists()).toBe(true)
  })

  it('stacks title and actions below the standard 768px breakpoint (responsive source assertion)', () => {
    expect(source).toMatch(/@media \(max-width: 768px\)/)
    const mobileBlock = source.split('@media (max-width: 768px)')[1] ?? ''
    expect(mobileBlock).toContain('flex-direction: column')
    expect(mobileBlock).toContain('flex-wrap: wrap')
  })
})
