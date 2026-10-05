/** nodes-timing-probe.mjs — /nodes 到底会停在骨架屏，还是 15s 后自己进错误态。
 *  为什么要专门测这个：服务端 500 要 15s 才回（credential_monitor.go:657 查询超时），
 *  而早先那张截图是在 ~4s 抓的 —— 那一刻必然还在骨架屏。
 *  ⇒ 「骨架屏」和「永久骨架屏」是两个完全不同的结论，差一个 15s 观察窗。 */
import { spawn } from 'node:child_process'
import { setTimeout as sleep } from 'node:timers/promises'
import os from 'node:os'; import path from 'node:path'; import { rmSync } from 'node:fs'
const PORT = 9446
const PROFILE = path.join(os.tmpdir(), 'llmgw-nodes-probe'); rmSync(PROFILE, {recursive:true, force:true})
const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const chrome = spawn(CHROME, ['--headless=new',`--remote-debugging-port=${PORT}`,`--user-data-dir=${PROFILE}`,'--no-first-run','--no-default-browser-check','about:blank'], {stdio:'ignore'})
process.on('exit', ()=>{try{chrome.kill('SIGKILL')}catch{}})
class CDP{constructor(w){this.w=w;this.i=0;this.p=new Map()}static async connect(u){const w=new WebSocket(u);await new Promise((r,j)=>{w.addEventListener('open',r,{once:true});w.addEventListener('error',()=>j(new Error('ws')),{once:true})});const c=new CDP(w);w.addEventListener('message',e=>{const m=JSON.parse(e.data);if(m.id&&c.p.has(m.id)){c.p.get(m.id)(m);c.p.delete(m.id)}});return c}
 send(me,pa={}){const id=++this.i;return new Promise(r=>{this.p.set(id,r);this.w.send(JSON.stringify({id,method:me,params:pa}))})}}
let ver=null; for(let i=0;i<40;i++){try{ver=await(await fetch(`http://127.0.0.1:${PORT}/json/version`)).json();break}catch{await sleep(500)}}
const t=await(await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`,{method:'PUT'})).json()
const cdp=await CDP.connect(t.webSocketDebuggerUrl)
await cdp.send('Page.enable'); await cdp.send('Runtime.enable')
await cdp.send('Emulation.setDeviceMetricsOverride',{width:393,height:852,deviceScaleFactor:3,mobile:true})
const nav=async(u,w=3000)=>{await cdp.send('Page.navigate',{url:u});await sleep(w)}
await nav('https://llmgateway.internal.example.com/m/login',3500)
for (const [i,v] of [[0,'admin'],[1,'__REDACTED_ADMIN_PASSWORD__']]) {
  await cdp.send('Runtime.evaluate',{expression:`document.querySelectorAll('input')[${i}].focus()`})
  await cdp.send('Input.insertText',{text:v}); await sleep(350)
}
await cdp.send('Runtime.evaluate',{expression:`document.querySelector('button[type=submit]').click()`})
await sleep(4000)
console.log('登录后:', (await cdp.send('Runtime.evaluate',{returnByValue:true,expression:'location.pathname'})).result.result.value)
console.log('\n=== /nodes 逐秒观察（观察窗 30s，服务端 500 需 15s）===')
const t0=Date.now()
await cdp.send('Page.navigate',{url:'https://llmgateway.internal.example.com/m/nodes'})
for (let i=0;i<10;i++) {
  const r = await cdp.send('Runtime.evaluate',{returnByValue:true,expression:`(() => {
    const sk = document.querySelectorAll('[class*=skeleton],[class*=Skeleton]').length
    const stateView = document.querySelector('.app-state,[class*=state-view]')
    return {
      skeletons: sk,
      stateText: stateView ? stateView.innerText.replace(/\\n/g,' | ').slice(0,90) : null,
      rows: document.querySelectorAll('.hyper-list__row').length,
      bodyLen: document.body.innerText.trim().length
    }
  })()`})
  const v = r.result.result.value
  const el = ((Date.now()-t0)/1000).toFixed(1)
  console.log(`  t+${el.padStart(5)}s  skeletons=${String(v.skeletons).padStart(2)}  rows=${String(v.rows).padStart(2)}  chars=${String(v.bodyLen).padStart(4)}  state=${v.stateText ?? '-'}`)
  if (v.rows>0 || (v.stateText && !/loading|加载/i.test(v.stateText))) { console.log(`  → 终态出现在 t+${el}s`); break }
  await sleep(3000)
}
cdp.send?.('Browser.close'); try{chrome.kill('SIGKILL')}catch{}
process.exit(0)
