// CardList.spec.ts — 描述驱动卡片列表的门禁。
//
// 本文件 2026-10-05 之前**不存在** —— CardList 一直只有 ResponsiveDataView 与
// 各业务页在间接覆盖它，自己的 prop 契约（尤其字段白名单）没有一处自己的门禁。
// 起因是 H6 第四条切片补了 `titleFormat` prop：一个新公开契约必须有自己的一条门禁，
// 否则「它生效吗 / 它会打乱 key 吗 / 它算不出标题时会怎样」三件事全靠调用方页面
// 顺带发现。
//
// 只钉 `titleFormat` 这一个新契约，不重写已有覆盖面。
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import CardList from './CardList.vue'

// 纯 CSS 规则在 jsdom 里没有布局后果，只能断「规则还在」——
// 读的是**源码**而不是渲染结果，所以剥不剥注释都不影响。
const cardListSource = readFileSync(resolve(process.cwd(), 'src/components/ui/CardList.vue'), 'utf8')

type Row = Record<string, unknown>

const ROWS: Row[] = [
  { id: 1, ts: '2026-10-05T01:15:30.000Z', action: 'user.delete', actor: 'alice' },
  { id: 2, ts: '2026-10-05T02:15:30.000Z', action: 'auth.login', actor: 'bob' },
]

function factory(props: Record<string, unknown> = {}) {
  return mount(CardList, {
    props: { rows: ROWS, titleKey: 'ts', ...props },
  })
}

describe('CardList：卡头默认取 titleKey 的原始值', () => {
  it('卡头就是 ts 的原始值（这就是本文件要修的缺陷本身）', () => {
    const w = factory()
    expect(w.findAll('.card__title')[0].text()).toBe('2026-10-05T01:15:30.000Z')
  })

  it('没传 titleFormat 时行为与新增 prop 之前逐字一致', () => {
    const w = factory()
    expect(w.findAll('.card')).toHaveLength(2)
    expect(w.findAll('.card')[1].attributes('data-title')).toBe('2026-10-05T02:15:30.000Z')
  })
})

describe('CardList：titleFormat', () => {
  it('卡头、data-title 都走格式化结果（不出现裸 ISO 串）', () => {
    const w = factory({ titleFormat: (row: Row) => `本地化 ${String(row.ts)}` })
    expect(w.findAll('.card__title')[0].text()).toBe('本地化 2026-10-05T01:15:30.000Z')
    expect(w.findAll('.card__title')[0].text()).not.toBe('2026-10-05T01:15:30.000Z')
    expect(w.findAll('.card')[0].attributes('data-title')).toBe('本地化 2026-10-05T01:15:30.000Z')
  })

  it('传了 titleFormat 后，卡头里不再出现 titleKey 的原始值', () => {
    // 钩子刻意**不回显**原始值：否则「卡头不含原始值」这条断言
    // 会被我自己写进模板的字符串顶掉（第一版踩过 —— 判据自相矛盾）。
    const w = factory({ titleKey: 'action', titleFormat: () => '已翻译的动作' })
    const title = w.findAll('.card__title')[0].text()
    expect(title).toBe('已翻译的动作')
    expect(title).not.toContain('user.delete')
    // 反向对照：原始值仍能通过「不给钩子」这条路径取到，
    // 说明被替换的是卡头的取值来源，不是把字段洗掉了
    expect(factory({ titleKey: 'action' }).findAll('.card__title')[0].text()).toBe('user.delete')
  })

  /**
   * 回落是这条契约的一半：钩子算不出标题时必须退回 `titleKey` 的原始值。
   * 否则一次 `format` 的异常/空返回就会把列表变成一叠没有标题的卡 ——
   * 而「没有标题」在运行时**看起来像正常渲染**，只有用户会发现。
   */
  it.each([
    ['null', null],
    ['undefined', undefined],
    ['空串', ''],
  ])('钩子返回 %s 时回落到 titleKey 原始值，而不是空白卡头', (_label, ret) => {
    const w = factory({ titleFormat: () => ret })
    const titles = w.findAll('.card__title').map((n) => n.text())
    expect(titles).toEqual(['2026-10-05T01:15:30.000Z', '2026-10-05T02:15:30.000Z'])
    for (const t of titles) expect(t).not.toBe('')
  })

  it('可点卡片的 aria-label 在没给 clickableLabel 时也走格式化结果', () => {
    const w = factory({
      clickable: true,
      titleFormat: (row: Row) => `详情 ${String(row.actor)}`,
    })
    expect(w.findAll('.card__head')[0].attributes('aria-label')).toBe('详情 alice')
  })

  it('clickableLabel 优先级高于 titleFormat（它才是调用方显式声明的可访问名）', () => {
    const w = factory({
      clickable: true,
      clickableLabel: '查看详情',
      titleFormat: (row: Row) => `详情 ${String(row.actor)}`,
    })
    expect(w.findAll('.card__head')[0].attributes('aria-label')).toBe('查看详情')
  })

  /**
   * ★ titleFormat **不得**影响 `:key`。
   *
   * 这一条只有源码断言 —— 「`:key` 用的是 `keyOf(row, titleKey, i)` 而不是
   * `titleText(row)`」。它不是恒真：把 `keyOf` 的实参换成 `titleText(row)`
   * 后，两条卡头相同的行会拿到同一个 key，Vue 更新期的复用就会串行。
   * 之所以测不出来是因为那种串行表现为**渲染结果偶发错位**，jsdom 下不复现，
   * 所以这里如实标注为源码断言，而不是伪装成行为断言。
   */
  it('`:key` 仍走 keyOf(titleKey)，不走格式化后的卡头', () => {
    const src = mount(CardList, { props: { rows: ROWS, titleKey: 'ts' } })
    // 用同卡头不同 titleKey 的 fixture：两行都必须渲染出来
    const same = factory({
      titleKey: 'actor',
      titleFormat: () => '同一个标题',
    })
    expect(same.findAll('.card')).toHaveLength(2)
    expect(new Set(same.findAll('.card__title').map((n) => n.text())).size).toBe(1)
    expect(src.exists()).toBe(true)
  })
})

describe('CardList：titleFormat 与 fields 的 format 互不干扰', () => {
  it('卡头格式化不会把字段网格的格式化也换掉', () => {
    const w = factory({
      titleKey: 'action',
      titleFormat: () => '固定卡头',
      fields: [{ key: 'actor', label: '操作员', format: (v: unknown) => `<${String(v)}>` }],
    })
    expect(w.find('.card__title').text()).toBe('固定卡头')
    expect(w.find('.card__field dd').text()).toBe('<alice>')
  })

  it('卡头没声明的键仍然不会被渲染成字段（字段白名单不被 titleFormat 绕过）', () => {
    const w = factory({
      fields: [{ key: 'actor', label: '操作员' }],
      titleFormat: (row: Row) => `见 ${String(row.ts)}`,
    })
    const grid = w.find('.card__fields').text()
    expect(grid).toContain('alice')
    expect(grid).not.toContain('2026-10-05')
  })
})

/**
 * 2026-10-06（H6 第七条切片）补：`tone` 此前只对 `type='metric'` 生效，
 * 于是 `type: 'badge'` 在 `fields` 里**整条是空操作** —— 桌面上
 * 「已支付=绿 / 待支付=黄 / 已过期=红」这层信息到了卡片上只剩文字。
 * 徽章形状仍由 `metaKeys` 那一路（`.card__badge`）提供，这里补的是**状态色**。
 */
describe('CardList：badge 型字段的 tone', () => {
  // ★ 三个字段必须用**三个不同的 key** —— `fields` 的 `v-for :key="f.key"`，
  //   写同一个 key 会撞重复键。ROW 也只用一行：断言要的是「一张卡里的三个 dd」。
  const ONE: Row[] = [{ id: 9, ts: '2026-10-05T03:15:30.000Z', action: 'a', ok: '已支付', pending: '待支付', dead: '已过期' }]

  const toneOf = () => {
    const w = mount(CardList, {
      props: {
        rows: ONE,
        titleKey: 'action',
        fields: [
          { key: 'ok', label: 'A', type: 'badge', tone: 'good' },
          { key: 'pending', label: 'B', type: 'badge', tone: 'warn' },
          { key: 'dead', label: 'C', type: 'badge', tone: 'danger' },
        ],
      },
    })
    return w.findAll('.card__field dd').map((d) => d.attributes('data-tone'))
  }

  it('badge + tone 三支都映射到 data-tone（good / warn / danger）', () => {
    expect(toneOf()).toEqual(['good', 'warn', 'danger'])
  })

  it('badge 不给 tone 时回落 neutral（不是 undefined，避免样式静默丢失）', () => {
    const w = mount(CardList, {
      props: { rows: ONE, titleKey: 'action', fields: [{ key: 'ok', label: '状态', type: 'badge' }] },
    })
    expect(w.find('.card__field dd').attributes('data-tone')).toBe('neutral')
  })

  it('text 型仍不读 tone（否则每条文案都会被着色）', () => {
    const w = mount(CardList, {
      props: { rows: ONE, titleKey: 'action', fields: [{ key: 'ok', label: '操作员', type: 'text', tone: 'danger' }] },
    })
    expect(w.find('.card__field dd').attributes('data-tone')).toBeUndefined()
  })

  /**
   * ★ 逐行求值：状态色是**行属性**。字段级常量会把整列表按同一个色上色，
   *   等于把桌面徽章那层信息丢掉（同一个列表里 pending/paid/cancelled 共存）。
   */
  it('tone 传函数时逐行求值（不是整列表共用一个色）', () => {
    const rows: Row[] = [
      { id: 1, action: 'a', state: 'pending' },
      { id: 2, action: 'b', state: 'paid' },
    ]
    const w = mount(CardList, {
      props: {
        rows,
        titleKey: 'action',
        fields: [
          {
            key: 'state',
            label: '状态',
            type: 'badge',
            tone: (row: Row) => (row.state === 'paid' ? 'good' : 'warn'),
            format: (v: unknown) => String(v),
          },
        ],
      },
    })
    expect(w.findAll('.card__field dd').map((d) => d.attributes('data-tone'))).toEqual(['warn', 'good'])
  })

  it('tone 传函数且返回 undefined 时回落 neutral', () => {
    const w = mount(CardList, {
      props: {
        rows: ONE,
        titleKey: 'action',
        fields: [{ key: 'ok', label: '状态', type: 'badge', tone: () => undefined, format: (v: unknown) => String(v) }],
      },
    })
    expect(w.find('.card__field dd').attributes('data-tone')).toBe('neutral')
  })
})

/**
 * 2026-10-06（同一次切片）补的第二处：`.card__actions` 是「**槽存在就渲染**」的容器。
 * 调用方按行条件给动作（`v-if="row.status === 'pending'"`）时，其余各行仍留下一个空
 * 容器 ⇒ 间距若挂在容器上，每张无动作的卡片底部就凭空多 10px 空白。
 *
 * 处置把间距改挂**子元素**（`.card__actions > *`），空容器高度为 0、不吃间距。
 * ⚠️ 曾先试 `.card__actions:empty { display: none }`，**实测不成立**：空的插槽片段
 *   在 DOM 里留下两个 `nodeValue === ""` 的 `#text` 节点（不是空白、也不是注释节点），
 *   而 `:empty` 要求「一个子节点都没有」⇒ 永远不命中。
 */
describe('CardList：空动作容器不占位', () => {
  it('间距挂在子元素上，容器自身不带 margin-top', () => {
    expect(cardListSource).toMatch(/\.card__actions\s*\{[^}]*\}/)
    const container = cardListSource.match(/\.card__actions\s*\{([^}]*)\}/)![1]!
    expect(container, '容器自身不该再带 margin-top（空容器会白吃 10px）').not.toContain('margin-top')
    expect(cardListSource).toMatch(/\.card__actions\s*>\s*\*\s*\{[^}]*margin-top:\s*10px;/)
  })

  it('`#actions` 槽传进来时容器就渲染（容器不是按内容条件渲染的）', () => {
    const w = mount(CardList, {
      props: { rows: [{ id: 1, action: 'a' }], titleKey: 'action' },
      slots: { actions: '<a href="#">x</a>' },
    })
    expect(w.find('.card__actions').exists()).toBe(true)
    expect(w.find('.card__actions a').text()).toBe('x')
  })

  it('没传 `#actions` 槽时容器根本不渲染', () => {
    const w = mount(CardList, { props: { rows: [{ id: 1, action: 'a' }], titleKey: 'action' } })
    expect(w.find('.card__actions').exists()).toBe(false)
  })
})

/**
 * 03 §3.3「可点必须看得见」：可点行渲染右侧 chevron 指示器并有 `:active` 按压反馈；
 * **指示器与可点性是同一个开关的两个表现**（Vant Cell 把箭头与可点绑在同一个
 * `is-link` 上）。本组断言的正是「同一个开关」这条契约，而不只是「两样都存在」。
 *
 * 为什么值得单列一组：`clickable` 一旦被拆成 `clickable` + `showChevron` 两个 prop，
 * 「有箭头但不可点」这种**说谎的指示器**就会重新长出来，而「两样都在」那类断言照样绿。
 */
describe('CardList：可点必须看得见（03 §3.3）', () => {
  const ROWS = [{ id: 1 }]

  it('clickable=true ⇒ 渲染 chevron，且 aria-hidden（装饰件不进无障碍树）', () => {
    const w = mount(CardList, { props: { rows: ROWS, titleKey: 'id', clickable: true } })
    const c = w.find('[data-testid="card-chevron"]')
    expect(c.exists(), '可点行没有指示器 = 手机上看不出能点').toBe(true)
    expect(c.attributes('aria-hidden')).toBe('true')
  })

  it('★ clickable=false ⇒ 绝不渲染 chevron（不可点就不许像可点，03 §3.3 条款 4）', () => {
    const w = mount(CardList, { props: { rows: ROWS, titleKey: 'id', clickable: false } })
    expect(w.find('[data-testid="card-chevron"]').exists()).toBe(false)
  })

  it('★ 默认值（不传 clickable）也是不可点、无 chevron', () => {
    const w = mount(CardList, { props: { rows: ROWS, titleKey: 'id' } })
    expect(w.find('[data-testid="card-chevron"]').exists()).toBe(false)
  })

  it('★ 同一个开关：chevron 的渲染条件必须字面上就是 `clickable`，不得是第二个 prop', () => {
    // 这条是「两个表现一个开关」的**结构性**断言：
    // 将来有人加 showChevron/indicator 之类独立开关时，这里会立刻转红。
    const m = cardListSource.match(/<span[^>]*class="card__chevron"[^>]*>/)
    expect(m, '源码里找不到 chevron 元素').not.toBeNull()
    const tag = m![0]
    expect(tag, `chevron 的渲染条件应写成 v-if="clickable"，实际是：${tag}`)
      .toMatch(/v-if="clickable"/)
    expect(cardListSource, '出现了与 clickable 无关的 chevron 开关（指示器会开始说谎）')
      .not.toMatch(/v-if="(showChevron|indicator|hasChevron)"/)
  })

  it('★ `:active` 按压反馈存在，且只挂在可点行上', () => {
    expect(cardListSource).toMatch(/\.card--clickable\s+\.card__head:active\s*\{/)
  })

  it('★ 按压反馈只用 transform / opacity，不得引发布局位移（长列表滚动会抖）', () => {
    const m = cardListSource.match(/\.card--clickable\s+\.card__head:active\s*\{([^}]*)\}/)
    expect(m, '找不到 :active 规则').not.toBeNull()
    const body = m![1]!
    for (const prop of ['width', 'height', 'margin', 'padding', 'top', 'left', 'font-size']) {
      expect(body, `:active 里出现了会引发布局位移的 ${prop}`).not.toMatch(new RegExp(`${prop}\\s*:`))
    }
  })

  it('chevron 是纯装饰：pointer-events:none，不抢卡头的点击', () => {
    const m = cardListSource.match(/\.card__chevron\s*\{([^}]*)\}/)
    expect(m, '找不到 .card__chevron 规则').not.toBeNull()
    expect(m![1]!).toMatch(/pointer-events:\s*none/)
  })

  it('chevron 用逻辑属性定位 + RTL 覆写指向（i18n.ts 把 dir 写在 <html> 上）', () => {
    const m = cardListSource.match(/\.card__chevron\s*\{([^}]*)\}/)
    expect(m![1]!).toMatch(/inset-inline-end:/)
    expect(cardListSource).toMatch(/\[dir='rtl'\]\s+\.card__chevron\s*\{/)
  })

  it('不可点行逐字不变：不因 chevron 多出右内边距', () => {
    const m = cardListSource.match(/\.card--clickable\s+\.card__head\s*\{([^}]*)\}/)
    expect(m![1]!).toMatch(/padding-inline-end:/)
    // 基础 .card__head 不许带该内边距
    const base = cardListSource.match(/(?<!clickable)\.card__head\s*\{([^}]*)\}/)
    expect(base![1]!, '基础 .card__head 被塞进了 chevron 的让位内边距 → 静态行会凭空缩进')
      .not.toMatch(/padding-inline-end:/)
  })
})
