#!/usr/bin/env node
/**
 * maestro/tools/resolve-ids.mjs —— 从**活着的网关**解析 Maestro 动态路由需要的真实 ID。
 *
 * 为什么需要它：maestro/flows/*.yaml 里有 16 处动态路由参数（`${PROVIDER_ID}`、
 * `${TENANT_CODE}`、`${REQUEST_ID}`…）。Maestro 的 `${}` 只做字符串替换，不会去
 * 问后端 ⇒ 不喂真值就会退化成 `/providers/`、`?tenant=` 这种**打不开的 URL**，
 * 断言必然红，而红的原因与被测页面无关。
 *
 * 口径：每个变量都尽量取**活系统里真实存在**的那一条；确实没有数据的
 * （本机没有审批、没有订单）会给一个**格式合法但不存在**的哨兵值，
 * 并在 stderr 上标明 —— 这类页面的断言必须降级为 optional（见 flows 里的注释）。
 *
 * 用法：
 *   node maestro/tools/resolve-ids.mjs                # 输出 KEY=VALUE 行
 *   node maestro/tools/resolve-ids.mjs --json         # 输出 JSON
 *   BASE_URL=... node maestro/tools/resolve-ids.mjs   # 换网关
 *
 * 凭据：只从**仓根 .env.local** 读（LLM_GATEWAY_ADMIN_USER / _PASSWORD）。
 * 口令不出现在 argv、不落盘、不打印。
 */
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(HERE, '..', '..')
const BASE_URL = (process.env.BASE_URL || 'http://127.0.0.1:8782').replace(/\/$/, '')
const AS_JSON = process.argv.includes('--json')

function readEnvFile(path) {
  const out = {}
  let text
  try {
    text = readFileSync(path, 'utf8')
  } catch {
    return out
  }
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const eq = line.indexOf('=')
    if (eq < 0) continue
    const k = line.slice(0, eq).trim()
    let v = line.slice(eq + 1).trim()
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) v = v.slice(1, -1)
    out[k] = v
  }
  return out
}

const envFile = process.env.LLMGW_ENV_FILE || resolve(REPO_ROOT, '.env.local')
const env = readEnvFile(envFile)
const USER = process.env.LLM_GATEWAY_ADMIN_USER || env.LLM_GATEWAY_ADMIN_USER
const PASS = process.env.LLM_GATEWAY_ADMIN_PASSWORD || env.LLM_GATEWAY_ADMIN_PASSWORD
if (!USER || !PASS) {
  console.error(`resolve-ids: 仓根 ${envFile} 里没有 LLM_GATEWAY_ADMIN_USER / _PASSWORD`)
  process.exit(2)
}

async function api(path, { method = 'GET', token = '', body = null } = {}) {
  const headers = { Accept: 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== null) headers['Content-Type'] = 'application/json'
  const res = await fetch(BASE_URL + path, {
    method,
    headers,
    body: body === null ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  let json = null
  try {
    json = JSON.parse(text)
  } catch {
    /* 非 JSON 响应（404 页面等）→ json 保持 null */
  }
  return { status: res.status, json, text }
}

const login = await api('/api/auth/token', { method: 'POST', body: { username: USER, password: PASS } })
if (login.status !== 200) {
  console.error(`resolve-ids: 登录失败 HTTP ${login.status} ${login.text.slice(0, 160)}`)
  process.exit(2)
}
const token = login.json?.access_token || ''
if (!token) {
  console.error('resolve-ids: 登录响应里没有 access_token')
  process.exit(2)
}

const missing = []
const values = {}

async function take(name, path, pick, fallback = null) {
  const r = await api(path, { token })
  if (r.status !== 200 || r.json === null) {
    missing.push(`${name}  ← ${path} HTTP ${r.status}`)
    if (fallback !== null) values[name] = fallback
    return
  }
  const v = pick(r.json)
  if (v === undefined || v === null || v === '') {
    missing.push(`${name}  ← ${path} 没有可用数据`)
    if (fallback !== null) values[name] = fallback
    return
  }
  values[name] = String(v)
}

// ── 租户 ──────────────────────────────────────────────────────────────────
await take('TENANT_CODE', '/api/admin/tenants', (j) => j?.[0]?.code, 'default')
values.TENANT_ID = values.TENANT_CODE // 路由参数就是租户 code（TenantDetailView.vue:33）

// ── 供应商 / 密钥 ─────────────────────────────────────────────────────────
await take('PROVIDER_ID', '/api/providers', (j) => j?.[0]?.id)
await take('API_KEY_ID', '/api/keys', (j) => j?.[0]?.id)

// ── 请求日志 ──────────────────────────────────────────────────────────────
await take('REQUEST_ID', '/api/logs?limit=5', (j) => j?.items?.[0]?.request_id)

// ── 会话族 ────────────────────────────────────────────────────────────────
const sess = await api('/api/admin/sessions?limit=5', { token })
const firstSession = sess.status === 200 ? sess.json?.sessions?.[0] : null
if (firstSession) {
  values.SESSION_ID = firstSession.session_id
  if (firstSession.current_credential_id) values.CREDENTIAL_ID = firstSession.current_credential_id
  if (firstSession.api_key_id) values.API_KEY_ID = firstSession.api_key_id
} else {
  missing.push('SESSION_ID  ← /api/admin/sessions 没有会话')
}

// ── 会话画像三个维度 ──────────────────────────────────────────────────────
await take('CLIENT_ID', '/api/admin/session-analytics/clients', (j) => j?.clients?.[0]?.client_id)
await take('TASK_ID', '/api/admin/session-analytics/tasks', (j) => j?.tasks?.[0]?.task_id)
// 「用户」维度：本机可能为空；会话里的 end_user/owner 字段是兜底来源
// ★ 必须给哨兵值：Maestro 的 ${VAR} 找不到变量时**渲染成字面量 "undefined"**
//   ⇒ URL 会变成 /admin/session-analytics/users/undefined（实测 R176）。
//   ⇒ 兜底成 'unknown'，页面标题就是「用户画像详情: unknown」，语义自洽。
await take('SESSION_OWNER', '/api/admin/session-analytics/users', (j) => j?.users?.[0]?.user_id ?? j?.users?.[0]?.owner, 'unknown')

// ── 路由工作类型 ──────────────────────────────────────────────────────────
await take('WORK_TYPE_KEY', '/api/admin/work-types', (j) => j?.[0]?.key)

// ── 探活详情页的 ?model= 参数 ─────────────────────────────────────────────
// /probe-health/detail 的所有数据（节点/路由/决策/监控/探活/价格）都按单个模型组织
// ⇒ 不带 ?model= 时页面只渲染「未指定模型」空态（实测 2026-10-28 R176）。
// 取值口径 = 探活看板里的 raw_model_name（ProbeHealthDetailView 用它调
// /api/admin/probe/model/<name>/nodes）。
await take('MODEL_NAME', '/api/admin/probe/dashboard', (j) => j?.models?.[0]?.raw_model_name)

// ── 审批 / 订单（本机可能压根没有数据） ────────────────────────────────────
await take('APPROVAL_ID', '/api/admin/approvals', (j) => j?.items?.[0]?.id, '0')
await take('ORDER_ID', '/api/admin/maas/orders', (j) => j?.items?.[0]?.id, '0')

if (missing.length) {
  console.error('resolve-ids: 以下变量在本机没有真实数据（已用哨兵值兜底，断言须降级 optional）：')
  for (const m of missing) console.error('  · ' + m)
}

if (AS_JSON) {
  console.log(JSON.stringify(values, null, 2))
} else {
  for (const [k, v] of Object.entries(values)) console.log(`${k}=${v}`)
}
