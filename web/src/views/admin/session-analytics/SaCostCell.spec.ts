// SaCostCell.spec.ts —— N9「混合单位成本不得被渲染成一个金额」的行为门。
//
// ★ 背景（缺陷 N9）：会话分析族的 `total_cost` = internal_cost(美元) +
//   external_cost(积分) ⇒ **跨单位相加**（统计标准 framework §3.2）⇒
//   渲染成 `$12.34` 等于凭空造出一个不存在的美元金额；而同一页的脚注又明说
//   「两种单位不同、不能当作同一个金额」⇒ 表格与脚注互相打脸。
//
// ★ 本门断的是**渲染结果**（不是源码形状）：
//   ① 混合值不得出现 $（也不得出现那个合计数）；
//   ② 两个单位必须**分开**呈现，各自带标签；
//   ③ 外部成本为 0 ⇒ 渲染 '—' + 标题（积分写侧未落地 ⇒ 不能把「没采到」
//      讲成「测得是 0」）—— 规则见同目录 saCostSplit.ts。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { testI18n } from '../../../test-utils/i18n'
import SaCostCell from './components/SaCostCell.vue'

function factory(props: Record<string, unknown> = {}) {
  return mount(SaCostCell, {
    props: { internalCost: 12.5, externalCost: 3, ...props },
    global: { plugins: [testI18n] },
  })
}

describe('N9 · 会话分析成本格：两个单位必须分开呈现', () => {
  it('① 混合值不得被渲染成带 $ 的金额', () => {
    const w = factory()
    const text = w.text()
    // ★ 美元符号**不得**出现在这一格里（内部成本那一侧的单位由标签与
    //   「美元」字样承担，不由 $ 冒充）。
    expect(text, '成本格里出现了 $').not.toContain('$')
    // 合计数（12.5 + 3 = 15.5）也不得出现 —— 它是一个不存在的量。
    expect(text, '渲染出了跨单位合计数 15.50').not.toContain('15.50')
  })

  it('② 两个单位各自成行、各带标签与数值', () => {
    const w = factory()
    const text = w.text()
    expect(text).toContain('内部成本（美元）')
    expect(text).toContain('12.50 美元')
    expect(text).toContain('外部成本（积分）')
    expect(text).toContain('3 积分')
  })

  it('③ 外部成本为 0 ⇒ 渲染「—」而不是「0 积分」（0 不代表测得为零）', () => {
    const w = factory({ externalCost: 0 })
    const text = w.text()
    expect(text, '把「未采集」渲染成了 0 积分').not.toContain('0 积分')
    expect(text).toContain('—')
    // 内部成本那一侧不受这条规则影响（它是真被测到的量，0 也是合法测量值）。
    expect(text).toContain('12.50 美元')
  })

  it('④ 外部成本落在占位符时必须挂上「未携带积分」的标题说明', () => {
    const w = factory({ externalCost: 0 })
    const title = w.find('.sa-cost-cell__value--placeholder').attributes('title')
    expect(title, '占位符必须带标题说明').toBeTruthy()
    expect(title).toContain('积分')
    expect(title).toContain('未') // zh-CN 文案：「…尚未单独采集…」
  })

  it('⑤ 外部成本有真实值时不得挂那个标题（否则会把真实值说成没采到）', () => {
    const w = factory({ externalCost: 3 })
    expect(w.find('.sa-cost-cell__value--placeholder').exists()).toBe(false)
  })

  it('⑥ 缺值（字段没来）⇒ 也是占位符，不渲染 0', () => {
    const w = factory({ internalCost: null, externalCost: undefined })
    const text = w.text()
    expect(text).not.toContain('0 美元')
    expect(text).not.toContain('0 积分')
    expect(text).toContain('—')
  })
})
