// liveTopologyFilter.test.ts — 过滤判据（2026-10-17 多选版）
//
// 多选语义是本轮改版的新契约，必须钉死三条：
//   · 同维度 OR（选了 A、B，命中其一即保留）
//   · 跨维度 AND（各维都命中才保留）
//   · 空选 = 该维不过滤
// 语义漂移的症状是「图上少了一半、右栏又多一半」，很难自查，所以写在这里。
//
// 「过滤后为空」有两种成因必须分开判：
//   (a) 真的没有请求；(b) 过滤器把它筛没了。
// 只要有一条允许侧断言，就必须同时钉一条「前提仍在场」的断言
// （判据纪律第 3 条的实证：只测允许侧会漏掉「压根没匹配上」）。

import { describe, expect, it } from 'vitest'
import { applyFilters, filterOptions, gatewayModuleStats, localProviderCodesOf, matchesFilters, modelSelectionStats } from '../lib/liveTopologyFilter'
import { DEFAULT_TOPO_FILTERS, GATEWAY_MODULES } from '../types/liveTopology'

const NOW = Date.parse('2026-10-17T12:00:00Z')

const base = {
  ts: new Date(NOW - 1_000).toISOString(),
  request_id: 'r1',
  status: 'success',
  provider_code: 'deepseek',
  credential_id: 7,
  canonical_name: 'glm-4.5',
  agent_name: 'claude-code',
}

const filters = (over: Partial<typeof DEFAULT_TOPO_FILTERS> = {}) => ({ ...DEFAULT_TOPO_FILTERS, ...over })

describe('matchesFilters', () => {
  it('passes everything when no filter is set', () => {
    expect(matchesFilters(base, filters(), NOW)).toBe(true)
  })

  it('treats multiple values in one dimension as OR', () => {
    expect(matchesFilters(base, filters({ providers: ['deepseek', 'moonshot'] }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ providers: ['moonshot', 'qianwen'] }), NOW)).toBe(false)
    expect(matchesFilters(base, filters({ clients: ['codex', 'claude-code'] }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ models: ['kimi', 'glm-4.5'] }), NOW)).toBe(true)
  })

  it('combines dimensions with AND', () => {
    const f = filters({ providers: ['deepseek'], clients: ['codex', 'claude-code'], models: ['kimi', 'glm-4.5'] })
    expect(matchesFilters(base, f, NOW)).toBe(true)
    // 每一维单独看都命中，只有模型这一维不命中 → 整体不通过
    expect(matchesFilters(base, filters({ providers: ['deepseek'], models: ['kimi'] }), NOW)).toBe(false)
  })

  it('matches providers by catalog code and by credential node', () => {
    expect(matchesFilters(base, filters({ providers: ['cred:7'] }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ providers: ['cred:9'] }), NOW)).toBe(false)
  })

  it('filters by credential node (node axis is stricter than provider)', () => {
    expect(matchesFilters(base, filters({ nodes: ['cred:7'] }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ nodes: ['cred:8', 'cred:7'] }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ nodes: ['cred:8'] }), NOW)).toBe(false)
  })

  it('filters by status', () => {
    expect(matchesFilters({ ...base, status: 'failure' }, filters({ status: 'failure' }), NOW)).toBe(true)
    expect(matchesFilters(base, filters({ status: 'failure' }), NOW)).toBe(false)
  })

  it('excludes requests outside the time window', () => {
    const old = { ...base, ts: new Date(NOW - 60 * 60_000).toISOString() }
    expect(matchesFilters(old, filters(), NOW)).toBe(false)
    // ★ 前提仍在场：同一条数据放大窗口就该回来 —— 防止「永远 false」的假绿
    expect(matchesFilters(old, filters({ windowMs: 2 * 60 * 60_000 }), NOW)).toBe(true)
  })

  it('keeps frames without a timestamp rather than dropping them silently', () => {
    expect(matchesFilters({ ...base, ts: undefined }, filters(), NOW)).toBe(true)
  })

  // ── 2026-10-18 数据开关（默认不选 = 默认隐藏）──────────────────────
  // 每条隐藏开关都配「打开就回来」的前提在场断言，防「永远 false」的假绿。
  it('★ hides probe requests by default and shows them when the switch is on', () => {
    const probe = { ...base, request_id: 'probe-1', is_probe: true }
    expect(matchesFilters(probe, filters(), NOW)).toBe(false)
    expect(matchesFilters(probe, filters({ showProbe: true }), NOW)).toBe(true)
    // 前提在场：同一批数据里非探测请求默认可见（隐藏是选择性的，不是全灭）
    expect(matchesFilters(base, filters(), NOW)).toBe(true)
  })

  it('★ hides requests routed through invalid nodes by default', () => {
    const hidden = new Set(['cred:7'])
    expect(matchesFilters(base, filters(), NOW, hidden)).toBe(false)
    expect(matchesFilters(base, filters({ showInvalidNodes: true }), NOW, hidden)).toBe(true)
    // 经**其他**节点的请求不受影响（选择性隐藏，不是全灭）
    expect(matchesFilters({ ...base, credential_id: 8 }, filters(), NOW, hidden)).toBe(true)
    // 没有 provider 节点的请求（未派发）不因无效节点集合被隐藏
    expect(matchesFilters({ ...base, credential_id: 0, provider_code: '' }, filters(), NOW, hidden)).toBe(true)
    // 空集合 = 没有无效节点，一切照旧
    expect(matchesFilters(base, filters(), NOW, new Set())).toBe(true)
  })
})

describe('applyFilters', () => {
  it('converges every axis at once (graph + both panels share one subset)', () => {
    const requests = [
      base,
      { ...base, request_id: 'r2', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
      { ...base, request_id: 'r3', status: 'failure' },
    ]
    expect(applyFilters(requests, filters(), NOW)).toHaveLength(3)
    expect(applyFilters(requests, filters({ providers: ['deepseek'] }), NOW)).toHaveLength(2)
    expect(applyFilters(requests, filters({ providers: ['deepseek'], status: 'failure' }), NOW)).toHaveLength(1)
    expect(applyFilters(requests, filters({ providers: ['deepseek'], clients: ['codex'] }), NOW)).toHaveLength(0)
  })

  it('ORs inside one dimension across a list', () => {
    const requests = [
      base,
      { ...base, request_id: 'r2', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
      { ...base, request_id: 'r3', provider_code: 'qianwen', credential_id: 9, canonical_name: 'qwen', agent_name: 'zcode' },
    ]
    expect(applyFilters(requests, filters({ providers: ['deepseek', 'moonshot'] }), NOW).map((r) => r.request_id)).toEqual(['r1', 'r2'])
  })

  it('★ default filters remove probes and invalid-node traffic in one pass', () => {
    // owner 2026-10-18「默认去除」的端到端口径：默认态一次过滤后
    // 探测与无效节点流量都不在，打开开关后各回各位。
    const requests = [
      base,
      { ...base, request_id: 'probe-1', is_probe: true },
      { ...base, request_id: 'r3', credential_id: 9 },
    ]
    const hidden = new Set(['cred:9'])
    expect(applyFilters(requests, filters(), NOW, hidden).map((r) => r.request_id)).toEqual(['r1'])
    expect(applyFilters(requests, filters({ showProbe: true }), NOW, hidden).map((r) => r.request_id)).toEqual(['r1', 'probe-1'])
    expect(applyFilters(requests, filters({ showInvalidNodes: true }), NOW, hidden).map((r) => r.request_id)).toEqual(['r1', 'r3'])
    expect(applyFilters(requests, filters({ showProbe: true, showInvalidNodes: true }), NOW, hidden)).toHaveLength(3)
  })
})

describe('filterOptions', () => {
  it('offers four independent dimensions with hit counts, most active first', () => {
    const requests = [
      base,
      { ...base, request_id: 'r2', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
      { ...base, request_id: 'r3', provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi', agent_name: 'codex' },
    ]
    const options = filterOptions(requests, NOW, 5 * 60_000)
    expect(options.providers).toContainEqual({ value: 'moonshot', label: 'moonshot', count: 2 })
    expect(options.providers).toContainEqual({ value: 'deepseek', label: 'deepseek', count: 1 })
    expect(options.clients[0]).toMatchObject({ value: 'codex', count: 2 })
    expect(options.models[0]).toMatchObject({ value: 'kimi', count: 2 })
    // 节点维度是凭据级，value 形如 cred:<id>，与供应端节点 id 同构
    expect(options.nodes.map((n) => n.value).sort()).toEqual(['cred:7', 'cred:8'])
  })

  it('ignores out-of-window traffic when listing candidates', () => {
    const old = { ...base, ts: new Date(NOW - 60 * 60_000).toISOString() }
    const options = filterOptions([old], NOW, 5 * 60_000)
    expect(options.clients).toHaveLength(0)
    expect(options.providers).toHaveLength(0)
    expect(options.nodes).toHaveLength(0)
  })
})

describe('gatewayModuleStats', () => {
  const WINDOW = 5 * 60_000

  it('covers all nine modules with zero defaults', () => {
    const stats = gatewayModuleStats([], NOW, WINDOW, 0)
    expect(Object.keys(stats).sort()).toEqual(GATEWAY_MODULES.map((m) => m.key).sort())
    for (const mod of GATEWAY_MODULES) {
      expect(stats[mod.key]).toEqual({ count: 0, errors: 0 })
    }
  })

  it('counts the main pipeline per request and the bypasses per hit', () => {
    const requests = [
      { ...base, gw_session_id: 's-1', cache_read_tokens: 120 },
      { ...base, request_id: 'r2', status: 'failure', error_kind: 'upstream_timeout', gw_session_id: 's-2', is_probe: true, requestType: 'probe' },
      { ...base, request_id: 'r3' },
    ]
    const stats = gatewayModuleStats(requests, NOW, WINDOW, 2)

    // 主链路：每条请求都经过
    expect(stats.sanitize.count).toBe(3)
    expect(stats.route.count).toBe(3)
    expect(stats.modelSelect.count).toBe(3)
    // 会话写入/文件：只有带 gw_session_id 的两条
    expect(stats.sessionWrite.count).toBe(2)
    expect(stats.sessionFile.count).toBe(2)
    // 缓存：**命中**记在 count（只有命中才有可看的量）
    expect(stats.cache.count).toBe(1)
    // 旁路：失败数、探测数、非 main 的任务数
    expect(stats.errorRecord.count).toBe(1)
    expect(stats.probeTask.count).toBe(1)
    expect(stats.taskDetect.count).toBe(1)
    // 节点状态管理 = 上报的节点数
    expect(stats.nodeState.count).toBe(2)
  })

  it('★ compress counts only requests the compression layer actually rewrote (compressed flag)', () => {
    // 环回口径（2026-10-28 修正）：判据是 compressed（strategy 非空且非
    // delta_append），不是 token_band 档位。band=below + compressed 的请求
    // （COUNT/相对窗口/IDLE 触发，211 实测 7 天 1069 条）必须计入；
    // band=forced 但没改成的（summary 失败，实测 80 条）不计。
    const requests = [
      { ...base, compressed: true },                                  // 环回压缩（band 任意）
      { ...base, request_id: 'r2', compressed: true, token_band: 'below' }, // below 但实际压缩 ⇒ 计
      { ...base, request_id: 'r3', token_band: 'forced' },             // 档位越线没改成 ⇒ 不计
      { ...base, request_id: 'r4' },                                  // 未上报 ⇒ 不计
    ]
    const stats = gatewayModuleStats(requests, NOW, 5 * 60_000, 0)
    expect(stats.compress.count).toBe(2)
  })

  it('★ never reports a cache hit as an error', () => {
    // 缓存命中是**成功**信号。早先版本把它累加进 `errors`，而 GatewayCoreBox
    // 用 `errors > 0` 决定画红 —— 于是「缓存工作正常」显示成「缓存故障」。
    // 这条判据钉的是类型契约（errors 只装失败/异常数）：
    // 把命中改回 errors 位，它立刻变红。
    const hits = [
      { ...base, cache_read_tokens: 120 },
      { ...base, request_id: 'r2', cache_write_tokens: 64 },
    ]
    const stats = gatewayModuleStats(hits, NOW, WINDOW, 0)
    expect(stats.cache.errors).toBe(0)
    expect(stats.cache.count).toBe(2)
  })

  it('★ only counts real failures in errors', () => {
    // 成功与在途请求都不许让任何模块变红：errors 应当只在真有失败时非零。
    const healthy = gatewayModuleStats(
      [{ ...base, status: 'success' }, { ...base, request_id: 'r2', status: 'in_progress' }],
      NOW,
      WINDOW,
      0,
    )
    for (const mod of GATEWAY_MODULES) {
      expect(healthy[mod.key].errors).toBe(0)
    }
  })

  it('ignores out-of-window requests', () => {
    const old = { ...base, ts: new Date(NOW - 60 * 60_000).toISOString() }
    const stats = gatewayModuleStats([old], NOW, WINDOW, 0)
    expect(stats.route.count).toBe(0)
  })

  it('★ does not fabricate a task count when the wire omits requestType', () => {
    // 线上字段是驼峰 requestType。早先误读 request_type 时，这条判据是红的：
    // 若有人再写回下划线形式，taskDetect 会恒为 0 而其余判据依然全绿。
    const stats = gatewayModuleStats([{ ...base, requestType: 'title' }], NOW, WINDOW, 0)
    expect(stats.taskDetect.count).toBe(1)
    // 没带该字段 = 未上报，保持 0 而不是编一个数
    expect(gatewayModuleStats([base], NOW, WINDOW, 0).taskDetect.count).toBe(0)
  })
})

// ── 选路扇出统计（2026-10-28 选路层轮）──────────────────────────────
// 判据钉三件事：三分桶互斥且守恒（auto+direct+explicit == total）；
// auto 的任务分布按计数降序取 top；供应商清单未测量时 direct 如实为 0
// 且不许把「未测量」伪装成「分拣完成」。
describe('modelSelectionStats', () => {
  const WINDOW = 5 * 60_000

  it('buckets mutually exclusively and total is conserved', () => {
    const requests = [
      { ...base, is_auto_request: true, task_type: 'code' },
      { ...base, request_id: 'r2', is_auto_request: true, task_type: 'chat' },
      { ...base, request_id: 'r3', provider_code: 'local-ollama' },
      { ...base, request_id: 'r4', provider_code: 'local-mlx-lm' },
      { ...base, request_id: 'r5' },
    ]
    const local = new Set(['local-ollama', 'local-mlx-lm', 'ollama', 'mlx'])
    const stats = modelSelectionStats(requests, NOW, WINDOW, local)
    expect(stats.auto).toBe(2)
    expect(stats.direct).toBe(2)
    expect(stats.explicit).toBe(1)
    expect(stats.total).toBe(5)
    // 守恒等式：三分桶之和 == 总量（分拣漏一条立刻红）
    expect(stats.auto + stats.direct + stats.explicit).toBe(stats.total)
  })

  it('ranks auto task types by count and caps the list', () => {
    const requests = [
      { ...base, is_auto_request: true, task_type: 'chat' },
      { ...base, request_id: 'r2', is_auto_request: true, task_type: 'code' },
      { ...base, request_id: 'r3', is_auto_request: true, task_type: 'code' },
      { ...base, request_id: 'r4', is_auto_request: true, task_type: 'reasoning' },
    ]
    const stats = modelSelectionStats(requests, NOW, WINDOW, undefined, 2)
    expect(stats.autoTasks.map((x) => x.type)).toEqual(['code', 'chat'])
    expect(stats.autoTasks[0].count).toBe(2)
  })

  it('★ leaves direct at 0 (not fabricated) when the provider list is unmeasured', () => {
    // 供应商清单没拉到 = 「未测量」，不是「没有直连」。此时直连请求会落进
    // explicit 桶（调用方必须据此把扇出带标注「未测量」），但守恒式仍成立。
    const requests = [
      { ...base, provider_code: 'local-ollama' },
      { ...base, request_id: 'r2' },
    ]
    const stats = modelSelectionStats(requests, NOW, WINDOW, undefined)
    expect(stats.direct).toBe(0)
    expect(stats.explicit).toBe(2)
    expect(stats.auto + stats.direct + stats.explicit).toBe(stats.total)
  })

  it('ignores out-of-window requests', () => {
    const old = { ...base, ts: new Date(NOW - 60 * 60_000).toISOString(), is_auto_request: true }
    const stats = modelSelectionStats([old], NOW, WINDOW, new Set())
    expect(stats.total).toBe(0)
  })
})

describe('localProviderCodesOf', () => {
  it('collects only kind=local provider codes (case/whitespace tolerant)', () => {
    const codes = localProviderCodesOf([
      { code: 'deepseek', kind: 'cloud' },
      { code: 'ollama', kind: 'local' },
      { code: 'local-mlx-lm', kind: ' Local ' },
      { code: 'no-kind-provider' },
    ])
    expect([...codes].sort()).toEqual(['local-mlx-lm', 'ollama'])
  })
})
