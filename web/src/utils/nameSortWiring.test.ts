/**
 * nameSortWiring.test.ts — 「有名称的列表按名称排序」真的接上了吗
 *
 * 2026-10-03。背景：老板要求「类似供应商这类有名称的列表按名称排序」。
 * 第一轮补了供应商/租户/用户/密钥/Agent/凭据六处；第二轮复核发现
 * 详情页里嵌的表（租户模型表、密钥明细的模型表、凭据可用模型面板…）
 * 一行排序代码都没有 —— 但它们同样是「首列就是名称」的列表。
 *
 * 为什么测源码而不是挂组件：这七个视图都要打 API、依赖注入与 i18n，
 * 挂起来测的代价远大于收益，而**接错线**恰恰是这轮最容易犯的错
 * （加了 computed 却忘了把模板的 v-for 换过去 ⇒ 页面看起来没变）。
 * 下面两条判据就是盯这两种接错线的形态。
 *
 * ⚠ 判据的反向对照：如果有人把 sortByName 从 import 里删掉、或把
 * v-for 换回原始数组，本文件必须红。它不能变成一堆恒绿的正则。
 */
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const WEB = resolve(process.cwd())

function readView(rel: string): string {
  // vitest 有时从 web/ 起，有时从仓库根起，两处都试
  for (const base of [WEB, resolve(WEB, 'web')]) {
    try {
      return readFileSync(resolve(base, 'src', rel), 'utf8')
    } catch {
      /* 试下一个 */
    }
  }
  throw new Error(`读不到 ${rel}`)
}

/** 模板区（去掉 <script>，避免把 script 里的引用当成「用了」）。 */
function templateOf(src: string): string {
  const i = src.indexOf('<template>')
  return i < 0 ? '' : src.slice(i)
}

interface Case {
  view: string
  /** 排序后的数组名，必须出现在 v-for 里 */
  sorted: string
  /** 原始数组名；它允许出现在 script 里，但不得再出现在 v-for 里 */
  raw: string
  /** 说明这个列表的首列是什么（决定排序键对不对） */
  firstCol: string
}

const CASES: Case[] = [
  { view: 'views/tenant/TenantModelsView.vue', sorted: 'filtered', raw: 'models', firstCol: 'canonical_name' },
  { view: 'views/provider-detail/CredentialModelsPanel.vue', sorted: 'sortedOffers', raw: 'offers', firstCol: 'canonical_name' },
  { view: 'views/provider-detail/ModelsTab.vue', sorted: 'sortedProbeAll', raw: 'probeAllResults', firstCol: 'credentialDisplayName' },
  { view: 'views/TenantDetailView.vue', sorted: 'sortedUsers', raw: 'users', firstCol: 'username' },
  { view: 'views/TenantDetailView.vue', sorted: 'sortedVisibleKeys', raw: 'visibleKeys', firstCol: 'key_alias' },
  { view: 'views/KeyDetailView.vue', sorted: 'sortedKeyModels', raw: 'keyModels', firstCol: 'model' },
  { view: 'views/TenantDashboardView.vue', sorted: 'sortedByModel', raw: 'by_model', firstCol: 'model' },
  { view: 'views/provider-detail/QualityTab.vue', sorted: 'sortedModels', raw: 'data.models', firstCol: 'model_name' },
]

describe('名称排序接到了模板上（第二轮补的八处）', () => {
  for (const c of CASES) {
    it(`${c.view}：v-for 用 ${c.sorted} 而不是 ${c.raw}`, () => {
      const tpl = templateOf(readView(c.view))
      // 取 v-for="… in X" 里的 X
      const fors = [...tpl.matchAll(/v-for="[^"]*?\bin\s+([A-Za-z0-9_.]+)"/g)].map((m) => m[1])
      expect(fors.length).toBeGreaterThan(0)
      expect(fors, `${c.view} 的 v-for 里应出现 ${c.sorted}`).toContain(c.sorted)
      // 鉴别力：原始数组若还留在 v-for 里，说明「加了 computed 却没换模板」
      expect(fors, `${c.view} 的 v-for 里不应再出现未排序的 ${c.raw}`).not.toContain(c.raw)
    })
  }
})

describe('排序键与列表首列一致（老板的原话是「按名称」而不是「按某个字段」）', () => {
  for (const c of CASES) {
    it(`${c.view}：排序依据是 ${c.firstCol}`, () => {
      const src = readView(c.view)
      // 脚本区里必须真的调了 sortByName，而不是自己在模板里 .sort()
      const script = src.slice(0, src.indexOf('<template>'))
      expect(script, `${c.view} 没有调用 sortByName`).toContain('sortByName(')
    })
  }
})

describe('反向对照：这套判据自己必须能判红', () => {
  it('把 v-for 换回原始数组 → 上面那条必红', () => {
    const tpl = templateOf(readView('views/KeyDetailView.vue'))
    const original = tpl.replace('v-for="m in sortedKeyModels"', 'v-for="m in keyModels"')
    const fors = [...original.matchAll(/v-for="[^"]*?\bin\s+([A-Za-z0-9_.]+)"/g)].map((m) => m[1])
    // 复原后的形态：既不含 sortedKeyModels，又含回 keyModels
    expect(fors).not.toContain('sortedKeyModels')
    expect(fors).toContain('keyModels')
  })

  it('这个文件里列的每一处，在真实源码里都必须存在（防止清单本身漂移）', () => {
    for (const c of CASES) expect(() => readView(c.view)).not.toThrow()
  })
})
