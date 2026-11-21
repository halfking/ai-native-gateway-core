#!/usr/bin/env node
// observe-error-playbook.mjs — 错误处理手册 48h 灰度观察采样器（2026-11-22 任务 4）
//
// 每 2 小时由自动化（cron 23 */2 * * *）调用一次：
//   1. /healthz 采版本身份（git_sha 变化如实记录）
//   2. 登录 /api/auth/token（demoadmin，与 docs/211-user-auth-final-verification.md 同源）
//   3. GET /stats + /executions?limit=100 → 聚合 status 分布
//   4. 追加一行 JSONL 到 docs/audit/evidence/2026-11-22-error-playbook-48h-observations.jsonl
//   5. ≥24 轮（48h）时做收口分析，写 2026-11-22-error-playbook-48h-final-summary.md，
//      退出码 3（自动化收到 3 ⇒ 调 CronPauseSelf 停表）
//
// 只读观察：不改任何生产配置。所有请求都是 GET（登录 POST 除外）。
//
// 用法：node maestro/tools/observe-error-playbook.mjs [--base https://llm.itestu.cn]

import { readFileSync, appendFileSync, existsSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, v, i, a) => {
  if (v.startsWith('--')) acc.push([v.slice(2), a[i + 1]])
  return acc
}, []))

const HERE = dirname(fileURLToPath(import.meta.url))
const REPO = resolve(HERE, '../..')
const OBS = resolve(REPO, 'docs/audit/evidence/2026-11-22-error-playbook-48h-observations.jsonl')
const SUMMARY = resolve(REPO, 'docs/audit/evidence/2026-11-22-error-playbook-48h-final-summary.md')
const BASE = (args.base || 'https://llm.itestu.cn').replace(/\/$/, '')

// 灰度开始锚点（2026-11-22 本地时间，任务 4 开启 settings 后立即建自动化）
const START_ISO = args.start || '2026-11-22T01:00:00+08:00'
const RUN_BUDGET = 24 // 每 2 小时一轮 × 24 = 48 小时

async function jfetch(path, opts = {}) {
  const r = await fetch(BASE + path, { credentials: 'omit', ...opts })
  const text = await r.text()
  let json = null
  try { json = JSON.parse(text) } catch { /* 门控原文是纯文本 */ }
  return { status: r.status, json, text }
}

const health = await jfetch('/healthz')
const login = await jfetch('/api/auth/token', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'demoadmin', password: 'Demo@admin123' }),
})
if (login.status !== 200) {
  console.error(`observe: login failed HTTP ${login.status} — 本轮采样失败（自动化下一轮重试）`)
  process.exit(1)
}
const tok = login.json?.access_token || login.json?.token
const auth = { Authorization: `Bearer ${tok}` }

const stats = await jfetch('/api/admin/error-playbook/stats', { headers: auth })
const execs = await jfetch('/api/admin/error-playbook/executions?limit=100', { headers: auth })
const entries = await jfetch('/api/admin/error-playbook/entries?limit=200', { headers: auth })

const st = stats.json || {}
const ex = (execs.json?.executions ?? [])
const en = (entries.json?.entries ?? [])
const statusCounts = {}
for (const e of ex) statusCounts[e.status] = (statusCounts[e.status] || 0) + 1
const triggerCounts = {}
for (const e of ex) triggerCounts[e.trigger_source] = (triggerCounts[e.trigger_source] || 0) + 1
const sourceCounts = {}
for (const e of en) sourceCounts[e.source] = (sourceCounts[e.source] || 0) + 1

const anomalies = []
if (st.provisioned === false) anomalies.push('provisioned=false')
if (st.engine_wired === false) anomalies.push('engine_wired=false')
if (stats.status !== 200) anomalies.push(`stats HTTP ${stats.status}`)
if ((statusCounts.awaiting_approval || 0) > 0) anomalies.push(`awaiting_approval=${statusCounts.awaiting_approval}`)
if ((statusCounts.failed || 0) > 0) anomalies.push(`failed=${statusCounts.failed}`)

const rec = {
  ts: new Date().toISOString(),
  local: new Date().toLocaleString('zh-CN', { hour12: false }),
  health: { version: health.json?.version, git_sha: health.json?.git_sha, build_seq: health.json?.build_seq, ready: health.json?.ready },
  stats: {
    engine_wired: st.engine_wired, llm_wired: st.llm_wired, provisioned: st.provisioned,
    total_entries: st.total_entries, enabled_entries: st.enabled_entries,
    auto_execute_entries: st.auto_execute_entries, executions_total: st.executions?.total,
    last_24h: st.executions?.last_24h ?? null, pending_unknown: st.pending_unknown ?? null,
    last_run_at: st.last_run_at ?? null,
  },
  window_status_counts: statusCounts,
  window_trigger_counts: triggerCounts,
  entries_by_source: sourceCounts,
  anomalies,
}
appendFileSync(OBS, JSON.stringify(rec) + '\n')
const lines = existsSync(OBS) ? readFileSync(OBS, 'utf8').trim().split('\n').filter(Boolean) : []
const n = lines.length
console.log(`observe: 第 ${n} 轮已写入 ${OBS}`)
console.log(`observe: git_sha=${rec.health.git_sha} total_entries=${st.total_entries} exec_total=${st.executions?.total} window_status=${JSON.stringify(statusCounts)} anomalies=${JSON.stringify(anomalies)}`)

// ── 48h 收口判定 ────────────────────────────────────────────────────────────
const elapsedMs = Date.now() - new Date(START_ISO).getTime()
const elapsedH = elapsedMs / 3600_000
if (n < RUN_BUDGET && elapsedH < 48) {
  console.log(`observe: 已运行 ${elapsedH.toFixed(1)}h / ${n} 轮 — 继续观察`)
  process.exit(0)
}

// 收口分析
const recs = lines.map((l) => { try { return JSON.parse(l) } catch { return null } }).filter(Boolean)
const agg = {}
for (const r of recs) for (const [k, v] of Object.entries(r.window_status_counts || {})) agg[k] = (agg[k] || 0) + v
const last = recs[recs.length - 1]
const succ = agg.succeeded || 0
const fail = agg.failed || 0
const approval = agg.awaiting_approval || 0
const totalTriggers = succ + fail + approval + (agg.no_action || 0) + (agg.skipped || 0)
const md = `# 错误处理手册 48h 灰度观察收口（${new Date().toLocaleString('zh-CN', { hour12: false })}）

- 观察窗口：${START_ISO} 起，共 ${n} 轮采样（每 2 小时；窗口内 /executions 前 100 条）
- 最终身份：${JSON.stringify(last?.health)}
- 最终条目面：total=${last?.stats?.total_entries} enabled=${last?.stats?.enabled_entries} auto_execute=${last?.stats?.auto_execute_entries}；来源分布=${JSON.stringify(last?.entries_by_source)}
- 窗口触发量（按轮聚合，同一次留痕会在多轮重复计）：
  - succeeded=${succ} failed=${fail} awaiting_approval=${approval}
  - 触发合计=${totalTriggers}
- 判读口径：
  - 标准执行成功率 = succeeded/(succeeded+failed) = ${succ + fail > 0 ? (succ / (succ + fail) * 100).toFixed(1) + '%' : 'n/a（无执行）'}
  - 误报率代理 ① = failed/触发合计 = ${totalTriggers > 0 ? (fail / totalTriggers * 100).toFixed(1) + '%' : 'n/a'}
  - 误报率代理 ② = awaiting_approval/触发合计 = ${totalTriggers > 0 ? (approval / totalTriggers * 100).toFixed(1) + '%' : 'n/a'}（confidence 门把变更类方案拦下待放行的比例）
- 异常轨迹：${recs.filter((r) => (r.anomalies || []).length).length} 轮带 anomalies；明细见 observations JSONL。
- 数据源：docs/audit/evidence/2026-11-22-error-playbook-48h-observations.jsonl（${n} 行）
`
writeFileSync(SUMMARY, md)
console.log(`observe: 48h 收口分析已写入 ${SUMMARY}`)
process.exit(3) // 自动化收到 3 ⇒ CronPauseSelf
