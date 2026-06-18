<script setup lang="ts">
import { ref, watch } from 'vue'
import {
  getProviderProbeHistory,
  getProviderProbeStates,
  triggerProviderProbe,
  type ProbeRun,
  type ProbeState,
} from '../../api'

const props = defineProps<{ providerId: number }>()

const runs = ref<ProbeRun[]>([])
const states = ref<ProbeState[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<string>('')
const stateFilter = ref<string>('')
const triggering = ref<Set<string>>(new Set())

const statusOptions = [
  { value: '', label: '全部' },
  { value: 'ok', label: '成功' },
  { value: 'http_4xx', label: '客户端错误 (4xx)' },
  { value: 'http_5xx', label: '服务端错误 (5xx)' },
  { value: 'network', label: '网络错误' },
  { value: 'auth', label: '鉴权错误' },
  { value: 'skipped', label: '已跳过' },
]

const stateOptions = [
  { value: '', label: '全部状态' },
  { value: 'unknown', label: '未知' },
  { value: 'recovering', label: '探测中' },
  { value: 'healthy_confirmed', label: '已恢复' },
  { value: 'broken_confirmed', label: '确认失败' },
]

const RequiredConsensus = 3

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [r, s] = await Promise.all([
      getProviderProbeHistory(props.providerId, {
        limit: 100,
        status: statusFilter.value || undefined,
      }),
      getProviderProbeStates(props.providerId, {
        state: stateFilter.value || undefined,
      }),
    ])
    // Go nil slice JSON-encodes as null — always coerce to array.
    runs.value = r.runs ?? []
    states.value = s.states ?? []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function trigger(credentialId: number, rawModel: string) {
  const key = `${credentialId}:${rawModel}`
  triggering.value.add(key)
  try {
    await triggerProviderProbe(props.providerId, credentialId, rawModel)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '触发失败'
  } finally {
    triggering.value.delete(key)
  }
}

function statusBadge(s: string) {
  if (s === 'ok') return 'badge-green'
  if (s === 'http_4xx') return 'badge-yellow'
  if (s === 'http_5xx' || s === 'auth') return 'badge-red'
  if (s === 'network') return 'badge-blue'
  return 'badge-gray'
}

function stateChangeBadge(s: string) {
  if (s === 'recovered') return 'badge-green'
  if (s === 'broke') return 'badge-red'
  return 'badge-gray'
}

function consensusBadge(s: string) {
  if (s === 'healthy_confirmed') return 'badge-green'
  if (s === 'broken_confirmed') return 'badge-red'
  if (s === 'recovering') return 'badge-yellow'
  return 'badge-gray'
}

function consensusLabel(s: string) {
  const hit = stateOptions.find(o => o.value === s)
  return hit?.label ?? s
}

function fmtTime(iso: string | null | undefined) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function fmtDelta(iso: string) {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return '即将'
  const mins = Math.round(ms / 60000)
  if (mins < 60) return `${mins} 分钟后`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs} 小时后`
  return `${Math.round(hrs / 24)} 天后`
}

watch(() => props.providerId, load, { immediate: true })
watch(statusFilter, load)
watch(stateFilter, load)
</script>

<template>
  <div class="probe-history">
    <div class="consensus-banner">
      <strong>共识机制</strong>
      <span>连续 {{ RequiredConsensus }} 次成功 → 标记为已恢复 · 连续 {{ RequiredConsensus }} 次失败 → 确认失败（停止探测）</span>
      <span class="banner-muted">回退间隔：1m → 5m → 15m → 60m（逐级）</span>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <details open class="probe-section card">
      <summary>当前共识状态（{{ states.length }}）</summary>
      <div class="compact-filter-bar probe-filter-bar">
        <span class="cf-hint">状态过滤</span>
        <select v-model="stateFilter" class="cf-select cf-status">
          <option v-for="o in stateOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">{{ loading ? '…' : '刷新' }}</button>
      </div>

      <div class="table-wrap">
        <table v-if="states.length > 0" class="data-table probe-table">
          <thead>
            <tr>
              <th>凭据</th>
              <th>模型</th>
              <th>状态</th>
              <th>连续成功</th>
              <th>连续失败</th>
              <th>总尝试</th>
              <th>上次结果</th>
              <th>下次探测</th>
              <th>状态变更于</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in states" :key="`${s.credential_id}-${s.raw_model_name}`">
              <td>#{{ s.credential_id }}</td>
              <td><code class="mono-sm">{{ s.raw_model_name }}</code></td>
              <td><span class="badge" :class="consensusBadge(s.state)">{{ consensusLabel(s.state) }}</span></td>
              <td><span class="counter counter-ok">{{ s.consecutive_successes }}/{{ RequiredConsensus }}</span></td>
              <td><span class="counter counter-fail">{{ s.consecutive_failures }}/{{ RequiredConsensus }}</span></td>
              <td>{{ s.total_attempts }}</td>
              <td class="cell-muted">{{ s.last_status ?? '—' }}</td>
              <td class="cell-muted ts">{{ fmtDelta(s.next_retry_at) }}</td>
              <td class="cell-muted ts">{{ fmtTime(s.last_state_change_at) }}</td>
              <td>
                <button
                  class="btn btn-ghost btn-sm"
                  :disabled="triggering.has(`${s.credential_id}:${s.raw_model_name}`)"
                  @click="trigger(s.credential_id, s.raw_model_name)"
                >
                  {{ triggering.has(`${s.credential_id}:${s.raw_model_name}`) ? '…' : '立即探测' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty-hint">暂无共识状态记录</div>
      </div>
    </details>

    <details class="probe-section card">
      <summary>探针历史记录（{{ runs.length }}）</summary>
      <div class="compact-filter-bar probe-filter-bar">
        <span class="cf-hint">状态过滤</span>
        <select v-model="statusFilter" class="cf-select probe-status-select">
          <option v-for="o in statusOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
        <span class="cf-meta">最近 100 条 · scheduler 自动 / manual 手动</span>
      </div>

      <div class="table-wrap">
        <table v-if="runs.length > 0" class="data-table probe-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>凭据</th>
              <th>模型</th>
              <th>状态</th>
              <th>HTTP</th>
              <th>错误</th>
              <th>延迟</th>
              <th>状态变化</th>
              <th>触发</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in runs" :key="r.id">
              <td class="cell-muted ts">{{ fmtTime(r.created_at) }}</td>
              <td>#{{ r.credential_id }}</td>
              <td><code class="mono-sm">{{ r.raw_model_name }}</code></td>
              <td><span class="badge" :class="statusBadge(r.status)">{{ r.status }}</span></td>
              <td>{{ r.http_status ?? '—' }}</td>
              <td class="err-cell">
                <code v-if="r.error_code" class="mono-sm">{{ r.error_code }}</code>
                <div v-if="r.error_message" class="cell-muted err-msg">{{ r.error_message }}</div>
              </td>
              <td>{{ r.latency_ms }}ms</td>
              <td><span class="badge" :class="stateChangeBadge(r.state_change)">{{ r.state_change }}</span></td>
              <td class="cell-muted">{{ r.triggered_by }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="!loading" class="empty-hint">暂无测试记录</div>
      </div>
    </details>
  </div>
</template>

<style scoped>
.probe-history {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.consensus-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px 16px;
  padding: 10px 14px;
  border-radius: var(--radius);
  font-size: 13px;
  background: rgba(99, 102, 241, 0.08);
  border: 1px solid rgba(99, 102, 241, 0.35);
  color: var(--text);
}

.consensus-banner strong {
  color: var(--accent-h);
}

.banner-muted {
  color: var(--muted);
  font-size: 12px;
}

.probe-section {
  padding: 0;
  overflow: hidden;
}

.probe-section > summary {
  padding: 12px 16px;
  cursor: pointer;
  font-weight: 600;
  font-size: 14px;
  user-select: none;
  list-style: none;
  border-bottom: 1px solid var(--border);
}

.probe-section > summary::-webkit-details-marker {
  display: none;
}

.probe-section > summary::before {
  content: '▶';
  display: inline-block;
  margin-right: 6px;
  transition: transform 0.15s;
  font-size: 10px;
  color: var(--muted);
}

.probe-section[open] > summary::before {
  transform: rotate(90deg);
}

.probe-filter-bar {
  margin: 12px 16px;
  width: auto;
}

.probe-status-select {
  width: 160px;
  flex-shrink: 0;
}

.table-wrap {
  overflow-x: auto;
  padding: 0 16px 16px;
}

.probe-table {
  width: 100%;
  font-size: 12px;
}

.mono-sm {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}

.cell-muted {
  color: var(--muted);
}

.ts {
  white-space: nowrap;
}

.err-cell {
  max-width: 360px;
}

.err-msg {
  font-size: 11px;
  margin-top: 4px;
}

.empty-hint {
  color: var(--muted);
  text-align: center;
  padding: 24px;
  font-size: 13px;
}

.counter {
  display: inline-block;
  min-width: 28px;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  text-align: center;
}

.counter-ok {
  background: rgba(63, 185, 80, 0.15);
  color: var(--success);
}

.counter-fail {
  background: rgba(248, 81, 73, 0.15);
  color: var(--danger);
}
</style>
