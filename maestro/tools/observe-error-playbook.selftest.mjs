// maestro/tools/observe-error-playbook.selftest.mjs — 采样器**判据与口径**的自检
// （不需要网络、不需要生产；只测 observe-error-playbook.mjs 导出的纯逻辑）。
//
// ★ 为什么要有它：首版采样器的四处量具缺陷（异常判据倒置 / 收口跨轮重复计数 /
//   top-100 截断 / 代理②误标）没有一处是被测试发现的 —— 都是事后从生产现象里
//   读出来的。观察工具自己的错误，比没有观察更坏：它让人以为「一切正常」。
//   ⇒ 每个缺陷都在这里用合成数据复现一次（该报的必须报），并配一条反向臂
//     （不该报的不得报），避免判据修过头。
//
// 用法：node maestro/tools/observe-error-playbook.selftest.mjs
// 退出码：0 = 自检全绿；1 = 自检失败。

import {
  aggregateExecutions, buildClosureSummary, closureDue, computeAnomalies, findMutatingExecutions,
  isMutatingStep, labelingSample, newestExecutionAt, parseArgs, samplingGaps, stepActions,
} from './observe-error-playbook.mjs'

const cases = []
const t = (name, fn) => {
  try {
    fn()
    cases.push({ name, ok: true })
  } catch (e) {
    cases.push({ name, ok: false, err: e.message })
  }
}
const eq = (a, b, what) => {
  if (a !== b) throw new Error(`${what || ''} 期望 ${JSON.stringify(b)}，实际 ${JSON.stringify(a)}`)
}
const ok = (cond, what) => {
  if (!cond) throw new Error(what || '断言不成立')
}
const has = (anoms, prefix) => anoms.filter((s) => String(s).startsWith(prefix))

const T0 = Date.parse('2026-11-23T08:00:00Z')
const NOW = new Date(T0)
const iso = (ms) => new Date(ms).toISOString()
const MIN = 60_000
const H = 3600_000
const START = '2026-11-22T01:00:00+08:00' // = 2026-11-21T17:00:00Z

const GATE_OFF = { enabled: true, auto_analyze: true, auto_execute: false }
const awaiting = (id, at, entry = 2, sample = '[rate_limit] upstream 429') => ({
  id, trigger_source: 'standard', status: 'awaiting_approval', entry_id: entry, error_type: 'rate_limit',
  created_at: iso(at), steps: [], context: { samples: [sample] },
})

// ── A. 异常判据 ─────────────────────────────────────────────────────────

t('A1 设计稳态不报异常：全局闸关 + 大量待确认 + worker 正常出留痕 ⇒ anomalies=[]', () => {
  const a = computeAnomalies({
    st: {
      provisioned: true, engine_wired: true, last_run_at: iso(T0 - MIN),
      executions: { total: 200, last_24h: { awaiting_approval: 196, succeeded: 4 } },
    },
    statsStatus: 200, gates: GATE_OFF, recentRows: [awaiting(200, T0 - 2 * MIN)],
    execTotal: 200, prev: { ts: iso(T0 - 2 * H), stats: { executions_total: 190 } }, now: NOW,
  })
  eq(JSON.stringify(a), '[]', 'anomalies')
})

t('A2 ★ 历史事故复现：开关开着、last_run_at=null、零留痕 ⇒ 必须报（首版对此 10 轮全静默）', () => {
  const st = { provisioned: true, engine_wired: true, executions: { total: 0, last_24h: {} } }
  // 首轮（没有上一轮可比）。
  const first = computeAnomalies({ st, statsStatus: 200, gates: GATE_OFF, recentRows: [], execTotal: 0, prev: null, now: NOW })
  eq(has(first, 'worker_not_completing_rounds').length, 1, '首轮应报 worker 不出活')
  // 第二轮：总数仍是 0 ⇒ 再报「自上一轮无新留痕」。
  const second = computeAnomalies({
    st, statsStatus: 200, gates: GATE_OFF, recentRows: [], execTotal: 0,
    prev: { ts: iso(T0 - 2 * H), stats: { executions_total: 0, last_run_at: null }, anomalies: [] }, now: NOW,
  })
  eq(has(second, 'worker_not_completing_rounds').length, 1, 'worker 不出活')
  eq(has(second, 'no_new_executions_since_prev_sample').length, 1, '自上一轮无新留痕')
})

t('A3 last_run_at=null 但 2 分钟前刚有新留痕（蓝绿切换清零）⇒ 不算 worker 死', () => {
  const a = computeAnomalies({
    st: { provisioned: true, engine_wired: true, executions: { total: 12, last_24h: {} } },
    statsStatus: 200, gates: GATE_OFF, recentRows: [awaiting(12, T0 - 2 * MIN)],
    execTotal: 12, prev: { ts: iso(T0 - 2 * H), stats: { executions_total: 10 } }, now: NOW,
  })
  eq(has(a, 'worker_not_completing_rounds').length, 0, '不应报 worker 不出活')
  eq(has(a, 'no_new_executions_since_prev_sample').length, 0, '不应报无新留痕')
})

t('A4 ★ 灰度约束：全局闸关 + 自动化动了变更类动作 ⇒ CONSTRAINT_VIOLATION 逐条点名；只读/手动/skipped 不算', () => {
  const rows = [
    { id: 501, trigger_source: 'auto_analyze', status: 'succeeded', entry_id: 0, error_type: 'k', steps: [{ action: 'set_settings_kv', status: 'ok' }] },
    { id: 502, trigger_source: 'standard', status: 'succeeded', entry_id: 3, error_type: 'q', steps: [{ action: 'call_admin_api', params: { method: 'POST', path: '/api/admin/x' }, status: 'ok' }] },
    { id: 503, trigger_source: 'standard', status: 'succeeded', steps: [{ action: 'call_admin_api', params: { method: 'GET', path: '/api/admin/y' }, status: 'ok' }] },
    { id: 504, trigger_source: 'manual', status: 'succeeded', steps: [{ action: 'disable_pi_rule', status: 'ok' }] },
    { id: 505, trigger_source: 'auto_analyze', status: 'awaiting_approval', steps: [{ action: 'set_settings_kv', status: 'skipped' }] },
    { id: 506, trigger_source: 'standard', status: 'failed', steps: [{ action: 'set_settings_kv', status: 'failed' }] },
    { id: 507, trigger_source: 'refinement', status: 'succeeded', steps: '[{"action":"disable_pi_rule","status":"ok"}]' },
  ]
  eq(findMutatingExecutions(rows).map((v) => v.id).join(','), '501,502,506,507', '变更类留痕')
  const base = {
    st: { provisioned: true, engine_wired: true, last_run_at: iso(T0 - MIN), executions: { total: 7, last_24h: {} } },
    statsStatus: 200, recentRows: rows, execTotal: 7, prev: null, now: NOW,
  }
  const off = computeAnomalies({ ...base, gates: GATE_OFF })
  eq(has(off, 'CONSTRAINT_VIOLATION').length, 4, '闸关时的违反条数')
  ok(off.some((s) => s.includes('#501') && s.includes('set_settings_kv')), '应点名 #501 与动作')
  ok(off.some((s) => s.includes('#502') && s.includes('call_admin_api')), '应点名 #502（call_admin_api POST）')
  ok(!off.some((s) => s.includes('#503')), 'call_admin_api GET 不是变更类')
  ok(!off.some((s) => s.includes('#504')), '手动触发不受自动执行闸约束')
  ok(!off.some((s) => s.includes('#505')), 'skipped 没执行，不算')
  // 反向臂：闸开着时变更类执行是允许的，不报。
  eq(has(computeAnomalies({ ...base, gates: { ...GATE_OFF, auto_execute: true }, expectGlobalAutoExecute: true }), 'CONSTRAINT_VIOLATION').length, 0, '闸开时不应报')
})

t('A5 全局闸被打开（与灰度约定不符）⇒ gate_drift；约定本身可改', () => {
  const base = { st: { provisioned: true, engine_wired: true }, statsStatus: 200, recentRows: [], execTotal: 0, prev: null, now: NOW }
  const on = { enabled: true, auto_analyze: true, auto_execute: true }
  eq(has(computeAnomalies({ ...base, gates: on }), 'gate_drift:auto_execute=true').length, 1, '默认约定 false')
  eq(has(computeAnomalies({ ...base, gates: on, expectGlobalAutoExecute: true }), 'gate_drift').length, 0, '约定 true 时不算漂移')
})

t('A6 失败数取完整聚合：最近一页没有 failed，也按 /stats.executions.last_24h 报', () => {
  const a = computeAnomalies({
    st: { provisioned: true, engine_wired: true, last_run_at: iso(T0 - MIN), executions: { total: 300, last_24h: { failed: 3, awaiting_approval: 297 } } },
    statsStatus: 200, gates: GATE_OFF, recentRows: [awaiting(300, T0 - MIN)],
    execTotal: 300, prev: { ts: iso(T0 - 2 * H), stats: { executions_total: 290 } }, now: NOW,
  })
  eq(a.includes('failed_last_24h=3'), true, 'failed')
})

t('A7 闸读不到 ⇒ gate_unreadable（不静默当成 false）', () => {
  const a = computeAnomalies({ st: { provisioned: true, engine_wired: true }, statsStatus: 200, gates: { enabled: null, auto_analyze: null, auto_execute: null }, recentRows: [], execTotal: 0, prev: null, now: NOW })
  eq(a.includes('gate_unreadable:enabled'), true, 'enabled')
  eq(a.includes('gate_unreadable:auto_execute'), true, 'auto_execute')
})

t('A8 开关关着 ⇒ 不判「worker 不出活」（关着没留痕是正常的）', () => {
  const a = computeAnomalies({ st: { provisioned: true, engine_wired: true, executions: { total: 0, last_24h: {} } }, statsStatus: 200, gates: { enabled: false, auto_analyze: true, auto_execute: false }, recentRows: [], execTotal: 0, prev: { ts: iso(T0 - 2 * H), stats: { executions_total: 0 } }, now: NOW })
  eq(a.filter((s) => s.startsWith('worker_') || s.startsWith('no_new_')).length, 0, '不应报不出活')
})

// ── B. 收口口径 ─────────────────────────────────────────────────────────

// 12 行旧格式采样：每轮首页都是 98 待确认 + 2 成功（首版会把它们相加成 1200）。
const OLD_RECS = Array.from({ length: 12 }, (_, i) => ({
  ts: iso(Date.parse('2026-11-21T18:23:00Z') + i * 2 * H),
  stats: { executions_total: i * 10 },
  window_status_counts: { awaiting_approval: 98, succeeded: 2 },
  anomalies: [],
}))
const WINDOW_START_MS = Date.parse(START)
const FULL_ROWS = [
  ...Array.from({ length: 200 }, (_, i) => awaiting(i + 1, WINDOW_START_MS + i * MIN, i % 2 ? 7 : 2)),
  ...Array.from({ length: 6 }, (_, i) => ({ id: 1000 + i, trigger_source: 'auto_analyze', status: 'succeeded', entry_id: 0, error_type: 'k', created_at: iso(WINDOW_START_MS + 50 * H), steps: [{ action: 'prompt_analysis', status: 'ok' }] })),
]
const ENTRIES = new Map([
  ['2', { id: 2, error_type: 'rate_limit', title: '上游限流', source: 'seed', auto_execute: false }],
  ['7', { id: 7, error_type: 'concurrent', title: '并发限流', source: 'pi_analysis', auto_execute: true }],
])

t('B1 ★ 重复计数回归：收口数字只来自全量留痕，不跨轮相加', () => {
  const md = buildClosureSummary({ recs: OLD_RECS, snap: { identity: { git_sha: 'x' }, gates: GATE_OFF, stats: { total_entries: 2, entries_by_source: { seed: 1, pi_analysis: 1 } }, entries: [...ENTRIES.values()], executions: FULL_ROWS, truncated: false }, startIso: START, now: NOW })
  ok(md.includes('窗口内全量 206 行，未截断'), '全量行数')
  ok(md.includes('触发合计（窗口内全部留痕）= 206'), '触发合计')
  ok(md.includes('误报率代理 ② = awaiting_approval/触发合计 = 97.1%'), '代理②')
  ok(!md.includes('1200') && !md.includes('1176'), '不得出现跨轮相加的数')
})

t('B2 代理② 的说明随全局闸而变（关 ⇒ 明示不反映误报；开 ⇒ 置信度门口径）', () => {
  const snap = { identity: {}, stats: {}, entries: [], executions: FULL_ROWS, truncated: false }
  const off = buildClosureSummary({ recs: OLD_RECS, snap: { ...snap, gates: GATE_OFF }, startIso: START, now: NOW })
  const on = buildClosureSummary({ recs: OLD_RECS, snap: { ...snap, gates: { ...GATE_OFF, auto_execute: true } }, startIso: START, now: NOW })
  ok(off.includes('**不反映误报**'), '闸关时必须明示代理②不反映误报')
  ok(!on.includes('**不反映误报**') && on.includes('被置信度门 / 条目闸拦下待放行'), '闸开时才是置信度门口径')
})

t('B3 窗口外的留痕不计入', () => {
  const md = buildClosureSummary({ recs: [], snap: { identity: {}, gates: GATE_OFF, stats: {}, entries: [], executions: [...FULL_ROWS, awaiting(9999, WINDOW_START_MS - H)], truncated: false }, startIso: START, now: NOW })
  ok(md.includes('窗口内全量 206 行'), '起点之前的那条不计')
})

t('B4 采样缺口：识别 > 2.5h 的空洞；连续采样无缺口', () => {
  const contiguous = Array.from({ length: 5 }, (_, i) => ({ ts: iso(T0 - (5 - i) * 2 * H) }))
  eq(samplingGaps(contiguous).length, 0, '连续采样')
  const gapped = [...contiguous.slice(0, 3), { ts: iso(T0) }]
  const gaps = samplingGaps(gapped)
  eq(gaps.length, 1, '缺口数')
  eq(gaps[0].hours, 6, '缺口时长（h）')  // -6h → T0
  ok(buildClosureSummary({ recs: gapped, snap: { identity: {}, gates: GATE_OFF, stats: {}, entries: [], executions: [], truncated: false }, startIso: START, now: NOW }).match(/^- 采样缺口[^\n]*（6h）/m), '收口档要列出缺口')
})

t('B5 closureDue 真值表（≥24 轮 或 ≥48h）', () => {
  eq([closureDue(24, 10), closureDue(12, 49), closureDue(25, 0), closureDue(23, 47.9), closureDue(12, 47.9), closureDue(0, 0)].join(','), 'true,true,true,false,false,false', '真值表')
})

t('B6 人工判误报样本：按条目归并、去重、截断到 5 条、只取待确认行', () => {
  const rows = [
    awaiting(1, T0 - 9 * MIN, 7, 'a'), awaiting(2, T0 - 8 * MIN, 7, 'a'), awaiting(3, T0 - 7 * MIN, 7, 'b   c'),
    awaiting(4, T0 - 6 * MIN, 2, 'only-one'),
    { id: 5, trigger_source: 'manual', status: 'succeeded', entry_id: 9, created_at: iso(T0 - 5 * MIN), steps: [], context: { samples: ['must-not-appear'] } },
    ...Array.from({ length: 7 }, (_, i) => awaiting(10 + i, T0 - (4 - i) * MIN, 3, `s${i}`)),
  ]
  const out = labelingSample(rows, ENTRIES)
  eq(out.map((l) => l.entry_id).join(','), '3,7,2', '按待确认留痕数降序')
  eq(JSON.stringify(out.find((l) => l.entry_id === '7').samples), '["a","b c"]', '去重且压空白')
  eq(out.find((l) => l.entry_id === '3').samples.length, 5, '每条目最多 5 条')
  eq(out.find((l) => l.entry_id === '2').title, '上游限流', '带出条目标题')
  ok(!JSON.stringify(out).includes('must-not-appear'), '非待确认行不进样本')
})

t('B7 aggregateExecutions：executed 与 mutating_executed 的口径', () => {
  const agg = aggregateExecutions([
    awaiting(1, T0),
    { id: 2, trigger_source: 'auto_analyze', status: 'succeeded', entry_id: 0, steps: [{ action: 'prompt_analysis', status: 'ok' }] },
    { id: 3, trigger_source: 'standard', status: 'failed', steps: [{ action: 'record_note', status: 'failed' }] },
    { id: 4, trigger_source: 'standard', status: 'succeeded', steps: [{ action: 'set_settings_kv', status: 'ok' }] },
    { id: 5, trigger_source: 'manual', status: 'succeeded', steps: [{ action: 'disable_pi_rule', status: 'ok' }] },
  ])
  eq([agg.rows, agg.executed, agg.mutating_executed].join(','), '5,4,2', '总数/实际执行/其中变更类')
  eq(agg.by_status_trigger['auto_analyze|succeeded'], 1, '状态×触发源')
})

t('B8 触顶时如实标注截断，不谎称全量', () => {
  const md = buildClosureSummary({ recs: [], snap: { identity: {}, gates: GATE_OFF, stats: {}, entries: [], executions: [], truncated: true }, startIso: START, now: NOW })
  ok(md.includes('⚠ 已截断'), '截断标注')
})

t('B9 不挂条目 / 已删除条目的留痕有可读名', () => {
  const md = buildClosureSummary({ recs: [], snap: { identity: {}, gates: GATE_OFF, stats: {}, entries: [...ENTRIES.values()], executions: FULL_ROWS, truncated: false }, startIso: START, now: NOW })
  ok(md.includes('不挂条目（分析阶段）') && md.includes('条目 #2 上游限流（rate_limit）'), '条目命名')
})

// ── C. 小件 ─────────────────────────────────────────────────────────────

t('C1 parseArgs：无值开关 / 带值参数', () => {
  const a = parseArgs(['--dry-run', '--base', 'https://x/', '--expect-global-auto-execute', 'true', '--closure-preview'])
  eq(JSON.stringify(a), JSON.stringify({ 'dry-run': true, base: 'https://x/', 'expect-global-auto-execute': 'true', 'closure-preview': true }), '解析结果')
  eq(Object.values(parseArgs(['--out', '--dry-run'])).includes('--dry-run'), false, '开关不得被当成前一个参数的值')
})

t('C2 stepActions：steps 是 JSON 字符串 / 非数组 / 坏 JSON 都不抛', () => {
  eq(stepActions({ steps: '[{"action":"record_note","status":"ok"}]' }).length, 1, 'JSON 字符串')
  eq(stepActions({ steps: 'nope' }).length, 0, '坏 JSON')
  eq(stepActions({ steps: { a: 1 } }).length, 0, '非数组')
  eq(stepActions({}).length, 0, '缺失')
})

t('C3 isMutatingStep 与后端同口径', () => {
  eq([isMutatingStep({ action: 'set_settings_kv' }), isMutatingStep({ action: 'disable_pi_rule' }), isMutatingStep({ action: 'record_note' })].join(','), 'true,true,false', '变更类原语')
  eq([isMutatingStep({ action: 'call_admin_api', params: { method: 'POST' } }), isMutatingStep({ action: 'call_admin_api', params: { method: 'get' } }), isMutatingStep({ action: 'call_admin_api', params: { method: 'OPTIONS' } }), isMutatingStep({ action: 'call_admin_api' })].join(','), 'true,false,false,true', 'call_admin_api 看方法（缺方法按变更）')
})

t('C4 newestExecutionAt：+08:00 与 Z 两种时间串混排取最新；无行 ⇒ null', () => {
  eq(newestExecutionAt([{ created_at: '2026-11-23T07:10:00Z' }, { created_at: '2026-11-23T15:32:15+08:00' }]), Date.parse('2026-11-23T07:32:15Z'), '最新时间')
  eq(newestExecutionAt([]), null, '无行')
  eq(newestExecutionAt([{ created_at: 'garbage' }]), null, '坏时间')
})

// ── 输出（与 selftest.mjs 同格式） ──────────────────────────────────────

let bad = 0
for (const c of cases) {
  console.log(`  ${c.ok ? '✓' : '✗'} ${c.name}${c.ok ? '' : '  —— ' + c.err}`)
  if (!c.ok) bad++
}
console.log(`\nSELFTEST: ${bad === 0 ? 'PASS' : `FAIL（${bad} / ${cases.length}）`}`)
process.exit(bad === 0 ? 0 : 1)
