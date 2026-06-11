<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { getDecisions, type RoutingDecision } from '../api'
import ModelPicker from '../components/ModelPicker.vue'

const rows = ref<RoutingDecision[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const sinceMinutes = ref(30)
const filterModel = ref('')
const filterSuccess = ref<'' | 'true' | 'false'>('')
const limit = ref(50)

// Detail panel
const selectedRow = ref<RoutingDecision | null>(null)

function openDetail(row: RoutingDecision) {
  selectedRow.value = row
}
function closeDetail() {
  selectedRow.value = null
}

let timer: ReturnType<typeof setInterval> | null = null

async function load() {
  loading.value = true
  error.value = null
  try {
    const params: Record<string, unknown> = {
      since_minutes: sinceMinutes.value,
      limit: limit.value,
    }
    if (filterModel.value.trim()) params.model = filterModel.value.trim()
    if (filterSuccess.value !== '') params.success = filterSuccess.value === 'true'
    rows.value = await getDecisions(params)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function fmtTs(ts: string) {
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
}

function traceList(v: unknown): string {
  if (!Array.isArray(v) || !v.length) return '—'
  return v
    .map((item) => {
      if (!item || typeof item !== 'object') return String(item)
      const row = item as Record<string, unknown>
      const provider = row.provider_id ?? '—'
      const credential = row.credential_id ?? '—'
      const reason = row.reason ?? row.raw_model ?? ''
      return `p${provider}/c${credential} ${reason}`.trim()
    })
    .join(' | ')
}

onMounted(() => {
  load()
  timer = setInterval(load, 5000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2 style="margin:0">路由决策日志</h2>
      <div style="font-size:12px;color:var(--muted)">每 5 秒自动刷新</div>
    </div>

    <!-- Filters -->
    <div class="card" style="margin-bottom:16px;display:flex;gap:16px;flex-wrap:wrap;align-items:center">
      <div style="display:flex;align-items:center;gap:8px;min-width:260px">
        <label style="font-size:13px;white-space:nowrap">模型筛选</label>
        <div style="width:220px">
          <ModelPicker
            v-model="filterModel"
            :allow-free-text="true"
            placeholder="选择或输入模型"
            @update:modelValue="load"
          />
        </div>
      </div>
      <div style="display:flex;align-items:center;gap:8px">
        <label style="font-size:13px;white-space:nowrap">状态</label>
        <select v-model="filterSuccess" @change="load" style="width:100px">
          <option value="">全部</option>
          <option value="true">成功</option>
          <option value="false">失败</option>
        </select>
      </div>
      <div style="display:flex;align-items:center;gap:8px">
        <label style="font-size:13px;white-space:nowrap">最近</label>
        <select v-model="sinceMinutes" @change="load" style="width:100px">
          <option :value="10">10 分钟</option>
          <option :value="30">30 分钟</option>
          <option :value="60">1 小时</option>
          <option :value="360">6 小时</option>
          <option :value="1440">24 小时</option>
        </select>
      </div>
      <div style="display:flex;align-items:center;gap:8px">
        <label style="font-size:13px;white-space:nowrap">条数</label>
        <select v-model="limit" @change="load" style="width:80px">
          <option :value="20">20</option>
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
        </select>
      </div>
      <button class="btn btn-ghost btn-sm" @click="load">刷新</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div class="card" style="overflow:auto">
      <table class="data-table" style="min-width:1500px">
        <thead>
          <tr>
            <th>时间</th>
            <th>状态</th>
            <th>模型</th>
            <th>解析</th>
            <th>Tier</th>
            <th>延迟</th>
            <th>供应商</th>
            <th>出站模型</th>
            <th>prompt_t</th>
            <th>comp_t</th>
            <th>费用</th>
            <th>候选链</th>
            <th>拦截原因</th>
            <th>错误</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length && !loading">
            <td colspan="13" style="text-align:center;padding:32px;color:var(--muted)">
              暂无决策记录
            </td>
          </tr>
          <tr v-for="r in rows" :key="r.request_id + r.ts" :class="{ 'row-fail': !r.success }" class="row-clickable" @click="openDetail(r)">
            <td style="white-space:nowrap;font-size:12px">{{ fmtTs(r.ts) }}</td>
            <td>
              <span :class="r.success ? 'badge-ok' : 'badge-err'">
                {{ r.success ? '✓' : '✗' }}
              </span>
            </td>
            <td style="font-size:12px;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ r.model }}</td>
            <td style="font-size:11px;max-width:220px;overflow:hidden;text-overflow:ellipsis">
              <div>{{ r.resolution_path ?? '—' }} / {{ r.canonical_model ?? '—' }}</div>
              <div style="color:var(--muted)">{{ (r.resolution_raw_models || []).join(', ') || '—' }}</div>
            </td>
            <td style="text-align:center">{{ r.tier ?? '—' }}</td>
            <td style="text-align:right">{{ r.latency_ms != null ? r.latency_ms + 'ms' : '—' }}</td>
            <td style="font-size:12px">{{ r.chosen_provider_id ?? '—' }}</td>
            <td style="font-size:12px;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ r.outbound_model ?? '—' }}</td>
            <td style="text-align:right">{{ r.prompt_tokens ?? '—' }}</td>
            <td style="text-align:right">{{ r.completion_tokens ?? '—' }}</td>
            <td style="text-align:right;font-size:12px">
              {{ r.cost_usd != null ? '$' + Number(r.cost_usd).toFixed(5) : '—' }}
            </td>
            <td style="font-size:11px;max-width:260px;overflow:hidden;text-overflow:ellipsis">
              {{ traceList((r.decision_trace || {}).planned_candidates) }}
            </td>
            <td style="font-size:11px;max-width:260px;overflow:hidden;text-overflow:ellipsis;color:var(--warning)">
              {{ traceList((r.decision_trace || {}).blocked_candidates) }}
            </td>
            <td style="font-size:11px;color:var(--danger);max-width:140px;overflow:hidden;text-overflow:ellipsis">
              {{ r.failure_detail_code ?? r.error_class ?? '' }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="loading" style="text-align:center;padding:8px;font-size:12px;color:var(--muted)">加载中…</div>

    <!-- Row detail modal -->
    <Teleport to="body">
      <div v-if="selectedRow" class="drawer-backdrop" @click="closeDetail">
        <div class="drawer-panel card" @click.stop>
          <div class="drawer-header">
            <span style="font-size:14px;font-weight:600">决策详情</span>
            <button class="btn btn-ghost btn-sm" @click="closeDetail">✕ 关闭</button>
          </div>
          <div class="detail-body">

            <!-- Basic -->
            <div class="drawer-section">
              <div class="drawer-section-title">基本信息</div>
              <div class="detail-grid">
                <span class="dk">时间</span><span class="dv">{{ selectedRow.ts }}</span>
                <span class="dk">Request ID</span><span class="dv mono">{{ selectedRow.request_id }}</span>
                <span class="dk">Idempotency Key</span><span class="dv mono">{{ selectedRow.idempotency_key ?? '—' }}</span>
                <span class="dk">Tenant</span><span class="dv mono">{{ selectedRow.tenant_id }}</span>
                <span class="dk">状态</span>
                <span class="dv">
                  <span :class="selectedRow.success ? 'badge-ok' : 'badge-err'">
                    {{ selectedRow.success ? '✓ 成功' : '✗ 失败' }}
                  </span>
                </span>
                <span class="dk">延迟</span><span class="dv">{{ selectedRow.latency_ms != null ? selectedRow.latency_ms + ' ms' : '—' }}</span>
                <span class="dk">客户端模型</span><span class="dv mono">{{ selectedRow.client_model ?? selectedRow.model }}</span>
                <span class="dk">出站模型</span><span class="dv mono">{{ selectedRow.outbound_model ?? '—' }}</span>
                <span class="dk">Request Mode</span><span class="dv">{{ selectedRow.request_mode ?? '—' }}</span>
                <span class="dk">协议</span><span class="dv">{{ selectedRow.egress_protocol ?? '—' }}</span>
                <span class="dk">Sticky Hit</span><span class="dv">{{ selectedRow.sticky_hit ? '✓' : '✗' }}</span>
              </div>
            </div>

            <!-- Resolution -->
            <div class="drawer-section">
              <div class="drawer-section-title">模型解析</div>
              <div class="detail-grid">
                <span class="dk">Resolution Path</span><span class="dv mono">{{ selectedRow.resolution_path ?? '—' }}</span>
                <span class="dk">Canonical Model</span><span class="dv mono">{{ selectedRow.canonical_model ?? '—' }}</span>
                <span class="dk">Raw Models</span>
                <span class="dv mono">{{ (selectedRow.resolution_raw_models || []).join(', ') || '—' }}</span>
                <span class="dk">Client Profile</span><span class="dv">{{ selectedRow.client_profile ?? '—' }}</span>
                <span class="dk">Transform Rule</span><span class="dv mono">{{ selectedRow.transform_rule_id ?? '—' }}</span>
              </div>
            </div>

            <!-- Routing -->
            <div class="drawer-section">
              <div class="drawer-section-title">路由决策</div>
              <div class="detail-grid">
                <span class="dk">供应商 ID</span><span class="dv">{{ selectedRow.chosen_provider_id ?? '—' }}</span>
                <span class="dk">凭据 ID</span><span class="dv">{{ selectedRow.chosen_credential_id ?? '—' }}</span>
                <span class="dk">Tier</span><span class="dv">{{ selectedRow.tier ?? '—' }}</span>
                <span class="dk">候选数</span><span class="dv">{{ selectedRow.candidates_tried }}</span>
              </div>
            </div>

            <!-- Tokens & Cost -->
            <div class="drawer-section">
              <div class="drawer-section-title">Token 与费用</div>
              <div class="detail-grid">
                <span class="dk">Prompt Tokens</span><span class="dv">{{ selectedRow.prompt_tokens ?? '—' }}</span>
                <span class="dk">Completion Tokens</span><span class="dv">{{ selectedRow.completion_tokens ?? '—' }}</span>
                <span class="dk">费用 (USD)</span>
                <span class="dv">{{ selectedRow.cost_usd != null ? '$' + Number(selectedRow.cost_usd).toFixed(6) : '—' }}</span>
                <span class="dk">请求体积</span><span class="dv">{{ selectedRow.request_bytes != null ? selectedRow.request_bytes + ' B' : '—' }}</span>
                <span class="dk">响应体积</span><span class="dv">{{ selectedRow.response_bytes != null ? selectedRow.response_bytes + ' B' : '—' }}</span>
              </div>
            </div>

            <!-- Error -->
            <div v-if="!selectedRow.success" class="drawer-section">
              <div class="drawer-section-title" style="color:var(--danger)">错误信息</div>
              <div class="detail-grid">
                <span class="dk">Error Class</span><span class="dv" style="color:var(--danger)">{{ selectedRow.error_class ?? '—' }}</span>
                <span class="dk">Failure Stage</span><span class="dv">{{ selectedRow.failure_stage ?? '—' }}</span>
                <span class="dk">Failure Code</span><span class="dv mono">{{ selectedRow.failure_detail_code ?? '—' }}</span>
              </div>
            </div>

            <!-- Decision Trace -->
            <div class="drawer-section">
              <div class="drawer-section-title">Decision Trace</div>
              <pre class="trace-json">{{ JSON.stringify(selectedRow.decision_trace, null, 2) }}</pre>
            </div>

          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.data-table { width: 100%; border-collapse: collapse; }
.data-table th {
  text-align: left;
  padding: 8px 12px;
  font-size: 12px;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.data-table td {
  padding: 7px 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.row-fail td { background: rgba(239,68,68,.05); }
.badge-ok  { color: #22c55e; font-weight: 600; }
.badge-err { color: #ef4444; font-weight: 600; }
.error-banner {
  background: rgba(239,68,68,.15);
  border: 1px solid #ef4444;
  border-radius: 8px;
  padding: 12px 16px;
  color: #ef4444;
  margin-bottom: 16px;
}
.row-clickable { cursor: pointer; }
.row-clickable:hover td { background: rgba(var(--accent-rgb, 99,102,241), .06); }

.detail-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.drawer-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.drawer-section-title {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: .06em;
  text-transform: uppercase;
  color: var(--muted);
  padding-bottom: 4px;
  border-bottom: 1px solid var(--border);
}
.detail-grid {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 4px 12px;
  font-size: 13px;
}
.dk {
  color: var(--muted);
  font-size: 12px;
  padding: 2px 0;
  white-space: nowrap;
}
.dv {
  word-break: break-all;
  padding: 2px 0;
}
.mono { font-family: monospace; font-size: 12px; }
.trace-json {
  font-family: monospace;
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--bg, #13131f);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  margin: 0;
  max-height: 320px;
  overflow-y: auto;
  color: var(--text, #cdd6f4);
}
</style>
