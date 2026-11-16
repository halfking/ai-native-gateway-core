// markdown.test.ts — lib/markdown.ts 的行为钉测。
//
// 覆盖：块级解析（标题/代码块/表格/列表/引用/段落）、行内 span 解析、
// 目录构建、关键词筛选。手册正文由这套解析器渲染 ⇒ 解析器错了 = 手册显示错了。

import { describe, expect, it } from 'vitest'
import {
  buildToc,
  filterManualBlocks,
  inlineText,
  parseInline,
  parseMarkdown,
  type ManualBlock,
} from './markdown'

describe('parseInline', () => {
  it('splits text, code, bold and italic spans', () => {
    const segments = parseInline('plain `code` **bold** *italic* end')
    expect(segments.map((s) => s.kind)).toEqual(['text', 'code', 'text', 'bold', 'text', 'italic', 'text'])
    expect(segments[1]).toMatchObject({ kind: 'code', text: 'code' })
    expect(segments[3]).toMatchObject({ kind: 'bold', text: 'bold' })
    expect(segments[5]).toMatchObject({ kind: 'italic', text: 'italic' })
  })

  it('extracts links with href and label', () => {
    const segments = parseInline('see [the portal](https://example.com/x)')
    expect(segments[1]).toMatchObject({
      kind: 'link',
      text: 'the portal',
      href: 'https://example.com/x',
    })
  })

  it('keeps backticked content literal (no nested bold)', () => {
    const segments = parseInline('`**not bold**`')
    expect(segments).toHaveLength(1)
    expect(segments[0]).toMatchObject({ kind: 'code', text: '**not bold**' })
  })

  it('returns plain text when there is no markup', () => {
    expect(inlineText(parseInline('plain text'))).toBe('plain text')
  })
})

describe('parseMarkdown', () => {
  const source = [
    '# Manual Title',
    '',
    'Intro paragraph.',
    '',
    '## Chapter One',
    '',
    'Body text.',
    '',
    '### Section A',
    '',
    '- first item',
    '- second item',
    '',
    '1. step one',
    '2. step two',
    '',
    '```bash',
    'echo hello',
    '```',
    '',
    '| Name | Meaning |',
    '| --- | --- |',
    '| a | b |',
    '',
    '> a quoted note',
    '',
    '## Chapter Two',
  ].join('\n')

  const blocks = parseMarkdown(source)

  it('parses the document title as a title block', () => {
    expect(blocks[0]).toMatchObject({ kind: 'title', text: 'Manual Title' })
  })

  it('assigns stable ids to chapters and sections', () => {
    const headings = blocks.filter((b): b is Extract<ManualBlock, { kind: 'heading' }> => b.kind === 'heading')
    expect(headings.map((h) => [h.level, h.id])).toEqual([
      [2, 'h2-1'],
      [3, 'h3-1-1'],
      [2, 'h2-2'],
    ])
  })

  it('parses unordered and ordered lists separately', () => {
    const lists = blocks.filter((b): b is Extract<ManualBlock, { kind: 'list' }> => b.kind === 'list')
    expect(lists).toHaveLength(2)
    expect(lists[0].ordered).toBe(false)
    expect(lists[0].items).toHaveLength(2)
    expect(lists[1].ordered).toBe(true)
    expect(lists[1].items).toHaveLength(2)
  })

  it('parses fenced code blocks with their language', () => {
    const code = blocks.find((b): b is Extract<ManualBlock, { kind: 'code' }> => b.kind === 'code')
    expect(code).toMatchObject({ lang: 'bash', text: 'echo hello' })
  })

  it('parses pipe tables into headers and rows', () => {
    const table = blocks.find((b): b is Extract<ManualBlock, { kind: 'table' }> => b.kind === 'table')
    expect(table).toBeTruthy()
    expect(table!.headers.map((h) => inlineText(h))).toEqual(['Name', 'Meaning'])
    expect(table!.rows).toHaveLength(1)
    expect(table!.rows[0].map((c) => inlineText(c))).toEqual(['a', 'b'])
  })

  it('parses block quotes', () => {
    const quote = blocks.find((b): b is Extract<ManualBlock, { kind: 'quote' }> => b.kind === 'quote')
    expect(quote).toBeTruthy()
    expect(inlineText(quote!.segments)).toBe('a quoted note')
  })

  it('is tolerant to CRLF line endings', () => {
    const crlf = parseMarkdown(source.replace(/\n/g, '\r\n'))
    expect(crlf.length).toBe(blocks.length)
  })
})

describe('buildToc', () => {
  it('groups sections under their chapter', () => {
    const toc = buildToc(
      parseMarkdown(['## A', '', '### A1', '', '## B', '', '### B1'].join('\n')),
    )
    expect(toc).toHaveLength(2)
    expect(toc[0].title).toBe('A')
    expect(toc[0].sections.map((s) => s.title)).toEqual(['A1'])
    expect(toc[1].sections.map((s) => s.title)).toEqual(['B1'])
  })
})

describe('filterManualBlocks', () => {
  const blocks = parseMarkdown(
    [
      '## Alpha',
      '',
      'text about routing',
      '',
      '### Alpha child',
      '',
      'text about quota',
      '',
      '## Beta',
      '',
      'text about routing again',
    ].join('\n'),
  )

  it('returns everything for an empty keyword', () => {
    expect(filterManualBlocks(blocks, '   ')).toBe(blocks)
  })

  it('keeps the chapter heading when a hit lives inside its section', () => {
    const filtered = filterManualBlocks(blocks, 'routing')
    const headings = filtered.filter((b): b is Extract<ManualBlock, { kind: 'heading' }> => b.kind === 'heading')
    // "Alpha" survives because its section contains the routing hit.
    expect(headings.map((h) => h.text)).toContain('Alpha')
    expect(headings.map((h) => h.text)).toContain('Beta')
  })

  it('drops blocks that do not match', () => {
    const filtered = filterManualBlocks(blocks, 'quota')
    expect(filtered.some((b) => b.kind === 'paragraph' && inlineText(b.segments).includes('routing'))).toBe(false)
    expect(filtered.some((b) => b.kind === 'paragraph' && inlineText(b.segments).includes('quota'))).toBe(true)
  })

  it('matches case-insensitively', () => {
    const filtered = filterManualBlocks(blocks, 'ROUTING')
    expect(filtered.length).toBeGreaterThan(0)
  })
})
