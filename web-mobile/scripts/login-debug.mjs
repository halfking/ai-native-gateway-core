#!/usr/bin/env node
/** login-debug.mjs — 只做一件事：把登录这一步拆开，看请求/响应/落地状态。 */
import { spawn } from 'node:child_process'
import { setTimeout as sleep } from 'node:timers/promises'
import os from 'node:os'; import path from 'node:path'; import { rmSync } from 'node:fs'

const PORT = 9445
const PROFILE = path.join(os.tmpdir(), 'llmgw-login-debug')
rmSync(PROFILE, { recursive: true, force: true })
const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

class CDP {
  constructor(ws){this.ws=ws;this.id=0;this.p=new Map();this.h=[]}
  static async connect(u){const ws=new WebSocket(u);await new Promise((r,j)=>{ws.addEventListener('open',r,{once:true});ws.addEventListener('error',()=>j(new Error('ws')),{once:true})})
    const c=new CDP(ws);ws.addEventListener('message',e=>{const m=JSON.parse(e.data)
      if(m.id&&c.p.has(m.id)){const{res,rej}=c.p.get(m.id);c.p.delete(m.id);m.error?rej(new Error(m.error.message)):res(m.result)}
      else if(m.method)c.h.forEach(f=>f(m))});return c}
  send(method,params={}){const id=++this.id;return new Promise((res,rej)=>{this.p.set(id,{res,rej});this.ws.send(JSON.stringify({id,method,params}))
    setTimeout(()=>{if(this.p.has(id)){this.p.delete(id);rej(new Error(method+' timeout'))}},30000)})}
  on(f){this.h.push(f)} close(){try{this.ws.close()}catch{}}
}

const chrome = spawn(CHROME, ['--headless=new',`--remote-debugging-port=${PORT}`,`--user-data-dir=${PROFILE}`,
  '--no-first-run','--no-default-browser-check','about:blank'], { stdio: 'ignore' })
process.on('exit', () => { try { chrome.kill('SIGKILL') } catch {} })

let ver = null
for (let i=0;i<40;i++){ try{ ver = await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json(); break }catch{ await sleep(500) } }
if (!ver) { console.error('chrome 未就绪'); process.exit(2) }

const t = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json()
const cdp = await CDP.connect(t.webSocketDebuggerUrl)
await cdp.send('Page.enable'); await cdp.send('Runtime.enable'); await cdp.send('Network.enable')
await cdp.send('Emulation.setDeviceMetricsOverride', { width:393, height:852, deviceScaleFactor:3, mobile:true })

const netLog = []
cdp.on((m) => {
  if (m.method === 'Network.responseReceived') {
    const r = m.params.response
    if (r.url.includes('/api/')) netLog.push({ kind:'resp', status:r.status, url:r.url.replace(/^https:\/\/[^/]+/,'') })
  }
  if (m.method === 'Network.loadingFailed') netLog.push({ kind:'fail', err:m.params.errorText, url:(m.params.requestId||'') })
})
cdp.on((m) => {
  if (m.method === 'Runtime.exceptionThrown') console.log('  [JS异常]', m.params.exceptionDetails?.exception?.description?.slice(0,200))
  if (m.method === 'Runtime.consoleAPICalled') console.log('  [console.'+m.params.type+']', (m.params.args||[]).map(a=>a.value??a.description).join(' ').slice(0,200))
})

await cdp.send('Page.navigate', { url: 'https://llmgateway.internal.example.com/m/login' })
await sleep(4000)

console.log('\n=== 1. 登录页 DOM 里的输入框 ===')
const inputs = await cdp.send('Runtime.evaluate', { returnByValue: true, expression: `
  Array.from(document.querySelectorAll('input,button')).map(e => ({
    tag:e.tagName, type:e.type||'', name:e.name||'', id:e.id||'', ph:e.placeholder||'',
    txt:(e.innerText||'').trim().slice(0,12), aria:e.getAttribute('aria-label')||'', disabled:e.disabled
  }))` })
console.log(JSON.stringify(inputs.result.value, null, 1))

console.log('\n=== 2. 填值并提交 ===')
const r = await cdp.send('Runtime.evaluate', { returnByValue: true, expression: `(() => {
  const setVal = (el, v) => {
    const p = HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(p,'value').set.call(el, v)
    el.dispatchEvent(new Event('input',{bubbles:true})); el.dispatchEvent(new Event('change',{bubbles:true}))
  }
  const ins = Array.from(document.querySelectorAll('input'))
  const u = ins[0], p = ins[1]
  if(!u||!p) return {ok:false,why:'输入框不足',n:ins.length}
  setVal(u, 'admin'); setVal(p, '__REDACTED_ADMIN_PASSWORD__')
  const form = document.querySelector('form')
  const btn = document.querySelector('button[type=submit]') || Array.from(document.querySelectorAll('button')).find(b=>/登录/.test(b.textContent))
  if(!btn) return {ok:false,why:'无提交按钮'}
  const before = { u:u.value, p:'*'.repeat(p.value.length), btnText:(btn.innerText||'').trim(), btnDisabled:btn.disabled, form: !!form }
  btn.click()
  return {ok:true, before}
})()` })
console.log(JSON.stringify(r.result.value, null, 1))

await sleep(4000)
console.log('\n=== 3. 提交后的网络与落地状态 ===')
console.log('网络:', JSON.stringify(netLog, null, 1))
const st = await cdp.send('Runtime.evaluate', { returnByValue: true, expression: `({
  path: location.pathname + location.search,
  text: document.body.innerText.trim().slice(0,200),
  cookieVisibleToJS: document.cookie
})` })
console.log(JSON.stringify(st.result.value, null, 1))

console.log('\n=== 4. 直接手测 API（绕过页面）===')
const direct = await cdp.send('Runtime.evaluate', { returnByValue: true, awaitPromise: true, expression: `
  fetch('/api/auth/token',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({username:'admin',password:'__REDACTED_ADMIN_PASSWORD__'}),credentials:'same-origin'})
    .then(async r => ({ status:r.status, setCookie:r.headers.get('set-cookie') ? 'yes' : 'no', body:(await r.text()).slice(0,120) }))
    .catch(e => ({ err: String(e) }))` })
console.log(JSON.stringify(direct.result.value, null, 1))

await sleep(500)
console.log('\n=== 5. 登录后再取 /api/auth/me ===')
const me = await cdp.send('Runtime.evaluate', { returnByValue: true, awaitPromise: true, expression: `
  fetch('/api/auth/me',{credentials:'same-origin'}).then(async r => ({ status:r.status, body:(await r.text()).slice(0,160) }))` })
console.log(JSON.stringify(me.result.value, null, 1))

cdp.close(); try { chrome.kill('SIGKILL') } catch {}
process.exit(0)
