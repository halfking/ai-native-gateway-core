import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// 名称列「尾部优先」门禁（UI规范 10 §4.6.76 / D-IDENT-01）。
//
// 缺陷：`font_scale=2.0` 时首页热门模型列表里
//   「claude-opus-5-5」与「claude-opus-5」**都显示为「claude-opus…」**
// ⇒ 两个模型渲染成同一串，用户无法区分。根因与 stat 卡同族：
// 名称列宽 `38%` 不随 font_scale 变（rem/% 都不缩放，§4.6.72/§4.6.74），
// 而字放大了 2× ⇒ 末尾的版本号被顺截断吃掉 —— 而末尾正是区分字符所在。
//
// 本组断言钉**三件事**，其中第 ③ 条是本轮实测才发现的：
//   ① `direction: rtl` 在（省略号落到行首、保留末尾区分字符）；
//   ② 仍然是 `nowrap`（本列要保的是行高节奏，不是换行）；
//   ③ **必须同时有 `text-align: left`** —— `direction: rtl` 会把默认对齐翻成行尾，
//      实测 100% 字号下短名字整体右移（首个字符距左沿 0px → 31.6/46/86.1px）。
//      只看 `direction: rtl` 的存在与否，会漏掉这条对**所有正常字号用户**可见的回归。

const HOME = resolve(process.cwd(), 'src/views/HomeView.vue')

function barNameRule(src: string): string {
  const stripped = src.replace(/\/\*[\s\S]*?\*\//g, '')
  const m = stripped.match(/\.home__bar-name\s*\{([^}]*)\}/)
  if (!m || m[1] == null) throw new Error('HomeView.vue 里找不到 .home__bar-name 规则')
  return m[1]
}

describe('热门模型名称列尾部优先（§4.6.76 / D-IDENT-01）', () => {
  const rule = barNameRule(readFileSync(HOME, 'utf8'))

  it('① 省略号落行首：direction: rtl', () => {
    expect(rule).toMatch(/direction\s*:\s*rtl/)
  })

  it('② 仍保持 nowrap —— 本列保的是行高节奏，不是换行', () => {
    expect(rule).toMatch(/white-space\s*:\s*nowrap/)
    expect(rule).not.toMatch(/white-space\s*:\s*normal/)
    // 换行是实测过的落选方案（2.0× 行高 47→82px，+75%），别被"顺手优化"改回来
    expect(rule).not.toMatch(/overflow-wrap\s*:\s*break-word/)
  })

  it('③ ★ direction: rtl 必须配 text-align: left，否则正常字号下短名字整体右移', () => {
    // 实测：只加 direction: rtl 时，100% 字号下首个字符距元素左沿
    // 0px → 31.6 / 46 / 86.1px（按名字长短），加上 text-align: left 才回到 0px。
    expect(rule).toMatch(/text-align\s*:\s*left/)
  })

  it('④ 名称列宽保持 38% —— 加宽是实测落选方案（进度条 146→98px）', () => {
    expect(rule).toMatch(/width\s*:\s*38%/)
  })
})
