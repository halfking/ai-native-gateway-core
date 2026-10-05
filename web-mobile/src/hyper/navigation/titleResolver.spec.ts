import { describe, expect, it } from 'vitest'
import { TitleResolver, clipTitle, TITLE_MAX_LENGTH } from './titleResolver'

function makeResolver(deps: Partial<ConstructorParameters<typeof TitleResolver>[0]> = {}) {
  return new TitleResolver({
    getPage: deps.getPage ?? (() => null),
    getOverlays: deps.getOverlays ?? (() => []),
    getRouteTitle: deps.getRouteTitle ?? (() => undefined),
    getAppName: deps.getAppName ?? (() => 'LLM Gateway'),
  })
}

describe('TitleResolver 优先级链（06 §2）', () => {
  it('无任何注册 → 路由 titleKey → 应用名', () => {
    expect(makeResolver().resolve()).toMatchObject({ title: 'LLM Gateway', source: 'document' })
    expect(makeResolver({ getRouteTitle: () => '总览' }).resolve()).toMatchObject({ title: '总览', source: 'route' })
  })

  it('页面注册标题 > 路由标题', () => {
    const r = makeResolver({
      getPage: () => ({ id: 'page-1', title: () => '节点健康' }),
      getRouteTitle: () => '总览',
    })
    expect(r.resolve()).toMatchObject({ title: '节点健康', source: 'registered', fromId: 'page-1' })
  })

  it('顶层弹层显式标题 > 页面标题', () => {
    const r = makeResolver({
      getPage: () => ({ id: 'page-1', title: () => '节点' }),
      getOverlays: () => [{ id: 'ov-a', title: () => '节点A详情' }],
    })
    expect(r.resolve()).toMatchObject({ title: '节点A详情', source: 'overlay', fromId: 'ov-a' })
  })

  it('无标题弹层继承打开时刻快照并记录 inheritedFrom（验收用例 1 的第 4 层）', () => {
    const r = makeResolver({
      getPage: () => ({ id: 'page-1', title: () => '节点' }),
      getOverlays: () => [
        { id: 'ov-a', title: () => '凭据 X' },
        { id: 'ov-b', title: undefined, snapshot: { title: '凭据 X', fromId: 'ov-a' } },
      ],
    })
    const resolved = r.resolve()
    expect(resolved).toMatchObject({ title: '凭据 X', source: 'inherited', inheritedFrom: 'ov-a' })
  })

  it('标题 trim、折叠空白、≤200 字符', () => {
    expect(clipTitle('  a \n b  ')).toBe('a b')
    expect(clipTitle('x'.repeat(500)).length).toBe(TITLE_MAX_LENGTH)
    expect(clipTitle('x'.repeat(500)).endsWith('…')).toBe(true)
  })

  it('显式标题为空白时不采用（禁止空白/弹窗字样兜底）', () => {
    const r = makeResolver({
      getOverlays: () => [{ id: 'ov-a', title: () => '   ' }],
      getRouteTitle: () => '总览',
    })
    expect(r.resolve().source).toBe('route')
  })
})
