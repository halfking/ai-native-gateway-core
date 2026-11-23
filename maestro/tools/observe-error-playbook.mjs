#!/usr/bin/env node
// observe-error-playbook.mjs — 错误处理手册 48h 灰度观察采样器（2026-11-22 任务 4）
//
// 每 2 小时由自动化（cron 23 */2 * * *）调用一次：
//   1. /healthz 采版本身份（git_sha 变化如实记录）
//   2. 登录 /api/auth/token（demoadmin，与 docs/211-user-auth-final-verification.md 同源）
//   3. 读全局闸（error_playbook.enabled / auto_analyze / auto_execute）—— 灰度约束的真源
//   4. GET /stats（SQL 聚合，完整口径）+ /entries + /executions 最近一页
//   5. 追加一行 JSONL 到 docs/audit/evidence/2026-11-22-error-playbook-48h-observations.jsonl
//   6. 满 24 轮或满 48h ⇒ 收口：全量分页拉取执行留痕，写收口分析
//      docs/audit/evidence/2026-11-22-error-playbook-48h-final-summary.md，退出码 3
//      （自动化收到 3 ⇒ 调 CronPauseSelf 停表）
//
// ★ 2026-11-23 审计轮重写口径（首版的四处量具缺陷，每一处都在本灰度里实际发生过）：
//   ① 异常判据倒置：把 awaiting_approval>0 报成异常 —— 全局执行闸关闭时命中
//      必然落 awaiting_approval，那是设计稳态 ⇒ 每轮都「异常」；而真正的灰度约束
//      （全局闸被打开 / 自动化执行了变更类动作 / worker 开着却不出留痕）一条都不报。
//      实况：开关开着而摄取链静默失败约 22 小时，10 轮采样全部 anomalies=[]。
//   ② 收口指标跨轮重复计数：把每轮 top-100 快照的计数直接相加 ⇒ 同一条留痕
//      被计「它在首页存活了几轮」次，分母没有意义。
//   ③ top-100 截断：留痕约 200 条/天增长，一页只覆盖十几小时。
//   ④ 代理②误标：awaiting_approval 占比被解释成「置信度门拦下待放行的比例」，
//      而在本灰度里它由全局闸决定、恒接近 100%，与误报无关。
//   另：last_run_at 是引擎的**进程内存**值，蓝绿切换即清零 ⇒ 单凭 null 不能判
//   worker 已死，必须配合「最近 15 分钟有没有新留痕」一起看。
//
// 只读观察：不改任何生产配置。所有请求都是 GET（登录 POST 除外）。
//
// 用法：node maestro/tools/observe-error-playbook.mjs [--base https://llm.itestu.cn]
//         [--start 2026-11-22T01:00:00+08:00] [--expect-global-auto-execute false|true]
//         [--dry-run] [--closure-preview] [--out FILE] [--summary FILE]
// 退出码：0 = 本轮正常；1 = 登录或采样失败；3 = 48h 收口（已写收口分析）。
//   --dry-run          只算不写（审计/验证用）：不追加 JSONL、不写收口档，退出码只有 0/1
//   --closure-preview  把收口分析打到 stdout（隐含 --dry-run；无视 48h 是否到期）
//   纯逻辑（判据/聚合/收口文案）全部导出，自测见 observe-error-playbook.selftest.mjs。

import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const REPO = resolve(HERE, '../..')
export const DEFAULT_OBS = resolve(REPO, 'docs/audit/evidence/2026-11-22-error-playbook-48h-observations.jsonl')
export const DEFAULT_SUMMARY = resolve(REPO, 'docs/audit/evidence/2026-11-22-error-playbook-48h-final-summary.md')
// 灰度开始锚点（2026-11-22 本地时间，任务 4 开启 settings 后立即建自动化）。
export const DEFAULT_START_ISO = '2026-11-22T01:00:00+08:00'
export const RUN_BUDGET = 24 // 每 2 小时一轮 × 24 = 48 小时
export const CLOSE_AFTER_HOURS = 48
export const MAX_ROWS = 50 * 200 // 全量分页硬上限（1 万行）；触顶即标记 truncated，不谎称「全量」
const PAGE = 200 // 服务端单页上限（ListExecutions：limit>200 会静默回落成 50）
const MAX_PAGES = MAX_ROWS / PAGE
const STALL_MS = 15 * 60_000 // 「最近 15 分钟没有新留痕」才算 worker 不出活
export const GAP_HOURS = 2.5 // 相邻两轮间隔超过它即记为采样缺口（正常节奏 2h）
export const GATE_KEYS = ['enabled', 'auto_analyze', 'auto_execute']
// 自动化触发源（manual 是管理员显式触发，不受自动执行闸约束）。
const AUTOMATION_TRIGGERS = new Set(['standard', 'auto_analyze', 'refinement'])
// 与后端 isReadOnlyMethod 同口径。
const READ_ONLY_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

// ── 纯逻辑（可测） ───────────────────────────────────────────────────────

export function parseArgs(argv) {
  const out = {}
  for (let i = 0; i < argv.length; i++) {
    const v = argv[i]
    if (typeof v !== 'string' || !v.startsWith('--')) continue
    const next = argv[i + 1]
    if (next === undefined || (typeof next === 'string' && next.startsWith('--'))) out[v.slice(2)] = true
    else {
      out[v.slice(2)] = next
      i++
    }
  }
  return out
}

const toBool = (v) => v === true || v === 'true' || v === '1'

function asObject(v) {
  if (v === null || v === undefined) return null
  if (typeof v === 'object') return Array.isArray(v) ? null : v
  if (typeof v === 'string') {
    try {
      const o = JSON.parse(v)
      return o && typeof o === 'object' && !Array.isArray(o) ? o : null
    } catch {
      return null
    }
  }
  return null
}

/** 留痕的步骤数组（steps 可能是数组，也可能是 JSON 字符串）。 */
export function stepActions(row) {
  let steps = row?.steps
  if (typeof steps === 'string') {
    try {
      steps = JSON.parse(steps)
    } catch {
      return []
    }
  }
  return Array.isArray(steps) ? steps.filter((s) => s && typeof s === 'object') : []
}

/** 单个步骤是否变更类 —— 与后端 Plan.Mutating() / isMutatingAction 同口径。 */
export function isMutatingStep(step) {
  const action = String(step?.action ?? '')
  if (action === 'set_settings_kv' || action === 'disable_pi_rule') return true
  if (action === 'call_admin_api') {
    const method = String(asObject(step.params)?.method ?? '').toUpperCase()
    return !READ_ONLY_METHODS.has(method) // 方法缺失按变更类算（保守）
  }
  return false
}

/**
 * 自动化触发、且真的动了变更类动作的留痕。
 * status=skipped 不算（闸拦下没执行）；ok / failed 都算 —— 全局闸关着时连尝试都不该发生。
 */
export function findMutatingExecutions(rows) {
  const out = []
  for (const r of rows || []) {
    if (!AUTOMATION_TRIGGERS.has(r?.trigger_source)) continue
    const acts = stepActions(r)
      .filter((s) => isMutatingStep(s) && s.status !== 'skipped')
      .map((s) => (s.status === 'ok' ? s.action : `${s.action}(${s.status})`))
    if (acts.length) {
      out.push({ id: r.id, trigger: r.trigger_source, entry_id: r.entry_id, error_type: r.error_type, created_at: r.created_at, actions: acts })
    }
  }
  return out
}

/** 最近一条留痕的时间（epoch ms）；没有可解析的留痕 ⇒ null。 */
export function newestExecutionAt(rows) {
  let newest = null
  for (const r of rows || []) {
    const t = Date.parse(r?.created_at ?? '')
    if (!Number.isNaN(t) && (newest === null || t > newest)) newest = t
  }
  return newest
}

/** 执行留痕的全量分解（输入必须是完整集合，结论才成立）。 */
export function aggregateExecutions(rows) {
  const agg = {
    rows: 0, by_status: {}, by_trigger: {}, by_status_trigger: {}, by_entry: {}, by_error_type: {},
    executed: 0, mutating_executed: 0,
  }
  const bump = (m, k) => {
    m[k] = (m[k] || 0) + 1
  }
  for (const r of rows || []) {
    agg.rows++
    const status = r?.status ?? 'unknown'
    const trigger = r?.trigger_source ?? 'unknown'
    bump(agg.by_status, status)
    bump(agg.by_trigger, trigger)
    bump(agg.by_status_trigger, `${trigger}|${status}`)
    bump(agg.by_entry, String(r?.entry_id ?? ''))
    bump(agg.by_error_type, r?.error_type ?? '')
    if (status === 'succeeded' || status === 'failed') agg.executed++
    if (stepActions(r).some((s) => isMutatingStep(s) && s.status === 'ok')) agg.mutating_executed++
  }
  return agg
}

function firstSample(row) {
  const ctx = asObject(row?.context)
  if (!ctx) return ''
  const s = Array.isArray(ctx.samples) ? ctx.samples[0] : ctx.message
  return typeof s === 'string' ? s : ''
}

/**
 * 人工判误报用的样本：按条目归并待确认留痕里的真实错误消息（去重、每条目最多 perEntry 条）。
 * 自动化指标没有「这次命中对不对」的 ground truth ⇒ 真误报率只能靠人看样本。
 */
export function labelingSample(rows, entriesById, perEntry = 5) {
  const byEntry = new Map()
  for (const r of rows || []) {
    if (r?.status !== 'awaiting_approval') continue
    const key = String(r.entry_id ?? '')
    if (!byEntry.has(key)) byEntry.set(key, { rows: 0, seen: new Set(), samples: [] })
    const g = byEntry.get(key)
    g.rows++
    const s = firstSample(r).replace(/\s+/g, ' ').trim().slice(0, 160)
    if (s && !g.seen.has(s) && g.samples.length < perEntry) {
      g.seen.add(s)
      g.samples.push(s)
    }
  }
  const out = []
  for (const [id, g] of byEntry) {
    const e = entriesById.get(id) || {}
    out.push({
      entry_id: id, error_type: e.error_type ?? '', title: e.title ?? '', source: e.source ?? '',
      auto_execute: e.auto_execute, rows: g.rows, samples: g.samples,
    })
  }
  return out.sort((a, b) => b.rows - a.rows)
}

/** 相邻两轮间隔超过 gapHours 的区间。 */
export function samplingGaps(recs, gapHours = GAP_HOURS) {
  const ts = (recs || [])
    .map((r) => ({ t: Date.parse(r?.ts ?? ''), iso: r?.ts }))
    .filter((x) => !Number.isNaN(x.t))
    .sort((a, b) => a.t - b.t)
  const gaps = []
  for (let i = 1; i < ts.length; i++) {
    const h = (ts[i].t - ts[i - 1].t) / 3600_000
    if (h > gapHours) gaps.push({ from: ts[i - 1].iso, to: ts[i].iso, hours: +h.toFixed(1) })
  }
  return gaps
}

export function closureDue(n, elapsedH) {
  return n >= RUN_BUDGET || elapsedH >= CLOSE_AFTER_HOURS
}

/**
 * 本轮异常判据。只报「与灰度意图不符」或「链路不出活」的事；
 * 设计稳态（全局闸关 ⇒ 命中落 awaiting_approval）不报。
 *
 * @param {object} p
 * @param {object} p.st        /stats 原文
 * @param {number} p.statsStatus
 * @param {object} p.gates     { enabled, auto_analyze, auto_execute }，读不到为 null
 * @param {object[]} p.recentRows  /executions 最近一页
 * @param {number} p.execTotal /stats.executions.total（完整口径）
 * @param {object|null} p.prev 上一轮采样记录（没有则 null；旧格式行也行）
 * @param {boolean} p.expectGlobalAutoExecute 灰度约定的全局 auto_execute 值
 * @param {Date} p.now
 */
export function computeAnomalies({ st, statsStatus, gates, recentRows, execTotal, prev, expectGlobalAutoExecute = false, now = new Date() }) {
  const a = []
  if (st?.provisioned === false) a.push('provisioned=false')
  if (st?.engine_wired === false) a.push('engine_wired=false')
  if (statsStatus !== 200) a.push(`stats HTTP ${statsStatus}`)
  if (gates?.enabled !== true && gates?.enabled !== false) a.push('gate_unreadable:enabled')
  if (gates?.auto_execute !== true && gates?.auto_execute !== false) a.push('gate_unreadable:auto_execute')

  // 全局执行闸与灰度约定不符。
  if ((gates?.auto_execute === true || gates?.auto_execute === false) && gates.auto_execute !== expectGlobalAutoExecute) {
    a.push(`gate_drift:auto_execute=${gates.auto_execute} (gray release expects ${expectGlobalAutoExecute})`)
  }
  // ★ 灰度约束：全局闸关着，自动化却动了变更类动作。
  if (gates?.auto_execute === false) {
    for (const v of findMutatingExecutions(recentRows)) {
      a.push(`CONSTRAINT_VIOLATION: automation executed a mutating action while auto_execute is off — ` +
        `execution #${v.id} trigger=${v.trigger} entry=${v.entry_id} error_type=${v.error_type} actions=${v.actions.join('+')}`)
    }
  }
  // 失败数取完整聚合（stats.executions.last_24h），不依赖最近一页。
  const failed24 = st?.executions?.last_24h?.failed
  if (Number(failed24) > 0) a.push(`failed_last_24h=${failed24}`)

  // ★ 链路不出活（开关开着才有意义）。last_run_at 是进程内存值，重启即清零，
  //   单凭 null 不算；要有「最近 15 分钟没有新留痕」佐证。
  if (gates?.enabled === true) {
    const newest = newestExecutionAt(recentRows)
    const noRun = st?.last_run_at === null || st?.last_run_at === undefined
    if (noRun && (newest === null || now.getTime() - newest > STALL_MS)) {
      a.push('worker_not_completing_rounds: enabled=true but last_run_at=null and no execution in the last 15 min ' +
        '(worker rounds are failing or never scheduled — check "error_playbook: worker round failed" in logs)')
    }
    const prevTotal = prev?.stats?.executions_total
    if (prev && typeof prevTotal === 'number' && execTotal === prevTotal) {
      const ageH = (now.getTime() - Date.parse(prev.ts ?? '')) / 3600_000
      if (!Number.isNaN(ageH) && ageH >= 1.5) {
        a.push(`no_new_executions_since_prev_sample: total stuck at ${execTotal} for ${ageH.toFixed(1)}h while enabled=true`)
      }
    }
  }
  return a
}

const pct = (n, d) => (d > 0 ? `${((n / d) * 100).toFixed(1)}%` : 'n/a')
const mdEscape = (s) => String(s ?? '').replace(/\|/g, '\\|').replace(/\r?\n/g, ' ')
const localStamp = (d) => d.toLocaleString('zh-CN', { hour12: false })

/**
 * 收口分析（Markdown）。所有数字来自收口时刻的**全量**留痕，不跨轮求和。
 *
 * @param {object} p
 * @param {object[]} p.recs  全部采样记录（旧格式行也行：缺 gates/recent 不影响）
 * @param {object} p.snap    { identity, gates, stats, entries, executions, truncated }
 */
export function buildClosureSummary({ recs, snap, startIso = DEFAULT_START_ISO, now = new Date() }) {
  recs = recs || []
  const startMs = Date.parse(startIso)
  const elapsedH = (now.getTime() - startMs) / 3600_000
  const rows = (snap.executions || []).filter((r) => {
    const t = Date.parse(r?.created_at ?? '')
    return !Number.isNaN(t) && t >= startMs
  })
  const agg = aggregateExecutions(rows)
  const entriesById = new Map((snap.entries || []).map((e) => [String(e.id), e]))
  const gates = snap.gates || {}
  const gateOff = gates.auto_execute === false
  const violations = findMutatingExecutions(rows)
  const succ = agg.by_status.succeeded || 0
  const fail = agg.by_status.failed || 0
  const approval = agg.by_status.awaiting_approval || 0
  const gaps = samplingGaps(recs)
  const lastRec = recs.reduce((m, r) => (Date.parse(r?.ts ?? '') > Date.parse(m?.ts ?? '') ? r : m), recs[0])
  const trailingH = lastRec ? (now.getTime() - Date.parse(lastRec.ts)) / 3600_000 : elapsedH
  const expectedRounds = Math.max(0, Math.floor(elapsedH / 2) + 1)
  const oldFormat = recs.filter((r) => !r || r.gates === undefined).length
  const anomalyRounds = recs.filter((r) => (r?.anomalies || []).length > 0)
  const label = labelingSample(rows, entriesById)
  const entryTitle = (id) => {
    const e = entriesById.get(String(id))
    return e ? `${e.title}（${e.error_type}）` : id === '0' ? '不挂条目（分析阶段）' : `已删除条目 #${id}`
  }
  const byEntryLines = Object.entries(agg.by_entry)
    .sort((x, y) => y[1] - x[1])
    .map(([id, n]) => `  - 条目 #${id} ${mdEscape(entryTitle(id))}：${n}`)
    .join('\n')
  const anomalyLines = anomalyRounds
    .map((r) => `  - ${r.ts}：${(r.anomalies || []).map(mdEscape).join('；')}`)
    .join('\n')

  return `# 错误处理手册 48h 灰度观察收口（${localStamp(now)}）

- 观察窗口：${startIso} 起 ${elapsedH.toFixed(1)} 小时；采样 ${recs.length} 轮（按每 2 小时应采约 ${expectedRounds} 轮，缺 ${Math.max(0, expectedRounds - recs.length)} 轮）
- 采样缺口（相邻两轮间隔 > ${GAP_HOURS}h）：${gaps.length ? gaps.map((g) => `${g.from} → ${g.to}（${g.hours}h）`).join('；') : '无'}；最后一轮距今 ${trailingH.toFixed(1)}h
- 最终身份：${JSON.stringify(snap.identity)}
- 全局闸（灰度约束的真源，来自 settings）：enabled=${gates.enabled} auto_analyze=${gates.auto_analyze} auto_execute=${gates.auto_execute}
- 条目面：total=${snap.stats?.total_entries} enabled=${snap.stats?.enabled_entries} auto_execute_entries=${snap.stats?.auto_execute_entries}；来源分布=${JSON.stringify(snap.stats?.entries_by_source)}
- 未知错误待分析（pending_unknown，进程内存）：${JSON.stringify(snap.stats?.pending_unknown ?? null)}

## 执行留痕（窗口内全量 ${agg.rows} 行，${snap.truncated ? `⚠ 已截断（超过 ${MAX_ROWS} 行上限），以下数字只覆盖已拉取部分` : '未截断'}）

- 按状态：${JSON.stringify(agg.by_status)}
- 按触发源：${JSON.stringify(agg.by_trigger)}
- 按状态×触发源：${JSON.stringify(agg.by_status_trigger)}
- 按条目：
${byEntryLines || '  - （无）'}
- 按错误类型：${JSON.stringify(agg.by_error_type)}

## 灰度约束核对

- 全局 auto_execute=${gates.auto_execute}。
- 自动化（standard / auto_analyze / refinement）执行了变更类动作（set_settings_kv / disable_pi_rule / call_admin_api 非 GET·HEAD·OPTIONS）的留痕：${agg.mutating_executed} 条。
${gateOff ? `- 其中发生在「全局闸关闭」约束下的：**${violations.length} 条**${violations.length ? '（约束被突破，逐条核对）' : '（约束成立）'}` : '- 当前全局闸为开启，变更类执行不受本次灰度约束；下表仅供核对。'}
${violations.map((v) => `  - #${v.id} ${v.created_at ?? ''} trigger=${v.trigger} entry=${v.entry_id} error_type=${v.error_type} actions=${v.actions.join('+')}`).join('\n')}

## 判读（自动化指标）

- 触发合计（窗口内全部留痕）= ${agg.rows}；其中实际执行了方案的（succeeded + failed）= ${agg.executed}
- 标准执行成功率 = succeeded/(succeeded+failed) = ${pct(succ, agg.executed)}
- 误报率代理 ① = failed/触发合计 = ${pct(fail, agg.rows)}
- 误报率代理 ② = awaiting_approval/触发合计 = ${pct(approval, agg.rows)}（${gateOff ? '全局 auto_execute 关闭时，命中必然落 awaiting_approval ⇒ 这个比例由闸门决定、恒接近 100%，**不反映误报**' : '全局 auto_execute 开启时，此比例才近似「被置信度门 / 条目闸拦下待放行」的份额'}）
- 未知错误 → PI 分析 → 落手册：自动分析留痕 ${agg.by_trigger.auto_analyze || 0} 条；PI 落库条目 ${snap.stats?.entries_by_source?.pi_analysis ?? 0} 条

## 误报率的人工判定样本

自动化指标**给不出真误报率** —— 没有「这次命中对不对」的 ground truth。判误报的办法是人看样本：
下表每个条目列出其待确认留痕里的真实错误消息（去重），逐条判断「该条目的处置方案是否适用于这类错误」；
判「不适用」的占比，就是该条目的误报率（人工口径，需在结论里注明样本量）。

| 条目 | 待确认留痕 | 错误样本（去重，最多 5 条） |
|---|---|---|
${label.map((l) => `| #${l.entry_id} ${mdEscape(entryTitle(l.entry_id))}<br>来源 ${l.source} · auto_execute ${l.auto_execute === undefined ? '?' : l.auto_execute} | ${l.rows} | ${l.samples.map(mdEscape).join('<br>') || '（无样本）'} |`).join('\n') || '| （窗口内无待确认留痕） | 0 | |'}

## 异常轨迹（${anomalyRounds.length} / ${recs.length} 轮带 anomalies）

${anomalyLines || '  - 无'}

- 数据源：${DEFAULT_OBS}（${recs.length} 行；其中 ${oldFormat} 行是 2026-11-23 审计前的旧格式，没有 gates / recent 字段，也没有可用的异常判据）
`
}

// ── I/O（不可测部分；验证靠 --dry-run 对现役跑） ───────────────────────

async function jfetch(base, path, opts = {}) {
  const r = await fetch(base + path, { credentials: 'omit', ...opts })
  const text = await r.text()
  let json = null
  try {
    json = JSON.parse(text)
  } catch {
    /* 门控原文是纯文本 */
  }
  return { status: r.status, json, text }
}

const authHeaders = (tok) => ({ Authorization: `Bearer ${tok}` })

async function login(base) {
  const r = await jfetch(base, '/api/auth/token', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'demoadmin', password: 'Demo@admin123' }),
  })
  if (r.status !== 200) return null
  return r.json?.access_token || r.json?.token || null
}

async function readGate(base, tok, key) {
  const r = await jfetch(base, `/api/admin/settings/error_playbook.${key}`, { headers: authHeaders(tok) })
  const v = r.json?.value
  return typeof v === 'boolean' ? v : null
}

/** 采一次现役状态；complete=true 时全量分页拉取执行留痕（收口用）。 */
export async function collect(base, tok, { complete = false } = {}) {
  const stats = await jfetch(base, '/api/admin/error-playbook/stats', { headers: authHeaders(tok) })
  const st = stats.json || {}
  const entriesR = await jfetch(base, '/api/admin/error-playbook/entries?limit=200', { headers: authHeaders(tok) })
  let executions = []
  let truncated = false
  for (let page = 0; ; page++) {
    const r = await jfetch(base, `/api/admin/error-playbook/executions?limit=${PAGE}&offset=${page * PAGE}`, { headers: authHeaders(tok) })
    if (r.status !== 200) throw new Error(`executions HTTP ${r.status} at offset ${page * PAGE}`)
    const list = r.json?.executions ?? []
    executions.push(...list)
    const total = Number(r.json?.total ?? executions.length)
    if (!complete || list.length < PAGE || executions.length >= total) break
    if (page + 1 >= MAX_PAGES) {
      truncated = true
      break
    }
  }
  return {
    statsStatus: stats.status,
    st,
    execTotal: Number(st.executions?.total ?? executions.length),
    entries: entriesR.json?.entries ?? [],
    executions,
    truncated,
  }
}

export function readJsonl(path) {
  if (!existsSync(path)) return []
  return readFileSync(path, 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((l) => {
      try {
        return JSON.parse(l)
      } catch {
        return null
      }
    })
    .filter(Boolean)
}

async function main(argv) {
  const args = parseArgs(argv)
  const base = String(args.base || 'https://llm.itestu.cn').replace(/\/$/, '')
  const startIso = String(args.start || DEFAULT_START_ISO)
  const expectGate = args['expect-global-auto-execute'] === undefined ? false : toBool(args['expect-global-auto-execute'])
  const preview = args['closure-preview'] === true
  const dryRun = args['dry-run'] === true || preview // preview 必须只算不写，否则会在自动化里提前触发收口
  const obsPath = resolve(String(args.out || DEFAULT_OBS))
  const summaryPath = resolve(String(args.summary || DEFAULT_SUMMARY))

  const health = await jfetch(base, '/healthz')
  const tok = await login(base)
  if (!tok) {
    console.error('observe: login failed — 本轮采样失败（自动化下一轮重试）')
    return 1
  }
  const gates = {}
  for (const k of GATE_KEYS) gates[k] = await readGate(base, tok, k)
  const snap = await collect(base, tok)

  const recs = readJsonl(obsPath)
  const prev = recs.length ? recs[recs.length - 1] : null
  const now = new Date()
  const anomalies = computeAnomalies({
    st: snap.st, statsStatus: snap.statsStatus, gates, recentRows: snap.executions,
    execTotal: snap.execTotal, prev, expectGlobalAutoExecute: expectGate, now,
  })
  const statusCounts = {}
  const triggerCounts = {}
  for (const e of snap.executions) {
    statusCounts[e.status] = (statusCounts[e.status] || 0) + 1
    triggerCounts[e.trigger_source] = (triggerCounts[e.trigger_source] || 0) + 1
  }
  const sourceCounts = {}
  for (const e of snap.entries) sourceCounts[e.source] = (sourceCounts[e.source] || 0) + 1

  const rec = {
    ts: now.toISOString(),
    local: localStamp(now),
    health: {
      version: health.json?.version, git_sha: health.json?.git_sha,
      build_seq: health.json?.build_seq, ready: health.json?.ready,
    },
    gates,
    stats: {
      engine_wired: snap.st.engine_wired, llm_wired: snap.st.llm_wired, provisioned: snap.st.provisioned,
      total_entries: snap.st.total_entries, enabled_entries: snap.st.enabled_entries,
      auto_execute_entries: snap.st.auto_execute_entries, executions_total: snap.execTotal,
      last_24h: snap.st.executions?.last_24h ?? null, pending_unknown: snap.st.pending_unknown ?? null,
      last_run_at: snap.st.last_run_at ?? null,
    },
    // 最近一页的分布：只作连续性参考。**收口不得跨轮把它们相加**（同一条留痕会在多轮重复出现）。
    window_status_counts: statusCounts,
    window_trigger_counts: triggerCounts,
    entries_by_source: sourceCounts,
    recent: {
      returned: snap.executions.length,
      newest_execution_at: (() => {
        const t = newestExecutionAt(snap.executions)
        return t === null ? null : new Date(t).toISOString()
      })(),
      mutating_executed: findMutatingExecutions(snap.executions),
    },
    anomalies,
  }
  if (dryRun) {
    console.log(`observe(dry-run): ${JSON.stringify(rec)}`)
  } else {
    appendFileSync(obsPath, JSON.stringify(rec) + '\n')
    console.log(`observe: 第 ${recs.length + 1} 轮已写入 ${obsPath}`)
  }
  console.log(`observe: git_sha=${rec.health.git_sha} gates=${JSON.stringify(gates)} total_entries=${rec.stats.total_entries} ` +
    `exec_total=${snap.execTotal} window_status=${JSON.stringify(statusCounts)} anomalies=${JSON.stringify(anomalies)}`)

  const allRecs = [...recs, rec]
  const elapsedH = (now.getTime() - Date.parse(startIso)) / 3600_000
  if (!closureDue(allRecs.length, elapsedH) && !preview) {
    console.log(`observe: 已运行 ${elapsedH.toFixed(1)}h / ${allRecs.length} 轮 — 继续观察`)
    return 0
  }

  const full = await collect(base, tok, { complete: true })
  const md = buildClosureSummary({
    recs: allRecs,
    snap: {
      identity: rec.health, gates, stats: full.st, entries: full.entries,
      executions: full.executions, truncated: full.truncated,
    },
    startIso,
    now,
  })
  if (dryRun) {
    console.log(preview ? md : 'observe(dry-run): 收口已到期，但 dry-run 不写收口档（加 --closure-preview 可预览）')
    return 0
  }
  writeFileSync(summaryPath, md)
  console.log(`observe: 48h 收口分析已写入 ${summaryPath}`)
  return 3 // 自动化收到 3 ⇒ CronPauseSelf
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href
if (isMain) {
  main(process.argv.slice(2))
    .then((code) => process.exit(code))
    .catch((e) => {
      console.error(`observe: ${e?.message ?? e}`)
      process.exit(1)
    })
}
