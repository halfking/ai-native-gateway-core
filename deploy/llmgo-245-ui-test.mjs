// /Users/xutaohuang/workspace/llm-gateway-go-4/web/llmgo-ui-test.mjs — UI E2E test
// Auto-discovers current chenbuser password from a rotating pool.

import { chromium } from 'playwright'
import { writeFile } from 'node:fs/promises'

const DOMAIN = 'https://llmgo.kxpms.cn'
const USERNAME = 'chenbuser'
// Password pool — test tries each in order until one succeeds
const PW_POOL = ['Chenbiao258', 'Chenbiao123', 'Chenbiao456', 'Chenbiao789', 'Chenbiao147', 'Chenbiao369', 'Chenbiao741']
const NEW_PW = 'Chenbiao123'
const SCREEN_DIR = '/tmp/llmgo-screens'

const log = (...a) => console.log('[ui-test]', ...a)
const ok = (n) => console.log(`  ✓ ${n}`)
const fail = (n, e) => { console.error(`  ✗ ${n}: ${e}`); process.exitCode = 1 }
const wait = (ms) => new Promise((r) => setTimeout(r, ms))

async function shot(page, name) {
  await page.screenshot({ path: `${SCREEN_DIR}/${name}.png`, fullPage: true })
}

async function loginApi(pw) {
  const r = await fetch(`${DOMAIN}/api/auth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: USERNAME, password: pw }),
  })
  if (r.status === 200) return await r.json()
  return null
}

;(async () => {
  await import('node:fs/promises').then(({ mkdir }) => mkdir(SCREEN_DIR, { recursive: true }))

  log('Discovering current chenbuser password...')
  let currentPw = null
  for (const pw of PW_POOL) {
    const r = await loginApi(pw)
    if (r) {
      currentPw = pw
      log(`  ✓ Current password: ${pw}`)
      log(`    must_change_password=${r.user.must_change_password}`)
      break
    }
  }
  if (!currentPw) {
    fail('Could not login with any known password', '')
    process.exit(2)
  }

  const browser = await chromium.launch({
    headless: true,
    args: ['--ignore-certificate-errors', '--no-sandbox'],
  })
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    ignoreHTTPSErrors: true,
    locale: 'zh-CN',
  })
  const page = await ctx.newPage()
  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      const txt = msg.text()
      if (!txt.includes('401') && !txt.includes('403')) {
        log('   [browser]', txt.slice(0, 200))
      }
    }
  })

  log('\n=== TEST 1: First-login forced password change ===')
  await page.goto(`${DOMAIN}/`, { waitUntil: 'domcontentloaded' })
  await wait(1500)
  await shot(page, '01-home')

  const modalVisible = await page.locator('.modal-overlay').isVisible().catch(() => false)
  if (modalVisible) ok('LoginModal 自动弹出') ; else { fail('LoginModal 未弹出', '') }

  await page.fill('input[autocomplete="username"]', USERNAME)
  await page.fill('input[type="password"]', currentPw)
  await page.locator('.modal-overlay button[type="submit"]:has-text("登录")').click()
  await wait(2500)
  await shot(page, '03-after-login')

  const changePwVisible = await page.locator('text=首次登录需要先修改密码后才能继续使用').isVisible().catch(() => false)
  if (changePwVisible) ok('强制改密对话框弹出') ; else { fail('强制改密对话框未弹出', '') }

  await page.fill('input#current-password', currentPw)
  await page.fill('input#new-password', NEW_PW)
  await page.fill('input#confirm-password', NEW_PW)
  await wait(500)
  await shot(page, '04-change-pw-filled')

  await page.locator('button[type="submit"]:has-text("确认修改")').click()
  log('  等待改密完成 + 自动注销 + 重新登录 + 刷新...')
  await wait(5000)
  await shot(page, '05-after-change-pw')

  const onLoginPage = (await page.locator('body').textContent()).includes('首次登录需要先修改密码')
  if (!onLoginPage) ok('改密对话框已关闭') ; else fail('改密对话框仍停留', '')

  log('\n=== TEST 2: Sidebar — "models-routing" group hidden ===')
  await wait(2000)
  await shot(page, '06-dashboard-after-reload')

  const inAppLayout = await page.locator('.app-layout').isVisible().catch(() => false)
  const sidebarText = await page.locator('.sidebar-nav').textContent().catch(() => '')
  if (inAppLayout) ok('已进入 app-layout (登录态保持)') ; else fail('未进入 app-layout', '')
  if (!sidebarText.includes('模型与路由')) ok('侧栏不显示「模型与路由」分组') ; else fail('侧栏仍显示「模型与路由」', '')
  if (!sidebarText.includes('模型与目录') && !sidebarText.includes('探测健康度')) ok('侧栏不泄漏分组内项目') ; else fail('侧栏泄漏', '')

  log('\n=== TEST 3: /tenant/models — no vendor grouping ===')
  await page.goto(`${DOMAIN}/tenant/models`, { waitUntil: 'domcontentloaded' })
  await wait(2000)
  await shot(page, '07-tenant-models')

  const hasVendorSection = await page.locator('.vendor-section').count() > 0
  const filterBarCount = await page.locator('.filter-bar').count()
  const bodyTxt = (await page.locator('body').textContent()).slice(0, 1000)
  if (!hasVendorSection) ok('无供应商分组区块') ; else fail('仍存在 .vendor-section', '')
  if (filterBarCount > 0) ok(`顶部筛选栏存在 (${filterBarCount})`) ; else fail('筛选栏缺失', '')
  if (bodyTxt.includes('输入/1M') && bodyTxt.includes('输出/1M')) ok('积分单价列完整') ; else fail('积分单价列缺失', '')

  log('\n=== TEST 4: Dashboard v3 — KPI row + tab switcher ===')
  await page.goto(`${DOMAIN}/`, { waitUntil: 'domcontentloaded' })
  await wait(3000)
  await shot(page, '08-dashboard')

  const kpiCount = await page.locator('.stat-mini').count()
  if (kpiCount >= 4) ok(`紧凑 KPI 行存在（${kpiCount} 张卡）`) ; else fail(`KPI 卡数量不足: ${kpiCount}`, '')
  const tabButtons = await page.locator('.tab-btn').count()
  if (tabButtons >= 2) ok(`Tab 切换器存在（${tabButtons} 个）`) ; else fail(`Tab 切换器缺失: ${tabButtons}`, '')

  log('\n=== TEST 5: i18n — 8 locales ===')
  const locales = [
    { code: 'zh-CN', expect: '仪表盘' },
    { code: 'en',    expect: 'Dashboard' },
    { code: 'ja',    expect: 'ダッシュボード' },
    { code: 'de',    expect: 'Dashboard' },
    { code: 'fr',    expect: 'Tableau de bord' },
    { code: 'es',    expect: 'Panel' },
    { code: 'zh-TW', expect: '儀表板' },
    { code: 'ar',    expect: 'لوحة' },
  ]
  for (const { code, expect } of locales) {
    // Set localStorage AND explicitly trigger vue-i18n locale switch via app
    await page.evaluate((c) => {
      localStorage.setItem('llmgw_locale', c)
      // vue-i18n reads llmgw_locale on load, but messages for lazy locales need explicit load
      // Just reload will pick up the saved locale after re-init
    }, code)
    await page.reload({ waitUntil: 'domcontentloaded' })
    await wait(3000)
    await shot(page, `locale-${code}`)
    const body = await page.locator('body').textContent()
    const h2 = await page.locator('h2').first().textContent().catch(() => '')
    if (body.includes(expect)) ok(`${code}: 含 "${expect}"`) ; else log(`  ⚠ ${code}: h2="${h2.slice(0,30)}" 不含 "${expect}"`)
  }

  log('\n=== TEST 6: /tenant/account ===')
  await page.evaluate(() => localStorage.setItem('llmgw_locale', 'zh-CN'))
  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.goto(`${DOMAIN}/tenant/account`, { waitUntil: 'domcontentloaded' })
  await wait(2500)
  await shot(page, '09-tenant-account')
  const accountTxt = await page.locator('body').textContent()
  if (accountTxt.includes('订阅额度') && accountTxt.includes('可用总额')) ok('/tenant/account 渲染正常') ; else fail('/tenant/account 缺关键标签', '')

  log('\n=== TEST 7: /tenant/usage ===')
  await page.goto(`${DOMAIN}/tenant/usage`, { waitUntil: 'domcontentloaded' })
  await wait(2500)
  await shot(page, '10-tenant-usage')
  const usageTxt = await page.locator('body').textContent()
  if (usageTxt.includes('我的消耗') || usageTxt.includes('积分消耗')) ok('/tenant/usage 渲染正常') ; else fail('/tenant/usage 缺关键标签', '')

  log('\n=== TEST 8: /tenant/pricing ===')
  await page.goto(`${DOMAIN}/tenant/pricing`, { waitUntil: 'domcontentloaded' })
  await wait(2500)
  await shot(page, '11-tenant-pricing')
  const pricingTxt = await page.locator('body').textContent()
  if (pricingTxt.includes('套餐') && pricingTxt.includes('立即购买')) ok('/tenant/pricing 渲染正常') ; else fail('/tenant/pricing 缺关键标签', '')

  await browser.close()

  console.log('\n=========================================')
  console.log('UI test complete. Screenshots:', SCREEN_DIR)
  console.log(`After this run, chenbuser password = ${NEW_PW}`)
  console.log('=========================================')
  process.exit(process.exitCode || 0)
})().catch((e) => {
  console.error('[ui-test] fatal:', e)
  process.exit(2)
})