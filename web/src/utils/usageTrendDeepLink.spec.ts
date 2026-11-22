// usageTrendDeepLink.spec.ts —— 深链「钉住维度」纯函数单测（2026-11-23 深链钉住轮）
//
// ★ 这条需求的行为契约：
//   在会话分析「Top5 供应商/模型」点一行跳到 /admin/usage-trends 时，
//   页面里的条件应当**自动选成那一项**（整个页面的数据都是它的），
//   并且**把对应维度的列表收起来**。
//
// ★ 为什么把口径抽成纯函数再测：UsageTrendExplorer 依赖 echarts + 路由 + 注入，
//   jsdom 下没有布局也没有真实路由 ⇒ 组件级测试只能测"字符串在不在源码里"，
//   测不到"参数在不在、钉没钉住、code 能不能解析成 id"这些真正的口径。

import { describe, expect, it } from 'vitest'
import {
  firstQueryString,
  isProviderIdParam,
  providerDisplayLabel,
  readPinnedDimensions,
  resolveProviderId,
  type ProviderLike,
} from './usageTrendDeepLink'

describe('深链钉住维度', () => {
  it('① 参数存在即钉住（provider/tenant/apikey/model 各自独立）', () => {
    const pinned = readPinnedDimensions({ provider: 'apiclaude', start: '2026-11-17', end: '2026-11-23' })
    expect(pinned.provider).toBe(true)
    expect(pinned.tenant).toBe(false)
    expect(pinned.apikey).toBe(false)
    expect(pinned.model).toBe(false)

    // 时间范围不是维度 ⇒ 永远不钉住（否则日期选择器会被藏起来）
    expect(readPinnedDimensions({ start: '2026-11-17', end: '2026-11-23' }).provider).toBe(false)
  })

  it('② 空值不算钉住；多值参数（model=a&model=b）算钉住', () => {
    expect(readPinnedDimensions({ provider: '' }).provider).toBe(false)
    expect(readPinnedDimensions({ provider: '   ' }).provider).toBe(false)
    expect(readPinnedDimensions({ model: ['a', 'b'] }).model).toBe(true)
  })

  it('③ firstQueryString 取同键多值里的第一个非空串', () => {
    expect(firstQueryString({ provider: ['apiclaude', 'minimax'] }, 'provider')).toBe('apiclaude')
    expect(firstQueryString({ provider: ['', 'minimax'] }, 'provider')).toBe('minimax')
    expect(firstQueryString({}, 'provider')).toBe('')
  })

  it('④ 供应商参数：只有纯数字才算 id，其余当 code', () => {
    expect(isProviderIdParam('14')).toBe(true)
    expect(isProviderIdParam('apiclaude')).toBe(false)
    expect(isProviderIdParam('速云U站')).toBe(false)
    expect(isProviderIdParam('0')).toBe(false)
    expect(isProviderIdParam('-3')).toBe(false)
    expect(isProviderIdParam('14.5')).toBe(false)
  })

  it('⑤ code→id 解析：code / catalog_code / display_name 三列任一命中', () => {
    const providers: ProviderLike[] = [
      { id: 14, code: 'minimax', catalog_code: 'minimax', display_name: 'MiniMax' },
      // 自建供应商：catalog_code 为空、display_name 另起名字（211 真库实测）
      { id: 13092, code: '速云U站', catalog_code: '', display_name: 'suyun' },
    ]
    expect(resolveProviderId(providers, 'minimax')).toBe(14)
    expect(resolveProviderId(providers, '速云U站')).toBe(13092)
    expect(resolveProviderId(providers, 'suyun')).toBe(13092)
    expect(resolveProviderId(providers, 'MiniMax')).toBe(14)
    // 解析不到 ⇒ null（调用方必须显式披露，不能静默退化成全量数据）
    expect(resolveProviderId(providers, 'not-exist')).toBeNull()
    expect(resolveProviderId(providers, '')).toBeNull()
  })

  it('⑥ 徽标展示名：深链 code 原样优先，其次展示名/catalog_code/code', () => {
    const providers: ProviderLike[] = [
      { id: 13092, code: '速云U站', catalog_code: '', display_name: 'suyun' },
      { id: 14, code: 'minimax', catalog_code: 'minimax', display_name: 'MiniMax' },
    ]
    // 深链给的是 code ⇒ 原样显示（与来源列表一致）
    expect(providerDisplayLabel(providers, '速云U站', 13092)).toBe('速云U站')
    // id 形态 ⇒ 显示展示名
    expect(providerDisplayLabel(providers, '', 14)).toBe('MiniMax')
    // 兜底：列表里没有 ⇒ #id
    expect(providerDisplayLabel(providers, '', 999)).toBe('#999')
    expect(providerDisplayLabel(providers, '', null)).toBe('')
  })
})
