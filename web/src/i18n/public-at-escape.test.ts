import { describe, it, expect } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../locales/zh-CN'
import enUS from '../locales/en-US'

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
