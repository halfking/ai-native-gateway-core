<script setup lang="ts">
import { computed } from 'vue'
import type { RoutingCandidate } from '../api/routing'

const props = defineProps<{
  candidate: RoutingCandidate | null
  loading?: boolean
}>()

interface FlagRow {
  key: string
  label: string
  value: string | number | boolean | null
  severity: 'ok' | 'warn' | 'danger' | 'info'
  description: string
  source: string
}

const fmtTime = (iso: string | null | undefined): string => {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

const isActive = (s: string | null | undefined) => !!s && s === 'active'
const isReady = (s: string | null | undefined) => !!s && s === 'ready'
const isOk = (s: string | null | undefined) => !!s && (s === 'ok' || s === 'closed')

const groups = computed(() => {
  const c = props.candidate
  if (!c) return [] as Array<{ title: string; rows: FlagRow[] }>
  return [
    {
      title: '可用性',
      rows: [
        {
          key: 'available',
          label: '可用（综合）',
          value: c.available ? '可用' : '不可用',
          severity: c.available ? 'ok' : 'danger',
          description: '依据 v_routable_credential_models.is_routable 综合判定',
          source: 'v_routable_credential_models.is_routable',
        },
        {
          key: 'runtime_routable',
          label: '运行时可路由',
          value: c.runtime_routable ? '是' : '否',
          severity: c.runtime_routable ? 'ok' : 'danger',
          description: '凭据维度运行时可达性（粘性 / 并发锁等）',
          source: 'cmb.runtime_routable',
        },
        {
          key: 'block_reason',
          label: '阻断原因',
          value: c.runtime_block_reason || c.block_reason || '—',
          severity: c.runtime_block_reason || c.block_reason ? 'danger' : 'ok',
          description: '不可路由根因代码',
          source: 'v_routable_credential_models.unavailable_reason',
        },
      ],
    },
    {
      title: '凭据 / Provider',
      rows: [
        {
          key: 'credential_status',
          label: '凭据状态',
          value: c.credential_status || '—',
          severity: isActive(c.credential_status) ? 'ok' : 'danger',
          description: 'active=启用',
          source: 'credentials.status',
        },
        {
          key: 'lifecycle_status',
          label: '生命周期',
          value: c.lifecycle_status || '—',
          severity: isActive(c.lifecycle_status) ? 'ok' : 'danger',
          description: 'active=在用',
          source: 'credentials.lifecycle_status',
        },
        {
          key: 'availability_state',
          label: '可用性状态',
          value: c.availability_state || '—',
          severity: isReady(c.availability_state) ? 'ok' : 'danger',
          description: 'ready / cooling / unreachable 等',
          source: 'credentials.availability_state',
        },
        {
          key: 'availability_recover_at',
          label: '可用性恢复时间',
          value: fmtTime(c.availability_recover_at),
          severity: 'info',
          description: '预计自动恢复时间',
          source: 'credentials.availability_recover_at',
        },
        {
          key: 'provider_enabled',
          label: 'Provider 启用',
          value: c.provider_enabled ? '是' : '否',
          severity: c.provider_enabled ? 'ok' : 'danger',
          description: '供应商启用状态',
          source: 'providers.enabled',
        },
      ],
    },
    {
      title: '配额 / 熔断',
      rows: [
        {
          key: 'quota_state',
          label: '配额状态',
          value: c.quota_state || '—',
          severity: isOk(c.quota_state) ? 'ok' : (c.quota_state === 'low' ? 'warn' : 'danger'),
          description: 'ok / low / exhausted',
          source: 'credentials.quota_state',
        },
        {
          key: 'balance_usd',
          label: '余额 (USD)',
          value: c.balance_usd ?? '—',
          severity: Number(c.balance_usd) > 5 ? 'ok' : (Number(c.balance_usd) > 0 ? 'warn' : 'danger'),
          description: '最近账单同步余额',
          source: 'credentials.balance_usd',
        },
        {
          key: 'circuit_state',
          label: '熔断器',
          value: c.circuit_state || 'closed',
          severity: isOk(c.circuit_state) ? 'ok' : 'danger',
          description: 'closed / open / half_open',
          source: 'credentials.circuit_state',
        },
        {
          key: 'cooling_until',
          label: '冷却至',
          value: fmtTime(c.cooling_until),
          severity: 'info',
          description: '熔断冷却截止',
          source: 'credentials.cooling_until',
        },
        {
          key: 'consecutive_failures',
          label: '连续失败',
          value: c.credential_consecutive_failures ?? c.consecutive_failures ?? 0,
          severity: (c.credential_consecutive_failures ?? c.consecutive_failures ?? 0) === 0 ? 'ok' : 'warn',
          description: '凭据级连续失败计数',
          source: 'credentials.consecutive_failures',
        },
      ],
    },
  ]
})
</script>

<template>
  <div v-if="loading && !candidate" class="nd-avail-loading" role="status">可用性加载中…</div>
  <p v-else-if="!candidate" class="nd-avail-empty">暂无路由候选可用性数据。</p>
  <div v-else class="nd-avail">
    <section v-for="group in groups" :key="group.title" class="nd-avail-group">
      <h3>{{ group.title }}</h3>
      <ul class="nd-avail-list">
        <li v-for="row in group.rows" :key="row.key" class="nd-avail-row" :data-severity="row.severity">
          <div class="nd-avail-head">
            <span class="nd-avail-label">{{ row.label }}</span>
            <span class="nd-avail-value" :class="`sev-${row.severity}`">{{ row.value }}</span>
          </div>
          <div class="nd-avail-meta">
            <div><strong>说明：</strong>{{ row.description }}</div>
            <div><strong>来源：</strong><code>{{ row.source }}</code></div>
          </div>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.nd-avail-loading, .nd-avail-empty { padding: 28px; text-align: center; color: var(--kx-muted); font-size: 12px; }
.nd-avail-group { border: 1px solid var(--kx-border); border-radius: 8px; padding: 14px; margin-bottom: 12px; }
.nd-avail-group h3 { margin: 0 0 10px; font-size: 14px; }
.nd-avail-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; }
.nd-avail-row { border: 1px solid var(--kx-border); border-radius: 6px; padding: 8px 10px; }
.nd-avail-head { display: flex; justify-content: space-between; gap: 8px; align-items: baseline; }
.nd-avail-label { font-size: 12px; font-weight: 600; }
.nd-avail-value { font-size: 12px; }
.nd-avail-meta { margin-top: 4px; font-size: 11px; color: var(--kx-muted); display: grid; gap: 2px; }
.nd-avail-meta code { font-size: 10px; }
.sev-ok { color: var(--kx-success); }
.sev-warn { color: var(--kx-warning); }
.sev-danger { color: var(--kx-danger); }
.sev-info { color: var(--kx-muted); }
</style>
