<template>
  <div class="blob-manager">
    <div class="card">
      <div class="card-header">
        <h3 class="card-title">大字段 Top {{ rows.length }}（按 request_body + outbound_body 真实字节数）</h3>
        <button class="btn btn-ghost btn-sm" @click="load" :disabled="loading">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
      <div class="blob-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>request_id</th>
              <th>session</th>
              <th>tenant</th>
              <th>时间</th>
              <th>请求体</th>
              <th>出参体</th>
              <th>合计</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.request_id">
              <td><code class="code">{{ r.request_id.slice(0, 16) }}…</code></td>
              <td><code class="code">{{ r.session_key?.slice(0, 14) || '—' }}</code></td>
              <td>{{ r.tenant_id }}</td>
              <td>{{ r.occurred_at.slice(0, 19).replace('T', ' ') }}</td>
              <td class="num">{{ humanBytes(r.request_body_bytes) }}</td>
              <td class="num">{{ humanBytes(r.outbound_body_bytes) }}</td>
              <td class="num strong">{{ r.total_human }}</td>
            </tr>
            <tr v-if="!rows.length && !loading">
              <td colspan="7" class="empty">暂无数据</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="card">
      <h3 class="card-title">清理策略</h3>
      <div class="cleanup-form">
        <div class="form-row">
          <label class="form-label">触发条件（任一满足即清理）：</label>
        </div>
        <div class="form-row">
          <div class="field">
            <label>早于 N 天</label>
            <input type="number" v-model.number="form.older_than_days" min="0" max="3650" placeholder="如 30" />
            <span class="hint">0 = 不按时间</span>
          </div>
          <div class="field">
            <label>单字段 ≥ N KB</label>
            <input type="number" v-model.number="form.larger_than_kb" min="0" max="1048576" placeholder="如 100" />
            <span class="hint">0 = 不按大小</span>
          </div>
          <div class="field">
            <label>范围</label>
            <select v-model="form.scope">
              <option value="current">当前租户</option>
              <option value="all">全租户（仅 super_admin）</option>
            </select>
          </div>
        </div>

        <div v-if="preview" class="preview-box">
          <div class="preview-row">
            <span>影响行数：</span>
            <strong>{{ formatNumber(preview.affected_rows) }}</strong>
          </div>
          <div class="preview-row">
            <span>预计释放：</span>
            <strong class="highlight">{{ preview.estimated_freed_human }}</strong>
          </div>
          <div v-if="preview.warning_message" class="preview-warn">
            ⚠️ {{ preview.warning_message }}
          </div>
        </div>

        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="onPreview" :disabled="loading">
            预览影响
          </button>
          <button
            class="btn btn-danger btn-sm"
            @click="onExecute"
            :disabled="loading || !preview"
          >
            执行清理（仅 super_admin）
          </button>
        </div>

        <div v-if="lastResult" class="result-box" :class="lastResult.executed ? 'executed' : 'preview'">
          <div>{{ lastResult.executed ? '已执行' : '预览' }}: 影响 {{ formatNumber(lastResult.affected_rows) }} 行，
            释放 {{ lastResult.estimated_freed_human }}，耗时 {{ lastResult.finished_at ? secondsBetween(lastResult.started_at, lastResult.finished_at) : '—' }}s
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import {
  dataLifecycleBlobTop,
  dataLifecycleBlobCleanupPreview,
  dataLifecycleBlobCleanupExecute,
  type BlobRow,
  type BlobCleanupRequest,
  type BlobCleanupResponse,
} from '../../api'

const rows = ref<BlobRow[]>([])
const preview = ref<BlobCleanupResponse | null>(null)
const lastResult = ref<BlobCleanupResponse | null>(null)
const loading = ref(false)
const form = ref<BlobCleanupRequest>({
  older_than_days: 30,
  larger_than_kb: 100,
  scope: 'current',
})

async function load() {
  loading.value = true
  try {
    const r = await dataLifecycleBlobTop(50)
    rows.value = r.rows
  } catch (e) {
    console.error('blob top failed', e)
  } finally {
    loading.value = false
  }
}

async function onPreview() {
  if (!form.value.older_than_days && !form.value.larger_than_kb) {
    alert('请至少指定一个条件')
    return
  }
  loading.value = true
  try {
    preview.value = await dataLifecycleBlobCleanupPreview(form.value)
  } catch (e: any) {
    alert('预览失败：' + (e?.response?.data?.error?.detail || e?.message || 'unknown'))
  } finally {
    loading.value = false
  }
}

async function onExecute() {
  if (!preview.value) return
  if (!confirm(`确认清理？\n影响 ${formatNumber(preview.value.affected_rows)} 行\n释放约 ${preview.value.estimated_freed_human}\n（仅置 NULL request_body / outbound_body，保留元数据）`)) return
  loading.value = true
  try {
    const r = await dataLifecycleBlobCleanupExecute(form.value)
    lastResult.value = r
    preview.value = null
    await load()
  } catch (e: any) {
    alert('执行失败：' + (e?.response?.data?.error?.detail || e?.message || 'unknown'))
  } finally {
    loading.value = false
  }
}

function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(1)} ${units[i]}`
}
function formatNumber(n: number): string { return n.toLocaleString('zh-CN') }
function secondsBetween(a: string, b: string): string {
  const ms = new Date(b).getTime() - new Date(a).getTime()
  return (ms / 1000).toFixed(1)
}

onMounted(load)
</script>

<style scoped>
.blob-manager { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }

.blob-table-wrap { overflow-x: auto; }
.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table th, .data-table td { padding: 8px 10px; border-bottom: 1px solid #30363d; text-align: left; }
.data-table th { color: #8b949e; font-weight: 500; white-space: nowrap; }
.data-table td { color: #e6edf3; font-variant-numeric: tabular-nums; }
.data-table .num { text-align: right; }
.data-table .strong { color: #e6edf3; font-weight: 600; }
.data-table .empty { text-align: center; color: #8b949e; padding: 24px; }
.code { font-family: ui-monospace, SFMono-Regular, monospace; font-size: 11px; padding: 2px 6px; background: #0f1117; border-radius: 4px; color: #818cf8; }

.cleanup-form { display: flex; flex-direction: column; gap: 12px; }
.form-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.form-label { font-size: 13px; color: #8b949e; }
.field { display: flex; flex-direction: column; gap: 4px; min-width: 160px; }
.field label { font-size: 12px; color: #8b949e; }
.field input, .field select {
  padding: 6px 10px; background: #0f1117; border: 1px solid #30363d; border-radius: 6px;
  color: #e6edf3; font-size: 13px;
}
.field input:focus, .field select:focus { outline: none; border-color: #6366f1; }
.field .hint { font-size: 11px; color: #6b7280; }

.preview-box { padding: 12px; background: rgba(99, 102, 241, 0.08); border: 1px solid rgba(99, 102, 241, 0.25); border-radius: 6px; }
.preview-row { display: flex; justify-content: space-between; margin: 4px 0; font-size: 13px; }
.preview-row .highlight { color: #fbbf24; font-weight: 600; }
.preview-warn { margin-top: 8px; padding: 6px 10px; background: rgba(251, 191, 36, 0.1); border-left: 2px solid #fbbf24; color: #fbbf24; font-size: 12px; border-radius: 4px; }

.form-actions { display: flex; gap: 8px; }

.btn { padding: 6px 14px; border-radius: 6px; border: 1px solid transparent; font-size: 13px; cursor: pointer; }
.btn-sm { padding: 4px 10px; font-size: 12px; }
.btn-primary { background: #6366f1; color: #fff; }
.btn-primary:hover:not(:disabled) { background: #818cf8; }
.btn-danger { background: #ef4444; color: #fff; }
.btn-danger:hover:not(:disabled) { background: #f87171; }
.btn-ghost { background: transparent; border-color: #30363d; color: #e6edf3; }
.btn-ghost:hover:not(:disabled) { background: #21262d; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

.result-box { padding: 8px 12px; border-radius: 6px; font-size: 13px; }
.result-box.preview { background: rgba(96, 165, 250, 0.1); color: #60a5fa; }
.result-box.executed { background: rgba(52, 211, 153, 0.1); color: #34d399; }
</style>
