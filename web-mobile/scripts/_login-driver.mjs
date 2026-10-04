/**
 * _login-driver.mjs —— 驱动移动端登录表单的**唯一实现**。
 *
 * ⚠️ 为什么必须单源：模拟器取证（mobile-audit.mjs）与 Chrome 分诊
 *   （chrome-triage.mjs）都要登同一个表单。两份实现历史上已经分叉过一次 ——
 *   Chrome 那份用合成 `el.value = x` + dispatchEvent('input')，结果登录按钮
 *   始终 disabled、请求根本没发出，一度看起来像「产品不允许登录」的产品缺陷。
 *   换成 CDP `Input.insertText`（真实输入管线）后按钮正常可用。
 *   ⇒ 「按钮 disabled」这种结论对**驱动方式**极度敏感，两份实现必然有一份在骗人。
 *
 * 判据：账号密码都填进去之后按钮仍 disabled ⇒ 判为**产品缺陷**（不是驱动问题）。
 */
import { setTimeout as sleep } from 'node:timers/promises'

/**
 * @param {object} cdp        最小 CDP 客户端（send(method, params)）
 * @param {object} opts
 * @param {string} opts.origin  例如 https://llmgateway.internal.example.com
 * @param {string} opts.base    例如 /m
 * @param {string} opts.user
 * @param {string} opts.pass
 * @param {number} [opts.settleMs] 导航后等待
 * @param {(url:string, ms:number)=>Promise<void>} opts.nav
 * @param {(expr:string, opts?:object)=>Promise<any>} opts.evalJs
 * @returns {Promise<{ok:boolean, route:string, why?:string}>}
 */
export async function driveLogin(cdp, opts) {
  const { origin, base, user, pass, settleMs = 3400, nav, evalJs } = opts

  await nav(`${origin}${base}/login`, settleMs)

  // 已经登录过：访问 /login 会被重定向回内容页，此时**没有输入框**是正确行为，
  // 不是失败。实测：设备上已有有效 cookie 时 /m/login → /m/。
  const at0 = String(await evalJs('location.pathname', true) ?? '')
  const inputs0 = Number(await evalJs(`document.querySelectorAll('input').length`, true) ?? 0)
  if (!at0.includes('/login') || inputs0 === 0) {
    console.log(`  已登录（/login 重定向到 ${at0}，无输入框）—— 跳过登录步骤`)
    return { ok: true, route: at0, alreadyAuthed: true }
  }

  const typeInto = async (idx, value) => {
    const ok = await evalJs(`(() => { const el = document.querySelectorAll('input')[${idx}]; if(!el) return false; el.focus(); return true })()`, true)
    if (!ok) throw new Error(`找不到第 ${idx} 个输入框`)
    await cdp.send('Input.insertText', { text: value })
    await sleep(450)
  }
  await typeInto(0, user)
  await typeInto(1, pass)

  const st = await evalJs(`(() => {
    const ins = Array.from(document.querySelectorAll('input'))
    const btn = document.querySelector('button[type=submit]')
      || Array.from(document.querySelectorAll('button')).find(b => /登录|Sign in|Login/i.test(b.textContent))
    return {
      values: ins.map(i => i.type === 'password' ? '*'.repeat(i.value.length) : i.value),
      btnDisabled: btn ? btn.disabled : null,
      btnText: btn ? btn.innerText.trim() : null,
    }
  })()`, true)
  console.log('  填值后按钮状态: ' + JSON.stringify(st))
  if (st.btnDisabled === null) return { ok: false, route: '', why: '找不到登录按钮' }
  if (st.btnDisabled) {
    return { ok: false, route: '', why: '账号密码都已填入，登录按钮仍 disabled —— 判为产品缺陷（不是驱动问题）' }
  }

  await evalJs(`(() => { const b = document.querySelector('button[type=submit]')
    || Array.from(document.querySelectorAll('button')).find(x => /登录|Sign in|Login/i.test(x.textContent));
    if (b) b.click(); return !!b })()`)
  await sleep(4500)

  const at = await evalJs('location.pathname', true)
  const ok = !String(at).includes('/login')
  if (!ok) {
    const hint = await evalJs(`((document.querySelector('.login__error,[class*=error]')||{}).innerText||'(无错误提示)')`, true)
    return { ok: false, route: String(at), why: '页面提示: ' + hint }
  }
  return { ok: true, route: String(at) }
}
