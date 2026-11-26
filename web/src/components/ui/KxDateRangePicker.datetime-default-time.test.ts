// KxDateRangePicker.datetime-default-time.test.ts —— datetime 精度「手选日期产出
// 有效整日窗」的行为契约（2026-11-26 修「请求日志页手选 2026-11-24 被弹回」轮）。
//
// ★ 报障链（211 实测）：用户点日历格选 2026-11-24、点应用 ⇒ 选择被丢弃、
//   触发器弹回旧值 ⇒ 「日期控制选择日期是无效的」。
// ★ 本文件钉的是组件层修复（default-time）：el-date-picker 的 datetime 日历
//   「只点日历格、不动时间」产出当天 00:00 ⇒ 开始=结束=00:00 零长窗 ⇒ 下游
//   （requestLogsWindow.pickerRangeToQueryWindow ⇒ 页面 e<=s 守卫）判无效丢弃。
//   修复 = 开始输入挂 default-time 00:00、结束输入挂 23:59（EP 2.14.3 单值
//   datetime 面板 panel-date-pick 消费**单个 Date** 的 default-time）⇒ 点格即产出
//   整日窗。视图层另一半（RequestLogsView 漏传 precision ⇒ date 精度渲染 ⇒ 产出
//   纯日期串 ⇒ parseLocalMinute 按 UTC 解析）由 RequestLogsView.window.contract
//   .spec.ts 的新钉钉住；两层缺一，手选都无效。
//
// ★ jsdom 实测纪律（zz 探针钉死，本文件据此写）：
//   - mousedown 输入框在 jsdom 下**不会**展开日历弹层（两个弹层恒 display:none）；
//   - 但两个 date-picker 的弹层**常驻**面板 DOM（persistent），input 的
//     aria-controls 与弹层 id 一一对应 ⇒ 按 aria-controls 定位弹层是唯一确定性
//     手法（querySelector 首/尾都会抓错侧，实测两边都写错过）；
//   - 对隐藏弹层里的日历格 click，EP 处理器照常触发（realPopper.test.ts 同款）。
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../../locales/zh-CN'
import KxDateRangePicker from './KxDateRangePicker.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN },
})

const FIXED = Date.UTC(2026, 8, 30, 12, 0, 0) // 2026-09-30（日历显示 9 月）

beforeEach(() => {
  vi.setSystemTime(FIXED)
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

function mountReal(precision: 'date' | 'datetime' = 'datetime') {
  return mount(KxDateRangePicker, {
    props:
      precision === 'datetime'
        ? { precision, modelValue: { start: '2026-09-24 00:00', end: '2026-09-30 23:59' } }
        : { precision, modelValue: { start: '2026-09-24', end: '2026-09-30' } },
    global: { plugins: [i18n] },
    attachTo: document.body,
  })
}

async function openPanel(w: ReturnType<typeof mountReal>) {
  ;(w.findComponent({ name: 'ElPopover' }).vm as any).$.emit('update:visible', true)
  for (let i = 0; i < 6; i++) await nextTick()
}

function livePanel(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[data-testid="kx-dr-panel"]')
  if (!el) throw new Error('活节点里没有 kx-dr-panel')
  return el
}

/** input 的 aria-controls ⇔ 弹层 id 一一对应（探针钉死），按它定位**本侧**弹层。 */
function popperFor(panel: HTMLElement, which: 'start' | 'end'): HTMLElement {
  const input = panel.querySelector<HTMLElement>(`[data-testid="kx-dr-input-${which}"] .el-input__inner`)
  if (!input) throw new Error(`没有 ${which} 日期输入框`)
  const popperId = input.getAttribute('aria-controls')
  if (!popperId) throw new Error(`${which} 输入框没有 aria-controls`)
  const popper = document.getElementById(popperId)
  if (!popper) throw new Error(`aria-controls=${popperId} 指向的弹层不在 DOM`)
  if (!panel.contains(popper)) throw new Error(`${which} 的弹层没落在面板容器内（append-to 修复面）`)
  return popper
}

/** 选中当月 N 日（点日历格、不碰时间输入——正是报障用户的操作形态）。 */
async function clickDay(panel: HTMLElement, which: 'start' | 'end', day: number) {
  const cal = popperFor(panel, which)
  // 只取当月格（排除 prev/next-month 溢出格，避免同名日期误中相邻月的 N 日）。
  const cell = Array.from(cal.querySelectorAll<HTMLElement>('.el-date-table td.available:not(.prev-month):not(.next-month)')).find(
    (td) => td.querySelector('.el-date-table-cell__text')?.textContent?.trim() === String(day),
  )
  if (!cell) throw new Error(`${which} 日历里没有 ${day} 日的可点格`)
  cell.click()
  for (let i = 0; i < 6; i++) await nextTick()
}

describe('KxDateRangePicker datetime 精度：手选日期产出有效整日窗（default-time 修复）', () => {
  it('★ 核心契约：只点日历格，开始=00:00、结束=23:59，应用提交有效整日窗', async () => {
    const w = mountReal('datetime')
    await openPanel(w)
    const panel = livePanel()
    const vm: any = w.vm

    // 用户形态：开始点 11 日、结束也点 11 日（单日查询）——修复前两端都是 00:00
    // 零长窗，被页面判无效丢弃。
    await clickDay(panel, 'start', 11)
    expect(vm.$.setupState.draftStart, '开始草稿必须带 00:00 时刻').toContain(' 00:00')
    await clickDay(panel, 'end', 11)
    expect(vm.$.setupState.draftEnd, '结束草稿必须带 23:59 时刻').toContain(' 23:59')
    expect(vm.$.setupState.draftStart.slice(0, 10), '起止落在同一天').toBe(vm.$.setupState.draftEnd.slice(0, 10))

    const applyBtn = panel.querySelector<HTMLButtonElement>('[data-testid="kx-dr-apply"]')
    expect(applyBtn, '应用按钮存在').not.toBeNull()
    expect(applyBtn!.disabled, '单日整日窗可应用').toBe(false)
    applyBtn!.click()
    for (let i = 0; i < 4; i++) await nextTick()

    const applied = w.emitted('apply')?.[0]?.[0] as { start: string; end: string } | undefined
    expect(applied, '应用提交 apply 事件').toBeTruthy()
    expect(applied!.start).toContain(' 00:00')
    expect(applied!.end).toContain(' 23:59')
    w.unmount()
  })

  it('提交的整日窗经页面真实换算（pickerRangeToQueryWindow）后 e>s、跨度 24h−1ms', async () => {
    const w = mountReal('datetime')
    await openPanel(w)
    const panel = livePanel()
    // 复现报障场景：两侧都手选 24 日（= 用户点名查不动的 2026-11-24 同形态）。
    await clickDay(panel, 'start', 24)
    await clickDay(panel, 'end', 24)
    const applyBtn = panel.querySelector<HTMLButtonElement>('[data-testid="kx-dr-apply"]')!
    expect(applyBtn.disabled, '单日整日窗可应用').toBe(false)
    applyBtn.click()
    for (let i = 0; i < 4; i++) await nextTick()
    const applied = w.emitted('apply')?.[0]?.[0] as { start: string; end: string }
    // 请求日志页 onHeaderRangeApply 的换算与守卫（真实函数）：
    // 修复前 zero-length 窗在此 e<=s ⇒ 静默 return（选择被丢弃）。
    const { pickerRangeToQueryWindow } = await import('../../utils/requestLogsWindow')
    const win = pickerRangeToQueryWindow(applied)
    expect(Number.isNaN(win.start.getTime()), '开始可解析').toBe(false)
    expect(Number.isNaN(win.end.getTime()), '结束可解析').toBe(false)
    expect(win.end.getTime()).toBeGreaterThan(win.start.getTime())
    expect(win.end.getTime() - win.start.getTime()).toBe(24 * 3600 * 1000 - 1)
    w.unmount()
  })

  it('空草稿（modelValue=null）点格 ⇒ default-time 直接产出 00:00/23:59（不靠归一化兜底）', async () => {
    // ★ default-time 的专属判据：归一化只在「同日零长窗」时兜底，救不了空草稿
    //   场景下「起=止=00:00 但用户想跨两天」（如起 10 日、止 12 日，两端都 00:00
    //   也能过 e>s）——而结束端若不是 23:59，跨日查询会丢掉最后一天的绝大部分。
    //   本条变异 default-time 时必红（且归一化救不了它：两日不同、时刻同为
    //   00:00，不是零长窗）。
    const w = mount(KxDateRangePicker, {
      props: { precision: 'datetime', modelValue: null },
      global: { plugins: [i18n] },
      attachTo: document.body,
    })
    await openPanel(w)
    const panel = livePanel()
    await clickDay(panel, 'start', 10)
    await clickDay(panel, 'end', 12)
    const vm: any = w.vm
    expect(vm.$.setupState.draftStart, '开始=10 日 00:00').toBe('2026-09-10 00:00')
    expect(vm.$.setupState.draftEnd, '结束必须=12 日 23:59（default-time；继承空值则只有日期无时刻）').toBe('2026-09-12 23:59')
    w.unmount()
  })

  it('滚动窗时刻上手选同一天（零长窗）⇒ 应用时归一化为整日窗（第三层修复）', async () => {
    // 生产形态：h24 滚动窗回显两端同分钟（如 07:51–07:51）。用户手选同一天时
    // 两端**继承**原时刻 ⇒ 跨度为零 ⇒ 页面判 e<=s 丢弃。组件必须在应用时把
    // 「同日 + 时刻相同」归一化成 00:00–23:59。
    const w = mount(KxDateRangePicker, {
      props: { precision: 'datetime', modelValue: { start: '2026-09-24 07:51', end: '2026-09-30 07:51' } },
      global: { plugins: [i18n] },
      attachTo: document.body,
    })
    await openPanel(w)
    const panel = livePanel()
    await clickDay(panel, 'start', 11)
    await clickDay(panel, 'end', 11)
    const vm: any = w.vm
    // 点格继承原时刻（EP 行为，探针实测）⇒ 11 日 07:51 / 11 日 07:51 零长窗。
    expect(vm.$.setupState.draftStart).toBe('2026-09-11 07:51')
    expect(vm.$.setupState.draftEnd).toBe('2026-09-11 07:51')
    const applyBtn = panel.querySelector<HTMLButtonElement>('[data-testid="kx-dr-apply"]')
    expect(applyBtn!.disabled, '同日零长窗 canApply 仍为真（draftInvalid 只拦 e<s）').toBe(false)
    applyBtn!.click()
    for (let i = 0; i < 4; i++) await nextTick()
    const applied = w.emitted('apply')?.[0]?.[0] as { start: string; end: string } | undefined
    expect(applied).toBeTruthy()
    expect(applied!.start).toBe('2026-09-11 00:00')
    expect(applied!.end).toBe('2026-09-11 23:59')
    w.unmount()
  })

  it('不同日、不同时刻的正常草稿不被归一化改写', async () => {
    const w = mountReal('datetime')
    await openPanel(w)
    const panel = livePanel()
    await clickDay(panel, 'start', 11)
    await clickDay(panel, 'end', 25)
    const vm: any = w.vm
    expect(vm.$.setupState.draftStart).toBe('2026-09-11 00:00')
    expect(vm.$.setupState.draftEnd).toBe('2026-09-25 23:59')
    w.unmount()
  })
})

// ── 对照臂（判据有效性守卫）：date 精度不挂 default-time ⇒ 点格产出纯日期串。
//    若有人把 default-time 无条件挂上（含 date 精度），日期选择语义会被悄悄改写
//    （结束日凭空多出 23:59），本臂转红。
describe('对照：date 精度（判据有效性守卫）', () => {
  it('date 精度点格产出纯日期串（不带时刻），default-time 未生效', async () => {
    const w = mountReal('date')
    await openPanel(w)
    const panel = livePanel()
    const vm: any = w.vm
    await clickDay(panel, 'start', 11)
    expect(vm.$.setupState.draftStart, 'date 精度产出 YYYY-MM-DD').toMatch(/^\d{4}-\d{2}-\d{2}$/)
    w.unmount()
  })
})
