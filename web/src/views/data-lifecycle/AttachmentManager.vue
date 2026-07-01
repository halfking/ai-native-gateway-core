<template>
  <div class="attachment-manager">
    <!-- 策略（只读）+ 统计 -->
    <div class="grid-2">
      <div class="card">
        <h3 class="card-title">附件统计</h3>
        <div v-if="stats" class="stats-grid">
          <div class="stat-box">
            <div class="stat-label">总附件数</div>
            <div class="stat-value">{{ formatNumber(stats.total_count) }}</div>
            <div class="stat-meta">所有记录中的 attachment 元素总数</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">总大小</div>
            <div class="stat-value">{{ humanBytes(stats.total_bytes) }}</div>
            <div class="stat-meta">request_logs.attachments JSONB 累计</div>
          </div>
        </div>
        <div v-if="stats && stats.breakdown.length" class="breakdown">
          <h4>按类型 / content_type</h4>
          <table class="data-table small">
            <thead>
              <tr>
                <th>type</th>
                <th>content_type</th>
                <th>数量</th>
                <th>总大小</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="b in stats.breakdown" :key="b.type + b.content_type">
                <td><code class="code">{{ b.type || '—' }}</code></td>
                <td>{{ b.content_type || '—' }}</td>
                <td class="num">{{ formatNumber(b.count) }}</td>
                <td class="num">{{ humanBytes(b.total_bytes) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">清理策略</h3>
        <div v-if="policy" class="policy-box">
          <div class="metric-row">
            <span>保留天数</span>
            <span class="metric-val">{{ policy.policy.retention_days }} 天</span>
          </div>
          <div class="metric-row">
            <span>单文件上限</span>
            <span class="metric-val">{{ humanBytes(policy.policy.max_size_bytes) }}</span>
          </div>
          <div class="metric-row">
            <span>自动清理</span>
            <span class="metric-val">
              <span :class="['pill', policy.policy.auto_cleanup ? 'ok' : 'dim']">
                {{ policy.policy.auto_cleanup ? '已启用' : '未启用' }}
              </span>
            </span>
          </div>
          <div class="metric-row">
            <span>清理文件实体</span>
            <span class="metric-val">
              <span :class="['pill', policy.policy.delete_filesystem ? 'ok' : 'dim']">
                {{ policy.policy.delete_filesystem ? '是' : '否（仅置 NULL）' }}
              </span>
            </span>
          </div>
          <p class="policy-desc">{{ policy.policy.description }}</p>
          <p class="policy-note">📌 {{ policy.note }}</p>
        </div>
      </div>
    </div>

    <!-- 清理操作 -->
    <div class="card">
      <h3 class="card-title">附件元数据清理</h3>
      <p class="hint">
        将 <code>request_logs.attachments</code> 列中 <strong>older_than_days</strong> 之前的元素置为 NULL。
        不删除文件系统中的实体文件（文件按 hash 命名可去重，需另外处理）。
      </p>
      <div class="cleanup-form">
        <div class="form-row">
          <div class="field">
            <label>早于 N 天（默认 30）</label>
            <input type="number" v-model.number="cleanup.older_than_days" min="1" max="3650" />
          </div>
          <div class="field">
            <label>备注 (审计用)</label>
            <input v-model="cleanup.reason" placeholder="可选" />
          </div>
        </div>

        <div v-if="lastResult" class="result-box" :class="lastResult.executed ? 'executed' : 'preview'">
          <template v-if="!lastResult.executed">
            <strong>预览：</strong>
            将置 NULL <strong>{{ formatNumber(lastResult.affected_records) }}</strong> 个元素，
            累计 <strong>{{ humanBytes(lastResult.total_bytes) }}</strong>
            <div class="action-note">{{ lastResult.action }}</div>
          </template>
          <template v-else>
            <strong>已执行：</strong>
            影响 <strong>{{ formatNumber(lastResult.rows_affected) }}</strong> 行
            <div class="action-note">{{ lastResult.action }}</div>
            <div class="action-note">文件实体：{{ lastResult.filesystem_files }}</div>
          </template>
        </div>

        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="onPreview" :disabled="loading">预览</button>
          <button class="btn btn-danger btn-sm" @click="onExecute" :disabled="loading || !lastResult || lastResult.executed">
            执行清理（仅 super_admin）
          </button>
        </div>
      </div>
    </div>

    <!-- 列表 -->
    <div class="card">
      <div class="card-header">
        <h3 class="card-title">最近含附件的请求</h3>
        <div class="filter-row">
          <input type="date" v-model="filter.since" @change="load" title="起始日期" />
          <input type="date" v-model="filter.until" @change="load" title="结束日期" />
          <button class="btn btn-ghost btn-sm" @click="load" :disabled="loading">
            {{ loading ? '加载中…' : '查询' }}
          </button>
        </div>
      </div>
      <div class="att-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>tenant</th>
              <th>model</th>
              <th>状态</th>
              <th>附件数</th>
              <th>大小合计</th>
              <th>详情</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="it in items" :key="it.request_id">
              <td>{{ formatDate(it.ts) }}</td>
              <td>{{ it.tenant_id }}</td>
              <td><code class="code">{{ it.client_model || '—' }}</code></td>
              <td>
                <span :class="['pill', it.success ? 'ok' : 'danger']">
                  {{ it.success ? '成功' : '失败' }}
                </span>
              </td>
              <td class="num">{{ it.attachments?.length || 0 }}</td>
              <td class="num">{{ humanBytes(attachmentsBytes(it.attachments)) }}</td>
              <td>
                <button class="link" @click="openDetail(it)">查看 JSONB</button>
              </td>
            </tr>
            <tr v-if="!items.length && !loading">
              <td colspan="7" class="empty">无匹配附件</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 详情弹窗 -->
    <div v-if="detail" class="modal-mask" @click.self="detail = null">
      <div class="modal">
        <div class="modal-header">
          <h3>request_id: {{ detail.request_id }}</h3>
          <button class="btn btn-ghost btn-sm" @click="detail = null">关闭</button>
        </div>
        <pre class="json-block">{{ JSON.stringify(detail.attachments, null, 2) }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import {
  attachmentList,
  attachmentStats,
  attachmentPolicyGet,
  attachmentCleanupPreview,
  attachmentCleanupExecute,
  type AttachmentListItem,
  type AttachmentStatsResponse,
  type AttachmentPolicyResponse,
} from '../../api'

const items = ref<AttachmentListItem[]>([])
const stats = ref<AttachmentStatsResponse | null>(null)
const policy = ref<AttachmentPolicyResponse | null>(null)
const detail = ref<AttachmentListItem | null>(null)
const loading = ref(false)
const filter = reactive({ since: '', until: '' })
const cleanup = reactive({ older_than_days: 30, reason: '' })

const lastResult = ref<any>(null)

async function load() {
  loading.value = true
  try {
    const [list, s, p] = await Promise.all([
      attachmentList({
        since: filter.since ? new Date(filter.since).toISOString() : undefined,
        until: filter.until ? new Date(filter.until + 'T23:59:59').toISOString() : undefined,
        limit: 100,
      }),
      attachmentStats(),
      attachmentPolicyGet(),
    ])
    items.value = list.items
    stats.value = s
    policy.value = p
  } catch (e) {
    console.error('attachment load failed', e)
  } finally {
    loading.value = false
  }
}

async function onPreview() {
  loading.value = true
  try {
    lastResult.value = await attachmentCleanupPreview({ older_than_days: cleanup.older_than_days })
    lastResult.value.executed = false
  } catch (e: any) {
    alert('预览失败：' + (e?.response?.data?.error?.detail || e?.message))
  } finally {
    loading.value = false
  }
}

async function onExecute() {
  if (!lastResult.value) return
  if (!confirm(`确认执行？\n将置 NULL ${formatNumber(lastResult.value.affected_records)} 个 elements\n累计 ${humanBytes(lastResult.value.total_bytes)}\n（不会删除文件系统实体文件）`)) return
  loading.value = true
  try {
    const r = await attachmentCleanupExecute({ older_than_days: cleanup.older_than_days })
    lastResult.value = { ...r, executed: true }
    await load()
  } catch (e: any) {
    alert('执行失败：' + (e?.response?.data?.error?.detail || e?.message))
  } finally {
    loading.value = false
  }
}

function openDetail(it: AttachmentListItem) {
  detail.value = it
}

function attachmentsBytes(arr: any[] | undefined): number {
  if (!arr) return 0
  let s = 0
  for (const a of arr) {
    if (a && typeof a === 'object' && a.size) s += Number(a.size) || 0
  }
  return s
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
function formatDate(s: string): string {
  if (!s) return '—'
  return s.slice(0, 19).replace('T', ' ')
}

onMounted(load)
</script>

<style scoped>
.attachment-manager { display: flex; flex-direction: column; gap: 16px; }
.grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.grid-2 > .card { margin-bottom: 0; }

.card-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.card-title { margin: 0 0 12px; font-size: 14px; font-weight: 600; color: #e6edf3; }

.stats-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 16px;
}
.stat-box {
  background: #0f1117;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 12px;
}
.stat-label { font-size: 11px; color: #8b949e; margin-bottom: 4px; }
.stat-value { font-size: 22px; font-weight: 700; color: #e6edf3; font-variant-numeric: tabular-nums; }
.stat-meta { font-size: 11px; color: #6b7280; margin-top: 4px; }

.breakdown h4 { font-size: 12px; color: #8b949e; margin: 8px 0; font-weight: 500; }

.policy-box { display: flex; flex-direction: column; gap: 0; }
.metric-row { display: flex; justify-content: space-between; align-items: center; padding: 6px 0; font-size: 13px; border-bottom: 1px solid rgba(48, 54, 61, 0.5); }
.metric-row:last-of-type { border-bottom: none; }
.metric-row > span:first-child { color: #8b949e; }
.metric-val { color: #e6edf3; font-variant-numeric: tabular-nums; }
.policy-desc { font-size: 12px; color: #8b949e; margin: 8px 0 0; }
.policy-note { font-size: 11px; color: #fbbf24; margin: 8px 0 0; }

.cleanup-form { display: flex; flex-direction: column; gap: 12px; }
.form-row { display: flex; gap: 12px; flex-wrap: wrap; }
.hint { font-size: 12px; color: #8b949e; margin: 0 0 12px; }
.hint code { background: #0f1117; padding: 1px 6px; border-radius: 4px; color: #818cf8; font-family: ui-monospace, SFMono-Regular, monospace; font-size: 11px; }

.field { display: flex; flex-direction: column; gap: 4px; min-width: 200px; }
.field label { font-size: 12px; color: #8b949e; }
.field input {
  padding: 6px 10px; background: #0f1117; border: 1px solid #30363d; border-radius: 6px;
  color: #e6edf3; font-size: 13px;
}
.field input:focus { outline: none; border-color: #6366f1; }

.result-box { padding: 10px 14px; border-radius: 6px; font-size: 13px; }
.result-box.preview { background: rgba(96, 165, 250, 0.1); color: #60a5fa; }
.result-box.executed { background: rgba(52, 211, 153, 0.1); color: #34d399; }
.action-note { font-size: 12px; color: #e6edf3; margin-top: 4px; opacity: 0.85; }

.form-actions { display: flex; gap: 8px; }

.att-table-wrap, .data-table-wrap { overflow-x: auto; }
.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table.small { font-size: 12px; }
.data-table th, .data-table td { padding: 8px 10px; border-bottom: 1px solid #30363d; text-align: left; }
.data-table th { color: #8b949e; font-weight: 500; white-space: nowrap; }
.data-table td { color: #e6edf3; font-variant-numeric: tabular-nums; }
.data-table .num { text-align: right; }
.data-table .empty { text-align: center; color: #8b949e; padding: 24px; }

.code { font-family: ui-monospace, SFMono-Regular, monospace; font-size: 11px; padding: 1px 6px; background: #0f1117; border-radius: 4px; color: #818cf8; }

.pill { display: inline-block; padding: 1px 8px; border-radius: 8px; font-size: 11px; font-weight: 500; }
.pill.ok { background: rgba(52, 211, 153, 0.15); color: #34d399; }
.pill.danger { background: rgba(248, 113, 113, 0.15); color: #f87171; }
.pill.dim { color: #6b7280; }

.link { color: #60a5fa; text-decoration: none; font-size: 12px; background: none; border: none; cursor: pointer; }
.link:hover { text-decoration: underline; }

.filter-row { display: flex; gap: 8px; align-items: center; }
.filter-row input {
  padding: 4px 10px; background: #0f1117; border: 1px solid #30363d; border-radius: 6px;
  color: #e6edf3; font-size: 13px;
}

.btn { padding: 6px 14px; border-radius: 6px; border: 1px solid transparent; font-size: 13px; cursor: pointer; }
.btn-sm { padding: 4px 10px; font-size: 12px; }
.btn-primary { background: #6366f1; color: #fff; }
.btn-primary:hover:not(:disabled) { background: #818cf8; }
.btn-danger { background: #ef4444; color: #fff; }
.btn-danger:hover:not(:disabled) { background: #f87171; }
.btn-ghost { background: transparent; border-color: #30363d; color: #e6edf3; }
.btn-ghost:hover:not(:disabled) { background: #21262d; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

.modal-mask {
  position: fixed; inset: 0; background: rgba(0,0,0,0.6);
  display: flex; align-items: center; justify-content: center; z-index: 9999;
}
.modal {
  background: #161b22; border: 1px solid #30363d; border-radius: 10px;
  width: min(800px, 90vw); max-height: 80vh;
  display: flex; flex-direction: column;
}
.modal-header { display: flex; justify-content: space-between; align-items: center; padding: 12px 16px; border-bottom: 1px solid #30363d; }
.modal-header h3 { margin: 0; font-size: 14px; color: #e6edf3; font-family: ui-monospace, SFMono-Regular, monospace; }
.json-block {
  padding: 16px; overflow: auto; flex: 1;
  font-family: ui-monospace, SFMono-Regular, monospace; font-size: 12px;
  color: #e6edf3; background: #0f1117; margin: 0;
  white-space: pre-wrap; word-break: break-all;
}

@media (max-width: 800px) {
  .grid-2 { grid-template-columns: 1fr; }
  .stats-grid { grid-template-columns: 1fr; }
}
</style>
