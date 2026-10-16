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
      } satisfies LiveRequest)
      i += 1
    }
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
  }
})

onUnmounted(() => restore?.())
</script>

<template>
  <LiveTopologyView />
</template>
