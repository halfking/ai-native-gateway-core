<script setup lang="ts">
// RequestTraceView.vue — 2026-07-17
// 独立请求链路追踪页面:
//   - 左侧: 近 1h 失败请求列表 (来自 /api/logs)
//   - 右侧: 选中请求的链路详情时间轴 + AI 提示词生成
//
// 数据源:
//   - 列表: 复用 RequestLogRow (request_logs 表中 failure 状态行, /api/logs)
//   - 详情: /api/admin/requests/{id}/trace (新增)
//   - AI 提示词: /api/admin/requests/{id}/ai-prompt (新增)
//
// 使用时:
//   - super_admin 访问 /admin/request-trace
//   - 普通用户不可见 (路由 meta.requiresSuper)
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getRequestLogs,
  type RequestLogRow,
} from '../api'
import {
  getRequestTrace,
  buildAIPrompt,
  type TraceEvent,
  type RequestTrace,
} from '../api/trace'

const { t } = useI18n()

// ─── 列表 ───────────────────────────────────────────────────────────────────
const failedRequests = ref<RequestLogRow[]>([])
const listLoading = ref(false)
const listError = ref<string | null>(null)
const listHours = ref<1 | 6 | 24>(1)
const autoRefresh = ref(true)
let timer: ReturnType<typeof setInterval> | null = null
const refreshCountdown = ref(0)

async function loadList() {
  listLoading.value = true
  listError.value = null
  try {
    const resp = await getRequestLogs({
      hours: listHours.value,
      success: false,
      limit: 100,
      offset: 0,
    } as any)
    failedRequests.value = resp.rows || []
  } catch (e: unknown) {
    listError.value = e instanceof Error ? e.message : '加载列表失败'
  } finally {
    listLoading.value = false
    refreshCountdown.value = 10
  }
}

function startAutoRefresh() {
  stopAutoRefresh()
  if (autoRefresh.value) {
    timer = setInterval(() => {
      if (refreshCountdown.value <= 0) {
        loadList()
      } else {
        refreshCountdown.value -= 1
      }
    }, 1000)
  }
}

function stopAutoRefresh() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(autoRefresh, (v) => (v ? startAutoRefresh() : stopAutoRefresh()))
watch(listHours, () => loadList())

onMounted(async () => {
  await loadList()
  startAutoRefresh()
})
onBeforeUnmount(stopAutoRefresh)

// ─── 详情 ───────────────────────────────────────────────────────────────────
const selectedRequestId = ref<string | null>(null)
const trace = ref<RequestTrace | null>(null)
const detailLoading = ref(false)
const detailError = ref<string | null>(null)
const traceSource = ref<'redis' | 'postgres' | ''>('')

async function selectRequest(row: RequestLogRow) {
  selectedRequestId.value = row.request_id
  trace.value = null
  detailError.value = null
  await loadTrace(row.request_id)
}

async function loadTrace(requestId: string) {
  detailLoading.value = true
  detailError.value = null
  try {
    const t = await getRequestTrace(requestId)
    trace.value = t
    traceSource.value = t.source
  } catch (e: unknown) {
    detailError.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    detailLoading.value = false
  }
}

// ─── AI 提示词 ───────────────────────────────────────────────────────────────
const showAIModal = ref(false)
const aiQuestion = ref('')
const aiLang = ref<'zh' | 'en'>('zh')
const aiPrompt = ref('')
const aiTokens = ref(0)
const aiEventCount = ref(0)
const aiGenerating = ref(false)
const aiError = ref<string | null>(null)
const aiCopySuccess = ref(false)

async function openAIModal() {
  if (!selectedRequestId.value) return
  showAIModal.value = true
  aiPrompt.value = ''
  aiError.value = null
}

async function generatePrompt() {
  if (!selectedRequestId.value) return
  aiGenerating.value = true
  aiError.value = null
  try {
    const langHint = aiLang.value
    const resp = await buildAIPrompt(selectedRequestId.value, aiQuestion.value || undefined, langHint)
    aiPrompt.value = resp.prompt
    aiTokens.value = resp.tokens_estimate
    aiEventCount.value = resp.event_count
  } catch (e: unknown) {
    aiError.value = e instanceof Error ? e.message : '生成失败'
  } finally {
    aiGenerating.value = false
  }
}

async function copyPrompt() {
  if (!aiPrompt.value) return
  try {
    await navigator.clipboard.writeText(aiPrompt.value)
    aiCopySuccess.value = true
    setTimeout(() => (aiCopySuccess.value = false), 2000)
  } catch {
    aiError.value = '剪贴板不可用, 请手动复制'
  }
}

// ─── 详情展开 / 折叠 ────────────────────────────────────────────────────────
const expandedEvents = ref<Set<number>>(new Set())

function toggleDetails(seq: number) {
  if (expandedEvents.value.has(seq)) {
    expandedEvents.value.delete(seq)
  } else {
    expandedEvents.value.add(seq)
  }
}

// ─── 工具 ───────────────────────────────────────────────────────────────────
function statusIcon(ev: TraceEvent): { icon: string; color: string; hintKey: string } {
  switch (ev.status) {
    case 'success':
      return { icon: '✅', color: '#52c41a', hintKey: 'iconHint.success' }
    case 'failed':
      return { icon: '❌', color: '#ff4d4f', hintKey: 'iconHint.failed' }
    case 'timeout':
      return { icon: '⏱️', color: '#faad14', hintKey: 'iconHint.timeout' }
    default:
      return { icon: '⏭️', color: '#bfbfbf', hintKey: 'iconHint.skipped' }
  }
}

const moduleLabel = (m: string) => t(`trace.stage.module_${m}`, m)

function fmtTime(iso: string) {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    const pad = (n: number) => n.toString().padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  } catch {
    return iso
  }
}

function truncStr(v: unknown, n = 60) {
  const s = v == null ? '' : String(v)
  if (s.length <= n) return s
  return s.slice(0, n) + '…'
}

// 简短复制整段 trace JSON
async function copyRawJson() {
  if (!trace.value) return
  try {
    await navigator.clipboard.writeText(JSON.stringify(trace.value, null, 2))
  } catch {
    /* best-effort */
  }
}

const statusLabel = computed(() => (trace: RequestTrace | null) => {
  if (!trace) return ''
  if (trace.final_status === 'success') return t('trace.detail.statusSuccess')
  if (trace.final_status === 'timeout') return t('trace.detail.statusTimeout')
  if (trace.final_status === 'failed') return t('trace.detail.statusFailed')
  return t('trace.detail.statusInProgress')
})
</script>

<template>
  <div class="trace-page" data-trace-page>
    <!-- 顶栏 ───────────────────────────────────────────────────────── -->
    <header class="trace-header">
      <div>
        <h1>{{ t('trace.page.title') }}</h1>
        <p class="trace-subtitle">{{ t('trace.page.subtitle') }}</p>
      </div>
      <div class="trace-header-actions">
        <label class="auto-refresh-toggle">
          <input v-model="autoRefresh" type="checkbox" />
          {{ t('trace.page.autoRefresh') }}
        </label>
        <button class="btn-secondary" :disabled="!selectedRequestId" @click="loadTrace(selectedRequestId!)">
          {{ t('trace.detail.refresh') }}
        </button>
        <button class="btn-primary" :disabled="!selectedRequestId" @click="openAIModal">
          {{ t('trace.aiPrompt.button') }}
        </button>
      </div>
    </header>

    <!-- 主体: 左侧列表 + 右侧详情 ────────────────────────────────────── -->
    <div class="trace-body">
      <!-- 左侧列表 -->
      <aside class="trace-list">
        <div class="trace-list-header">
          <h3>{{ t('trace.list.title') }}</h3>
          <div class="trace-list-controls">
            <select v-model.number="listHours">
              <option :value="1">{{ t('trace.list.filterTimeRangeOptions.h1') }}</option>
              <option :value="6">{{ t('trace.list.filterTimeRangeOptions.h6') }}</option>
              <option :value="24">{{ t('trace.list.filterTimeRangeOptions.h24') }}</option>
            </select>
            <span v-if="autoRefresh" class="refresh-hint">{{ t('trace.page.refreshIn', { n: refreshCountdown }) }}</span>
          </div>
        </div>
        <div v-if="listLoading && !failedRequests.length" class="loading">{{ t('requests.common.loading') }}</div>
        <div v-else-if="listError" class="error">{{ listError }}</div>
        <div v-else-if="!failedRequests.length" class="empty">{{ t('trace.list.empty') }}</div>
        <table v-else class="trace-list-table">
          <thead>
            <tr>
              <th>{{ t('trace.list.colTime') }}</th>
              <th>{{ t('trace.list.colModel') }}</th>
              <th>{{ t('trace.list.colStatus') }}</th>
              <th>{{ t('trace.list.colLatency') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in failedRequests"
              :key="row.request_id"
              :class="{ active: selectedRequestId === row.request_id }"
              @click="selectRequest(row)"
            >
              <td class="muted">{{ fmtTime(row.ts) }}</td>
              <td class="model-cell">{{ row.client_model || row.outbound_model || '—' }}</td>
              <td>
                <span class="badge badge-failed">{{ row.error_kind || row.request_status }}</span>
              </td>
              <td class="muted">{{ row.latency_ms ?? '—' }}ms</td>
            </tr>
          </tbody>
        </table>
      </aside>

      <!-- 右侧详情 -->
      <section class="trace-detail">
        <div v-if="!selectedRequestId" class="placeholder">
          {{ t('trace.list.selectToView') }}
        </div>
        <div v-else>
          <!-- 基本信息 -->
          <div class="detail-card">
            <div class="detail-card-header">
              <h3>{{ t('trace.detail.basicInfo') }}</h3>
              <span v-if="traceSource" class="trace-source-pill" :class="'src-' + traceSource">
                {{ traceSource === 'redis' ? t('trace.detail.sourceRedis') : t('trace.detail.sourcePostgres') }}
              </span>
            </div>
            <div v-if="detailLoading" class="loading">{{ t('requests.common.loading') }}</div>
            <div v-else-if="detailError" class="error">{{ detailError }}</div>
            <div v-else-if="trace" class="trace-meta-grid">
              <div>
                <label>{{ t('trace.detail.requestId') }}</label>
                <code>{{ trace.request_id }}</code>
              </div>
              <div>
                <label>{{ t('trace.detail.finalStatus') }}</label>
                <span class="status-text" :class="'status-' + trace.final_status">{{ statusLabel(trace) }}</span>
              </div>
              <div v-if="trace.failed_at_stage">
                <label>{{ t('trace.detail.failedAtStage') }}</label>
                <code>{{ t('trace.stage.' + trace.failed_at_stage, trace.failed_at_stage) }}</code>
              </div>
              <div>
                <label>{{ t('trace.detail.eventCount') }}</label>
                <span>{{ trace.events.length }}</span>
              </div>
              <div>
                <label>{{ t('trace.detail.totalDuration') }}</label>
                <span>{{ trace.total_duration_ms }}ms</span>
              </div>
              <button class="link-btn" @click="copyRawJson">{{ t('trace.detail.copyJson') }}</button>
            </div>
            <div v-else class="empty">{{ t('trace.detail.emptyEvents') }}</div>
          </div>

          <!-- 时间轴 -->
          <div v-if="trace && trace.events.length" class="detail-card">
            <h3>{{ t('trace.detail.title') }}</h3>
            <ol class="timeline">
              <li
                v-for="ev in trace.events"
                :key="ev.seq"
                :class="['timeline-item', 'status-' + ev.status]"
              >
                <div class="timeline-item-head">
                  <span class="seq">{{ ev.seq }}</span>
                  <span class="icon">{{ statusIcon(ev).icon }}</span>
                  <span class="stage">{{ t('trace.stage.' + ev.stage, ev.stage) }}</span>
                  <span class="module">{{ moduleLabel(ev.module) }}</span>
                  <span v-if="ev.duration_ms > 0" class="duration">{{ ev.duration_ms }}ms</span>
                  <button class="toggle-btn" @click="toggleDetails(ev.seq)">
                    {{ expandedEvents.has(ev.seq) ? '收起 ▲' : '展开 ▼' }}
                  </button>
                </div>
                <div v-if="expandedEvents.has(ev.seq)" class="timeline-item-body">
                  <div class="muted small">{{ fmtTime(ev.timestamp) }}</div>
                  <div v-if="ev.error" class="error error-text">⚠ {{ ev.error }}</div>
                  <table v-if="ev.details && Object.keys(ev.details).length" class="kv-table">
                    <tr v-for="(v, k) in ev.details" :key="k">
                      <td class="k">{{ k }}</td>
                      <td class="v">{{ truncStr(v, 200) }}</td>
                    </tr>
                  </table>
                  <div v-if="ev.snapshot" class="snapshot-box">
                    <h4>📦 失败快照</h4>
                    <div v-if="ev.snapshot.candidates && ev.snapshot.candidates.length">
                      候选凭据数: {{ ev.snapshot.candidates.length }}
                    </div>
                    <div v-if="ev.snapshot.routing_state">路由状态: {{ ev.snapshot.routing_state }}</div>
                    <div v-if="ev.snapshot.credential_mode">凭据模式: {{ ev.snapshot.credential_mode }}</div>
                    <div v-if="ev.snapshot.concurrency_slot">
                      并发槽位: {{ ev.snapshot.concurrency_slot.in_use }} / {{ ev.snapshot.concurrency_slot.max_slots }}
                      <span v-if="ev.snapshot.concurrency_slot.blocked" class="badge badge-failed">
                        BLOCKED: {{ ev.snapshot.concurrency_slot.reason }}
                      </span>
                    </div>
                    <div v-if="ev.snapshot.node_probe_state && Object.keys(ev.snapshot.node_probe_state).length">
                      <h5>节点探测状态</h5>
                      <table class="kv-table">
                        <tr v-for="(v, k) in ev.snapshot.node_probe_state" :key="k">
                          <td class="k">{{ k }}</td>
                          <td class="v">{{ truncStr(v, 120) }}</td>
                        </tr>
                      </table>
                    </div>
                    <div v-if="ev.snapshot.failure_hint" class="failure-hint">
                      💡 {{ t('trace.failureHint.' + ev.snapshot.failure_hint, ev.snapshot.failure_hint) }}
                    </div>
                  </div>
                </div>
              </li>
            </ol>
          </div>
        </div>
      </section>
    </div>

    <!-- AI 提示词 Modal ──────────────────────────────────────────────── -->
    <div v-if="showAIModal" class="modal-mask" @click.self="showAIModal = false">
      <div class="modal-body">
        <h2>{{ t('trace.aiPrompt.modalTitle') }}</h2>
        <div class="modal-form">
          <label>
            <span>{{ t('trace.aiPrompt.userQuestionLabel') }}</span>
            <textarea
              v-model="aiQuestion"
              :placeholder="t('trace.aiPrompt.userQuestionPlaceholder')"
              rows="3"
            />
          </label>
          <label>
            <span>{{ t('trace.aiPrompt.langLabel') }}</span>
            <select v-model="aiLang">
              <option value="zh">{{ t('trace.aiPrompt.langZh') }}</option>
              <option value="en">{{ t('trace.aiPrompt.langEn') }}</option>
            </select>
          </label>
          <div class="modal-actions">
            <button class="btn-secondary" @click="showAIModal = false">关闭</button>
            <button class="btn-primary" :disabled="aiGenerating" @click="generatePrompt">
              {{ aiGenerating ? t('trace.aiPrompt.generating') : t('trace.aiPrompt.generate') }}
            </button>
          </div>
        </div>
        <div v-if="aiError" class="error">{{ aiError }}</div>
        <div v-if="aiPrompt" class="ai-result">
          <div class="ai-result-header">
            <span>{{ t('trace.aiPrompt.resultLabel') }} ({{ t('trace.aiPrompt.tokenEstimate', { n: aiTokens }) }}, {{ aiEventCount }} events)</span>
            <button class="btn-secondary" @click="copyPrompt">
              {{ aiCopySuccess ? t('trace.aiPrompt.copySuccess') : t('trace.aiPrompt.copy') }}
            </button>
          </div>
          <pre>{{ aiPrompt }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.trace-page {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 64px);
  background: #f5f7fa;
}
.trace-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 24px;
  background: #fff;
  border-bottom: 1px solid #e8e8e8;
}
.trace-header h1 {
  margin: 0;
  font-size: 18px;
  color: rgba(0, 0, 0, 0.85);
}
.trace-subtitle {
  margin: 4px 0 0;
  color: rgba(0, 0, 0, 0.45);
  font-size: 13px;
}
.trace-header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}
.auto-refresh-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
}
.btn-primary,
.btn-secondary {
  padding: 6px 14px;
  border-radius: 4px;
  border: 1px solid transparent;
  cursor: pointer;
  font-size: 13px;
}
.btn-primary {
  background: #1890ff;
  color: #fff;
}
.btn-primary:disabled,
.btn-secondary:disabled {
  background: #f5f5f5;
  color: rgba(0, 0, 0, 0.25);
  cursor: not-allowed;
}
.btn-secondary {
  background: #fff;
  border-color: #d9d9d9;
  color: rgba(0, 0, 0, 0.85);
}
.link-btn {
  background: none;
  border: none;
  color: #1890ff;
  cursor: pointer;
  padding: 0;
}

.trace-body {
  display: flex;
  flex: 1;
  overflow: hidden;
}
.trace-list {
  width: 380px;
  background: #fff;
  border-right: 1px solid #e8e8e8;
  display: flex;
  flex-direction: column;
}
.trace-list-header {
  padding: 12px 16px;
  border-bottom: 1px solid #e8e8e8;
}
.trace-list-header h3 {
  margin: 0 0 8px;
  font-size: 14px;
}
.trace-list-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.refresh-hint {
  color: rgba(0, 0, 0, 0.45);
}
.trace-list-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  table-layout: fixed;
}
.trace-list-table th,
.trace-list-table td {
  padding: 8px 6px;
  text-align: left;
  border-bottom: 1px solid #f0f0f0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.trace-list-table tbody tr {
  cursor: pointer;
}
.trace-list-table tbody tr.active {
  background: #e6f7ff;
}
.trace-list-table tbody tr:hover {
  background: #fafafa;
}
.model-cell {
  font-family: monospace;
  font-size: 11px;
}

.trace-detail {
  flex: 1;
  overflow-y: auto;
  padding: 16px 24px;
}
.placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: rgba(0, 0, 0, 0.25);
  font-size: 14px;
}
.detail-card {
  background: #fff;
  border-radius: 4px;
  padding: 16px 20px;
  margin-bottom: 16px;
}
.detail-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.detail-card h3 {
  margin: 0 0 12px;
  font-size: 14px;
}
.trace-meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px 16px;
}
.trace-meta-grid label {
  font-size: 11px;
  color: rgba(0, 0, 0, 0.45);
  display: block;
}
.trace-meta-grid code {
  font-size: 12px;
}
.status-text {
  padding: 2px 8px;
  border-radius: 2px;
  font-size: 12px;
}
.status-failed {
  background: #fff1f0;
  color: #cf1322;
}
.status-success {
  background: #f6ffed;
  color: #389e0d;
}
.status-timeout {
  background: #fffbe6;
  color: #d48806;
}
.trace-source-pill {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 10px;
}
.src-redis {
  background: #e6f7ff;
  color: #1890ff;
}
.src-postgres {
  background: #f9f0ff;
  color: #722ed1;
}

.timeline {
  list-style: none;
  padding: 0;
  margin: 0;
}
.timeline-item {
  position: relative;
  padding: 10px 0;
  border-bottom: 1px dashed #f0f0f0;
}
.timeline-item:last-child {
  border-bottom: none;
}
.timeline-item-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.timeline-item-head .seq {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: #f0f0f0;
  font-size: 11px;
}
.timeline-item.status-failed .seq {
  background: #ffccc7;
}
.timeline-item.status-success .seq {
  background: #d9f7be;
}
.timeline-item.status-timeout .seq {
  background: #ffe58f;
}
.timeline-item-head .stage {
  font-weight: 500;
}
.timeline-item-head .module {
  font-size: 11px;
  color: rgba(0, 0, 0, 0.45);
  background: #fafafa;
  padding: 2px 6px;
  border-radius: 2px;
}
.timeline-item-head .duration {
  font-family: monospace;
  font-size: 11px;
  color: rgba(0, 0, 0, 0.45);
}
.toggle-btn {
  margin-left: auto;
  font-size: 11px;
  background: none;
  border: 1px solid #d9d9d9;
  padding: 2px 8px;
  border-radius: 2px;
  cursor: pointer;
}
.timeline-item-body {
  padding: 8px 0 4px 32px;
}
.kv-table {
  font-size: 11px;
  margin-top: 4px;
  border-collapse: collapse;
}
.kv-table td {
  padding: 2px 8px;
  border-bottom: 1px solid #f0f0f0;
}
.kv-table td.k {
  color: rgba(0, 0, 0, 0.45);
  font-weight: 500;
}
.kv-table td.v {
  font-family: monospace;
  word-break: break-word;
  max-width: 480px;
}
.snapshot-box {
  margin-top: 8px;
  padding: 8px 12px;
  border-left: 3px solid #faad14;
  background: #fffbe6;
  font-size: 12px;
}
.snapshot-box h4 {
  margin: 0 0 4px;
  font-size: 12px;
}
.snapshot-box h5 {
  margin: 6px 0 4px;
  font-size: 11px;
}
.failure-hint {
  margin-top: 4px;
  color: #874d00;
}

.badge {
  display: inline-block;
  padding: 1px 6px;
  font-size: 11px;
  border-radius: 2px;
}
.badge-failed {
  background: #fff1f0;
  color: #cf1322;
}

.loading,
.empty,
.error {
  padding: 16px;
  color: rgba(0, 0, 0, 0.45);
  font-size: 13px;
}
.error {
  color: #ff4d4f;
}
.muted {
  color: rgba(0, 0, 0, 0.45);
}
.small {
  font-size: 11px;
}
.error-text {
  padding: 4px 8px;
  background: #fff1f0;
  border-radius: 2px;
  margin-top: 4px;
  font-size: 12px;
}

/* AI Modal */
.modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal-body {
  background: #fff;
  border-radius: 4px;
  width: 720px;
  max-width: 90vw;
  max-height: 85vh;
  padding: 20px 24px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.modal-body h2 {
  margin: 0;
  font-size: 16px;
}
.modal-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.modal-form label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
}
.modal-form textarea,
.modal-form select {
  border: 1px solid #d9d9d9;
  border-radius: 2px;
  padding: 6px 8px;
  font-size: 13px;
  font-family: inherit;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.ai-result {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ai-result-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 12px;
  color: rgba(0, 0, 0, 0.65);
}
.ai-result pre {
  background: #f5f5f5;
  border-radius: 2px;
  padding: 12px;
  font-size: 11px;
  white-space: pre-wrap;
  max-height: 320px;
  overflow-y: auto;
}
</style>
