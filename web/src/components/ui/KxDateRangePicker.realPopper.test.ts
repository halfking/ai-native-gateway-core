// KxDateRangePicker.realPopper.test.ts — 真 EP 装配下的「日历弹层嵌套」契约（2026-11-26 修复轮）。
//
// ★ 为什么需要这个文件：KxDateRangePicker.test.ts 对 ElPopover / ElDatePicker 全部打桩，
//   桩没有 Teleport、没有 vueuse onClickOutside ⇒ 「选第一个日期 ⇒ 整个面板被关掉」
//   这个生产 bug（/dashboard?tab=board 用户实测）在桩测里**永远不会红**。
//   本文件挂真组件，把修复契约钉在真实关闭链路上。
//
// ★ 被钉的机制（EP 2.14.3 + @vueuse/core 14.3.0，源码取证）：
//   - ElPopover(trigger=click) 的自动关闭 = tooltip content 的
//     onClickOutside(popperContentRef)：click 的 composedPath 不含 popper DOM ⇒ onClose；
//   - 面板内 ElDatePicker 的日历弹层默认 teleport 到 body ⇒ 点日历格 = 「外部点击」
//     ⇒ 经 hideAfter(200ms) 延迟后整个面板被关（这就是用户看到的「选完第一个就关」）；
//   - 修复 = 日历弹层 :append-to 本组件面板容器（teleported 被 EP 2.14.3 picker.vue
//     硬编码为 true 且不可覆盖；append-to 经 $attrs 透传到 tooltip content 的 Teleport）
//     ⇒ 日历 DOM 落在 popperContentRef 子树内 ⇒ 判「内部点击」⇒ 面板保持。
//
// ★ jsdom 测试形态的三条纪律（本文件实测钉死）：
//   1. mount 必须 attachTo(document.body)（popover/popper 需要真实 DOM 锚点与事件冒泡）；
//   2. 开面板不能靠 element.click()：EP 的 trigger 监听挂在 ElPopperTrigger 的
//      OnlyChild 包装上，VTU 环境下点击事件到得了 button 但到不了 EP 的 handler
//      ⇒ 测试直接驱动 v-model（emit update:visible），关闭链路（onClickOutside）
//      仍是真实路径，判据不受影响；
//   3. popover 收起走 hideAfter=200ms 延迟 ⇒ 判「面板是否被关」必须先等 300ms+，
//      否则会把「还没关」误读成「没关」（量具假绿）。
//   断言一律读 document 活节点（Teleport 内容不在 wrapper.find 可达范围，
//   见 AppDrawer.drag.test.ts 同款纪律）。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { defineComponent, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ElDatePicker, ElPopover } from 'element-plus'
import KxDateRangePicker from './KxDateRangePicker.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: {
        button: { apply: '应用' },
        dateRange: {
          title: '时间范围',
          custom: '自定义',
          startDate: '开始日期',
          endDate: '结束日期',
          openAria: '选择时间范围',
          endBeforeStart: '结束需不早于开始',
          afterLatest: '不能晚于 {date}',
          spanTooLong: '跨度超过 {n} 天',
          pickDate: '选择日期',
          preset: { today: '今天', yesterday: '昨天', last7d: '近 7 天', last14d: '近 14 天', last30d: '近 30 天', thisMonth: '本月', lastMonth: '上月' },
        },
      },
    },
  },
})

const FIXED = Date.UTC(2026, 8, 30, 12, 0, 0)

beforeEach(() => {
  vi.setSystemTime(FIXED)
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

function mountReal() {
  return mount(KxDateRangePicker, {
    props: { modelValue: { start: '2026-09-24', end: '2026-09-30' } },
    global: { plugins: [i18n] },
    attachTo: document.body,
  })
}

/** 开面板：EP trigger 监听在 VTU 下不可达（见文件头纪律②），直接驱动 v-model。 */
async function openPanel(w: ReturnType<typeof mountReal>) {
  ;(w.findComponent({ name: 'ElPopover' }).vm as any).$.emit('update:visible', true)
  for (let i = 0; i < 6; i++) await nextTick()
}

function livePanel(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[data-testid="kx-dr-panel"]')
  if (!el) throw new Error('活节点里没有 kx-dr-panel')
  return el
}

/** 打开某侧日历弹层（EP：mousedown 输入框即弹），返回日历 popper（应已落在面板内）。 */
async function openCalendar(panel: HTMLElement, which: 'start' | 'end'): Promise<HTMLElement | null> {
  const input = panel.querySelector<HTMLElement>(`[data-testid="kx-dr-input-${which}"] .el-input__inner`)
  if (!input) throw new Error(`没有 ${which} 日期输入框`)
  input.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
  for (let i = 0; i < 6; i++) await nextTick()
  return panel.querySelector<HTMLElement>('.el-picker__popper')
}

/** popover 收起走 hideAfter(200ms) 延迟：判「被关」必须先等过这个窗口（文件头纪律③）。 */
async function waitHideAfter() {
  await new Promise((r) => setTimeout(r, 350))
  for (let i = 0; i < 6; i++) await nextTick()
}

describe('KxDateRangePicker（真 EP 装配）：日历弹层嵌套契约', () => {
  it('★ 核心契约：点日历格选第一个日期，面板不被关闭（生产 bug 复现面）', async () => {
    const w = mountReal()
    await openPanel(w)
    const panel = livePanel()
    const cal = await openCalendar(panel, 'start')
    expect(cal, '日历弹层必须落在面板容器内（append-to=panelRef 修复落点）').not.toBeNull()
    const cell = cal!.querySelector<HTMLElement>('.el-date-table td.available')
    expect(cell, '日历里应有可点的日期格').not.toBeNull()
    cell!.click()
    // 等过 hideAfter 窗口：若该点击被判外点，此刻面板必已关闭（旧形态实测在此变 false）
    await waitHideAfter()
    const vm: any = w.vm
    expect(vm.$.setupState.panelOpen, '选第一个日期后面板必须保持打开').toBe(true)
    expect(document.querySelector('[data-testid="kx-dr-panel"]')).not.toBeNull()
    expect(vm.$.setupState.draftStart, '点中的日期应写入开始日期草稿').not.toBe('')
    w.unmount()
  })

  it('连选开始与结束两个日期，面板全程保持，最后可应用提交', async () => {
    const w = mountReal()
    await openPanel(w)
    const panel = livePanel()
    const vm: any = w.vm

    const calStart = await openCalendar(panel, 'start')
    ;(calStart!.querySelector('.el-date-table td.available') as HTMLElement).click()
    await waitHideAfter()
    expect(vm.$.setupState.panelOpen, '选完开始日期面板保持').toBe(true)

    const calEnd = await openCalendar(panel, 'end')
    ;(calEnd!.querySelector('.el-date-table td.available') as HTMLElement).click()
    await waitHideAfter()
    expect(vm.$.setupState.panelOpen, '选完结束日期面板保持').toBe(true)
    expect(vm.$.setupState.draftStart).not.toBe('')
    expect(vm.$.setupState.draftEnd).not.toBe('')

    const applyBtn = panel.querySelector<HTMLButtonElement>('[data-testid="kx-dr-apply"]')
    expect(applyBtn, '应用按钮存在').not.toBeNull()
    expect(applyBtn!.disabled, '两日期齐备后应用可用').toBe(false)
    applyBtn!.click()
    for (let i = 0; i < 4; i++) await nextTick()
    const applied = w.emitted('apply')
    expect(applied, '应用提交 apply 事件').toBeTruthy()
    w.unmount()
  })

  it('修复落点收口：日历弹层不挂在 body 直下（修复前形态的回归守卫）', async () => {
    const w = mountReal()
    await openPanel(w)
    const panel = livePanel()
    await openCalendar(panel, 'start')
    const directOnBody = Array.from(document.body.children).filter((el) => el.classList.contains('el-picker__popper'))
    expect(directOnBody, '日历弹层不得 body 直挂（= 修复前形态）').toEqual([])
    expect(panel.querySelector('.el-picker__popper'), '日历弹层必须落在面板容器内').not.toBeNull()
    w.unmount()
  })
})

// ── 对照组（判据有效性守卫）：同样的点击序列，在「append-to 缺省（body 直挂）」的
//    旧形态下，面板会在 hideAfter 后被关 ⇒ 证明上面三条判据不是恒绿，红得其所。
describe('对照：append-to 缺省的旧形态（判据有效性守卫）', () => {
  const OldStyle = defineComponent({
    template: `<el-popover v-model:visible="open" trigger="click" :width="344">
      <template #reference><button class="trg">open</button></template>
      <div data-testid="panel-old"><el-date-picker model-value="" type="date" /></div>
    </el-popover>`,
    components: { ElDatePicker, ElPopover },
    setup() {
      return { open: ref(false) }
    },
  })

  it('点日历格 ⇒ 等过 hideAfter ⇒ 面板被关（证明判据能红）', async () => {
    const w = mount(OldStyle, { global: { plugins: [i18n] }, attachTo: document.body })
    ;(w.findComponent({ name: 'ElPopover' }).vm as any).$.emit('update:visible', true)
    for (let i = 0; i < 6; i++) await nextTick()
    const panel = document.querySelector<HTMLElement>('[data-testid="panel-old"]')!
    const input = panel.querySelector<HTMLElement>('.el-input__inner')!
    input.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    for (let i = 0; i < 6; i++) await nextTick()
    const popper = document.querySelector<HTMLElement>('.el-picker__popper')
    expect(popper, '旧形态日历弹层 body 直挂').not.toBeNull()
    expect(panel.contains(popper!), '旧形态日历弹层不在面板内').toBe(false)
    const cell = popper!.querySelector<HTMLElement>('.el-date-table td.available')
    expect(cell).not.toBeNull()
    cell!.click()
    await waitHideAfter()
    expect((w.vm as any).open, '旧形态：点日历格后面板被关（对照组预期红）').toBe(false)
    w.unmount()
  })
})
