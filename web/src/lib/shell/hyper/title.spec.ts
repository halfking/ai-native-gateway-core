/**
 * hyper/title.spec.ts — 标题真源门禁（docs/UI规范/00 §5.4 · H1）。
 *
 * 覆盖参考规范 06 §2 的验收样例：列表「员工」→详情「张某」→「编辑员工」弹窗
 * →无标题确认框，标题依次为「员工/张某/编辑员工/编辑员工」。
 */
import { beforeEach, describe, expect, it } from 'vitest'
import { TitleResolver, resolveDomTitle, sanitizeTitle, TITLE_MAX_LENGTH } from './title'

describe('sanitizeTitle', () => {
  it('去标签、折叠空白、去首尾', () => {
    expect(sanitizeTitle('  <b>张某</b>   李四 ')).toBe('张某 李四')
  })

  it('空值与纯空白返回 null（不编造标题）', () => {
    expect(sanitizeTitle(null)).toBeNull()
    expect(sanitizeTitle(undefined)).toBeNull()
    expect(sanitizeTitle('   ')).toBeNull()
    expect(sanitizeTitle('<div></div>')).toBeNull()
  })

  it('超长截断到上限并加省略号', () => {
    const out = sanitizeTitle('x'.repeat(500))!
    expect(out).toHaveLength(TITLE_MAX_LENGTH)
    expect(out.endsWith('…')).toBe(true)
  })

  it('非字符串输入不抛错（服务端脏数据兜底）', () => {
    expect(sanitizeTitle(123)).toBe('123')
    expect(sanitizeTitle(0)).toBe('0')
  })
})

describe('resolveDomTitle：作用域隔离', () => {
  it('只在自己给的根节点内找，不向 document 兜底', () => {
    document.body.innerHTML = `
      <div id="bg"><h1>背景页标题</h1></div>
      <div id="ov"></div>
    `
    const ov = document.getElementById('ov')!
    // 覆盖层内无标题 → 返回 null，而不是「背景页标题」
    expect(resolveDomTitle(ov)).toBeNull()
    expect(resolveDomTitle(document.getElementById('bg'))).toBe('背景页标题')
  })

  it('data-shell-title 优先于 h1', () => {
    document.body.innerHTML = `<div id="x"><h1>标题节点</h1><span data-shell-title="标记标题"></span></div>`
    expect(resolveDomTitle(document.getElementById('x'))).toBe('标记标题')
  })

  it('根节点为 null 时返回 null', () => {
    expect(resolveDomTitle(null)).toBeNull()
    expect(resolveDomTitle(undefined)).toBeNull()
  })
})

describe('TitleResolver：解析次序', () => {
  let r: TitleResolver
  beforeEach(() => {
    r = new TitleResolver()
  })

  it('参考规范 06 §8 验收链：员工 → 张某 → 编辑员工 → 无标题确认框', () => {
    r.registerPageTitle('e1', { title: '员工', epoch: 0 })
    r.registerPageTitle('e2', { title: '张某', epoch: 0 })

    // 列表页
    expect(r.resolve({ currentEntryId: 'e1' }).title).toBe('员工')
    // 详情页
    expect(r.resolve({ currentEntryId: 'e2' }).title).toBe('张某')

    // 「编辑员工」弹窗：显式标题
    const edit = r.resolve({ currentEntryId: 'e2', overlay: { id: 'o1', title: '编辑员工' } })
    expect(edit.title).toBe('编辑员工')
    expect(edit.source).toBe('registered')

    // 无标题确认框：继承打开前的有效标题（= 编辑员工），不是「确认」
    const snap = edit
    const confirm = r.resolve({ currentEntryId: 'e2', overlay: { id: 'o2' } }, snap)
    expect(confirm.title).toBe('编辑员工')
    expect(confirm.source).toBe('inherited')
    // 继承来源是「产生该标题的那一层」——这里是有显式标题的弹窗 o1，
    // 而不是背后的页面 e2。弹窗套弹窗时这条链才解释得通。
    expect(confirm.inheritedFrom).toBe('o1')
  })

  it('无标题弹层不会把标题置空或填成「弹窗」', () => {
    r.registerPageTitle('e1', { title: '请求日志', epoch: 0 })
    const parent = r.resolve({ currentEntryId: 'e1' })
    const child = r.resolve({ currentEntryId: 'e1', overlay: { id: 'o' } }, parent)
    expect(child.title).toBe('请求日志')
  })

  it('覆盖层 DOM 回退只查覆盖层自身（rootId 探针）', () => {
    r.registerPageTitle('e1', { title: '请求日志', epoch: 0 })
    const out = r.resolve(
      { currentEntryId: 'e1', overlay: { id: 'o1', rootId: 'o1' } },
      null,
    )
    // 探针返回覆盖层自己的标题
    const withDom = r.resolve(
      {
        currentEntryId: 'e1',
        overlay: { id: 'o1', rootId: 'o1' },
        overlayDomTitle: (id) => (id === 'o1' ? '详情抽屉' : null),
      },
      null,
    )
    expect(withDom.title).toBe('详情抽屉')
    expect(withDom.source).toBe('aria')
    // 探针返回 null 时继续走继承，而不是取背景页标题
    expect(out.title).toBe('请求日志')
  })

  it('回落链：页面登记 → 页面 DOM → route → document', () => {
    // 只有 route
    expect(r.resolve({ currentEntryId: 'missing', routeTitle: '路由标题' })).toMatchObject({
      title: '路由标题',
      source: 'route',
    })
    // route 也没有 → document
    expect(r.resolve({ routeTitle: null, documentTitle: '文档标题' })).toMatchObject({
      title: '文档标题',
      source: 'document',
    })
    // 都没有 → appName
    expect(r.resolve({ appName: 'LLM Gateway' })).toMatchObject({
      title: 'LLM Gateway',
      source: 'document',
    })
    // 全空也不抛错
    expect(r.resolve({}).title).toBe('')
  })

  it('页面标题到达后覆盖路由回落（source 升级）', () => {
    const before = r.resolve({ currentEntryId: 'e1', routeTitle: '路由标题' })
    expect(before.source).toBe('route')
    r.registerPageTitle('e1', { title: '真实标题', epoch: 0 })
    const after = r.resolve({ currentEntryId: 'e1', routeTitle: '路由标题' })
    expect(after).toMatchObject({ title: '真实标题', source: 'registered' })
  })

  it('页面 DOM 探针在无显式登记时生效', () => {
    const out = r.resolve({
      currentEntryId: 'e1',
      pageDomTitle: () => 'DOM 标题',
      routeTitle: '路由标题',
    })
    expect(out).toMatchObject({ title: 'DOM 标题', source: 'dom' })
  })
})

describe('TitleResolver：异步竞态（epoch 闸门）', () => {
  let r: TitleResolver
  beforeEach(() => {
    r = new TitleResolver()
  })

  it('旧 epoch 的晚到结果被拒绝，不覆盖新标题', () => {
    r.bumpEpoch('e1') // → epoch 1
    expect(r.registerPageTitle('e1', { title: '新页面标题', epoch: 1 })).toBe(true)
    // 旧请求在途，回来时带着 epoch 0
    expect(r.registerPageTitle('e1', { title: '旧页面标题', epoch: 0 })).toBe(false)
    expect(r.resolve({ currentEntryId: 'e1' }).title).toBe('新页面标题')
  })

  it('同 epoch 允许覆盖（同一轮内先到先得）', () => {
    r.bumpEpoch('e1')
    expect(r.registerPageTitle('e1', { title: 'A', epoch: 1 })).toBe(true)
    expect(r.registerPageTitle('e1', { title: 'B', epoch: 1 })).toBe(true)
    expect(r.resolve({ currentEntryId: 'e1' }).title).toBe('B')
  })

  it('本次不带标题时保留上一次标题，不被擦成空', () => {
    r.bumpEpoch('e1')
    r.registerPageTitle('e1', { title: '保留我', epoch: 1 })
    r.registerPageTitle('e1', { title: null, titleKey: 'some.key', epoch: 2 })
    expect(r.resolve({ currentEntryId: 'e1' }).title).toBe('保留我')
    expect(r.getEntryTitle('e1')?.titleKey).toBe('some.key')
  })

  it('冷启动直接开弹层（无继承快照）也能拿到可用标题', () => {
    r.registerPageTitle('e1', { title: '页面标题', epoch: 0 })
    const out = r.resolve({ currentEntryId: 'e1', overlay: { id: 'o1' } }, null)
    expect(out.title).toBe('页面标题')
    expect(out.source).toBe('registered')
  })
})
