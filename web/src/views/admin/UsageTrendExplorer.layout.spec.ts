/**
 * UsageTrendExplorer.layout.spec.ts — 「折线图加载完被清空」+「全部展开不内部滚动」的布局契约门。
 *
 * 存在理由（2026-11-23 真实踩坑，两轮诉求叠在同一处）：
 *
 * ① 现象＝折线图先显示、数据加载完成后消失。**不是数据被清空，是画布被压成 0 高**。
 *    本机 211 实测（浏览器内量得）：
 *      .ute 容器 610px（定高 flex 列 + overflow:hidden）
 *      .ute__rankings 1304px（flex:0 0 auto ⇒ 永不让步，6 张 Top5 卡片撑出来的）
 *      .ute__chart 26px（flex:1 1 auto + min-height:0 ⇒ 唯一可无限收缩的项）
 *      .mtc / canvas 0px（canvas 的 CSS 是 height:100% !important ⇒ 父容器 0 ⇒ 画布 0）
 *      Chart.js 自以为 607px（内联 height 被 !important 压过）
 *    ⇒ 排行榜加载中（≈150px）时图表拿得到空间，加载完（1304px）才塌 —— 看着像
 *      「数据一来图就没」，实际是 flex 收缩把画布压没了。
 *
 * ② owner 追加诉求＝「页面不需要内部滚动，所有列表展开，不要在列表内滚动」。
 *    ⇒ 滚动责任上移到外层 `.main-body--fill`；`.ute` 高度由内容决定；
 *      表格与排行榜**整段铺开**（不设 max-height、不设内部 overflow）。
 *
 * 单测抓不到这类塌陷：jsdom 没有布局引擎（所有元素高度恒 0），
 * 组件逻辑测试也证明图表组件本身是稳定的。
 * 所以这里钉**CSS 契约**（源码级断言），把不变量锁住：
 *   ① 图表区有固定高度基准 + 硬下限（塌陷的直接防线，且高度唯一确定）；
 *   ② 排行榜与表格**不得**有 max-height / 内部 overflow（列表完整展开）；
 *   ③ 页面容器**不得**是滚动容器（不内部滚动）；
 *   ④ 外层 `.main-body--fill` **必须**承担滚动（否则 ②③ 之后无处可滚 = 内容被裁）。
 *
 * 通用式：**「元素消失」先量高度，再怀疑数据**。CSS `!important` 的高度
 * 声明与 JS 库的内联尺寸会互相覆盖，两边都可能与肉眼所见不一致。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const USAGE_TREND = resolve(process.cwd(), 'src/views/admin/UsageTrendExplorer.vue')
const APP_VUE = resolve(process.cwd(), 'src/App.vue')

/**
 * 取 <style> 段。
 *
 * ★ 必须取**最后一个** style 标签：.vue 里注释中也可能出现字面量 `<style>`
 *   （例如「已修在 <style> 的 xxx」），用 `/<style[^>]*>([\s\S]*?)<\/style>/`
 *   会从注释里的那个字面量开始匹配，把整段脚本一起吞进来 ⇒ 后续解析全错位。
 */
function styleBlock(file: string): string {
  const raw = readFileSync(file, 'utf8')
  const start = raw.lastIndexOf('<style')
  expect(start, `${file} 必须有 <style> 段`).toBeGreaterThan(-1)
  const open = raw.indexOf('>', start)
  const end = raw.indexOf('</style>', open)
  expect(end, `${file} 的 <style> 段必须闭合`).toBeGreaterThan(-1)
  return raw.slice(open + 1, end)
}

/**
 * 取某个选择器的规则体。
 *
 * ★ 必须是**顶层**规则（不取 @media 里的），且同名的多条按**后者覆盖前者**取最后一条。
 * 坑：① `.ute__chart,\n.ute__table { ... }` 是**分组选择器** —— 用
 *      `indexOf('.ute__table {')` 会命中分组里那一行，拿到的却是另一个选择器的
 *      规则体（背景/边框那组），断言会莫名其妙地红。
 *    ② @media(768px) 里的同名规则在文件里更靠后 ⇒ 「取最后一条」会拿到断点内的
 *      覆盖值，而不是桌面端基线 —— 所以这里只扫**顶层**（花括号深度 0）。
 */
function ruleBody(selector: string, file = USAGE_TREND): string {
  const css = styleBlock(file).replace(/\/\*[\s\S]*?\*\//g, '')
  const hits: string[] = []
  let depth = 0
  let sel = ''
  let body = ''
  let collecting = false
  for (const ch of css) {
    if (ch === '{') {
      if (depth === 0) {
        sel = sel.trim()
        body = ''
        collecting = sel.split(',').map((s) => s.trim()).includes(selector)
      }
      depth++
      continue
    }
    if (ch === '}') {
      depth--
      if (depth === 0 && collecting) hits.push(body)
      sel = ''
      body = ''
      collecting = false
      continue
    }
    if (depth === 0) {
      sel += ch
      continue
    }
    if (depth === 1 && collecting) body += ch
  }
  expect(hits.length, `样式表里必须存在顶层规则 ${selector}`).toBeGreaterThan(0)
  return hits[hits.length - 1]
}

describe('UsageTrendExplorer 布局契约（图表不塌陷 + 列表完整展开）', () => {
  it('① 图表区有固定高度基准与硬下限（塌陷的直接防线）', () => {
    const body = ruleBody('.ute__chart')
    const minHeight = body.match(/min-height:\s*([0-9]+)px/)
    expect(minHeight, '.ute__chart 必须声明 min-height（塌陷的直接防线）').toBeTruthy()
    expect(Number(minHeight![1])).toBeGreaterThanOrEqual(280)
    // flex 基准固定 ⇒ 高度唯一确定（页面高度由内容决定后，basis:auto 会回退到
    // canvas 固有高度，并在 Chart.js 写回 canvas.height 属性后自我放大）
    expect(body, '.ute__chart 必须用固定 flex 基准（0 0 Npx），不能随 flex 伸缩')
      .toMatch(/flex:\s*0\s+0\s+\d+px/)
    expect(body).not.toMatch(/min-height:\s*0/)
  })

  it('② 排行榜完整展开：不得有高度上限，也不得内部滚动', () => {
    const body = ruleBody('.ute__rankings')
    expect(body, '排行榜必须完整展开 ⇒ 不得再设 max-height').not.toMatch(/max-height/)
    expect(body, '排行榜不得内部滚动').not.toMatch(/overflow:\s*(auto|scroll|hidden)/)
    expect(body, '排行榜必须是不伸缩的整段（flex:0 0 auto）').toMatch(/flex:\s*0\s+0\s+auto/)
  })

  it('③ 表格完整展开：不得有高度上限，也不得内部滚动', () => {
    const body = ruleBody('.ute__table')
    expect(body, '表格必须完整展开 ⇒ 不得再设 max-height').not.toMatch(/max-height/)
    expect(body, '表格不得内部滚动').not.toMatch(/overflow:\s*(auto|scroll|hidden)/)
    expect(body, '表格必须是不伸缩的整段（flex:0 0 auto）').toMatch(/flex:\s*0\s+0\s+auto/)
  })

  it('④ 页面容器不是滚动容器（滚动责任上移到外层）', () => {
    const body = ruleBody('.ute')
    expect(body, '.ute 不得是滚动容器（页面不内部滚动）')
      .not.toMatch(/overflow:\s*(auto|scroll|hidden)/)
    // 内容多高页面就多高：不得再被 flex 压回一屏高
    expect(body, '.ute 必须 flex:0 0 auto（内容多高就多高，不被压缩）')
      .toMatch(/flex:\s*0\s+0\s+auto/)
  })

  it('⑤ 外层 .main-body--fill 必须承担滚动（否则内容无处可滚 = 被裁）', () => {
    const body = ruleBody('.main-body.main-body--fill', APP_VUE)
    expect(body, '外层必须能滚（overflow-y:auto / overflow:auto）')
      .toMatch(/overflow(-y)?:\s*auto/)
    expect(body, '外层不得再是 overflow:hidden（会把展开的内容裁掉）')
      .not.toMatch(/overflow:\s*hidden/)
  })
})
