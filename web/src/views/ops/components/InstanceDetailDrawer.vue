<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getCenterInstance,
  getInstanceStatus,
  getInstanceRuntimeMetrics,
  getInstanceRuntimeAlerts,
  getHeartbeatHistory,
  type CenterInstance,
  type InstanceStatusReport,
  type RuntimeMetricPoint,
  type InstanceRuntimeAlert,
  type HeartbeatRecord,
} from '../../../api/ops'
import { formatRelativeTime, memPressurePct, successRate } from '../regionTopology'

const props = defineProps<{
  modelValue: boolean
  instanceId: string | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
}>()

const { t } = useI18n()
const loading = ref(false)
const instance = ref<CenterInstance | null>(null)
const status = ref<InstanceStatusReport | null>(null)
const metrics = ref<RuntimeMetricPoint[]>([])
const alerts = ref<InstanceRuntimeAlert[]>([])
const heartbeats = ref<HeartbeatRecord[]>([])

const open = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit('update:modelValue', v),
})

const latest = computed(() => metrics.value[0] ?? null)

const requestSuccessPct = computed(() => {
  if (latest.value && latest.value.last_5min_success_pct > 0) {
    return latest.value.last_5min_success_pct
  }
  if (!status.value) return null
  return successRate(status.value.requests_ok, status.value.requests_total)
})

const cpuPct = computed(() => latest.value?.cpu_usage_pct ?? null)
const memPct = computed(() => {
  if (!latest.value) return null
  return memPressurePct(latest.value.mem_used_mb, latest.value.mem_total_mb)
})

function pressureColor(pct: number | null) {
  if (pct == null) return '#909399'
  if (pct >= 85) return '#f56c6c'
  if (pct >= 65) return '#e6a23c'
  return '#67c23a'
}

function successColor(pct: number | null) {
  if (pct == null) return '#909399'
  if (pct < 90) return '#f56c6c'
  if (pct < 97) return '#e6a23c'
  return '#67c23a'
}

function formatNum(n: number | null | undefined, digits = 1) {
  if (n == null || Number.isNaN(n)) return '—'
  return n.toFixed(digits)
}

function formatDate(iso?: string) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString()
}

function rel(iso?: string) {
  return formatRelativeTime(iso, {
    justNow: t('ops.overview.justNow'),
    minutesAgo: (n) => t('ops.overview.minutesAgo', { n }),
    hoursAgo: (n) => t('ops.overview.hoursAgo', { n }),
    daysAgo: (n) => t('ops.overview.daysAgo', { n }),
  }) || '—'
}

function severityType(sev: string) {
  if (sev === 'critical' || sev === 'error') return 'danger'
  if (sev === 'warning') return 'warning'
  return 'info'
}

async function loadDetail(id: string) {
  loading.value = true
  instance.value = null
  status.value = null
  metrics.value = []
  alerts.value = []
  heartbeats.value = []
  try {
    const [inst, st, metricItems, alertItems, hb] = await Promise.all([
      getCenterInstance(id),
      getInstanceStatus(id),
      getInstanceRuntimeMetrics(id).catch(() => [] as RuntimeMetricPoint[]),
      getInstanceRuntimeAlerts(id).catch(() => [] as InstanceRuntimeAlert[]),
      getHeartbeatHistory(id, 6).catch(() => [] as HeartbeatRecord[]),
    ])
    instance.value = inst
    status.value = st
    metrics.value = metricItems
    alerts.value = alertItems
    heartbeats.value = hb.slice(0, 8)
  } catch (err) {
    ElMessage.error(t('ops.overview.nodeDetailLoadFailed'))
    console.error(err)
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.modelValue, props.instanceId] as const,
  ([visible, id]) => {
    if (visible && id) void loadDetail(id)
  },
)
</script>

<template>
  <el-drawer
    v-model="open"
    size="480px"
    :title="instance?.hostname || t('ops.overview.nodeDetailTitle')"
    destroy-on-close
  >
    <div v-loading="loading" class="drawer-body">
      <div v-if="instance" class="identity">
        <div class="id-row">
          <el-tag size="small" :type="instance.status === 'online' ? 'success' : instance.status === 'degraded' ? 'warning' : 'danger'">
            {{ t(`ops.center.status.${instance.status}`) }}
          </el-tag>
          <span class="mono">{{ instance.instance_id }}</span>
        </div>
        <div class="meta-grid">
          <div><span class="k">{{ t('ops.center.region') }}</span><span>{{ instance.region || '—' }}</span></div>
          <div><span class="k">{{ t('ops.center.version') }}</span><span>{{ instance.version }} #{{ instance.build_seq }}</span></div>
          <div><span class="k">{{ t('ops.center.ipAddress') }}</span><span>{{ instance.ip_address || '—' }}</span></div>
          <div><span class="k">{{ t('ops.center.lastHeartbeat') }}</span><span>{{ rel(instance.last_heartbeat) }}</span></div>
        </div>
      </div>

      <section class="block">
        <h3>{{ t('ops.overview.perfPressure') }}</h3>
        <div class="pressure-grid">
          <div class="pressure-item">
            <div class="pressure-label">CPU</div>
            <el-progress
              type="dashboard"
              :percentage="Math.min(100, Math.round(cpuPct ?? 0))"
              :color="pressureColor(cpuPct)"
              :width="88"
              :format="() => (cpuPct == null ? '—' : `${formatNum(cpuPct, 0)}%`)"
            />
          </div>
          <div class="pressure-item">
            <div class="pressure-label">{{ t('ops.overview.memory') }}</div>
            <el-progress
              type="dashboard"
              :percentage="Math.min(100, Math.round(memPct ?? 0))"
              :color="pressureColor(memPct)"
              :width="88"
              :format="() => (memPct == null ? '—' : `${formatNum(memPct, 0)}%`)"
            />
          </div>
        </div>
        <div class="stat-row">
          <div>
            <div class="stat-k">TPS (5m)</div>
            <div class="stat-v">{{ formatNum(latest?.last_5min_tps, 2) }}</div>
          </div>
          <div>
            <div class="stat-k">P99</div>
            <div class="stat-v">{{ latest ? `${formatNum(latest.last_5min_p99_ms / 1000, 1)}s` : '—' }}</div>
          </div>
          <div>
            <div class="stat-k">{{ t('ops.overview.concurrency') }}</div>
            <div class="stat-v">{{ latest?.current_concurrency ?? '—' }}</div>
          </div>
        </div>
      </section>

      <section class="block">
        <h3>{{ t('ops.overview.requestSummary') }}</h3>
        <div class="stat-row">
          <div>
            <div class="stat-k">{{ t('ops.overview.requestsTotal') }}</div>
            <div class="stat-v">{{ status?.requests_total ?? '—' }}</div>
          </div>
          <div>
            <div class="stat-k">{{ t('ops.overview.successRate') }}</div>
            <div class="stat-v" :style="{ color: successColor(requestSuccessPct) }">
              {{ requestSuccessPct == null ? '—' : `${formatNum(requestSuccessPct, 1)}%` }}
            </div>
          </div>
          <div>
            <div class="stat-k">{{ t('ops.overview.requestsErr') }}</div>
            <div class="stat-v danger">{{ status?.requests_err ?? '—' }}</div>
          </div>
        </div>
        <div class="stat-row secondary">
          <div>
            <div class="stat-k">{{ t('ops.overview.avgLatency') }}</div>
            <div class="stat-v">{{ status ? `${formatNum(status.avg_latency_ms, 0)}ms` : '—' }}</div>
          </div>
          <div>
            <div class="stat-k">P99</div>
            <div class="stat-v">{{ status ? `${formatNum(status.p99_latency_ms, 0)}ms` : '—' }}</div>
          </div>
          <div>
            <div class="stat-k">{{ t('ops.overview.requestsOk') }}</div>
            <div class="stat-v">{{ status?.requests_ok ?? '—' }}</div>
          </div>
        </div>
      </section>

      <section class="block">
        <h3>{{ t('ops.overview.nodeErrors') }}</h3>
        <div v-if="alerts.length === 0" class="empty">{{ t('ops.overview.noNodeErrors') }}</div>
        <ul v-else class="alert-list">
          <li v-for="a in alerts" :key="a.id">
            <el-tag size="small" :type="severityType(a.severity)">{{ a.severity }}</el-tag>
            <div class="alert-body">
              <div class="alert-title">{{ a.title }}</div>
              <div class="alert-msg">{{ a.message }}</div>
              <div class="alert-time">{{ formatDate(a.detected_at) }}</div>
            </div>
          </li>
        </ul>
      </section>

      <section v-if="heartbeats.length" class="block">
        <h3>{{ t('ops.center.heartbeatHistory') }}</h3>
        <el-table :data="heartbeats" size="small" max-height="220">
          <el-table-column :label="t('common.createdAt')" min-width="140">
            <template #default="scope">{{ formatDate(scope?.row?.timestamp) }}</template>
          </el-table-column>
          <el-table-column label="Goroutines" width="90">
            <template #default="scope">{{ scope?.row?.num_goroutine ?? '—' }}</template>
          </el-table-column>
          <el-table-column :label="t('ops.center.memoryUsage')" width="100">
            <template #default="scope">{{ formatNum(scope?.row?.alloc_mb, 0) }}</template>
          </el-table-column>
        </el-table>
      </section>
    </div>
  </el-drawer>
</template>

<style scoped>
.drawer-body {
  min-height: 240px;
  padding-bottom: 24px;
}

.identity {
  margin-bottom: 18px;
}

.id-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
}

.meta-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 12px;
  font-size: 12px;
}

.meta-grid .k {
  display: block;
  color: var(--el-text-color-secondary);
  margin-bottom: 2px;
}

.block {
  margin-bottom: 20px;
}

.block h3 {
  margin: 0 0 10px;
  font-size: 13px;
  font-weight: 650;
  color: var(--el-text-color-regular);
}

.pressure-grid {
  display: flex;
  justify-content: space-around;
  margin-bottom: 8px;
}

.pressure-item {
  text-align: center;
}

.pressure-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}

.stat-row {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px;
  margin-top: 8px;
}

.stat-row.secondary {
  opacity: 0.9;
}

.stat-k {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.stat-v {
  margin-top: 2px;
  font-size: 18px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.danger {
  color: var(--el-color-danger);
}

.empty {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  padding: 12px;
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
  text-align: center;
}

.alert-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.alert-list li {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}

.alert-title {
  font-size: 13px;
  font-weight: 600;
}

.alert-msg {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 2px;
}

.alert-time {
  font-size: 11px;
  color: var(--el-text-color-placeholder);
  margin-top: 4px;
}
</style>
