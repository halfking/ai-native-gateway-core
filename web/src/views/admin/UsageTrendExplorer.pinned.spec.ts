// UsageTrendExplorer.pinned.spec.ts —— 「深链钉住维度」的页面契约钉测（2026-11-23 深链钉住轮）
//
// ★ owner 诉求：在会话分析「Top5 供应商 / 模型」点一行跳到用量趋势时，
//   页面里的条件应当**自动选成那一项**（整个页面的数据都是它的），
//   并且**把该维度的列表收起来**；其它维度同理。
//
// ★ 为什么是源码契约而不是 mount：本页依赖 echarts（jsdom 无 canvas）+ 路由 + 注入，
//   仓内既有先例（sessionCharts.init.test.ts / UsageTrendExplorer.layout.spec.ts）
//   都是同一取舍 ⇒ 真正的口径（参数在不在、钉没钉住、code 能不能解析）由
//   usageTrendDeepLink.spec.ts 的纯函数单测负责，本文件只钉"页面接线"。
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
  it('① 四个维度的选择器在深链钉住时收起，只留只读徽标', async () => {
    const src = await read('views/admin/UsageTrendExplorer.vue')
    expect(src).toContain('v-if="!pinned.provider"')
    expect(src).toContain('v-if="showTenantFilter && !pinned.tenant"')
    expect(src).toContain('v-if="!pinned.apikey"')
    expect(src).toContain('v-if="!pinned.model"')
    // 徽标（可解除）与对应的解除动作
    for (const dim of ['provider', 'tenant', 'apikey', 'model']) {
      expect(src).toContain(`data-testid="ute-pinned-${dim}"`)
    }
    for (const fn of ['releaseProvider', 'releaseTenant', 'releaseApiKey', 'releaseModel']) {
      expect(src).toContain(`function ${fn}(`)
    }
  })

  it('② 供应商深链 code 必须解析成 provider_id 才能过滤（否则等于没过滤）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src).toContain('resolveProviderId(providers.value, providerCodeParam.value)')
    expect(src).toContain('providerId.value = id')
    // 取数必须带上解析后的 provider_id
    expect(src).toMatch(/provider_id: providerId\.value \|\| undefined/)
  })

  it('③ 深链给的是 code 时 URL 保留 code（别改写成 id）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src).toMatch(/if \(providerCodeParam\.value\) next\.provider = providerCodeParam\.value/)
  })

  it('④ 解析不到供应商要显式披露，不能静默退化成全量数据', async () => {
    const src = await read('views/admin/UsageTrendExplorer.vue')
    expect(src).toContain('providerUnresolved')
    expect(src).toContain('usageTrend.pinnedUnresolved')
  })

  it('⑤ 供应商列表要认规范 code（自建供应商 catalog_code 常为空）', async () => {
    const picker = stripComments(await read('components/ProviderPicker.vue'))
    expect(picker).toMatch(/p\.code === code/)
    const api = await read('api/providers.ts')
    expect(api).toMatch(/code\?: string/)
  })

  it('⑥ 会话分析 Top5 的供应商行传的就是 providers.code（深链形态对齐）', async () => {
    const src = await read('views/admin/session-analytics/components/SaTop5Rankings.vue')
    expect(src).toMatch(/query:\s*\{\s*provider,\s*start:\s*props\.startDate,\s*end:\s*props\.endDate\s*\}/)
    // 模型行同理
    expect(src).toMatch(/query:\s*\{\s*model,\s*start:\s*props\.startDate,\s*end:\s*props\.endDate\s*\}/)
  })
})
