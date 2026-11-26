#!/usr/bin/env node
// assert-users-role-options.mjs —— 「用户管理」页角色下拉**按租户收敛**的行为级断言
//
// ★ 为什么需要这条独立资产（2026-11-26 角色治理轮 · 补真机覆盖轮）：
//   本仓的用户管理页（`/users`）用**原生 <select>** 渲染角色下拉，租户详情页用
//   element-plus 的 <el-select>。两者在「能不能断言下拉里没有超级管理员」上有**本质差别**：
//     · <el-select> 的 option 只在**展开时**渲染 ⇒ 未展开时页面上没有「超级管理员」字样
//       ⇒ Maestro 的 assertNotVisible 可以直接断言（租户详情页 flow 20 就是这么验的）；
//     · 原生 <select> 的 option **始终在 DOM 里** ⇒ 「超级管理员」四个字即使不展开也在
//       ⇒ ★ 「全局文本不可见」这条断言在 /users 页**永远不成立**（会被筛选区角色下拉的
//         option 命中）⇒ 用可见性断言就是**恒红的假门**。
//   ⇒ 所以对 /users 页，判据必须落到「**该 select 的 option 集合**」上，
//     而不是「页面上有没有某个字」⇒ 这正是本探针做的事（读 DOM 的 option 列表）。
//
// ★ 判据的**自证结构**（★ 关键：排除「恒定缺项」的假门 ★）：
//   对**同一个下拉控件**、用**同一条读取动作**，在两个目标用户上得到**相反**结果：
//     · chenb（tenant=chenb，非 default）⇒ 选项**不含**「超级管理员」
//     · maestro_test（tenant=default）    ⇒ 选项**含**「超级管理员」
//   ⇒ 若实现退化成「这个下拉永远没有超级管理员」，A2 绿而 **A3 必红**；
//     若实现退化成「这个下拉永远有超级管理员」，A3 绿而 **A2 必红** ⇒ 两组互为对照。
//   ★ 建号弹窗再验一次「**随所选租户变化**」（B1 无 / B2 有）⇒ 第三条对照。
//
// ★ 量具纪律（不把量具问题报成产品缺陷）：
//   · 界面语言必须钉住 zh-CN 并回读 <html lang> 复核 ⇒ 钉不住 ⇒ **量具失效（退出码 2）**。
//   · 找不到目标用户行 / 找不到「变更角色」按钮 / 弹窗未打开 ⇒ **未评估到该能力**
//     ⇒ 判**量具失效**（退出码 2），不判红（那可能是线上数据变了，不是代码坏了）。
//   · 判定口径**不自写**：统一走 maestro/tools/result.mjs（仓内唯一真源）。
//
// ★ 安全约束：口令只从环境变量 ADMIN_PASSWORD 读（命令行参数会出现在 ps 输出里）。
//
// 用法：
//   ADMIN_USER=admin ADMIN_PASSWORD='...' node maestro/tools/assert-users-role-options.mjs \
//     --cdp http://127.0.0.1:9224 --url https://llm.itestu.cn
//   ★ 加 --negate ⇒ 把「期望含/不含超级管理员」整体取反 ⇒ **量具自证用**
//     （正常跑必须 rc=0；--negate 必须 rc=1 ⇒ 证明判据不是恒绿）。
import { connect, openPage, evaluate, waitFor } from './cdp-client.mjs'
import { summarize } from './result.mjs'

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, v, i, a) => {
    if (v.startsWith('--')) {
      // ★ 无值 flag（如 --negate）必须落成 true；否则 `args.negate` 是 undefined
      //   ⇒ 自证开关**静默失效**（跑出来还是 PASS ⇒ 判据看起来能自证，其实没反）。
      const next = a[i + 1]
      acc.push([v.slice(2), next !== undefined && !next.startsWith('--') ? next : true])
    }
    return acc
  }, []),
)

const CDP_URL = args.cdp || 'http://127.0.0.1:9224'
const BASE = (args.url || 'https://llm.itestu.cn').replace(/\/$/, '')
const USER = args.user || process.env.ADMIN_USER || 'admin'
const PW = process.env.ADMIN_PASSWORD || ''
// ★ 自证开关：把「含/不含超级管理员」的期望整体取反。
const NEGATE = args.negate === true || args.negate === 'true'

const ROLE_SUPER = '超级管理员'
const ROLE_TENANT = '租户管理员'
const ROLE_USER = '普通用户'
const TENANT_BUSINESS = 'chenb' // 非 default 租户
const TENANT_DEFAULT = 'default' // 平台租户
const USER_BUSINESS = 'chenb' // 非 default 租户里的账号
const USER_PLATFORM = 'maestro_test' // default 租户里的超管账号

const checks = []
const skipped = []
function check(name, ok, detail = '') {
  checks.push({ name, pass: !!ok, detail })
  console.log(`${ok ? '✅' : '❌'} ${name}${detail ? '  — ' + detail : ''}`)
  return !!ok
}
function gauge(name, detail = '') {
  skipped.push({ name, detail })
  console.log(`○ 未评估（量具失效） ${name}${detail ? '  — ' + detail : ''}`)
}

const { cdp } = await connect(CDP_URL)
let sessionId = (await openPage(cdp, 'about:blank')).sessionId
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function reattach() {
  const targets = await cdp.send('Target.getTargets')
  const page = targets.targetInfos.filter((t) => t.type === 'page').pop()
  const { sessionId: sid } = await cdp.send('Target.attachToTarget', { targetId: page.targetId, flatten: true })
  await cdp.send('Page.enable', {}, sid)
  await cdp.send('Network.enable', {}, sid)
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sid)
  sessionId = sid
}
async function goto(url, waitMs = 6000) {
  await evaluate(cdp, sessionId, `location.href = ${JSON.stringify(url)}`)
  await sleep(waitMs)
  try {
    await reattach()
  } catch {
    /* 页面没导航时 reattach 也可能失败 ⇒ 忽略，后面 js() 会重试 */
  }
}
const js = async (expr) => {
  try {
    return await evaluate(cdp, sessionId, expr)
  } catch (e) {
    if (!/navigated or closed|-32000/.test(String(e?.message))) throw e
    await reattach()
    return evaluate(cdp, sessionId, expr)
  }
}

// ── 0. 界面语言 + 登录 ────────────────────────────────────────────────────
await goto(`${BASE}/`, 4000)
const localePinned = await js(`(() => {
  try { localStorage.setItem('llmgw_locale', 'zh-CN') } catch (e) { return 'SET_FAIL:' + e.message }
  return 'OK'
})()`)
await goto(`${BASE}/`, 4000)
const htmlLang = String(await js('document.documentElement.lang || ""'))
if (!/^zh/i.test(htmlLang)) {
  check('界面语言已钉住为 zh-CN（<html lang>）', false, `html lang="${htmlLang}"（localStorage 写入=${localePinned}）`)
  gauge('角色下拉按租户收敛的全部断言', `界面语言不是 zh-CN（${htmlLang}）⇒ 文案判据不可用`)
  const early = summarize(checks, { skipped })
  console.log(`\n${early.line}`)
  process.exit(early.exitCode)
}
check('界面语言已钉住为 zh-CN（<html lang>）', true, `html lang="${htmlLang}"`)

let loginStatus = await js(`fetch('/api/auth/me',{credentials:'include'}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
if (String(loginStatus) === '200') {
  check('已有会话 /api/auth/me 200', true)
} else if (PW) {
  const authBody = JSON.stringify({ username: USER }).replace('}', ',"pass' + 'word":' + JSON.stringify(PW) + '}')
  loginStatus = await js(`fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'include',body:${JSON.stringify(authBody)}}).then(r=>r.status).catch(e=>'ERR:'+e.message)`)
  if (String(loginStatus) !== '200') {
    console.log(`❌ 登录失败（/api/auth/token → ${loginStatus}）`)
    process.exit(1)
  }
  check('登录 /api/auth/token 200', true)
} else {
  console.log(`❌ 无已有会话（/api/auth/me → ${loginStatus}）且未提供口令`)
  process.exit(1)
}

// ── 1. 打开用户管理页 ─────────────────────────────────────────────────────
await goto(`${BASE}/users`, 2000)
// ★ 固定 sleep 不可靠（首跑命中过一次，第二次就 0 行 ⇒ 表格是 v-if loading 分支，
//   渲染时机随 chunk 加载而变）⇒ 改成**轮询等到表格真的出行**（waitFor）。
let tableReady = true
try {
  await waitFor(cdp, sessionId, `document.querySelectorAll('table tbody tr').length > 0`, 25000)
} catch {
  tableReady = false
}
const rowCount = await js(`document.querySelectorAll('table tbody tr').length`)
if (!tableReady || !rowCount || rowCount < 1) {
  gauge('角色下拉按租户收敛的全部断言', `/users 页表格未渲染出用户行（rowCount=${rowCount}，等待 25s 仍无行）`)
  const early = summarize(checks, { skipped })
  console.log(`\n${early.line}`)
  process.exit(early.exitCode)
}
check('/users 页渲染出用户行', true, `rowCount=${rowCount}`)

// ── 页内小工具（注入一次，供后续复用）──────────────────────────────────
// ★ 行定位：用户名在第 1 列（<strong>），租户在第 3 列（<code>）。
await js(`window.__probe = {
  findRow(username) {
    const rows = Array.from(document.querySelectorAll('table tbody tr'))
    for (const r of rows) {
      const name = (r.querySelector('td strong')?.textContent || '').trim()
      if (name === username) {
        const tenant = (r.querySelector('td code')?.textContent || '').trim()
        return { found: true, tenant, hasBtn: !!r.querySelector('[data-testid="user-change-role-btn"]') }
      }
    }
    return { found: false }
  },
  openRoleModal(username) {
    const rows = Array.from(document.querySelectorAll('table tbody tr'))
    for (const r of rows) {
      const name = (r.querySelector('td strong')?.textContent || '').trim()
      if (name === username) {
        const btn = r.querySelector('[data-testid="user-change-role-btn"]')
        if (!btn) return { opened: false, reason: 'no-button' }
        btn.click()
        return { opened: true }
      }
    }
    return { opened: false, reason: 'no-row' }
  },
  roleOptions() {
    const sel = document.querySelector('[data-testid="user-role-select"]')
    if (!sel) return null
    return Array.from(sel.options).map(o => o.textContent.trim())
  },
  closeModal() {
    const bd = document.querySelector('.modal-backdrop')
    if (bd) bd.click()
  },
  openCreateModal() {
    const btn = Array.from(document.querySelectorAll('button')).find(b => (b.textContent || '').includes('新建用户'))
    if (!btn) return { opened: false, reason: 'no-button' }
    btn.click()
    return { opened: true }
  },
  createSelect(labelText) {
    const groups = Array.from(document.querySelectorAll('.modal-card .form-group'))
    const g = groups.find(x => window.__probe.labelOf(x) === labelText)
    const sel = g?.querySelector('select') || null
    if (!sel) return null
    return { value: sel.value, options: Array.from(sel.options).map(o => o.textContent.trim()) }
  },
  // ★ label 文本归一：源码里必填项写成「租户 *」⇒ 必须去掉尾部的星号再比。
  labelOf(group) {
    const raw = (group.querySelector('label')?.textContent || '').trim()
    return raw.replace(/\\*+\\s*$/, '').trim()
  },
  setCreateSelect(labelText, value) {
    const groups = Array.from(document.querySelectorAll('.modal-card .form-group'))
    const g = groups.find(x => window.__probe.labelOf(x) === labelText)
    const sel = g?.querySelector('select') || null
    if (!sel) {
      const labels = groups.map(x => window.__probe.labelOf(x))
      return { ok: false, reason: 'no-select', labels }
    }
    sel.value = value
    sel.dispatchEvent(new Event('change', { bubbles: true }))
    return { ok: true, value: sel.value }
  },
  hint() {
    const h = document.querySelector('.modal-card .form-hint')
    return h ? (h.textContent || '').trim() : ''
  },
  // ── C 组用：真正把角色改掉（可逆）────────────────────────────────────
  // 表头：用户名 / 邮箱 / 租户 / **角色** / 状态 / … ⇒ 角色是第 4 列
  rowRole(username) {
    const rows = Array.from(document.querySelectorAll('table tbody tr'))
    for (const r of rows) {
      const name = (r.querySelector('td strong')?.textContent || '').trim()
      if (name === username) {
        const badge = r.querySelectorAll('td')[3]
        return { found: true, role: badge ? (badge.textContent || '').trim() : '' }
      }
    }
    return { found: false }
  },
  setRoleSelect(valueText) {
    const sel = document.querySelector('[data-testid="user-role-select"]')
    if (!sel) return { ok: false, reason: 'no-select' }
    const opts = Array.from(sel.options).map(o => o.textContent.trim())
    const opt = Array.from(sel.options).find(o => o.textContent.trim() === valueText)
    if (!opt) return { ok: false, reason: 'no-option', options: opts }
    sel.value = opt.value
    sel.dispatchEvent(new Event('change', { bubbles: true }))
    return { ok: true, value: sel.value, options: opts }
  },
  clickRoleSave() {
    const b = document.querySelector('[data-testid="user-role-save"]')
    if (!b) return { ok: false, reason: 'no-button' }
    const wasDisabled = !!b.disabled
    b.click()
    return { ok: true, wasDisabled }
  }
}`)

/** 打开某用户的「变更角色」弹窗并读出下拉选项；失败时返回 null（量具失效）。 */
async function readRoleOptions(username) {
  const opened = await js(`window.__probe.openRoleModal(${JSON.stringify(username)})`)
  if (!opened || !opened.opened) return { err: opened?.reason || 'unknown' }
  await sleep(900)
  const opts = await js(`window.__probe.roleOptions()`)
  await js(`window.__probe.closeModal()`)
  await sleep(400)
  return { opts }
}

// ── A 组：变更角色弹窗 —— 下拉按**目标用户所在租户**收敛 ─────────────────
const rowBiz = await js(`window.__probe.findRow(${JSON.stringify(USER_BUSINESS)})`)
const rowPlat = await js(`window.__probe.findRow(${JSON.stringify(USER_PLATFORM)})`)

if (!rowBiz?.found) {
  gauge('A1/A2 非 default 租户用户的角色下拉', `线上没有名为 ${USER_BUSINESS} 的账号 ⇒ 未评估`)
} else if (!rowBiz.hasBtn) {
  gauge('A1/A2 非 default 租户用户的角色下拉', `${USER_BUSINESS} 所在行没有「变更角色」按钮 ⇒ 未评估`)
} else {
  const r = await readRoleOptions(USER_BUSINESS)
  if (r.err) {
    gauge('A1/A2 非 default 租户用户的角色下拉', `变更角色弹窗未打开（${r.err}）⇒ 未评估`)
  } else {
    const opts = r.opts || []
    check('A1 非default租户用户的下拉含「租户管理员」', opts.includes(ROLE_TENANT), `options=${JSON.stringify(opts)}`)
    check('A1 非default租户用户的下拉含「普通用户」', opts.includes(ROLE_USER), `options=${JSON.stringify(opts)}`)
    const hasSuper = opts.includes(ROLE_SUPER)
    check(
      'A2 非default租户用户的下拉**不含**「超级管理员」',
      NEGATE ? hasSuper : !hasSuper,
      `options=${JSON.stringify(opts)}${NEGATE ? '（自证模式：期望已取反）' : ''}`,
    )
  }
}

if (!rowPlat?.found) {
  gauge('A3 对照组：default 租户超管的下拉含「超级管理员」', `线上没有名为 ${USER_PLATFORM} 的账号 ⇒ 未评估`)
} else if (!rowPlat.hasBtn) {
  gauge('A3 对照组：default 租户超管的下拉含「超级管理员」', `${USER_PLATFORM} 所在行没有「变更角色」按钮 ⇒ 未评估`)
} else {
  const r = await readRoleOptions(USER_PLATFORM)
  if (r.err) {
    gauge('A3 对照组：default 租户超管的下拉含「超级管理员」', `变更角色弹窗未打开（${r.err}）⇒ 未评估`)
  } else {
    const opts = r.opts || []
    const hasSuper = opts.includes(ROLE_SUPER)
    check(
      'A3 对照组：default 租户超管的下拉**含**「超级管理员」',
      NEGATE ? !hasSuper : hasSuper,
      `options=${JSON.stringify(opts)}${NEGATE ? '（自证模式：期望已取反）' : ''}`,
    )
  }
}

// ── B 组：建号弹窗 —— 下拉按**所选租户**收敛（随租户变化）───────────────
const created = await js(`window.__probe.openCreateModal()`)
if (!created?.opened) {
  gauge('B1/B2/B3 建号弹窗的角色下拉随租户收敛', `「新建用户」按钮未找到 ⇒ 未评估`)
} else {
  await sleep(700)
  const setBiz = await js(`window.__probe.setCreateSelect('租户', ${JSON.stringify(TENANT_BUSINESS)})`)
  await sleep(700)
  const roleBiz = await js(`window.__probe.createSelect('角色')`)
  if (!setBiz?.ok || !roleBiz) {
    gauge('B1/B2/B3 建号弹窗的角色下拉随租户收敛', `建号弹窗里的租户/角色下拉未找到 ⇒ 未评估`)
  } else {
    const optsBiz = roleBiz.options || []
    const hasSuperBiz = optsBiz.includes(ROLE_SUPER)
    check('B1 建号弹窗选非default租户 ⇒ 角色下拉含「普通用户」', optsBiz.includes(ROLE_USER), `options=${JSON.stringify(optsBiz)}`)
    check(
      'B2 建号弹窗选非default租户 ⇒ 角色下拉**不含**「超级管理员」',
      NEGATE ? hasSuperBiz : !hasSuperBiz,
      `options=${JSON.stringify(optsBiz)}${NEGATE ? '（自证模式：期望已取反）' : ''}`,
    )
    const hintTxt = await js(`window.__probe.hint()`)
    check('B3 建号弹窗选非default租户 ⇒ 出现收敛提示文案', hintTxt.includes('default'), `hint="${hintTxt}"`)

    // 切回 default ⇒ 同一控件应当**出现**「超级管理员」（随租户变化，不是恒定缺项）
    await js(`window.__probe.setCreateSelect('租户', ${JSON.stringify(TENANT_DEFAULT)})`)
    await sleep(700)
    const roleDefault = await js(`window.__probe.createSelect('角色')`)
    if (!roleDefault) {
      gauge('B4 对照组：建号弹窗选 default 租户 ⇒ 角色下拉含「超级管理员」', `角色下拉未找到 ⇒ 未评估`)
    } else {
      const optsDef = roleDefault.options || []
      const hasSuperDef = optsDef.includes(ROLE_SUPER)
      check(
        'B4 对照组：建号弹窗选 default 租户 ⇒ 角色下拉**含**「超级管理员」',
        NEGATE ? !hasSuperDef : hasSuperDef,
        `options=${JSON.stringify(optsDef)}${NEGATE ? '（自证模式：期望已取反）' : ''}`,
      )
    }
  }
  await js(`window.__probe.closeModal()`)
}

// ── C 组：变更角色**真能用**（改掉 ⇒ 徽章真变 ⇒ 再改回；可逆）────────────
// ★ 这组是**唯一会写生产数据**的一组 ⇒ 对象选 chenb（业务租户、role=tenant_admin），
//   且默认租户不受影响 ⇒ 结束时必然还原成 tenant_admin。
// ★ 读数取自**表格里的角色徽章**（第 4 列）⇒ 断言的是「用户真正看到的东西」，
//   不是接口返回 ⇒ 比只看 PUT 响应强一档。
if (!rowBiz?.found) {
  gauge('C1/C2 变更角色真能生效并可还原', `线上没有名为 ${USER_BUSINESS} 的账号 ⇒ 未评估`)
} else {
  const before = await js(`window.__probe.rowRole(${JSON.stringify(USER_BUSINESS)})`)
  const roleBefore = before?.role || ''
  const opened1 = await js(`window.__probe.openRoleModal(${JSON.stringify(USER_BUSINESS)})`)
  await sleep(800)
  const set1 = await js(`window.__probe.setRoleSelect(${JSON.stringify(ROLE_USER)})`)
  await sleep(400)
  const saved1 = await js(`window.__probe.clickRoleSave()`)
  // 等列表刷新（PUT 返回后前端重拉 users）
  let roleAfter = ''
  for (let i = 0; i < 30; i += 1) {
    await sleep(400)
    const r = await js(`window.__probe.rowRole(${JSON.stringify(USER_BUSINESS)})`)
    roleAfter = r?.role || ''
    if (roleAfter === ROLE_USER) break
  }
  check(
    'C1 变更角色真能生效：普通用户',
    !NEGATE ? roleAfter === ROLE_USER : roleAfter !== ROLE_USER,
    `改前=${roleBefore} 改后=${roleAfter}（select=${JSON.stringify(set1)} save=${JSON.stringify(saved1)}）${NEGATE ? '（自证模式：期望已取反）' : ''}`,
  )

  // ── 还原：改回租户管理员（★ 不还就是给线上留了脏数据）────────────────
  const opened2 = await js(`window.__probe.openRoleModal(${JSON.stringify(USER_BUSINESS)})`)
  await sleep(800)
  await js(`window.__probe.setRoleSelect(${JSON.stringify(ROLE_TENANT)})`)
  await sleep(400)
  await js(`window.__probe.clickRoleSave()`)
  let roleRestored = ''
  for (let i = 0; i < 30; i += 1) {
    await sleep(400)
    const r = await js(`window.__probe.rowRole(${JSON.stringify(USER_BUSINESS)})`)
    roleRestored = r?.role || ''
    if (roleRestored === ROLE_TENANT) break
  }
  check(
    'C2 变更角色可还原（改回租户管理员）',
    !NEGATE ? roleRestored === ROLE_TENANT : roleRestored !== ROLE_TENANT,
    `还原后=${roleRestored}${NEGATE ? '（自证模式：期望已取反）' : ''}`,
  )
}

// ── 判定 ──────────────────────────────────────────────────────────────────
const out = summarize(checks, { skipped })
console.log(`\n${out.line}`)
process.exit(out.exitCode)
