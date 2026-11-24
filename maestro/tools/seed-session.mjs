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
//     [--mode simple|expert] [--reuse-tab]
//
// ★ `--reuse-tab`：**不新建页**，而是接管**已有的第一个 page target**
//   （注入完**不关它**）⇒ 用来给「不是我们起的浏览器」注入会话。
//   ★ 为什么需要它（2026-11-25 实测）：Maestro 的 **web 驱动自起 Chrome**
//     （Selenium 的 `ChromeDriver` + 临时 profile `org.chromium.Chromium.scoped_dir.*`），
//     **`--driver-host-port` 对 web 完全无效**（那是给 Android adb forward 用的）
//     ⇒ runner 自己起的 Chrome **根本不是 flow 跑的那个浏览器**
//     ⇒ 往它注入等于注入了一个没人用的浏览器。
//   ⇒ 而 ChromeDriver 起的 Chrome 会把 DevTools 端口写进
//     `<profile>/DevToolsActivePort` ⇒ 找到那个端口就能用 CDP 接管它。
//
// ★ 安全约束：JWT 只从 --token-file 读（0600 的临时文件），**不打印**；
//   调用方负责在用完后删掉那个文件。

import { readFileSync } from 'node:fs'
import { connect, evaluate, openPage } from './cdp-client.mjs'

function parseArgs(argv) {
  const out = { cdp: 'http://127.0.0.1:9222', url: '', tokenFile: '', mode: 'simple', reuseTab: false }
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (a === '--cdp') out.cdp = argv[++i]
    else if (a === '--url') out.url = argv[++i]
    else if (a === '--token-file') out.tokenFile = argv[++i]
    else if (a === '--mode') out.mode = argv[++i]
    else if (a === '--reuse-tab') out.reuseTab = true
  }
  return out
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

/** 接管**已有的第一个 page target**（`--reuse-tab` 用）。 */
async function attachExistingPage(cdp) {
  const { targetInfos } = await cdp.send('Target.getTargets')
  const page = (targetInfos || []).find((t) => t.type === 'page')
  if (!page) throw new Error('这个浏览器里没有可接管的 page target')
  const { sessionId } = await cdp.send('Target.attachToTarget', {
    targetId: page.targetId,
    flatten: true,
  })
  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Network.enable', {}, sessionId)
  return { targetId: page.targetId, sessionId }
}

async function main() {
  // ★ 退出看门狗（2026-11-25 实测踩到）：**WebSocket 的 close 握手可能不返回**
  //   （Chrome 侧不回 close 帧 ⇒ Node 的事件循环被 socket 吊住 ⇒ 进程不退出
  //   ⇒ 上游 runner 被卡死在这里，只能人工杀）。
  //   ⇒ ① 正常路径**显式 exit**（不等 close 握手）；② unref 的看门狗兜底。
  //   ⇒ unref 的语义：「不因为这个定时器而让事件循环活着」⇒ 空闲时不会拖住进程，
  //     但进程被别的原因吊住时它**仍会**触发 ⇒ 正好是我们要的兜底。
  const watchdog = setTimeout(() => {
    console.error('seed-session: 看门狗超时（30s）⇒ 强制退出')
    process.exit(2)
  }, 30000)
  watchdog.unref()

  const args = parseArgs(process.argv.slice(2))
  if (!args.url || !args.tokenFile) {
    console.error('usage: seed-session.mjs --url <base> --token-file <path> [--cdp ...] [--mode ...] [--reuse-tab]')
    process.exit(2)
  }
  const token = readFileSync(args.tokenFile, 'utf8').trim()
  if (!token) {
    console.error('token file is empty')
    process.exit(2)
  }

  const { cdp, ws } = await connect(args.cdp)
  // ★ --reuse-tab ⇒ 接管已有的页（**不关它**：那是别人的浏览器）
  const { targetId, sessionId } = args.reuseTab
    ? await attachExistingPage(cdp)
    : await openPage(cdp, 'about:blank')

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
  await cdp.send('Page.navigate', { url: `${args.url}/` }, sessionId)
  await loaded.catch(() => {})

  const value = await evaluate(
    cdp,
    sessionId,
    `(() => {
      localStorage.setItem('llmgw_work_mode', ${JSON.stringify(args.mode)});
      localStorage.setItem('llmgw_user_info', ${JSON.stringify(JSON.stringify(USER_INFO))});
      return { mode: localStorage.getItem('llmgw_work_mode'), user: localStorage.getItem('llmgw_user_info') };
    })()`,
  )
  if (!value || !value.user) throw new Error('localStorage seeding failed')

  // 探测一次 /api/auth/me：确认注入的 cookie 真被后端认（而不是只写进了浏览器）。
  // ★ 只回报状态码与角色，**不回显响应体** —— /api/auth/me 的响应里带着完整
  //   access_token，落进终端日志就等于把它抄了一份出去。
  const probeValue = await evaluate(
    cdp,
    sessionId,
    `fetch('/api/auth/me', {credentials:'same-origin'})
       .then(async r => {
         let role = '';
         try { const j = JSON.parse(await r.text()); role = (j.user && j.user.role) || j.role || ''; } catch (e) { role = ''; }
         return { status: r.status, role };
       })`,
  )

  // ★ --reuse-tab 时**不关这个页**：它是别人的浏览器（Maestro 正在用它）。
  if (!args.reuseTab) {
    await cdp.send('Target.closeTarget', { targetId })
  }

  console.log(JSON.stringify({ seeded: true, mode: value.mode, reusedTab: args.reuseTab, authMe: probeValue }, null, 2))
  ws.close()
  // ★ 显式退出：不等 WebSocket 的 close 握手（它可能永远不返回 ⇒ 进程吊死）。
  process.exit(0)
}

main().catch((err) => {
  console.error(`seed-session failed: ${err.message}`)
  process.exit(1)
})
