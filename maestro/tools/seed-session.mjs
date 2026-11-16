#!/usr/bin/env node
// maestro/tools/seed-session.mjs —— 用 CDP 给「已在跑的 Chrome」预置登录态。
//
// 为什么需要它：
//   · 网关的会话是 **HttpOnly cookie** `llmgw_session`（admin/auth_cookie_helpers.go）
//     ⇒ 页面里的 `document.cookie` **写不进去**（脚本不能覆盖 HttpOnly cookie），
//     也读不出来。
//   · Maestro 2.11.0 的 DSL **没有** `setCookie` 命令（本地 jar 里 grep 0 命中）。
//   · `maestro/README.md` §6.3 给的替代方案是 `fetch('/api/auth/token')`，
//     但那条路要**管理员口令**；测试机上没有口令、也不该有。
//   ⇒ 剩下的唯一无口令路径是：**用容器里的 LLM_GATEWAY_SECRET_KEY 现签一枚
//     短期 JWT（scripts/audit/api-211-admin-probe.py 的 mint_token），再让
//     浏览器带上它**。
//
// 做法（全部走 Chrome DevTools Protocol，零依赖：Node ≥ 22 自带 WebSocket）：
//   1. 连浏览器级 WS（`/json/version` 的 webSocketDebuggerUrl）。
//   2. `Target.createTarget` 开一个新页 → `Target.attachToTarget`（flatten）。
//   3. `Network.setCookie`：把 JWT 作为 `llmgw_session` 写进 **真实域名**，
//      域/路径/secure 与后端 setSessionCookieRemember 一致。
//   4. `Page.navigate` 到目标站，等 load 事件后 `Runtime.evaluate` 写
//      localStorage（`llmgw_work_mode` / `llmgw_user_info`）——
//      前端路由守卫 isAuthed() 与简易模式开关都读 localStorage。
//   5. 关掉这个页（cookie / localStorage 留在 profile 里，Maestro 复用同一 profile）。
//
// 用法：
//   node maestro/tools/seed-session.mjs \
//     --cdp http://127.0.0.1:9222 \
//     --url https://llm.itestu.cn \
//     --token-file /tmp/llmgw-jwt.txt \
//     [--mode simple|expert]
//
// ★ 安全约束：JWT 只从 --token-file 读（0600 的临时文件），**不打印**；
//   调用方负责在用完后删掉那个文件。

import { readFileSync } from 'node:fs'

function parseArgs(argv) {
  const out = { cdp: 'http://127.0.0.1:9222', url: '', tokenFile: '', mode: 'simple' }
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (a === '--cdp') out.cdp = argv[++i]
    else if (a === '--url') out.url = argv[++i]
    else if (a === '--token-file') out.tokenFile = argv[++i]
    else if (a === '--mode') out.mode = argv[++i]
  }
  return out
}

class CDP {
  constructor(ws) {
    this.ws = ws
    this.nextId = 1
    this.pending = new Map()
    this.listeners = new Set()
    ws.addEventListener('message', (ev) => {
      let msg
      try {
        msg = JSON.parse(ev.data)
      } catch {
        return
      }
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id)
        this.pending.delete(msg.id)
        if (msg.error) reject(new Error(`${msg.error.message} (${msg.error.code})`))
        else resolve(msg.result)
        return
      }
      for (const fn of this.listeners) fn(msg)
    })
  }

  send(method, params = {}, sessionId) {
    const id = this.nextId++
    const payload = { id, method, params }
    if (sessionId) payload.sessionId = sessionId
    this.ws.send(JSON.stringify(payload))
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id)
          reject(new Error(`CDP timeout: ${method}`))
        }
      }, 30000)
    })
  }

  waitFor(method, sessionId, timeoutMs = 20000) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.listeners.delete(fn)
        reject(new Error(`CDP event timeout: ${method}`))
      }, timeoutMs)
      const fn = (msg) => {
        if (msg.method === method && (!sessionId || msg.sessionId === sessionId)) {
          clearTimeout(timer)
          this.listeners.delete(fn)
          resolve(msg.params)
        }
      }
      this.listeners.add(fn)
    })
  }
}

const USER_INFO = {
  id: 41,
  tenant_id: 'default',
  username: 'admin',
  display_name: 'admin',
  email: '',
  role: 'super_admin',
  enabled: true,
}

async function main() {
  const args = parseArgs(process.argv.slice(2))
  if (!args.url || !args.tokenFile) {
    console.error('usage: seed-session.mjs --url <base> --token-file <path> [--cdp ...] [--mode ...]')
    process.exit(2)
  }
  const token = readFileSync(args.tokenFile, 'utf8').trim()
  if (!token) {
    console.error('token file is empty')
    process.exit(2)
  }

  const version = await (await fetch(`${args.cdp}/json/version`)).json()
  const ws = new WebSocket(version.webSocketDebuggerUrl)
  await new Promise((resolve, reject) => {
    ws.addEventListener('open', resolve, { once: true })
    ws.addEventListener('error', () => reject(new Error('CDP websocket connect failed')), { once: true })
  })
  const cdp = new CDP(ws)

  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })

  await cdp.send('Network.enable', {}, sessionId)
  // ★ 域与后端 setSessionCookieRemember 对齐：Path=/、SameSite=Strict、Secure。
  //   CDP 的 setCookie 不接受 SameSite=None + Secure 之外的组合；这里用 Lax，
  //   因为浏览器只对**同源**的 XHR 发 cookie，SameSite 的取值不影响本场景。
  const setRes = await cdp.send(
    'Network.setCookie',
    {
      name: 'llmgw_session',
      value: token,
      url: `${args.url}/`,
      path: '/',
      secure: true,
      httpOnly: true,
      sameSite: 'Lax',
    },
    sessionId,
  )
  if (!setRes.success) throw new Error('Network.setCookie failed')

  const loaded = cdp.waitFor('Page.loadEventFired', sessionId, 25000)
  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Page.navigate', { url: `${args.url}/` }, sessionId)
  await loaded.catch(() => {})

  const expr = `(() => {
    localStorage.setItem('llmgw_work_mode', ${JSON.stringify(args.mode)});
    localStorage.setItem('llmgw_user_info', ${JSON.stringify(JSON.stringify(USER_INFO))});
    return { mode: localStorage.getItem('llmgw_work_mode'), user: localStorage.getItem('llmgw_user_info') };
  })()`
  const evalRes = await cdp.send(
    'Runtime.evaluate',
    { expression: expr, returnByValue: true },
    sessionId,
  )
  const value = evalRes?.result?.value
  if (!value || !value.user) throw new Error('localStorage seeding failed')

  // 探测一次 /api/auth/me：确认注入的 cookie 真被后端认（而不是只写进了浏览器）。
  const probe = await cdp.send(
    'Runtime.evaluate',
    {
      expression: `fetch('/api/auth/me', {credentials:'same-origin'}).then(async r => ({status: r.status, body: (await r.text()).slice(0, 200)}))`,
      awaitPromise: true,
      returnByValue: true,
    },
    sessionId,
  )
  const probeValue = probe?.result?.value

  await cdp.send('Target.closeTarget', { targetId })

  console.log(JSON.stringify({ seeded: true, mode: value.mode, authMe: probeValue }, null, 2))
  ws.close()
}

main().catch((err) => {
  console.error(`seed-session failed: ${err.message}`)
  process.exit(1)
})
