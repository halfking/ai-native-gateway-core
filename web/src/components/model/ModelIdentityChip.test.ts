// ModelIdentityChip.test.ts — 模型身份三段术语的**单一真源**门禁。
//
// 起因（2026-10-04）：LogsTab 的 CJK 门抓到表头 `… / 标准 / 出站` 是硬编码中文。
// 顺着查下去发现 `ModelIdentityChip.vue` 自己的三段微标签（客户端 / 标准 / 出站）
// 连同 4 个 title **也全是硬编码中文** —— 同一族缺陷，且是全英文界面
// 直接露出中文的那一种（不只影响本组件的 4 个调用方）。
//
// 这里钉三件事：
// 1. chip 模板里没有硬编码中文（剥注释后扫描）
// 2. 三段术语在 **8 个语言都有非空词条**，且非 CJK 语种不含中文
// 3. **切换 locale 时标签真的跟着变** —— 这是唯一能证明「走 i18n 而非硬编码」的判据：
//    硬编码中文的组件在 en-US 下仍然吐中文，这条会当场报红
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import ModelIdentityChip from './ModelIdentityChip.vue'

import arSA from '../../locales/ar-SA/models'
import deDE from '../../locales/de-DE/models'
import enUS from '../../locales/en-US/models'
import esES from '../../locales/es-ES/models'
import frFR from '../../locales/fr-FR/models'
import jaJP from '../../locales/ja-JP/models'
import zhCN from '../../locales/zh-CN/models'
import zhTW from '../../locales/zh-TW/models'

const ALL_LOCALES = {
  'ar-SA': arSA, 'de-DE': deDE, 'en-US': enUS, 'es-ES': esES,
  'fr-FR': frFR, 'ja-JP': jaJP, 'zh-CN': zhCN, 'zh-TW': zhTW,
} as const

/** 三个身份标签 + 四个 title。少一个就是漏译。 */
const KEYS = [
  'client', 'canonical', 'outbound',
  'titleClient', 'titleCanonical', 'titleOutbound', 'titleRaw',
] as const

/**
 * 取 modelIdentity 子树。
 * 不能整体断言成 `Record<string, Record<string, Record<string, string>>>` ——
 * models 模块是「嵌套 + 扁平」混存的（`page.title` 是 string，`chip.status` 又是叶子），
 * 那个断言过不了 tsc。只在真正要取的那一层收窄。
 */
function miOf(models: unknown): Record<string, string> {
  return (models as { modelIdentity?: Record<string, string> }).modelIdentity ?? {}
}

/** 这几个语言在界面上不该出现 CJK 字符（抄了 zh-CN 就是漏译）。 */
const CJK_FREE = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR'] as const

const CJK = /[一-鿿]/

const source = readFileSync(resolve(process.cwd(), 'src/components/model/ModelIdentityChip.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  // 行注释：避开 `://`（协议），否则会把 URL 之后整行吃掉
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

function i18nFor(messages: Record<string, unknown>, locale: string) {
  return createI18n({ legacy: false, locale, messages: { [locale]: { models: messages } } })
}

describe('ModelIdentityChip：模板无硬编码中文', () => {
  it('剥注释后没有 CJK 字符', () => {
    const hits = codeOnly.match(/[一-鿿]/g)
    expect(hits, `模板里出现了硬编码中文：${hits?.join('')}`).toBeNull()
  })

  it('三个身份标签走 models.modelIdentity 词条，不是内联字面量', () => {
    expect(codeOnly).toContain('models.modelIdentity.')
    for (const k of ['client', 'canonical', 'outbound']) {
      expect(codeOnly, `标签 ${k} 应来自词条`).toContain(`mi('${k}')`)
    }
  })

  it('四个 title 也走词条（titleRaw 必须带 {model} 插值，不能退回字符串拼接）', () => {
    for (const k of ['titleClient', 'titleCanonical', 'titleOutbound', 'titleRaw']) {
      expect(codeOnly, `title ${k} 应来自词条`).toContain(`mi('${k}')`)
    }
  })

  /**
   * ★ 这一条是双向的，两个方向都要拦住：
   *   · 写死 `title="标准名"` 或 `:title="'原名: ' + rawModel"` 而没接词条
   *   · 接了词条但模板没用上（孤儿 key，8 个语言白维护）
   * ⇒ **模板里的 title 属性数 == i18n title 调用数**，且四个键各用一次。
   *
   * ★ 写这一条时我先写成了 `not.toMatch(/:\s*title\s*=\s*['"]/)`，
   *   它把**正确**的 `:title="mi('titleClient')"` 也判红了 ——
   *   判据红了先怀疑判据，而不是去改已经正确的实现。
   */
  it('title 属性数与 i18n title 调用数一致（无写死、无孤儿键）', () => {
    const attrs = (codeOnly.match(/(?<![\w-])title\s*=\s*["']/g) ?? []).length
    const calls = (codeOnly.match(/mi\('title[A-Za-z]*'\)/g) ?? []).length
    expect(attrs, '模板里的 title 属性应与 i18n title 调用一一对应').toBe(calls)
    for (const k of ['titleClient', 'titleCanonical', 'titleOutbound', 'titleRaw']) {
      expect((codeOnly.match(new RegExp(`mi\\('${k}'\\)`, 'g')) ?? []).length, `${k} 应恰好用一次`).toBe(1)
    }
  })
})

describe('ModelIdentityChip：8 语言词条齐全', () => {
  for (const [lang, models] of Object.entries(ALL_LOCALES)) {
    it(`${lang} 七个键都在且非空`, () => {
      const mi = miOf(models)
      expect(mi, `${lang} 缺 models.modelIdentity 词条组`).toBeTruthy()
      for (const k of KEYS) {
        const v = mi?.[k]
        expect(typeof v, `${lang}.modelIdentity.${k} 缺失`).toBe('string')
        expect((v as string).trim().length, `${lang}.modelIdentity.${k} 是空串`).toBeGreaterThan(0)
      }
    })
  }

  for (const lang of CJK_FREE) {
    it(`${lang} 词条不含中文（防止漏译直接抄 zh-CN）`, () => {
      const mi = miOf(ALL_LOCALES[lang])
      for (const k of KEYS) {
        expect(mi[k], `${lang}.modelIdentity.${k} 含中文：${mi[k]}`).not.toMatch(CJK)
      }
    })
  }

  it('titleRaw 八个语言都保留 {model} 插值', () => {
    for (const [lang, models] of Object.entries(ALL_LOCALES)) {
      const mi = miOf(models)
      expect(mi.titleRaw, `${lang}.titleRaw 丢了 {model} 插值`).toContain('{model}')
    }
  })
})

describe('ModelIdentityChip：切 locale 标签真的跟着变', () => {
  const props = {
    clientModel: 'gpt-4o-mini',
    canonicalName: 'gpt-4o-mini',
    outboundModel: 'azure/gpt-4o-mini',
    rawModel: 'gpt-4o-mini-2024',
  }

  function labelsUnder(locale: 'zh-CN' | 'en-US') {
    const messages = (locale === 'zh-CN' ? zhCN : enUS) as Record<string, unknown>
    const w = mount(ModelIdentityChip, {
      props,
      global: { plugins: [i18nFor(messages, locale)] },
    })
    return w.findAll('.mic-label').map((n) => n.text())
  }

  it('zh-CN 出三段中文标签', () => {
    expect(labelsUnder('zh-CN')).toEqual(['客户端', '标准', '出站'])
  })

  it('en-US 出三段英文标签 —— 硬编码中文的组件会在这里露出「客户端」', () => {
    expect(labelsUnder('en-US')).toEqual(['Client', 'Canonical', 'Outbound'])
  })

  it('两套标签不重叠（证明不是同一份字面量）', () => {
    expect(labelsUnder('en-US')).not.toEqual(labelsUnder('zh-CN'))
  })

  it('title 属性跟随 locale，不回落成中文', () => {
    const w = mount(ModelIdentityChip, {
      props,
      global: { plugins: [i18nFor(enUS as Record<string, unknown>, 'en-US')] },
    })
    const titles = w.findAll('.mic-part').map((n) => n.attributes('title'))
    for (const t of titles) {
      expect(t, `en-US 下 title 仍是中文：${t}`).toBeTruthy()
      expect(t).not.toMatch(CJK)
    }
  })
})

describe('跨文件：表头与 chip 共用同一真源', () => {
  const logsTab = readFileSync(resolve(process.cwd(), 'src/views/provider-detail/LogsTab.vue'), 'utf8')
  const logsTabCode = logsTab
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

  it('LogsTab 表头从 models.modelIdentity 取三段，不另写一份字符串', () => {
    expect(logsTabCode).toContain('models.modelIdentity.')
    for (const k of ['client', 'canonical', 'outbound']) {
      expect(logsTabCode, `表头应复用 chip 词条 ${k}`).toContain(`mi('${k}')`)
    }
  })

  it('LogsTab 与 chip 引用的是同一个命名空间前缀', () => {
    // 防漂移：两边任一改了命名空间，表头就会与行内标签对不上
    const a = codeOnly.match(/models\.modelIdentity\./)
    const b = logsTabCode.match(/models\.modelIdentity\./)
    expect(a).not.toBeNull()
    expect(b).not.toBeNull()
    expect(a![0]).toBe(b![0])
  })
})
