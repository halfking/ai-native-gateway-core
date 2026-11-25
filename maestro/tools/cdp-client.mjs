// maestro/tools/cdp-client.mjs —— Maestro web 驱动之外的浏览器自动化公共件。
//
// ★ 为什么需要它（2026-11-17 实测）：
//   Maestro 2.11.0 的 **web 驱动不支持 evalScript** —— `CdpWebDriver` 根本没有
//   `evaluateScript` 方法（javap 实测），命令被吞掉报 COMPLETED 但什么都不做；
//   而 `${output.x = ...}` 只在 JVM 侧求值，拿不到 DOM。
//   ⇒ 想在**页面内**做断言（同一页面、同一时刻、与页面自己的请求逐字对拍），
//     就得走 Chrome DevTools Protocol 直接驱动浏览器。
//
// ★ 本模块零依赖：Node ≥ 22 自带 WebSocket + fetch。
//
// 用法：
//   import { connect } from './cdp-client.mjs'
//   const { cdp, ws } = await connect('http://127.0.0.1:9222')

/** CDP 命令客户端：send() 发命令，waitFor() 等事件。 */
export class CDP {
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

/** 连上某个 CDP 端点（浏览器级 WebSocket）。 */
export async function connect(cdpUrl = 'http://127.0.0.1:9222') {
  const version = await (await fetch(`${cdpUrl}/json/version`)).json()
  const ws = new WebSocket(version.webSocketDebuggerUrl)
  await new Promise((resolve, reject) => {
    ws.addEventListener('open', resolve, { once: true })
    ws.addEventListener('error', () => reject(new Error('CDP websocket connect failed')), { once: true })
  })
  return { cdp: new CDP(ws), ws, version }
}

/**
 * 开一个新页并 attach（flatten 模式），返回可用的 sessionId。
 * ★ Network / Storage 域只在 **page session** 可用（浏览器级 WS 上调
 *   'Network.enable' 会报 wasn't found）⇒ 必须先 attach 一个 page target。
 */
export async function openPage(cdp, url = 'about:blank') {
  const { targetId } = await cdp.send('Target.createTarget', { url })
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Network.enable', {}, sessionId)
  return { targetId, sessionId }
}

/** 在页面里同步求值（awaitPromise=true，支持 async IIFE）。 */
export async function evaluate(cdp, sessionId, expression) {
  const res = await cdp.send(
    'Runtime.evaluate',
    { expression, awaitPromise: true, returnByValue: true },
    sessionId,
  )
  if (res?.exceptionDetails) {
    const d = res.exceptionDetails
    throw new Error(`页面内求值异常：${d.exception?.description || d.text}`)
  }
  return res?.result?.value
}

/** 轮询直到表达式为真（或超时）。 */
export async function waitFor(cdp, sessionId, expression, timeoutMs = 30000, intervalMs = 500) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    let value = null
    try {
      value = await evaluate(cdp, sessionId, expression)
    } catch {
      value = null
    }
    if (value) return value
    if (Date.now() > deadline) throw new Error(`等待超时：${expression}`)
    await new Promise((r) => setTimeout(r, intervalMs))
  }
}


/** Navigate to a new same-origin document and wait until its DOM can be queried. */
export async function navigateToDocument(cdp, sessionId, url, timeoutMs = 30000) {
  const previousDocument = await evaluate(cdp, sessionId, 'performance.timeOrigin')
  const { errorText } = await cdp.send('Page.navigate', { url }, sessionId)
  if (errorText) throw new Error(`CDP navigation failed: ${errorText}`)
  const origin = JSON.stringify(new URL(url).origin)
  return waitFor(cdp, sessionId,
    `location.origin === ${origin} && performance.timeOrigin !== ${previousDocument} && document.readyState !== 'loading'`,
    timeoutMs)
}
