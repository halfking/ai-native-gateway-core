/**
 * UsageTrendExplorer.layout.spec.ts — 「折线图加载完成被清空」的布局契约门。
 *
 * 存在理由（2026-11-23 真实踩坑）：
 * 现象＝折线图先显示、数据加载完成后消失。**不是数据被清空，是画布被压成 0 高**。
 * 本机 211 实测（浏览器内量得）：
 *   .ute 容器 610px（定高 flex 列 + overflow:hidden）
 *   .ute__rankings 1304px（flex:0 0 auto ⇒ 永不让步，6 张 Top5 卡片撑出来的）
 *   .ute__chart 26px（flex:1 1 auto + min-height:0 ⇒ 唯一可无限收缩的项）
 *   .mtc / canvas 0px（canvas 的 CSS 是 height:100% !important ⇒ 父容器 0 ⇒ 画布 0）
 *   Chart.js 自以为 607px（内联 height 被 !important 压过）
 * ⇒ 排行榜加载中（≈150px）时图表拿得到空间，加载完（1304px）才塌 —— 看着像
 *   「数据一来图就没」，实际是 flex 收缩把画布压没了。
 *
 * 单测抓不到这个：jsdom 没有布局引擎（所有元素高度恒 0），
 * 组件逻辑测试也证明图表组件本身是稳定的。
 * 所以这里钉**CSS 契约**（源码级断言），把三条不变量锁住：
 *   ① 图表区有硬下限（min-height）—— 塌陷的直接防线；
 *   ② 排行榜区有高度上限 + 内部滚动 —— 不再独占空间把图表挤没；
 *   ③ 页面容器用 auto 而非 hidden —— 残差滚动可达，而不是被裁掉。
 *
 * 通用式：**「元素消失」先量高度，再怀疑数据**。CSS `!important` 的高度
 * 声明与 JS 库的内联尺寸会互相覆盖，两边都可能与肉眼所见不一致。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(process.cwd(), 'src/views/admin/UsageTrendExplorer.vue')
const raw = readFileSync(SRC, 'utf8')

/** 取 <style scoped> 段。 */
function styleBlock(): string {
  const m = raw.match(/<style[^>]*>([\s\S]*?)<\/style>/)
  expect(m, 'UsageTrendExplorer.vue 必须有 <style> 段').toBeTruthy()
  return m![1]
}

/** 取某个选择器的规则体（按 `选择器 {` 精确匹配，忽略注释）。 */
function ruleBody(selector: string): string {
  const css = styleBlock().replace(/\/\*[\s\S]*?\*\//g, '')
  const idx = css.indexOf(`${selector} {`)
  expect(idx, `样式表里必须存在规则 ${selector}`).toBeGreaterThan(-1)
  const open = css.indexOf('{', idx)
  const close = css.indexOf('}', open)
  return css.slice(open + 1, close)
}

describe('UsageTrendExplorer 布局契约（折线图不被压成 0 高）', () => {
  it('① 图表区有硬下限 min-height（≤768 断点同值 280px）', () => {
    const body = ruleBody('.ute__chart')
    const minHeight = body.match(/min-height:\s*([0-9]+)px/)
    expect(minHeight, '.ute__chart 必须声明 min-height（塌陷的直接防线）').toBeTruthy()
    expect(Number(minHeight![1])).toBeGreaterThanOrEqual(280)
  })

  it('② 排行榜区有高度上限 + 内部滚动（不再独占空间）', () => {
    const body = ruleBody('.ute__rankings')
    expect(body).toMatch(/max-height:\s*\d+%/)
    expect(body, '排行榜区必须内部滚动，否则它会把兄弟区块挤没').toMatch(/overflow:\s*auto/)
    // 不得再是「永不让步」（flex-shrink:0 / flex:0 0 auto）
    expect(body).not.toMatch(/flex-shrink:\s*0/)
    expect(body).not.toMatch(/flex:\s*0\s+0\s+auto/)
  })

  it('③ 页面容器用 auto 而非 hidden（残差滚动可达，不被裁掉）', () => {
    const body = ruleBody('.ute')
    expect(body).toMatch(/overflow:\s*auto/)
    expect(body).not.toMatch(/overflow:\s*hidden/)
  })

  it('④ 图表容器仍是 flex 可增长项（min-height 不等于 0）', () => {
    const body = ruleBody('.ute__chart')
    expect(body).not.toMatch(/min-height:\s*0/)
  })
})
