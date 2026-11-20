// PageHelpDrawer.test.ts —— 页面级帮助抽屉钉测（2026-11-20）
//
// 钉住五件事：
//  1. 未打开时只渲染 ？ 图标按钮（不渲染抽屉面板）。
//  2. 打开后：标题 = titleKey 翻译、正文含 md 里的关键口径词。
//  3. 关键词过滤：命中保留、不命中显示空态文案。
//  4. 路由切换（routePath 变化）⇒ 抽屉自动收起 + 搜索词清空。
//  5. 未登记路由（entry=null）⇒ 显示「本页暂无帮助」空态而非崩。
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { h, nextTick } from 'vue'
import PageHelpDrawer from './PageHelpDrawer.vue'

// jsdom 无真实弹出层 ⇒ ElDrawer 用桩件：按 modelValue 决定是否渲染 header/default
// slot（与 UserProfileListView.degraded.test.ts 同先例；这里加 modelValue 控制
// 以便钉「routePath 变化 ⇒ 抽屉收起」，加 header slot 因为标题在 header 里）。
// eslint-disable-next-line @typescript-eslint/no-explicit-any -- 桩件与先例同形，any 换取 vue-tsc 通过
const ElDrawerStub: any = {
  props: { modelValue: { type: Boolean, default: false } },
  setup(props: { modelValue: boolean }, { slots }: any) {
    return () =>
      props.modelValue
        ? h('div', { class: 'el-drawer-stub' }, [slots.header?.(), slots.default?.()])
        : null
  },
}

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  globalInjection: true,
  messages: {
    'zh-CN': {
      pageHelp: {
        open: '打开本页的数据说明',
        close: '关闭数据说明',
        intro: '本页讲这一页展示哪些数据、数字怎么算出来的，用于对齐数据、自查偏差原因。',
        search: '搜索本页说明',
        emptyNoResult: '本页说明里没有匹配这句话的内容，换个关键词再试',
        emptyNoContent: '这一页还没有配置数据说明，请联系管理员补充。',
        fallbackTitle: '本页数据说明',
        title: {
          dashboard: '总览页数据说明',
          keys: '密钥页数据说明',
        },
      },
    },
  },
})

function mountDrawer(routePath: string) {
  return mount(PageHelpDrawer, {
    props: { routePath },
    global: {
      plugins: [i18n],
      stubs: { ElDrawer: ElDrawerStub },
    },
  })
}

describe('PageHelpDrawer', () => {
  it('初始只渲染 ？ 触发按钮，抽屉不展开', () => {
    const w = mountDrawer('/dashboard')
    expect(w.find('.page-help-trigger').exists()).toBe(true)
    expect(w.find('.page-help-body').exists()).toBe(false)
  })

  it('点击图标展开抽屉：标题用 titleKey 译文，正文含 md 口径关键词', async () => {
    const w = mountDrawer('/dashboard')
    await w.find('.page-help-trigger').trigger('click')
    await nextTick()
    expect(w.find('.page-help-header-title').text()).toBe('总览页数据说明')
    const body = w.find('.page-help-body').text()
    // dashboard.zh-CN.md 里的真实口径词（窗口内有流量）
    expect(body).toContain('口径')
  })

  it('关键词命中：过滤后仍显示匹配段落；关键词不命中：显示空态句', async () => {
    const w = mountDrawer('/dashboard')
    await w.find('.page-help-trigger').trigger('click')
    await nextTick()

    const input = w.find('.page-help-search')
    await input.setValue('一定不存在的词组xyz')
    await nextTick()
    expect(w.text()).toContain('没有匹配这句话的内容')

    await input.setValue('UTC')
    await nextTick()
    expect(w.text()).not.toContain('没有匹配这句话的内容')
  })

  it('routePath 变化 ⇒ 抽屉收起、搜索词清空（重新打开后输入框为空）', async () => {
    const w = mountDrawer('/dashboard')
    await w.find('.page-help-trigger').trigger('click')
    await nextTick()
    expect(w.find('.page-help-body').exists()).toBe(true)

    await w.find('.page-help-search').setValue('口径')
    await w.setProps({ routePath: '/keys' })
    await nextTick()
    // 抽屉已收起（桩件不再渲染 slot 内容）
    expect(w.find('.page-help-body').exists()).toBe(false)

    // 重新打开 ⇒ 输入框已被 watch 清空，且正文切到 /keys 的内容
    await w.find('.page-help-trigger').trigger('click')
    await nextTick()
    expect((w.find('.page-help-search').element as HTMLInputElement).value).toBe('')
    expect(w.find('.page-help-header-title').text()).toBe('密钥页数据说明')
  })

  it('未登记路由 ⇒ 空态提示而非崩（fallbackTitle 兜底标题）', async () => {
    const w = mountDrawer('/no-such-page')
    await w.find('.page-help-trigger').trigger('click')
    await nextTick()
    expect(w.find('.page-help-header-title').text()).toBe('本页数据说明')
    expect(w.text()).toContain('还没有配置数据说明')
  })
})
