// HelpManualView.test.ts — 帮助手册页的装配与覆盖度钉测。
//
// 这道门守两件事：
//   ① 手册必须覆盖约定的十个主题（下载/安装/部署/供应商/凭据/租户/用户/
//      API Key/客户端调用/常见故障）—— 少一章即红；
//   ② 手册页真的把正文渲染出来（标题、目录、代码块、表格），且搜索能筛。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import HelpManualView from './HelpManualView.vue'
import { testI18n } from '../test-utils/i18n'
import { parseMarkdown } from '../lib/markdown'
import manualSource from '../../content/help-manual.zh-CN.md?raw'

/** 手册必须覆盖的十个主题（顺序即目录顺序）。 */
const REQUIRED_CHAPTERS = [
  '1. 下载',
  '2. 安装',
  '3. 部署',
  '4. 供应商配置',
  '5. 凭据管理',
  '6. 租户管理',
  '7. 用户管理',
  '8. API Key 设置',
  '9. 客户端调用',
  '10. 常见故障处理',
]

const chapters = parseMarkdown(manualSource)
  .filter((b): b is Extract<typeof b, { kind: 'heading' }> => b.kind === 'heading' && b.level === 2)
  .map((b) => b.text)

describe('帮助手册正文覆盖度', () => {
  it('包含约定的十个主题章节', () => {
    for (const chapter of REQUIRED_CHAPTERS) {
      expect(chapters).toContain(chapter)
    }
  })

  it('章节顺序与目录顺序一致', () => {
    const positions = REQUIRED_CHAPTERS.map((c) => chapters.indexOf(c))
    expect(positions.every((p) => p >= 0)).toBe(true)
    const sorted = [...positions].sort((a, b) => a - b)
    expect(positions).toEqual(sorted)
  })

  it('每个主题章节下都有子章节（不是空壳标题）', () => {
    const blocks = parseMarkdown(manualSource)
    let current = ''
    const childCounts = new Map<string, number>()
    for (const block of blocks) {
      if (block.kind !== 'heading') continue
      if (block.level === 2) {
        current = block.text
        childCounts.set(current, 0)
      } else if (current) {
        childCounts.set(current, (childCounts.get(current) ?? 0) + 1)
      }
    }
    for (const chapter of REQUIRED_CHAPTERS) {
      expect(childCounts.get(chapter) ?? 0).toBeGreaterThan(0)
    }
  })

  it('正文含代码块、表格与可复制的调用示例', () => {
    const kinds = new Set(parseMarkdown(manualSource).map((b) => b.kind))
    expect(kinds.has('code')).toBe(true)
    expect(kinds.has('table')).toBe(true)
    expect(kinds.has('list')).toBe(true)
  })
})

describe('HelpManualView', () => {
  it('渲染页头、目录与正文', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })

    expect(w.text()).toContain('帮助手册')
    expect(w.findAll('.manual-toc-link')).toHaveLength(REQUIRED_CHAPTERS.length)
    expect(w.findAll('.manual-h2').length).toBeGreaterThanOrEqual(REQUIRED_CHAPTERS.length)
    expect(w.findAll('pre.manual-code').length).toBeGreaterThan(0)
    expect(w.findAll('table.manual-table').length).toBeGreaterThan(0)
  })

  it('表格带语义表头（th scope=col）', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const headerCells = w.findAll('table.manual-table th')
    expect(headerCells.length).toBeGreaterThan(0)
    for (const cell of headerCells) {
      expect(cell.attributes('scope')).toBe('col')
    }
  })

  it('搜索按关键词筛正文，无匹配时给出空态', async () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })

    const input = w.find('input')
    expect(input.exists()).toBe(true)

    await input.setValue('count_tokens')
    const filteredHeadings = w.findAll('.manual-h2')
    expect(filteredHeadings.length).toBe(1)
    expect(filteredHeadings[0].text()).toBe('9. 客户端调用')

    await input.setValue('这个关键词一定搜不到任何内容')
    expect(w.find('.manual-empty').exists()).toBe(true)
    expect(w.text()).toContain('没有匹配的内容')
  })

  it('代码块带复制按钮', () => {
    const w = mount(HelpManualView, { global: { plugins: [testI18n] } })
    const buttons = w.findAll('.manual-copy')
    expect(buttons.length).toBeGreaterThan(0)
    expect(buttons[0].text()).toBe('复制')
  })
})
