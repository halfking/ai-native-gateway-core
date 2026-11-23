// PageHelpDrawer.test.ts —— 页面级帮助抽屉钉测（2026-11-20；L1 多语正文轮扩展）
//
// 钉住七件事：
//  1. 未打开时只渲染 ？ 图标按钮（不渲染抽屉面板）。
//  2. 打开后：标题 = titleKey 翻译、正文含 md 里的关键口径词。
//  3. 关键词过滤：命中保留、不命中显示空态文案。
//  4. 路由切换（routePath 变化）⇒ 抽屉自动收起 + 搜索词清空。
//  5. 未登记路由（entry=null）⇒ 显示「本页暂无帮助」空态而非崩。
//  6. ★ L1 多语正文：locale=en-US ⇒ 渲染 <id>.en-US.md 的英文正文；
//     无译文的语种 ⇒ 回退中文正文（不空抽屉）。
//  7. locale 切换 ⇒ 搜索词清空（旧语种关键词对新语种正文必然失配）。
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

/** 每个用例独立 i18n 实例（locale 可变，避免用例间串扰） */
function makeI18n(locale: string) {
  return createI18n({
    legacy: false,
    locale,
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
            dashboardStatsStandards: '看板与会话统计的统计标准',
            keys: '密钥页数据说明',
          },
        },
      },
      'en-US': {
        pageHelp: {
          open: 'Open the data notes for this page',
          close: 'Close data notes',
          intro: 'What this page shows and how each number is calculated, for aligning data and checking deviations.',
          search: 'Search these notes',
          emptyNoResult: 'No match in these notes; try another keyword',
          emptyNoContent: 'No data notes configured for this page yet; contact an administrator.',
          fallbackTitle: 'Data notes',
          title: {
            dashboard: 'Overview data notes',
            keys: 'API keys data notes',
          },
        },
      },
      // ★ 2026-11-21：多语补翻轮后仓内 8 语种 chrome 全齐（含 fr-FR）⇒ mock 补 fr-FR
      //   让「未登记路由 + fr-FR」用例走真实回退链（chrome 出本语种文案、正文空态）。
      'fr-FR': {
        pageHelp: {
          open: "Ouvrir les notes de données de cette page",
          close: "Fermer les notes de données",
          intro: "Ce que montre cette page et comment chaque chiffre est calculé, pour aligner les données et vérifier les écarts.",
          search: "Rechercher dans ces notes",
          emptyNoResult: "Aucune correspondance dans ces notes ; essayez un autre mot-clé",
          emptyNoContent: "Aucune note de données n'est configurée pour cette page ; contactez un administrateur.",
          fallbackTitle: 'Notes de données de cette page',
          title: {
            dashboard: 'Notes de données — vue d’ensemble',
            keys: 'Notes de données — clés API',
          },
        },
      },
    },
  })
}

function mountDrawer(routePath: string, locale = 'zh-CN') {
  return mount(PageHelpDrawer, {
    props: { routePath },
    global: {
      plugins: [makeI18n(locale)],
      stubs: { ElDrawer: ElDrawerStub },
    },
  })
}

/** 打开抽屉并等一轮渲染 */
async function openBody(w: ReturnType<typeof mountDrawer>) {
  await w.find('.page-help-trigger').trigger('click')
  await nextTick()
}

describe('PageHelpDrawer', () => {
  it('初始只渲染 ？ 触发按钮，抽屉不展开', () => {
    const w = mountDrawer('/dashboard')
    expect(w.find('.page-help-trigger').exists()).toBe(true)
    expect(w.find('.page-help-body').exists()).toBe(false)
  })

  it('点击图标展开抽屉：标题用 titleKey 译文，正文含 md 口径关键词', async () => {
    const w = mountDrawer('/dashboard')
    await openBody(w)
    expect(w.find('.page-help-header-title').text()).toBe('总览页数据说明')
    const body = w.find('.page-help-body').text()
    // dashboard.zh-CN.md 里的真实口径词
    expect(body).toContain('口径')
  })

  it('关键词命中：过滤后仍显示匹配段落；关键词不命中：显示空态句', async () => {
    const w = mountDrawer('/dashboard')
    await openBody(w)

    const input = w.find('.page-help-search')
    await input.setValue('一定不存在的词组xyz')
    await nextTick()
    expect(w.text()).toContain('没有匹配这句话的内容')

    // 命中关键词：2026-11-23 统计标准轮把 dashboard.zh-CN.md 里的「UTC 零点」
    // 改成了「系统时区零点」（旧口径是错的），原来用 'UTC' 当命中词会失配 ⇒
    // 换成正文里确实存在的词（口径）。
    await input.setValue('口径')
    await nextTick()
    expect(w.text()).not.toContain('没有匹配这句话的内容')
  })

  it('routePath 变化 ⇒ 抽屉收起、搜索词清空（重新打开后输入框为空）', async () => {
    const w = mountDrawer('/dashboard')
    await openBody(w)
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
    await openBody(w)
    expect(w.find('.page-help-header-title').text()).toBe('本页数据说明')
    expect(w.text()).toContain('还没有配置数据说明')
  })

  it('★ L1：locale=en-US ⇒ 渲染英文正文（dashboard.en-US.md 已落地）', async () => {
    const w = mountDrawer('/dashboard', 'en-US')
    await openBody(w)
    expect(w.find('.page-help-header-title').text()).toBe('Overview data notes')
    const body = w.find('.page-help-body').text()
    expect(body).not.toContain('口径说明')
  })

  it('★ L1：未登记路由 + 非中文 locale ⇒ 本语种空态（不崩、无跨语种残片）', async () => {
    // ★ 2026-11-21 二次改写：多语补翻轮后 93 个登记 id 已全量 7 语种（616 文件），
    //   「已登记 id 无译文」的纯回退场景在仓内不存在了 ⇒ 回退分支改用
    //   「未登记路由（entry=null ⇒ id undefined）+ 非中文 locale」组合钉：
    //   content computed 的 `?? contentByKey.get(zh-CN)` 在 id undefined 时同样返回
    //   undefined ⇒ 走 emptyNoContent 空态（zh-CN 文案），绝不能崩、不能渲染外文残片。
    //   （回退取值逻辑本身由上一条 en-US 用例正向钉住：locale 命中即用。）
    const w = mountDrawer('/no-such-page', 'fr-FR')
    await openBody(w)
    expect(w.find('.page-help-header-title').text()).toBe('Notes de données de cette page')
    // 未登记路由 ⇒ 正文空态（本语种 chrome 文案），不崩、不出现其他语种残片
    expect(w.text()).toContain("Aucune note de données n'est configurée pour cette page")
    expect(w.text()).not.toContain('还没有配置数据说明')
  })

  it('★ locale 切换 ⇒ 搜索词清空', async () => {
    const i18n = makeI18n('zh-CN')
    const w = mount(PageHelpDrawer, {
      props: { routePath: '/dashboard' },
      global: { plugins: [i18n], stubs: { ElDrawer: ElDrawerStub } },
    })
    await openBody(w)
    await w.find('.page-help-search').setValue('口径')
    ;(i18n.global.locale as unknown as { value: string }).value = 'en-US'
    await nextTick()
    expect((w.find('.page-help-search').element as HTMLInputElement).value).toBe('')
  })

  // ── 2026-11-23 统计标准轮：页面内嵌形态 ──────────────────────────────
  it('★ 页面内嵌：contentId 命中统计标准正文（不依赖路由）', async () => {
    const w = mount(PageHelpDrawer, {
      props: { routePath: '/dashboard', contentId: 'dashboard-stats-standards' },
      global: { plugins: [makeI18n('zh-CN')], stubs: { ElDrawer: ElDrawerStub } },
    })
    await openBody(w)
    // 标题出自注册表（页面不自带文案 ⇒ 语种切换只有一个来源）
    expect(w.find('.page-help-header-title').text()).toBe('看板与会话统计的统计标准')
    const body = w.find('.page-help-body').text()
    // 正文真的渲染出来了（import.meta.glob 路径写错会静默空抽屉 ⇒ 必须钉正文非空）
    expect(body).toContain('统计标准')
    expect(body).toContain('65,774')
    expect(body).toContain('72,098')
    expect(body).toContain('供应商返回金额')
  })

  it.each([
    '/admin/reconciliation',
    '/admin/usage',
    '/admin/session-analytics/users',
    '/admin/board-operational/providers',
    '/admin/analytics/model/gpt-5',
    '/tenant/usage',
  ])(
    'includes the localized comparison table in %s',
    async (routePath) => {
      const w = mount(PageHelpDrawer, {
        props: { routePath },
        global: { plugins: [makeI18n('en-US')], stubs: { ElDrawer: ElDrawerStub } },
      })
      await openBody(w)
      expect(w.findAll('table').some((table) => table.text().includes('Provider-reported amount'))).toBe(true)
      expect(w.find('.page-help-body').text()).toContain('Active and new sessions')
    },
  )

  it('keeps unrelated page help focused on that page', async () => {
    const w = mount(PageHelpDrawer, {
      props: { routePath: '/keys' },
      global: { plugins: [makeI18n('en-US')], stubs: { ElDrawer: ElDrawerStub } },
    })
    await openBody(w)
    expect(w.find('.page-help-body').text()).not.toContain('Provider-reported amount')
  })

  it('★ 页面内嵌：label 渲染「图标 + 文字」形态；不给 label 仍是纯图标', async () => {
    const labeled = mount(PageHelpDrawer, {
      props: { routePath: '/dashboard', contentId: 'dashboard-stats-standards', label: '统计标准' },
      global: { plugins: [makeI18n('zh-CN')], stubs: { ElDrawer: ElDrawerStub } },
    })
    expect(labeled.find('.page-help-trigger__label').text()).toBe('统计标准')
    expect(labeled.find('.page-help-trigger').classes()).toContain('page-help-trigger--labeled')

    const plain = mount(PageHelpDrawer, {
      props: { routePath: '/dashboard' },
      global: { plugins: [makeI18n('zh-CN')], stubs: { ElDrawer: ElDrawerStub } },
    })
    expect(plain.find('.page-help-trigger__label').exists()).toBe(false)
    expect(plain.find('.page-help-trigger').classes()).not.toContain('page-help-trigger--labeled')
  })

  it('★ 页面内嵌：contentId 未登记 ⇒ 空态而非崩', async () => {
    const w = mount(PageHelpDrawer, {
      props: { routePath: '/dashboard', contentId: 'no-such-content-id' },
      global: { plugins: [makeI18n('zh-CN')], stubs: { ElDrawer: ElDrawerStub } },
    })
    await openBody(w)
    expect(w.text()).toContain('还没有配置数据说明')
  })
})
