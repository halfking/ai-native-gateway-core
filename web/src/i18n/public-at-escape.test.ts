import { describe, it, expect } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../locales/zh-CN'
import enUS from '../locales/en-US'
import zhTW from '../locales/zh-TW'
import jaJP from '../locales/ja-JP'
import frFR from '../locales/fr-FR'
import deDE from '../locales/de-DE'
import esES from '../locales/es-ES'
import arSA from '../locales/ar-SA'

describe('public.support.emailPlaceholder', () => {
  it('zh-CN resolves @ without linked-format error', () => {
    const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zhCN } })
    expect(i18n.global.t('public.support.emailPlaceholder')).toBe('you@company.com')
  })

  it('en-US resolves @ without linked-format error', () => {
    const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: enUS } })
    expect(i18n.global.t('public.support.emailPlaceholder')).toBe('you@company.com')
  })
})

describe('tenants.create.contactPlaceholder — @ escape coverage (2026-11-12)', () => {
  // 本族守卫覆盖 8 语种的 tenants.create.contactPlaceholder，防止裸 @ 字符触发
  // vue-i18n 的 linked-message 语法解析器（`@:key` 起始符）导致 Invalid linked format 崩溃。
  // 正确形式：{'@'} 转义语法（见 public.support.emailPlaceholder 范式）。
  //
  // ★ 守卫强度：断言值里**必须含 @**（转义后的 @ 会出现在最终字符串里），
  // 且**不得含裸 @ 后面跟 { 的模式**（那是编译错误降级后的残留）。
  const locales = [
    { code: 'zh-CN', bundle: zhCN },
    { code: 'en-US', bundle: enUS },
    { code: 'zh-TW', bundle: zhTW },
    { code: 'ja-JP', bundle: jaJP },
    { code: 'fr-FR', bundle: frFR },
    { code: 'de-DE', bundle: deDE },
    { code: 'es-ES', bundle: esES },
    { code: 'ar-SA', bundle: arSA },
  ]

  locales.forEach(({ code, bundle }) => {
    it(`${code} resolves without linked-format error`, () => {
      const i18n = createI18n({ legacy: false, locale: code, messages: { [code]: bundle } })
      const resolved = i18n.global.t('tenants.create.contactPlaceholder')
      
      // 断言 1: 必须成功解析为非空字符串
      expect(typeof resolved).toBe('string')
      expect(resolved.length).toBeGreaterThan(0)
      
      // 断言 2: 结果里必须含 @ 字符（证明 {'@'} 转义生效了）
      expect(resolved).toContain('@')
      
      // 断言 3: 不得含编译错误降级的残留模式（裸 @ 后跟 {SENSITIVE 等）
      // 编译失败时 vue-i18n 会把原文当 fallback，那会含 @{ 连写
      expect(resolved).not.toMatch(/@\{/)
    })
  })
})
