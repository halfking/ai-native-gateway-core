import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import StatisticsHelpSheet from './StatisticsHelpSheet.vue'
import { locale, setLocale } from '@/i18n'

describe('StatisticsHelpSheet', () => {
  const originalLocale = locale.value

  it('reuses the desktop page guide and shared localized standards', async () => {
    setLocale('en-US')
    const wrapper = mount(StatisticsHelpSheet, {
      props: { modelValue: true, contentId: 'usage-enhanced', contextKey: 'statisticsHelp.pages.usageEnhanced' },
      global: { stubs: { AppSheet: { template: '<section><slot /></section>', props: ['modelValue', 'title'] } } },
    })
    expect(wrapper.text()).toContain('This page combines several endpoints')
    expect(wrapper.text()).toContain('Cached tokens and cache hit rates')
    expect(wrapper.text()).toContain('Count inbound requests by tenant and request ID')
    wrapper.unmount()
    setLocale(originalLocale)
  })

  it('falls back to the shared Chinese standards when English is unavailable', () => {
    setLocale('zh-CN')
    const wrapper = mount(StatisticsHelpSheet, {
      props: { modelValue: true, contentId: 'session-detail', contextKey: 'statisticsHelp.pages.sessionAudit' },
      global: { stubs: { AppSheet: { template: '<section><slot /></section>', props: ['modelValue', 'title'] } } },
    })
    expect(wrapper.text()).toContain('展开会话可查看逐轮记录')
    expect(wrapper.text()).toContain('客户端请求数')
    expect(wrapper.text()).toContain('未知或缺失值显示为缺失')
    wrapper.unmount()
    setLocale(originalLocale)
  })

  it('shows the monthly reconciliation context without the daily snapshot guide', () => {
    setLocale('zh-CN')
    const wrapper = mount(StatisticsHelpSheet, {
      props: { modelValue: true, contextKey: 'statisticsHelp.pages.reconciliation' },
      global: { stubs: { AppSheet: { template: '<section><slot /></section>' } } },
    })
    expect(wrapper.text()).toContain('网关月汇总与导入账单')
    expect(wrapper.text()).toContain('客户端请求数')
    expect(wrapper.text()).not.toContain('每日聚合任务')
    wrapper.unmount()
    setLocale(originalLocale)
  })
})
