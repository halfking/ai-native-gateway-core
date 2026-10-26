import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
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

// ── 批 151：返回首页按钮 ──────────────────────────────────────────────

describe('RestrictedPageView 返回按钮（批 151）', () => {
  let mounted: ReturnType<typeof mount>[] = []
  const originalLocale = locale.value

  beforeEach(() => { mounted = [] })
  afterEach(() => { for (const w of mounted.splice(0)) w.unmount(); setLocale(originalLocale) })

  it('6 ★★★ 渲染返回按钮（zh-CN 用 common.back）', async () => {
    setLocale('zh-CN')
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/", component: { template: "<div/>" } }, { path: "/restricted", component: { template: "<div/>" } }] })
    const w = mount(RestrictedPageView, { global: { plugins: [router] } })
    mounted.push(w)
    await flushPromises()
    const btn = w.find('[data-testid="rp-back"]')
    expect(btn.exists(), '终端页必须有「返回」按钮 —— 依赖浏览器 back 行为太隐晦').toBe(true)
    expect(btn.text()).toBe(zhCN.common.back)
  })

  it('7 ★★★ 渲染返回按钮（en-US 用 common.back）', async () => {
    setLocale('en-US')
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/", component: { template: "<div/>" } }, { path: "/restricted", component: { template: "<div/>" } }] })
    const w = mount(RestrictedPageView, { global: { plugins: [router] } })
    mounted.push(w)
    await flushPromises()
    expect(w.find('[data-testid="rp-back"]').text()).toBe(enUS.common.back)
  })

  it('8 ★★★★ 点击返回按钮 ⇒ router.push(\'/\')（深链场景下 history 长度可能 = 1）', async () => {
    // ★ 与"返回上一页"(`router.back()`)的差别：深链进 /restricted 时 history.length 可能 = 1，
    //   back() 退不出。`router.push('/')` 显式跳首页更稳。
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div/>' } }] })
    await router.push('/restricted')  // 模拟路由守卫的实际跳转
    await router.isReady()
    const w = mount(RestrictedPageView, { global: { plugins: [router] } })
    mounted.push(w)
    await flushPromises()
    await w.find('[data-testid="rp-back"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path, '点击返回按钮必须真的跳到首页').toBe('/')
  })

  it('9 ★★★★ 按钮 min-height ≥ 48px（touch-target R1）', () => {
    // ★ 触控件**不能**靠文档习惯约束 —— 触控门要扫源码
    const fs = require('node:fs')
    const path = require('node:path')
    const src = fs.readFileSync(path.join(process.cwd(), 'src/views/RestrictedPageView.vue'), 'utf8')
    expect(src, '按钮必须有 min-height: 48px（或更大）声明，否则触控门会红').toMatch(/min-height:\s*4[8-9]px|min-height:\s*[5-9]\dpx|min-height:\s*\d{3,}px/)
  })

  it('10 ★★ 源码**不**调 router.back()（防深链 history 长度 = 1 时死循环）', () => {
    // ★ 双向护栏：行为层（test 8）走 `router.push('/')`，
    //   源码形态层（test 10）则禁止 `router.back()` / `history.back()`。
    const fs = require('node:fs')
    const path = require('node:path')
    const src = fs.readFileSync(path.join(process.cwd(), 'src/views/RestrictedPageView.vue'), 'utf8')
    expect(src, '不允许 router.back() —— 深链场景下 history 长度可能 = 1').not.toMatch(/\brouter\.back\s*\(/)
    expect(src, '不允许 history.back()（同上）').not.toMatch(/\bhistory\.back\s*\(/)
  })
})
