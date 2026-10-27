// hardcodedCjk.maskInvariance.test.ts — 「注释剥离必须长度不变」的回归门（2026-10-27，第 20 增量）
//
// ★★★ 为什么这条要单独设门：
//   `maskSource` 是**所有** i18n / 文案类门的公共底座（copyClarity / copy-clarity /
//   internalJargon / primaryFocus 都靠它拿「剥掉注释的源码」）。
//   它的**长度不变性**是这些门能报**正确行号**的唯一前提 ——
//   偏移一旦漂移，门报的行号就是错的，而**行号错的门比没有门更坏**（会去改错的地方）。
//
// ★ 2026-10-27 实测到的真缺陷（本门就是它的锁）：
//   `maskHtmlCommentsOutside` 逐段拼接「保护区之外的文本」时，
//   **保护区重叠** ⇒ 某一段被拼了两遍 ⇒ 输出长度比输入长。
//   触发条件：`<style>` 标签出现在 `<script>` 内容区间内
//   （如 `FreeDiscoveryView.vue`：script 内容区间 24→19960，而 `<style>` 开在 12768）。
//   实测 3 个文件长度不一致：CorrelationsView(+3894) / FreeDiscoveryView(+7185) /
//   ModelIntegrityView(+9810) ⇒ 它们的门报的行号全部偏移。
//   ⇒ 修法：先把保护区**合并成互不相交的区间**，再逐段处理，并保证游标单调不减。

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { collectFiles, maskSource } from './hardcodedCjk'

describe('maskSource 长度不变性', () => {
  it('全仓每个源文件的剥离结果与原文等长（偏移不漂 ⇒ 行号可信）', () => {
    const files = collectFiles()
    const bad: string[] = []
    for (const f of files) {
      const src = readFileSync(f, 'utf8')
      const masked = maskSource(f, src)
      if (masked.length !== src.length) {
        bad.push(`${f}  ${src.length} → ${masked.length}（差 ${masked.length - src.length}）`)
      }
    }
    expect(
      bad,
      'maskSource 必须**等长**替换（只把非换行字符替换成空格）——长度一变，所有复用它的门报的行号就全错了：\n' +
        bad.join('\n'),
    ).toEqual([])
  })

  it('★ 回归：`<style>` 落在 `<script>` 区间内时也不得重复拼接', () => {
    // 这正是 FreeDiscoveryView.vue / CorrelationsView.vue / ModelIntegrityView.vue 的形状：
    // script 的字符串里出现了 `<style>` 字面量 ⇒ style 保护区与 script 保护区**重叠**。
    const src = [
      '<script setup>',
      "const s = '<style>.x{color:red}</style>'",
      '</script>',
      '<template><div>{{ s }}</div></template>',
      '<style>',
      '.a{color:blue}',
      '</style>',
      '',
    ].join('\n')
    const masked = maskSource('/tmp/regression/probe.vue', src)
    expect(masked.length, '重叠保护区曾把内容拼了两遍（长度 +N）').toBe(src.length)
    expect(masked.split('\n').length, '行数也必须一致').toBe(src.split('\n').length)
  })

  it('★ 剥离只动注释，不动代码（同一段代码在剥离前后仍能对上）', () => {
    const src = ['<template>', '  <div>甲</div>', '  <!-- 注释里的乙 -->', '</template>', ''].join('\n')
    const masked = maskSource('/tmp/regression/probe2.vue', src)
    expect(masked).toHaveLength(src.length)
    expect(masked).toContain('<div>甲</div>')
    expect(masked).not.toContain('乙')
  })
})
