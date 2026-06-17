<script setup lang="ts">
import { ref, watch, computed } from 'vue'
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
    runs.value = r.runs
    states.value = s.states
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

function statusClass(s: string) {
  return `status-pill status-${s}`
}

function stateClass(s: string) {
  return `state-pill state-${s}`
}

function stateBadgeClass(s: string) {
  return `consensus-badge consensus-${s}`
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

const grouped = computed(() => {
  // Group by (credential, model) for the trigger column.
  const map = new Map<string, ProbeRun[]>()
  for (const r of runs.value) {
    const k = `${r.credential_id}:${r.raw_model_name}`
    if (!map.has(k)) map.set(k, [])
    map.get(k)!.push(r)
  }
  return map
})

watch(() => props.providerId, load, { immediate: true })
watch(statusFilter, load)
watch(stateFilter, load)
</script>

<template>
  <div class="probe-history">
    <!-- 共识状态面板：3 次成功才变更 -->
    <div class="consensus-banner">
      <strong>共识机制</strong>
      <span>连续 {{ RequiredConsensus }} 次成功 → 标记为已恢复 · 连续 {{ RequiredConsensus }} 次失败 → 确认失败（停止探测）</span>
      <span class="muted small">回退间隔：1m → 5m → 15m → 60m（逐级）</span>
    </div>

    <!-- 当前共识状态 -->
    <details open class="probe-section">
      <summary>当前共识状态（{{ states.length }}）</summary>
      <div class="probe-toolbar">
        <label>
          状态过滤：
          <select v-model="stateFilter">
            <option v-for="o in stateOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
        </label>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">刷新</button>
      </div>

      <table v-if="states.length > 0" class="probe-table">
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
            <td>{{ s.credential_id }}</td>
            <td class="mono">{{ s.raw_model_name }}</td>
            <td><span :class="stateBadgeClass(s.state)">{{ s.state }}</span></td>
            <td>
              <span class="counter counter-ok">{{ s.consecutive_successes }}/{{ RequiredConsensus }}</span>
            </td>
            <td>
              <span class="counter counter-fail">{{ s.consecutive_failures }}/{{ RequiredConsensus }}</span>
            </td>
            <td>{{ s.total_attempts }}</td>
            <td>{{ s.last_status ?? '—' }}</td>
            <td class="ts">{{ fmtDelta(s.next_retry_at) }}</td>
            <td class="ts">{{ fmtTime(s.last_state_change_at) }}</td>
            <td>
              <button
                class="btn btn-xs"
                :disabled="triggering.has(`${s.credential_id}:${s.raw_model_name}`)"
                @click="trigger(s.credential_id, s.raw_model_name)"
              >
                {{ triggering.has(`${s.credential_id}:${s.raw_model_name}`) ? '…' : '立即探测' }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else-if="!loading" class="empty">暂无共识状态记录</div>
    </details>

    <!-- 探针历史记录 -->
    <details class="probe-section">
      <summary>探针历史记录（{{ runs.length }}）</summary>
      <div class="probe-toolbar">
        <label>
          状态过滤：
          <select v-model="statusFilter">
            <option v-for="o in statusOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
        </label>
        <span class="muted">最近 100 条 · 触发方式：scheduler 自动 / manual 手动</span>
      </div>

      <div v-if="error" class="error-bar">{{ error }}</div>

      <table v-if="runs.length > 0" class="probe-table">
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
            <td class="ts">{{ fmtTime(r.created_at) }}</td>
            <td>{{ r.credential_id }}</td>
            <td class="mono">{{ r.raw_model_name }}</td>
            <td><span :class="statusClass(r.status)">{{ r.status }}</span></td>
            <td>{{ r.http_status ?? '—' }}</td>
            <td class="err-cell">
              <code v-if="r.error_code">{{ r.error_code }}</code>
              <div v-if="r.error_message" class="muted small">{{ r.error_message }}</div>
            </td>
            <td>{{ r.latency_ms }}ms</td>
            <td><span :class="stateClass(r.state_change)">{{ r.state_change }}</span></td>
            <td>{{ r.triggered_by }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else-if="!loading" class="empty">暂无测试记录</div>
    </details>
  </div>
</template>

<style scoped>
.probe-history { display: flex; flex-direction: column; gap: 16px; }
.consensus-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px 16px;
  padding: 8px 12px;
  border-radius: var(--radius, 4px);
  background: rgba(59, 130, 246, 0.06);
  border: 1px solid rgba(59, 130, 246, 0.30);
  font-size: 13px;
}
.consensus-banner strong { color: #3b82f6; }
.probe-section {
  border: 1px solid var(--border, #e1e4e8);
  border-radius: var(--radius, 4px);
  background: var(--card, #fff);
  padding: 0;
}
.probe-section > summary {
  padding: 10px 14px;
  cursor: pointer;
  font-weight: 600;
  font-size: 14px;
  user-select: none;
  list-style: none;
}
.probe-section > summary::-webkit-details-marker { display: none; }
.probe-section > summary::before {
  content: '▶';
  display: inline-block;
  margin-right: 6px;
  transition: transform 0.15s;
  font-size: 10px;
  color: var(--text-secondary, #6b7280);
}
.probe-section[open] > summary::before { transform: rotate(90deg); }
.probe-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 14px 10px;
}
.probe-toolbar select { padding: 4px 8px; border-radius: 4px; border: 1px solid #ccc; }
.probe-table { width: calc(100% - 28px); margin: 0 14px 14px; border-collapse: collapse; font-size: 13px; }
.probe-table th, .probe-table td { padding: 6px 8px; border-bottom: 1px solid #eaecef; text-align: left; vertical-align: top; }
.probe-table th { background: #f6f8fa; font-weight: 600; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.ts { white-space: nowrap; font-size: 12px; }
.err-cell { max-width: 360px; }
.muted { color: #6a737d; }
.small { font-size: 11px; }
.error-bar { background: #ffeef0; border: 1px solid #f97583; padding: 8px 12px; border-radius: 4px; color: #b31d28; }
.empty { padding: 24px; text-align: center; color: #6a737d; }
.status-pill { padding: 1px 6px; border-radius: 3px; font-size: 11px; font-weight: 600; color: #fff; }
.status-ok { background: #28a745; }
.status-http_4xx { background: #d29922; }
.status-http_5xx { background: #cb2431; }
.status-network { background: #6f42c1; }
.status-auth { background: #cb2431; }
.status-skipped { background: #6a737d; }
.status-unknown { background: #6a737d; }
.state-pill { padding: 1px 6px; border-radius: 3px; font-size: 11px; }
.state-recovered { background: #d4edda; color: #155724; }
.state-broke { background: #f8d7da; color: #721c24; }
.state-unchanged { background: #e2e3e5; color: #383d41; }
.consensus-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.consensus-unknown { background: #e2e3e5; color: #383d41; }
.consensus-recovering { background: #fff3cd; color: #856404; }
.consensus-healthy_confirmed { background: #d4edda; color: #155724; }
.consensus-broken_confirmed { background: #f8d7da; color: #721c24; }
.counter {
  display: inline-block;
  min-width: 28px;
  padding: 1px 6px;
  border-radius: 3px;
  font-size: 12px;
  font-weight: 600;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  text-align: center;
}
.counter-ok { background: #d4edda; color: #155724; }
.counter-fail { background: #f8d7da; color: #721c24; }
</style>
