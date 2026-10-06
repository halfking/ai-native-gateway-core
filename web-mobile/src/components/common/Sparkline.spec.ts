import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import Sparkline from './Sparkline.vue'

// Sparkline 宽度契约门（§4.6.42）。
//
// 缺陷（真实浏览器实跑读数，非源码推断）：`/m` 总览页「近 7 日趋势」的两条曲线
// 只占卡片右侧约 1/3。量法：776 CSS px 视口下描边跨度约 340 设备 px ÷ DPR 1.523
// ≈ **223 CSS px**，恰好等于 HomeView 传的 `:width="220"`；而卡片可用宽度约
// 688 CSS px ⇒ 左侧 2/3 全空。
//
// 根因：`width` 被烘进 SVG 的 `width` 属性 + `display:block`，SVG 于是永远
// 只有固有宽度，在 `display:flex; justify-content:space-between` 的行里
// 右侧留空。**视口越宽、占比越小** —— 正踩「适配不同真机屏幕」这条。
//
// 本门分两半，量的是**被验的那一半**：
//   · 渲染出的属性（真实 mount，不是我自造的字符串）；
//   · scoped 样式（读源文件，注释已剥离）—— jsdom 不做布局，
//     `getComputedStyle` 拿不到 flex/width 的实际计算值，故样式走源码判。

// 本仓既有口径（照抄 web-mobile/src/styles/safeArea.spec.ts：`resolve(process.cwd(),'src')`）。
// ⚠️ 不要用 `fileURLToPath(new URL(..., import.meta.url))`：本仓 vitest 配置下
//    `import.meta.url` 不是 file:// 协议，直接抛 `The URL must be of scheme file`。
const SRC = resolve(process.cwd(), 'src/components/common/Sparkline.vue')

/** 本仓既定口径：剥离块注释 + HTML 注释 + trim 后以 // / * / /* 开头的整行。
 *  刻意不做「// 到行尾」正则。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .split('\n')
    .map((l) => (/^\s*(\/\/|\*|\/\*)/.test(l) ? '' : l))
    .join('\n')
}

function scopedCss(): string {
  return stripComments(readFileSync(SRC, 'utf8'))
}

const pts = [3, 7, 2, 9, 4, 8, 5]

describe('Sparkline 宽度契约（§4.6.42）', () => {
  it('渲染出的 <svg> 不得带 width 属性 —— 宽度必须由 CSS 决定，不能钉在 prop 上', () => {
    const w = mount(Sparkline, { props: { points: pts, width: 220, height: 40 } })
    const svg = w.get('svg')
    expect(svg.attributes('width')).toBeUndefined()
  })

  it('viewBox 仍按 width prop 计算（路径坐标依赖它，只放宽显示不改数据）', () => {
    const w = mount(Sparkline, { props: { points: pts, width: 220, height: 40 } })
    expect(w.get('svg').attributes('viewBox')).toBe('0 0 220 40')
  })

  it('必须 preserveAspectRatio="none" —— 否则 width:100% 会按比例留信箱边', () => {
    const w = mount(Sparkline, { props: { points: pts, width: 220, height: 40 } })
    expect(w.get('svg').attributes('preserveAspectRatio')).toBe('none')
  })

  it('描边必须 non-scaling-stroke —— 非等比缩放会把线宽横向拉粗', () => {
    const w = mount(Sparkline, { props: { points: pts, width: 220, height: 40 } })
    const stroke = w.findAll('path').find((p) => p.attributes('fill') === 'none')
    expect(stroke).toBeDefined()
    expect(stroke!.attributes('vector-effect')).toBe('non-scaling-stroke')
  })

  it('scoped 样式必须横向吃满剩余空间：width:100% + flex:1 1 auto + min-width:0', () => {
    const css = scopedCss()
    expect(css).toMatch(/\.sparkline\s*\{[^}]*\bwidth:\s*100%/)
    expect(css).toMatch(/\.sparkline\s*\{[^}]*\bflex:\s*1 1 auto/)
    expect(css).toMatch(/\.sparkline\s*\{[^}]*\bmin-width:\s*0/)
  })

  it('⚠ 不得写 height:auto —— CSS 优先于 SVG height 表现属性，会把高度按比例撑开', () => {
    // 这是修复过程中我自己踩的：600px 宽时 600/220*40 ≈ 109px，高度失控。
    const css = scopedCss()
    expect(css).not.toMatch(/\.sparkline\s*\{[^}]*\bheight:\s*auto/)
  })

  it('路径仍由 points 算出并铺满 viewBox 宽度（修复只动显示层，不动数据）', () => {
    const width = 220
    const w = mount(Sparkline, { props: { points: pts, width, height: 40 } })
    const stroke = w.findAll('path').find((p) => p.attributes('fill') === 'none')!
    const d = stroke.attributes('d')!
    // 断言**几何性质**而不是字符串格式：首点 x=0、末点 x=width。
    // （初版写成 `startsWith('M0 ')`，而 `x.toFixed(1)` 产出的是 `M0.0` ⇒ 红。
    //   那是量具猜错了格式，不是产品问题；改断言，不动实现。）
    const xs = [...d.matchAll(/[ML]([\d.]+) /g)].map((m) => Number(m[1]))
    expect(xs).toHaveLength(pts.length)
    expect(xs[0]).toBe(0)
    expect(xs[xs.length - 1]).toBe(width)
    // 单调递增且不溢出 viewBox
    // ⚠ 本仓 `noUncheckedIndexedAccess: true` ⇒ 索引访问是 `number | undefined`，
    //   直接传给 toBeGreaterThan 会让 `vue-tsc` 判红（TS2345）。
    for (let i = 1; i < xs.length; i++) {
      const cur = xs[i]!
      const prev = xs[i - 1]!
      expect(cur).toBeGreaterThan(prev)
    }
    expect(Math.max(...xs)).toBeLessThanOrEqual(width)
  })

  it('点少于 2 个时不画线，且不崩（既有行为不许被这次修复碰坏）', () => {
    const w = mount(Sparkline, { props: { points: [5], width: 220, height: 40 } })
    const stroke = w.findAll('path').find((p) => p.attributes('fill') === 'none')!
    expect(stroke.attributes('d')).toBe('')
  })
})