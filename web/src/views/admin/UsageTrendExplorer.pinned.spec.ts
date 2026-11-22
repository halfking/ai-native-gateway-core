// UsageTrendExplorer.pinned.spec.ts —— 「深链钉住维度」的页面契约钉测
// （2026-11-23 深链钉住轮；★ 2026-11-23 用量趋势深链轮扩展：4 维 → 9 维）
//
// ★ owner 诉求（原始表述见 utils/usageTrendDeepLink.ts 的文件头）：
//   「在页面的条件选择中自动选择相关的条件的下拉或选项，
//     并且在页面的列表中隐藏相关的列表，不用显示多余的数据。」
//
// ★ 2026-11-23 实测取证（211 现役，浏览器实点）：
//   ① `?model=claude-opus-5-5` ⇒ 条件已自动选中 ✔，但**「Top 5 模型」卡片没收起**
//      ⇒ 顶着「Top 5」标题只有 1 行（13.5K）= **多余的数据**；
//   ② `?end_user_id=anonymous` ⇒ 页面**根本不认这个条件**（条件区全是「全部」、
//      Top 10 用户仍是全站 3 个用户）⇒ ★ URL 承诺的条件与页面呈现的数据
//      **不一致**（"界面显示的 ≠ 数据反映的"）。
//
// ★ 为什么是源码契约而不是 mount：本页依赖 echarts（jsdom 无 canvas）+ 路由 + 注入，
//   仓内既有先例（sessionCharts.init.test.ts / UsageTrendExplorer.layout.spec.ts）
//   都是同一取舍 ⇒ 真正的口径（参数在不在、钉没钉住、收哪张卡、code 能不能解析）
//   由 usageTrendDeepLink.spec.ts 的纯函数单测负责，本文件只钉"页面接线"。
//
// ★ 判据纪律：每条断言都必须**能在被测对象坏掉时变红**。
import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

async function read(rel: string): Promise<string> {
  for (const c of CANDS(rel)) {
    try {
      return await readFile(c, 'utf8')
    } catch {
      /* next */
    }
  }
  throw new Error(`${rel} not found`)
}

/** 去注释（组件头注释里会引用旧实现，别把说明文字当成实现）。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
}

describe('用量趋势：深链钉住的维度', () => {
  it('① ★ 选择器**保持可见**（钉住时不得整块收起）——条件要「自动选中该项」', async () => {
    const src = await read('views/admin/UsageTrendExplorer.vue')
    // ★ 反向断言（有牙）：一旦有人改回「v-if=!pinned.X 收起选择器」，这里立刻红
    for (const dim of ['provider', 'tenant', 'apikey', 'model']) {
      expect(
        src.includes(`v-if="!pinned.${dim}"`),
        `钉住 ${dim} 时不得收起选择器（owner 诉求：自动选中下拉项，而不是藏起来）`,
      ).toBe(false)
    }
    // 四个选择器都还在
    expect(src).toContain('<ProviderPicker')
    expect(src).toContain('<ModelPicker')
    expect(stripComments(src)).toContain('v-model="tenantId"')
    expect(stripComments(src)).toContain('v-model="apiKeyId"')
    // 解除按钮（钉住时可见 ⇒ ✕ 释放这条深链条件）
    for (const dim of ['provider', 'tenant', 'apikey', 'model']) {
      expect(src).toContain(`data-testid="ute-pinned-${dim}"`)
    }
    for (const fn of ['releaseProvider', 'releaseTenant', 'releaseApiKey', 'releaseModel']) {
      expect(src).toContain(`function ${fn}(`)
    }
  })

  it('② ★ 被钉住的维度 ⇒ **收起**它对应的那张排行卡片（不多显示多余数据）', async () => {
    const src = await read('views/admin/UsageTrendExplorer.vue')
    for (const arm of ['models', 'clients', 'users', 'apiKeys', 'projects', 'tasks']) {
      expect(
        src.includes(`v-if="!hiddenArms.has('${arm}')"`),
        `排行卡片 ${arm} 必须按钉住维度收起（被钉住后它必然只剩一行 = 多余数据）`,
      ).toBe(true)
    }
    // 收起规则由纯函数算出来（单一真相）⇒ 页面不许自己另判一套
    expect(src).toContain('hiddenRankingArms(pinned.value)')
  })

  it('③ ★ 五维排行条件在条件区以只读徽标呈现（页面上没有这些选择器）', async () => {
    const src = await read('views/admin/UsageTrendExplorer.vue')
    expect(src).toContain('v-for="dim in chipDims"')
    expect(src).toContain('ute-pinned-${dim}')
    expect(src).toContain('function releaseDimension(')
    // 五个维度都要从深链读出来
    for (const dim of ['client', 'user', 'project', 'task', 'apikeyPrefix']) {
      expect(src).toContain(`readDeepLinkValue(route.query, '${dim}')`)
    }
  })

  it('④ ★ 五维条件**真的下发**给后端（图表 + 排行都要）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    // 图表（trend-series）与排行（top-rankings）两处都要带 dims
    const dimsUsages = src.split('dims: rankingDims.value').length - 1
    expect(dimsUsages, '图表与排行两个取数都必须带五维过滤').toBeGreaterThanOrEqual(2)
    // 五个维度都要出现在下发值里
    for (const key of ['client_model', 'end_user_id', 'api_key_prefix', 'application_code', 'task_type']) {
      expect(src).toContain(`${key}: `)
    }
  })

  it('⑤ ★ 五维条件必须**写回 URL**（否则会被 syncQuery 的全量重建悄悄抹掉）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    for (const [key, val] of [
      ['client_model', 'clientModel.value'],
      ['end_user_id', 'endUserId.value'],
      ['api_key_prefix', 'apiKeyPrefix.value'],
      ['application_code', 'applicationCode.value'],
      ['task_type', 'taskType.value'],
    ]) {
      expect(src).toContain(`next.${key} = ${val}`)
    }
  })

  it('⑥ ★ 「清除全部」也要清掉五维条件（否则条件与 URL 都还在）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    const clearAll = src.slice(src.indexOf('function clearAllFilters('), src.indexOf('function clearAllFilters(') + 700)
    for (const v of ['clientModel', 'endUserId', 'applicationCode', 'taskType', 'apiKeyPrefix']) {
      expect(clearAll).toContain(`${v}.value = ''`)
    }
  })

  it('⑦ 供应商深链 code 必须解析成 provider_id 才能过滤（否则等于没过滤）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src).toContain('resolveProviderId(providers.value, providerCodeParam.value)')
    expect(src).toContain('providerId.value = id')
    // 取数必须带上解析后的 provider_id
    expect(src).toMatch(/provider_id: providerId\.value \|\| undefined/)
  })

  it('⑧ 深链给的是 code 时 URL 保留 code（别改写成 id）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src).toMatch(/if \(providerCodeParam\.value\) next\.provider = providerCodeParam\.value/)
  })

  it('⑨ 解析不到供应商要显式披露，不能静默退化成全量数据', async () => {
    const src = await read('views/admin/UsageTrendExplorer.vue')
    expect(src).toContain('providerUnresolved')
    expect(src).toContain('usageTrend.pinnedUnresolved')
  })

  it('⑩ 供应商列表要认规范 code（自建供应商 catalog_code 常为空）', async () => {
    const picker = stripComments(await read('components/ProviderPicker.vue'))
    expect(picker).toMatch(/p\.code === code/)
    const api = await read('api/providers.ts')
    expect(api).toMatch(/code\?: string/)
  })

  it('⑪ 会话分析 Top5 的供应商行传的就是 providers.code（深链形态对齐）', async () => {
    const src = await read('views/admin/session-analytics/components/SaTop5Rankings.vue')
    expect(src).toMatch(/query:\s*\{\s*provider,\s*start:\s*props\.startDate,\s*end:\s*props\.endDate\s*\}/)
    // 模型行同理
    expect(src).toMatch(/query:\s*\{\s*model,\s*start:\s*props\.startDate,\s*end:\s*props\.endDate\s*\}/)
  })
})
