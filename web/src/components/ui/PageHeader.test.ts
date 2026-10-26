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

  // ★ 2026-10-26：这条断言钉住「标题不得被操作区挤成 0 宽」这条布局契约。
  //   背景（实测）：德语页标题宽 = 0px（完全消失）、法语页 = 28px（逐字竖排）。
  //   成因是 flex 规则让「谁让路」写死成标题（actions flex-shrink:0 + main min-width:0）。
  //   ⚠ 这里只能断言**源码形态**（jsdom 不做布局，clientWidth 恒为 0 ⇒ 断不出宽度）。
  //   真正的宽度断言在 scripts/ui-sweep.mjs 的浏览器实测里（见 ui-sweep 报告）。
  //
  //   ⚠⚠ 断言前必须**剥掉注释**：本文件的头注释里写着「原形态：
  //   .app-page-header__actions { flex-shrink: 0 }」—— 那是一段说明文字，
  //   不是 CSS。第一版没剥注释 ⇒ 断言匹配到注释里的字面量 ⇒ 恒红的假读数
  //   （实测：改完 CSS 仍报 " flex-shrink: 0 "）。
  //   ⇒ 判据要钉【样式】，不能被【提到样式的文字】污染。
  it('lets the title keep its width and wraps the action row instead (layout contract, source assertion)', () => {
    const css = source
      .replace(/\/\*[\s\S]*?\*\//g, '')   // 块注释
      .replace(/^\s*\/\/.*$/gm, '')        // 行注释
    const main = css.split('.app-page-header__main {')[1]?.split('}')[0] ?? ''
    const actions = css.split('.app-page-header__actions {')[1]?.split('}')[0] ?? ''

    // ① 标题区不许参与收缩 —— 少了这行，德语页标题会变 0 宽
    expect(main).toMatch(/flex:\s*0\s+0\s+auto/)
    // ② 标题区有宽度上限：长标题不让操作区无米下锅
    expect(main).toMatch(/max-width:\s*50%/)
    // ③ 操作区必须可收缩 —— 原来的 flex-shrink: 0 就是「谁让路」的元凶
    expect(actions).toMatch(/flex:\s*0\s+1\s+auto/)
    expect(actions).not.toMatch(/flex-shrink:\s*0/)
    // ④ 操作区放不下时换行，而不是把内容挤出去
    expect(actions).toMatch(/flex-wrap:\s*wrap/)
  })

  it('drops the half-width cap for the title when the header stacks (mobile source assertion)', () => {
    const css = source
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/^\s*\/\/.*$/gm, '')
    const mobileBlock = css.split('@media (max-width: 768px)')[1] ?? ''
    expect(mobileBlock).toMatch(/\.app-page-header__main\s*\{[^}]*max-width:\s*none/)
  })
})
