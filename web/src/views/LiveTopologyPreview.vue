<script setup lang="ts">
// LiveTopologyPreview.vue —— **仅 DEV** 的实时拓扑夹具预览页（2026-10-16）
//
// 为什么要它：拓扑页的第一验收标准是「够炫够直观」，这条判据没法用
// 单测表达；而 `/live` 需要登录态 + 真实 SSE 才能看到数据。仓库里已有同款
// 做法（views/DispatchWaterfallPreview.vue，路由只在 import.meta.env.DEV 注册），
// 这里照抄：直接把夹具写进 liveStreamStore 的单例状态，组件照常跑
// 过滤/布局/动画/降级全链路，不加任何生产代码分支。
//
// 生产构建里这个文件不会被路由引用（vite 按路由 chunk 收集），也不注册菜单。

import { onMounted, onUnmounted } from 'vue'
import LiveTopologyView from './LiveTopologyView.vue'
import { liveStreamState } from '../composables/liveStreamStore'
import type { ActionEvent, LiveNodeStatus, LiveRequest } from '../composables/liveStreamStore'

const T0 = Date.now()

function iso(offsetMs: number): string {
  return new Date(T0 - offsetMs).toISOString()
}

const NODES: LiveNodeStatus[] = [
  { credential_id: 7, manual_disabled: false, credential_label: 'ds-key-a', provider_code: 'deepseek', health_status: 'healthy', circuit_state: 'closed', availability_state: 'available', quota_state: 'ok', in_flight: 3, last_latency_ms: 820, raw_models: ['deepseek-chat'] },
  { credential_id: 8, manual_disabled: false, credential_label: 'mm-key-b', provider_code: 'moonshot', health_status: 'healthy', circuit_state: 'closed', availability_state: 'available', quota_state: 'ok', in_flight: 1, last_latency_ms: 640 },
  { credential_id: 9, manual_disabled: false, credential_label: 'qz-key-c', provider_code: 'qianwen', health_status: 'warning', circuit_state: 'half_open', availability_state: 'available', quota_state: 'quota_low', in_flight: 0, last_latency_ms: 2400 },
  { credential_id: 10, manual_disabled: false, credential_label: 'zs-key-d', provider_code: 'zhipu', health_status: 'unreachable', circuit_state: 'open', availability_state: 'unavailable', quota_state: 'ok', in_flight: 0, last_error: 'upstream_timeout' },
  { credential_id: 11, manual_disabled: false, credential_label: 'ds-key-e', provider_code: 'deepseek', health_status: 'healthy', circuit_state: 'closed', availability_state: 'available', quota_state: 'ok', in_flight: 0 },
  // 内部直连节点（2026-10-28 选路层轮）：本地托管供应商，供直连徽标/分桶验收。
  { credential_id: 21, manual_disabled: false, credential_label: 'local-no-key', provider_code: 'local-ollama', health_status: 'healthy', circuit_state: 'closed', availability_state: 'available', quota_state: 'ok', in_flight: 1, last_latency_ms: 950 },
]

const CLIENTS = [
  { agent_name: 'claude-code', client_protocol: 'anthropic', n: 34 },
  { agent_name: 'codex', client_protocol: 'openai', n: 21 },
  { agent_name: 'curl', client_protocol: 'openai', n: 12 },
  { agent_name: 'zcode', client_protocol: 'anthropic', n: 7 },
]

const CHAINS = [
  { provider_code: 'deepseek', credential_id: 7, credential_label: 'ds-key-a', canonical_name: 'deepseek-chat', n: 30, health: 'healthy' },
  { provider_code: 'moonshot', credential_id: 8, credential_label: 'mm-key-b', canonical_name: 'moonshot-v1-128k', n: 22, health: 'healthy' },
  { provider_code: 'qianwen', credential_id: 9, credential_label: 'qz-key-c', canonical_name: 'qwen-max', n: 13, health: 'healthy' },
  { provider_code: 'zhipu', credential_id: 10, credential_label: 'zs-key-d', canonical_name: 'glm-4.5', n: 9, health: 'failure' },
]

function buildRequests(): LiveRequest[] {
  const out: LiveRequest[] = []
  let i = 0
  for (const client of CLIENTS) {
    for (let k = 0; k < client.n; k += 1) {
      const chain = CHAINS[(i * 7 + k * 3) % CHAINS.length]
      const isFail = chain.health === 'failure' && k % 4 !== 0
      const ts = iso(1000 + i * 900 + k * 320)
      out.push({
        type: 'request',
        request_id: `preview-${i}-${k}`,
        ts,
        model: chain.canonical_name,
        canonical_name: chain.canonical_name,
        model_category: 'domestic',
        provider_code: chain.provider_code,
        credential_id: chain.credential_id,
        credential_label: chain.credential_label,
        status: isFail ? 'failure' : 'success',
        latency_ms: isFail ? 5000 + k * 40 : 420 + ((k * 137) % 1800),
        prompt_tokens: 600 + ((k * 91) % 3200),
        completion_tokens: 120 + ((k * 37) % 900),
        total_tokens: 720 + ((k * 128) % 4100),
        cost_usd: 0.0021 * (k + 1),
        error_kind: isFail ? 'upstream_timeout' : null,
        failure_stage: isFail ? 'upstream' : null,
        agent_name: client.agent_name,
        agent_type: 'cli',
        client_protocol: client.client_protocol,
        is_probe: k % 11 === 0,
        // 2026-10-28 选路层轮：让选路扇出带与压缩格有非零读数可验收。
        // 每 7 条里 1 条 auto（带任务类型分布）；压缩按环回口径标
        // compressed（2026-10-28 遗留②：压缩格计数判据从 token_band 档位
        // 换成「压缩层实际改写出站体」——每 6 条里 1 条实际压缩）。
        ...(k % 7 === 3
          ? { is_auto_request: true, task_type: (['code', 'chat', 'reasoning'] as const)[k % 3], client_model: chain.canonical_name }
          : {}),
        ...(k % 6 === 2
          ? { compressed: true, token_band: k % 12 === 2 ? ('below' as const) : ('forced' as const) }
          : {}),
      } satisfies LiveRequest)
      i += 1
    }
  }
  // 内部直连链（2026-10-28 选路层轮）：本地托管供应商（kind='local'）的
  // 请求 + 对应 node_update —— 验收「直连」扇出分桶与供应端节点直连徽标。
  // ★ 直连判定靠 providers.kind，DEV 夹具页没有后端可查 —— 由本页在挂载时
  //   向 useLiveTopology 注入一个 dev-only 集合（见下方 loadLocalProviders 桩）。
  for (let k = 0; k < 5; k += 1) {
    out.push({
      type: 'request', request_id: `preview-local-${k}`, ts: iso(800 + k * 640),
      model: k % 2 ? 'qwen3:30b-a3b' : 'qwen3.8:27b-mlx',
      canonical_name: k % 2 ? 'qwen3:30b-a3b' : 'qwen3.8:27b-mlx',
      provider_code: 'local-ollama', credential_id: 21, credential_label: 'local-no-key',
      status: 'success', latency_ms: 900 + k * 120,
      prompt_tokens: 400, completion_tokens: 180, total_tokens: 580,
      agent_name: 'zcode', client_protocol: 'openai',
    } satisfies LiveRequest)
  }
  // 两条在途请求：让「进行中」状态与在途环有东西可画
  out.push({
    type: 'request', request_id: 'preview-inflight-1', ts: iso(200), model: 'deepseek-chat',
    canonical_name: 'deepseek-chat', provider_code: 'deepseek', credential_id: 7,
    credential_label: 'ds-key-a', status: 'in_progress', latency_ms: null,
    total_tokens: 1500, agent_name: 'claude-code', client_protocol: 'anthropic',
  } satisfies LiveRequest)
  out.push({
    type: 'request', request_id: 'preview-inflight-2', ts: iso(120), model: 'qwen-max',
    canonical_name: 'qwen-max', provider_code: 'qianwen', credential_id: 9,
    credential_label: 'qz-key-c', status: 'in_progress', latency_ms: null,
    total_tokens: 2400, agent_name: 'codex', client_protocol: 'openai',
  } satisfies LiveRequest)
  return out
}

function buildActions(): Map<string, ActionEvent[]> {
  const map = new Map<string, ActionEvent[]>()
  const target = liveStreamState.requests.find((r) => r.credential_id === 10)?.request_id
  if (!target) return map
  map.set(target, [
    { seq: 1, action: 'auth', stage_category: 'routing', status: 'ok', ts: iso(3000) },
    { seq: 2, action: 'enqueue', stage_category: 'routing', queue_depth: 3, status: 'ok' },
    { seq: 3, action: 'resolve', stage_category: 'routing', auto_decision: 'weight', credential_id: 10, credential_label: 'zs-key-d', blocked_reasons: ['quota_cap_near'] },
    { seq: 4, action: 'upstream', stage_category: 'llm', credential_id: 10, ttfb_ms: 2100, error_kind: 'upstream_timeout' },
    { seq: 5, action: 'fallback', stage_category: 'retrying', from_credential_id: 10, to_credential_id: 7, credential_label: 'ds-key-a', reason: 'circuit_open' },
    { seq: 6, action: 'terminal', stage_category: 'terminal', status: 'failure', error_kind: 'upstream_timeout' },
  ])
  return map
}

let restore: (() => void) | null = null

/** DEV 夹具页没有后端可查 provider kinds（kind='local' 判定直连分桶）。
 *  只拦这一个端点、回夹具 JSON；卸载时还原 —— 不在生产代码加 dev 分支。
 *  ★ 在 **setup 顶层**装（不是 onMounted）：LiveTopologyView 是子组件，
 *    它的 onMounted（loadLocalProviders 发 fetch）先于本页 onMounted 执行，
 *    装晚了拦截就是死的（请求已飞）。 */
const unpatchKinds = patchProviderKindsFetch()

function patchProviderKindsFetch(): () => void {
  const raw = window.fetch.bind(window)
  window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.includes('/api/admin/topology/provider-kinds')) {
      return Promise.resolve(
        new Response(
          JSON.stringify({ items: [{ code: 'local-ollama', kind: 'local' }, { code: 'local-mlx-lm', kind: 'local' }] }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        ),
      )
    }
    return raw(input, init)
  }) as typeof window.fetch
  return () => { window.fetch = raw }
}

onMounted(() => {
  const snapshot = {
    requests: [...liveStreamState.requests],
    nodes: [...liveStreamState.nodes],
    actions: new Map(liveStreamState.actions),
    connection: liveStreamState.connection,
    initialDataReceived: liveStreamState.initialDataReceived,
    lastEventAt: liveStreamState.lastEventAt,
  }
  liveStreamState.requests = buildRequests() as never
  liveStreamState.nodes = NODES as never
  liveStreamState.actions = buildActions() as never
  liveStreamState.connection = 'open'
  liveStreamState.initialDataReceived = true
  restore = () => {
    liveStreamState.requests = snapshot.requests as never
    liveStreamState.nodes = snapshot.nodes as never
    liveStreamState.actions = snapshot.actions as never
    liveStreamState.connection = snapshot.connection
    liveStreamState.initialDataReceived = snapshot.initialDataReceived
    liveStreamState.lastEventAt = snapshot.lastEventAt
    unpatchKinds()
  }
})

onUnmounted(() => restore?.())
</script>

<template>
  <LiveTopologyView />
</template>
