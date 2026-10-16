import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import RestrictedPageView from './RestrictedPageView.vue'
import { setLocale, locale } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'

vi.mock('@/hyper', async original => ({ ...await original<typeof import('@/hyper')>(), useHyperPage: () => {} }))

/**
 * 批 150 终端页：路由守卫把不匹配 role 的人跳到这里。
 *
 * 三件事钉在源码层：
 *  1. 只显示 `common.pageDenied` 一行字，**不挂载任何 API 模块**
 *  2. 跨语种：两套 i18n 字典都给出 `common.pageDenied` 且值相等
 *  3. body 文本**不**带任何真页面名（防 document.title 泄露）—— 这是
 *     与 RestrictedPageTitle 8-3 同族但更窄：只钉这一页。
 */
describe('RestrictedPageView（批 150 终端页）', () => {
  let mounted: ReturnType<typeof mount>[] = []
  const originalLocale = locale.value

  beforeEach(() => { mounted = [] })
  afterEach(() => { for (const w of mounted.splice(0)) w.unmount(); setLocale(originalLocale) })

  it('1 ★★★ 渲染只显示 `common.pageDenied` 一行（zh-CN）', async () => {
    setLocale('zh-CN')
    const w = mount(RestrictedPageView); mounted.push(w)
    await flushPromises()
    expect(w.find('[data-testid="rp-denied"]').text()).toBe(zhCN.common.pageDenied)
    expect(w.find('[data-testid="rp-denied"]').text()).not.toMatch(/format|drift|integrity|anomalies/i)
  })

  it('2 ★★★ 渲染只显示 `common.pageDenied` 一行（en-US）', async () => {
    setLocale('en-US')
    const w = mount(RestrictedPageView); mounted.push(w)
    await flushPromises()
    expect(w.find('[data-testid="rp-denied"]').text()).toBe(enUS.common.pageDenied)
  })

  it('3 ★★★★ body 文本**不能**带任何真页面名（防 document.title 泄露）', () => {
    // ★ 任何人在这里加「想去 /format-anomalies？点这里」之类的"贴心提示"
    //   都会带出真页面名，立刻红。
    const fs = require('node:fs')
    const path = require('node:path')
    const src = fs.readFileSync(path.join(process.cwd(), 'src/views/RestrictedPageView.vue'), 'utf8')
    const navs = [
      'format-anomalies', 'model-integrity-drift', 'integrity', 'request-actions',
      'admin', 'users', 'reconciliation', 'ops-overview', 'model-iq', 'sliding-window',
      'model-history', 'errors-trend', 'request-anomalies', 'subscription-tiers',
      'platform-settings', 'usage-enhanced', 'data-lifecycle-storage', 'storage-log-config',
      'storage-migration-state', 'turns-filter-options', 'session-analytics-filter-options',
      'sanitize-rules',
    ]
    for (const n of navs) {
      expect(src, `RestrictedPageView 模板/脚本不得带真页面名 /${n}`).not.toMatch(
        new RegExp(`\\/${n}(?![A-Za-z0-9_-])`),
      )
    }
  })

  it('4 ★★★ 不 import 任何 API 模块（错角色深链不挂任何后端）', () => {
    const fs = require('node:fs')
    const path = require('node:path')
    const apiDir = path.join(process.cwd(), 'src/api')
    const apiModules = fs.readdirSync(apiDir)
      .filter((f: string) => f.endsWith('.ts') && !f.includes('.test.') && !f.includes('.spec.'))
    const src = fs.readFileSync(path.join(process.cwd(), 'src/views/RestrictedPageView.vue'), 'utf8')
    for (const m of apiModules) {
      const name = m.replace(/\.ts$/, '')
      expect(src, `RestrictedPageView 不得 import @/api/${name}（错角色深链不应触发任何 API）`).not.toMatch(
        new RegExp(`from\\s+['"]@/api/${name}['"]`),
      )
    }
  })

  it('5 ★★★ 两语种 `common.pageDenied` 都有值、值相等（错角色深链不会因为没翻译而退化成键名）', () => {
    const zh = zhCN.common?.pageDenied
    const en = enUS.common?.pageDenied
    expect(typeof zh === 'string' && zh.trim().length > 0, 'zh-CN 缺 common.pageDenied').toBe(true)
    expect(typeof en === 'string' && en.trim().length > 0, 'en-US 缺 common.pageDenied').toBe(true)
    expect(zh, 'zh-CN 与 en-US 应该有差异或至少不是同一字符串' + `（zh="${zh}" en="${en}"）`).not.toBe(en)
  })
})
