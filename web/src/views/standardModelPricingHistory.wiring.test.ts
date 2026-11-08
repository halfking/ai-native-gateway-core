/**
 * standardModelPricingHistory.wiring.test.ts — 价格期版本账视图接线门
 *
 * 2026-11-09（运营审计轮项2，迁移 952）。价格历史/订价变化是「能看见
 * 订价变化模式」的展示面，最容易犯的接错线形态与 nameSortWiring 同族：
 * API 加了、modal 写了、但**行上没有按钮**或 **i18n 键不存在**（缺键
 * 渲染字面键名，用户直接看到 standardModelPricing.history.xxx）。
 *
 * ⚠ 反向对照：删掉行上的 openHistory 按钮 / 删掉 history 键组，本文件红。
 */
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const WEB = resolve(process.cwd())

function readView(rel: string): string {
  for (const base of [WEB, resolve(WEB, 'web')]) {
    try {
      return readFileSync(resolve(base, 'src', rel), 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error(`cannot read ${rel}`)
}

describe('StandardModelPricingView 价格历史接线', () => {
  const view = readView('views/StandardModelPricingView.vue')
  const template = view.slice(view.indexOf('<template>'))

  it('每行有「历史」入口且真的调 openHistory', () => {
    expect(template).toContain('@click="openHistory(row)"')
    expect(view).toContain('async function openHistory(row')
    expect(view).toContain('getAdminMaasModelPriceHistory(row.canonical_id)')
  })

  it('工具栏有「订价变化」入口且真的调 openChanges', () => {
    expect(template).toContain('@click="openChanges"')
    expect(view).toContain('getAdminMaasModelPriceChanges(30, 200)')
  })

  it('两个 modal 都渲染 price-history-list', () => {
    expect(template.match(/price-history-list/g)?.length).toBeGreaterThanOrEqual(2)
  })

  it('8 语种 history 键组齐全（缺键会渲染字面键名）', () => {
    const locales = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']
    for (const loc of locales) {
      const src = readView(`locales/${loc}/standardModelPricing.ts`)
      for (const key of ['title', 'changesTitle', 'open', 'openChanges', 'empty', 'current', 'until', 'mode', 'source', 'modePayAsYouGo', 'modeTokenPlan', 'modeBooster']) {
        expect(src, `${loc} missing history.${key}`).toMatch(new RegExp(`\\b${key}: `))
      }
    }
  })

  it('zh-TW 不得与 zh-CN 同值（same-as-source 棘轮）', () => {
    const tw = readView('locales/zh-TW/standardModelPricing.ts')
    const cn = readView('locales/zh-CN/standardModelPricing.ts')
    const grab = (src: string, key: string) =>
      src.match(new RegExp(`${key}: (?:'([^']*)'|"([^"]*)")`))?.slice(1).find(Boolean)
    for (const key of ['title', 'empty', 'modePayAsYouGo', 'modeTokenPlan', 'modeBooster']) {
      expect(grab(tw, key), `zh-TW history.${key} must differ from zh-CN`).not.toBe(grab(cn, key))
    }
  })
})
