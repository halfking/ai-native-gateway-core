// serve-current-build.mjs —— 把**当前 main 的构建**挂到本地，同时把 /api 代理到线上取真实数据。
//
// 为什么需要它（一次真实的假阴性）：
//   2026-10-07 全路由审计跑线上时，**144/165 组**判成「无内容」。查下去那 144 组的
//   正文一字不差都是 `页面不存在 地址可能已变更…回总览` —— 是 404 页。
//   根因不是页面坏：**线上构建 e27a8fc2 只有 8 条路由，当前 main 有 62 条**，
//   新增的 55 条全部打到 catch-all。
//   ⇒ 「线上跑一遍」审计的是**部署版本**，不是我要验的代码。本脚本补这个缺口。
//
// 它不做任何改写：静态资源取自 dist/，API 原样转发到线上。只换 origin，不换数据。
//
// 用法：node scripts/serve-current-build.mjs [--port 8791] [--api https://llmgateway.internal.example.com]
import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { join, extname, resolve } from 'node:path'

const argv = process.argv.slice(2)
const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 && argv[i + 1] ? argv[i + 1] : d }
const PORT = Number(arg('port', '8791'))
const API = arg('api', 'https://llmgateway.internal.example.com')
const DIST = resolve(arg('dist', 'dist'))

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml',
  '.png': 'image/png', '.jpg': 'image/jpeg', '.webp': 'image/webp', '.woff2': 'font/woff2', '.ico': 'image/x-icon',
}

const send = (res, code, buf, type, extra = {}) => {
  res.writeHead(code, { 'Content-Type': type, 'Cache-Control': 'no-store', ...extra })
  res.end(buf)
}

/** /api/* 原样转发到线上；cookie 双向透传，Set-Cookie 的 Domain 改成本地（否则浏览器不收）。 */
async function proxy(req, res) {
  const target = API + req.url
  const chunks = []
  for await (const c of req) chunks.push(c)
  const body = Buffer.concat(chunks)
  const headers = { ...req.headers, host: new URL(API).host }
  delete headers['accept-encoding'] // 避免拿到 gzip 却按原文写回
  const r = await fetch(target, { method: req.method, headers, body: body.length ? body : undefined, redirect: 'manual' })
  const out = Buffer.from(await r.arrayBuffer())
  const h = { 'Content-Type': r.headers.get('content-type') || 'application/json', 'Cache-Control': 'no-store' }
  for (const sc of r.headers.getSetCookie?.() ?? []) {
    // 把 Domain=llmgateway.internal.example.com 去掉，改成 host-only，才能落在 127.0.0.1 上
    h['Set-Cookie'] = sc.replace(/;\s*Domain=[^;]+/i, '')
  }
  res.writeHead(r.status, h)
  res.end(out)
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1')
  const p = url.pathname

  if (p.startsWith('/api/')) {
    try { await proxy(req, res) } catch (e) { send(res, 502, String(e), 'text/plain') }
    return
  }
  // 构建产物：/m-assets/* → dist/assets/*
  if (p.startsWith('/m-assets/')) {
    const f = join(DIST, p.replace('/m-assets/', ''))
    try {
      const b = await readFile(f)
      return send(res, 200, b, MIME[extname(f)] ?? 'application/octet-stream')
    } catch { return send(res, 404, 'not found', 'text/plain') }
  }
  // SPA：/m 与 /m/** 一律回 index.html（前端路由自己解析路径）
  if (p === '/m' || p.startsWith('/m/')) {
    const rel = p.replace(/^\/m\/?/, '')
    if (rel && !rel.includes('/')) {
      const f = join(DIST, rel)
      try { if ((await stat(f)).isFile()) return send(res, 200, await readFile(f), MIME[extname(f)] ?? 'text/plain') } catch {}
    }
    try { return send(res, 200, await readFile(join(DIST, 'index.html')), MIME['.html']) } catch { return send(res, 500, 'no dist/index.html', 'text/plain') }
  }
  send(res, 404, 'not found', 'text/plain')
})

server.listen(PORT, '127.0.0.1', () => {
  console.error(`[serve] dist=${DIST}`)
  console.error(`[serve] http://127.0.0.1:${PORT}/m/  ← 当前构建`)
  console.error(`[serve] /api/* → ${API}  ← 线上真实数据`)
})