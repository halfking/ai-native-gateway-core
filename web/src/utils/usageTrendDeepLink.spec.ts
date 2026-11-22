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
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import {
  DIMENSION_SPECS,
  firstQueryString,
  hiddenRankingArms,
  isProviderIdParam,
  providerDisplayLabel,
  readDeepLinkValue,
  readPinnedDimensions,
  resolveProviderId,
  type ProviderLike,
} from './usageTrendDeepLink'

// ★ 8 语种的 locale 模块（静态 import ⇒ 类型安全，且不受动态 import 路径解析影响）。
//   用途：钉住「维度表里的 labelKey 在**每一个**语种都解析得到真值」——
//   ★ 本轮实测教训：labelKey 写成 `filter.deepLink.clientModel`（**漏了顶层
//     命名空间前缀** `requests.list.`）⇒ 该键在 8 个语种全部不存在 ⇒
//     页面 chip 标签渲染成原始键、跨语种键存在性门变红 ⇒ ⇒ labelKey 必须
//     是**完整 i18n 路径**，不能是"看起来像路径"的片段。
import arRequests from '../locales/ar-SA/requests'
import arUsageTrend from '../locales/ar-SA/usageTrend'
import deRequests from '../locales/de-DE/requests'
import deUsageTrend from '../locales/de-DE/usageTrend'
import enRequests from '../locales/en-US/requests'
import enUsageTrend from '../locales/en-US/usageTrend'
import esRequests from '../locales/es-ES/requests'
import esUsageTrend from '../locales/es-ES/usageTrend'
import frRequests from '../locales/fr-FR/requests'
import frUsageTrend from '../locales/fr-FR/usageTrend'
import jaRequests from '../locales/ja-JP/requests'
import jaUsageTrend from '../locales/ja-JP/usageTrend'
import cnRequests from '../locales/zh-CN/requests'
import cnUsageTrend from '../locales/zh-CN/usageTrend'
import twRequests from '../locales/zh-TW/requests'
import twUsageTrend from '../locales/zh-TW/usageTrend'

type Bundle = Record<string, unknown>

// ★ bundle 形态必须与各语种 `index.ts` 的聚合形态**同构**：模块导出被挂在
//   「命名空间 key」下（requests.ts ⇒ `requests.*`；usageTrend.ts ⇒ `usageTrend.*`）
//   ⇒ 直接把模块铺平会让 `requests.list.…` / `usageTrend.…` 两条路径都取不到
//   ⇒ ★ 这里必须按 index.ts 的挂法拼（否则这条钉测自己就红）。
const LOCALE_BUNDLES: Record<string, Bundle> = {
  'ar-SA': { requests: arRequests, usageTrend: arUsageTrend },
  'de-DE': { requests: deRequests, usageTrend: deUsageTrend },
  'en-US': { requests: enRequests, usageTrend: enUsageTrend },
  'es-ES': { requests: esRequests, usageTrend: esUsageTrend },
  'fr-FR': { requests: frRequests, usageTrend: frUsageTrend },
  'ja-JP': { requests: jaRequests, usageTrend: jaUsageTrend },
  'zh-CN': { requests: cnRequests, usageTrend: cnUsageTrend },
  'zh-TW': { requests: twRequests, usageTrend: twUsageTrend },
}

/** 按点号路径取值（取不到返回 undefined）。 */
function pickPath(bundle: Bundle, path: string): unknown {
  return path.split('.').reduce<unknown>((acc, key) => {
    if (acc === null || typeof acc !== 'object') return undefined
    return (acc as Record<string, unknown>)[key]
  }, bundle)
}

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

  // ★★★ 2026-11-23 用量趋势深链轮（扩展）：维度 4 → 9
  //
  // ★ 实测取证（211 现役，浏览器实点）：
  //   ① `?model=claude-opus-5-5` ⇒ 条件已自动选中 ✔，但「Top 5 模型」卡片没收起
  //      ⇒ 顶着「Top 5」标题只有 1 行（13.5K）= **多余的数据**；
  //   ② `?end_user_id=anonymous` ⇒ 页面**根本不认这个条件**（条件区全是「全部」、
  //      Top 10 用户仍是全站 3 个用户）⇒ URL 承诺的条件与页面呈现的数据不一致。

  it('⑦ 九个维度都能被深链钉住（含排行六维里页面原本不认识的五个）', () => {
    const pinned = readPinnedDimensions({
      provider: 'apiclaude',
      tenant: 't1',
      apikey: '12',
      model: 'gpt-5',
      client_model: 'claude-opus-5-5',
      end_user_id: 'anonymous',
      api_key_prefix: 'sk-jybFTc1****',
      application_code: 'default',
      task_type: 'unknown',
    })
    for (const dim of [
      'provider', 'tenant', 'apikey', 'model',
      'client', 'user', 'project', 'task', 'apikeyPrefix',
    ] as const) {
      expect(pinned[dim], `维度 ${dim} 应当被钉住`).toBe(true)
    }
    // 时间范围不是维度 ⇒ 不钉住
    expect(readPinnedDimensions({ start: '2026-11-17', end: '2026-11-23' }).model).toBe(false)
  })

  it('⑧ ★ 被钉住的维度 ⇒ 收起它对应的那张排行卡片（其余保留为下钻拆解）', () => {
    expect(hiddenRankingArms(readPinnedDimensions({ model: 'gpt-5' }))).toEqual(['models'])
    expect(hiddenRankingArms(readPinnedDimensions({ client_model: 'x' }))).toEqual(['clients'])
    expect(hiddenRankingArms(readPinnedDimensions({ end_user_id: 'anonymous' }))).toEqual(['users'])
    expect(hiddenRankingArms(readPinnedDimensions({ apikey: '12' }))).toEqual(['apiKeys'])
    expect(hiddenRankingArms(readPinnedDimensions({ api_key_prefix: 'sk-x' }))).toEqual(['apiKeys'])
    expect(hiddenRankingArms(readPinnedDimensions({ application_code: 'x' }))).toEqual(['projects'])
    expect(hiddenRankingArms(readPinnedDimensions({ task_type: 'x' }))).toEqual(['tasks'])
    // ★ 供应商/租户**没有**对应卡片 ⇒ 不收起任何卡片（它们是 breakdowns，不是冗余）
    expect(hiddenRankingArms(readPinnedDimensions({ provider: 'x' }))).toEqual([])
    expect(hiddenRankingArms(readPinnedDimensions({ tenant: 't1' }))).toEqual([])
    // 无条件 ⇒ 六张卡全保留
    expect(hiddenRankingArms(readPinnedDimensions({ start: '2026-11-17' }))).toEqual([])
    // 两个维度指向同一张卡（apikey / api_key_prefix）⇒ 只列一次
    expect(hiddenRankingArms(readPinnedDimensions({ apikey: '1', api_key_prefix: 'sk-x' }))).toEqual(['apiKeys'])
  })

  it('⑨ ★ 五维参数名与请求日志页逐字相同（两页可互贴链接 ⇒ 同一套维度词汇）', async () => {
    // ★ 这是**跨文件契约**：用量趋势页与请求日志页必须认同一批 query 参数名，
    //   否则「从排行卡片点过去」与「把链接贴回用量趋势页」会得到两种含义。
    const logsSrc = await readFile(
      resolve(process.cwd(), 'src/api/logs.ts'),
      'utf8',
    )
    for (const dim of ['client', 'user', 'project', 'task', 'apikeyPrefix'] as const) {
      const param = DIMENSION_SPECS[dim].param
      expect(
        logsSrc.includes(`qs.set('${param}',`),
        `请求日志页必须认识 query 参数 ${param}（用量趋势页的维度表用的是它）`,
      ).toBe(true)
    }
    // ★ 维度表自身的参数名（防手滑改名）
    expect(DIMENSION_SPECS.client.param).toBe('client_model')
    expect(DIMENSION_SPECS.user.param).toBe('end_user_id')
    expect(DIMENSION_SPECS.project.param).toBe('application_code')
    expect(DIMENSION_SPECS.task.param).toBe('task_type')
    expect(DIMENSION_SPECS.apikeyPrefix.param).toBe('api_key_prefix')
  })

  it('⑩ ★ 没有选择器的维度用 chip 呈现；有选择器的维度用 picker', () => {
    // 页面上有选择器的四个 ⇒ picker（条件区自动选中该项）
    for (const dim of ['provider', 'tenant', 'apikey', 'model'] as const) {
      expect(DIMENSION_SPECS[dim].control, `维度 ${dim} 应当有选择器`).toBe('picker')
    }
    // 页面上没有选择器的五个 ⇒ chip（只读徽标 + ✕ 解除）
    for (const dim of ['client', 'user', 'project', 'task', 'apikeyPrefix'] as const) {
      expect(DIMENSION_SPECS[dim].control, `维度 ${dim} 只能用只读徽标`).toBe('chip')
    }
  })

  it('⑪ readDeepLinkValue 取该维度的值（chip 徽标要显示它）', () => {
    expect(readDeepLinkValue({ end_user_id: 'anonymous' }, 'user')).toBe('anonymous')
    expect(readDeepLinkValue({ client_model: '  claude-opus-5-5  ' }, 'client')).toBe('claude-opus-5-5')
    expect(readDeepLinkValue({}, 'user')).toBe('')
  })

  it('⑫ ★★ 每个维度的 labelKey 在全部 8 个语种都解析得到非空真值', () => {
    // ★ 本轮实测教训：labelKey 若漏了顶层命名空间前缀（如写成
    //   `filter.deepLink.clientModel` 而非 `requests.list.filter.deepLink.clientModel`）
    //   ⇒ 该键在 8 个语种**全部不存在** ⇒ 页面 chip 标签渲染成原始键串、
    //   跨语种键存在性门变红 ⇒ ⇒ labelKey 必须是**完整 i18n 路径**。
    const dims = Object.keys(DIMENSION_SPECS) as (keyof typeof DIMENSION_SPECS)[]
    for (const dim of dims) {
      const labelKey = DIMENSION_SPECS[dim].labelKey
      for (const [loc, bundle] of Object.entries(LOCALE_BUNDLES)) {
        const value = pickPath(bundle, labelKey)
        expect(
          typeof value === 'string' && value.trim() !== '',
          `维度 ${dim} 的 labelKey "${labelKey}" 在语种 ${loc} 解析不到非空字符串（实测 ${JSON.stringify(value)}）`,
        ).toBe(true)
      }
    }
  })

  it('⑬ ★ chip 维度的 labelKey 与请求日志页实际使用的键同源', async () => {
    // ★ 两个页面共用同一套维度词汇 ⇒ 标签也必须是同一批键 ⇒
    //   请求日志页源码里出现过的 deepLink 键，必须覆盖本表的 5 个 chip 维度。
    const src = await readFile(resolve(__dirname, '../views/RequestLogsView.vue'), 'utf8')
    for (const dim of ['client', 'user', 'project', 'task', 'apikeyPrefix'] as const) {
      const labelKey = DIMENSION_SPECS[dim].labelKey
      expect(
        src.includes(`'${labelKey}'`),
        `维度 ${dim} 的 labelKey "${labelKey}" 必须与请求日志页用的是同一个键（两页同源）`,
      ).toBe(true)
    }
  })
})
