<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { getModelTaskIndex, type ModelTaskIndexItem } from '../../api-autoroute'

const props = defineProps<{
  taskType?: string
  top?: number
}>()

const loading = ref(false)
const bucket = ref<string | null>(null)
const items = ref<ModelTaskIndexItem[]>([])
const warning = ref('')

async function load() {
  if (!props.taskType) {
    items.value = []
    bucket.value = null
    return
  }
  loading.value = true
  warning.value = ''
  try {
    const res = await getModelTaskIndex(props.taskType, props.top ?? 10)
    bucket.value = res.bucket
    items.value = res.items
    warning.value = res.warning ?? ''
  } catch (e) {
    console.error('ModelTaskIndexPanel', e)
    items.value = []
  } finally {
    loading.value = false
  }
}

function fmtPct(n?: number): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  return (n * 100).toFixed(1) + '%'
}

function fmtMs(n?: number): string {
  if (!n || n <= 0) return '-'
  return n < 1000 ? `${Math.round(n)}ms` : `${(n / 1000).toFixed(1)}s`
}

watch(() => props.taskType, load)
onMounted(load)
</script>

<template>
  <div class="mti-panel">
    <div v-if="!taskType" class="empty-hint">点击热力图行以查看该任务类型的模型指数</div>
    <div v-else-if="loading" class="empty-hint">加载模型任务指数…</div>
    <div v-else-if="warning && !items.length" class="empty-hint">{{ warning }}</div>
    <div v-else-if="!items.length" class="empty-hint">该任务类型暂无指数数据</div>
    <template v-else>
      <div v-if="bucket" class="mti-meta text-muted">数据桶 {{ new Date(bucket).toLocaleString() }}</div>
      <div class="table-scroll">
        <table class="dense-table">
          <thead>
            <tr>
              <th>#</th>
              <th>模型</th>
              <th>样本</th>
              <th>成功率</th>
              <th>P95</th>
              <th>均延迟</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in items" :key="(row.canonical_id ?? 0) + ':' + row.task_type + ':' + i">
              <td class="num">{{ i + 1 }}</td>
              <td>
                <div class="model-name">{{ row.canonical_name || '-' }}</div>
                <div v-if="row.primary_credential_id" class="text-muted mono-sm">cred #{{ row.primary_credential_id }}</div>
              </td>
              <td>{{ row.sample_count ?? '-' }}</td>
              <td>{{ fmtPct(row.success_rate) }}</td>
              <td>{{ fmtMs(row.p95_latency_ms) }}</td>
              <td>{{ fmtMs(row.avg_latency_ms) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<style scoped>
.mti-panel { width: 100%; }
.mti-meta { font-size: 10px; margin-bottom: 6px; }
.empty-hint {
  padding: 12px;
  text-align: center;
  color: var(--muted);
  font-size: 11px;
}
.table-scroll { overflow-x: auto; }
.dense-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 10px;
}
.dense-table th,
.dense-table td {
  border-bottom: 1px solid var(--border);
  padding: 4px 6px;
  text-align: left;
}
.dense-table th { color: var(--muted); font-weight: 500; }
.num { text-align: center; color: var(--muted); }
.model-name { font-weight: 500; }
.mono-sm { font-size: 9px; font-family: var(--mono, monospace); }
</style>
