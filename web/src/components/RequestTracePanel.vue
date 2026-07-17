<script setup lang="ts">
// RequestTracePanel.vue — 2026-07-17
// 内联版请求链路追踪面板（替代此前的 RequestTraceModal 弹窗）。
//
// 设计目标:
//   - 不再弹出遮罩式 modal, 而是在「请求详情」与「Tabs/按钮」之间展开一层,
//     让操作员在同一屏内看到时间线 + 失败快照 + AI 提示词生成, 不丢失上下文。
//   - 全黑色调: 所有背景都是 var(--bg) / var(--card) / var(--bg-subtle),
//     严禁大块亮色 (rgba 红黄绿警告框);状态色只用于小尺寸文字/小图标/左侧细条。
//   - 兼容既有 API: 暴露 requestId, 内部自己 watch 并加载。
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getRequestTrace,
  buildAIPrompt,
  type RequestTrace,
  type TraceEvent,
} from '../api/trace'

const { t } = useI18n()

const props = defineProps<{
  requestId: string | null
}>()

const emit = defineEmits<{
  close: []
}>()

// ── 加载状态 ──────────────────────────────────────────────────────────────
const loading = ref(false)
const error = ref<string | null>(null)
const trace = ref<RequestTrace | null>(null)

// 展开的事件 seq
const expanded = ref<Set<number>>(new Set())

// ── AI 模态 (本次为内嵌展开, 不是 modal) ───────────────────────────────
const showAIPanel = ref(false)
const aiQuestion = ref('')
const aiGenerating = ref(false)
const aiPrompt = ref('')
const aiTokens = ref(0)
const aiEventCount = ref(0)
const aiError = ref<string | null>(null)
const aiCopySuccess = ref(false)

// 监控 open/close 重新加载
watch(
  () => props.requestId,
  async (id) => {
    error.value = null
    trace.value = null
    expanded.value = new Set()
    if (showAIPanel.value) closeAI()
    if (!id) return
    await loadTrace(id)
  },
  { immediate: true },
)

async function loadTrace(id: string) {
  loading.value = true
  error.value = null
  try {
    const data = await getRequestTrace(id)
    trace.value = data
    // 默认展开失败事件 + 第一个事件
    if (data.events.length) {
      const ex = new Set<number>()
      if (data.final_status === 'failed' && data.failed_at_stage) {
        const failed = data.events.find((e) => e.stage === data.failed_at_stage)
        if (failed) ex.add(failed.seq)
      }
      data.events.forEach((e) => {
        if (e.status === 'failed') ex.add(e.seq)
      })
      expanded.value = ex
    }
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : '加载失败'
    // 2026-07-17: 后端修复后, "no rows in result set" 不会再 500 上抛;
    // 此处仍兼容旧的 404 / not_found 错误信息 → 走空状态 UI。
    if (msg.includes('404') || msg.includes('not found') || msg.includes('no rows')) {
      error.value = 'not_found'
    } else {
      error.value = msg
    }
  } finally {
    loading.value = false
  }
}

function toggleDetails(seq: number) {
  if (expanded.value.has(seq)) {
    expanded.value.delete(seq)
  } else {
    expanded.value.add(seq)
  }
}

function isExpanded(seq: number) {
  return expanded.value.has(seq)
}

// ── 状态图标 — 使用暗色调, 仅在小尺寸元素上染色 ────────────────────
const statusColor: Record<string, string> = {
  success: 'var(--trace-status-ok, #3fb950)',
  failed: 'var(--trace-status-fail, #f85149)',
  timeout: 'var(--trace-status-warn, #d29922)',
  skipped: 'var(--trace-status-skip, #8b949e)',
}
function eventColor(ev: TraceEvent): string {
  return statusColor[ev.status] || statusColor.success
}
const iconFor = (s: string) =>
  s === 'failed' ? '✕' : s === 'timeout' ? '⌛' : s === 'skipped' ? '⏭' : '✓'

// ── Stage 显示名 ──────────────────────────────────────────────────────────
const STAGE_LABEL_KEYS: Record<string, string> = {
  receive_request: 'trace.stage.receive_request',
  authenticate: 'trace.stage.authenticate',
  rate_limit_check: 'trace.stage.rate_limit_check',
  body_parse: 'trace.stage.body_parse',
  session_lookup: 'trace.stage.session_lookup',
  route_resolve: 'trace.stage.route_resolve',
  route_credential: 'trace.stage.route_credential',
  upstream_request: 'trace.stage.upstream_request',
  stream_start: 'trace.stage.stream_start',
  stream_chunk: 'trace.stage.stream_chunk',
  stream_complete: 'trace.stage.stream_complete',
  request_complete: 'trace.stage.request_complete',
}
const MODULE_LABEL_KEYS: Record<string, string> = {
  middleware: 'trace.stage.module_middleware',
  auth: 'trace.stage.module_auth',
  ratelimit: 'trace.stage.module_ratelimit',
  handler: 'trace.stage.module_handler',
  executor: 'trace.stage.module_executor',
  upstream: 'trace.stage.module_upstream',
  session: 'trace.stage.module_session',
}
function stageName(stage: string) {
  const k = STAGE_LABEL_KEYS[stage]
  return k ? t(k) : stage
}
function moduleName(mod: string) {
  const k = MODULE_LABEL_KEYS[mod]
  return k ? t(k) : mod
}

// ── 工具函数 ──────────────────────────────────────────────────────────────
function fmtTime(iso: string) {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    const p = (n: number) => n.toString().padStart(2, '0')
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  } catch {
    return iso
  }
}
function valueToText(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v)
  } catch {
    return String(v)
  }
}
function truncStr(v: unknown, n = 80): string {
  const s = valueToText(v)
  if (s.length <= n) return s
  return s.slice(0, n) + '…'
}

// ── 派生状态 ──────────────────────────────────────────────────────────────
const failedCount = computed(() =>
  trace.value ? trace.value.events.filter((e) => e.status === 'failed').length : 0,
)
const finalStatusText = computed(() => {
  const fs = trace.value?.final_status
  if (!fs) return '—'
  if (fs === 'success') return t('trace.detail.statusSuccess')
  if (fs === 'failed') return t('trace.detail.statusFailed')
  if (fs === 'timeout') return t('trace.detail.statusTimeout')
  return t('trace.detail.statusInProgress')
})
const finalStatusColor = computed(() => {
  const fs = trace.value?.final_status
  if (fs === 'success') return 'var(--success, #3fb950)'
  if (fs === 'failed') return 'var(--danger, #f85149)'
  if (fs === 'timeout') return 'var(--warning, #d29922)'
  return 'var(--text-muted, #8b949e)'
})
const sourceLabel = computed(() => {
  const src = trace.value?.source
  if (src === 'redis') return t('trace.detail.sourceRedis')
  if (src === 'postgres') return t('trace.detail.sourcePostgres')
  return ''
})

// ── AI 提示词 ─────────────────────────────────────────────────────────────
function openAI() {
  showAIPanel.value = true
  aiPrompt.value = ''
  aiError.value = null
}
function closeAI() {
  showAIPanel.value = false
}
async function generatePrompt() {
  if (!props.requestId) return
  aiGenerating.value = true
  aiError.value = null
  try {
    const resp = await buildAIPrompt(props.requestId, aiQuestion.value || undefined)
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
async function copyRawJson() {
  if (!trace.value) return
  try {
    await navigator.clipboard.writeText(JSON.stringify(trace.value, null, 2))
  } catch {
    /* noop */
  }
}
</script>

<template>
  <section v-if="requestId" class="trace-panel" role="region" aria-label="请求链路追踪">
    <!-- 内嵌面板 header — 与抽屉内其他 section 视觉一致, 不抢戏 -->
    <header class="trace-panel-header">
      <div class="trace-panel-title">
        <span class="icon" aria-hidden="true">🔗</span>
        <span class="title">{{ t('trace.modal.title') }}</span>
        <code v-if="trace" class="rid">{{ trace.request_id }}</code>
      </div>
      <div class="trace-panel-actions">
        <button
          v-if="trace"
          class="btn btn-secondary btn-sm"
          type="button"
          @click="openAI"
        >
          {{ t('trace.aiPrompt.button') }}
        </button>
        <button
          v-if="trace"
          class="btn btn-secondary btn-sm"
          type="button"
          @click="copyRawJson"
        >
          {{ t('trace.detail.copyJson') }}
        </button>
        <button class="btn btn-ghost btn-sm" type="button" @click="emit('close')">
          {{ t('trace.modal.close') }}
        </button>
      </div>
    </header>

    <!-- 主体 -->
    <div class="trace-panel-body">
      <!-- 加载 -->
      <div v-if="loading" class="trace-loading">
        <div class="spinner" />
        <span>{{ t('requests.common.loading') }}</span>
      </div>

      <!-- 空数据: 区分 not_found 与其它 -->
      <div v-else-if="error === 'not_found'" class="trace-empty">
        <div class="empty-icon" aria-hidden="true">📭</div>
        <h4>{{ t('trace.empty.title') }}</h4>
        <p>{{ t('trace.empty.desc') }}</p>
        <ul class="empty-hints">
          <li>{{ t('trace.empty.hint1') }}</li>
          <li>{{ t('trace.empty.hint2') }}</li>
          <li>{{ t('trace.empty.hint3') }}</li>
        </ul>
      </div>
      <div v-else-if="error" class="trace-error">
        <span aria-hidden="true">⚠</span>
        <span>{{ error }}</span>
      </div>

      <!-- 成功加载 trace -->
      <template v-else-if="trace">
        <!-- 摘要条 — 纯文字 + 小 chip, 无亮色背景块 -->
        <div class="trace-summary">
          <div class="summary-item">
            <span class="label">{{ t('trace.detail.finalStatus') }}</span>
            <strong :style="{ color: finalStatusColor }">{{ finalStatusText }}</strong>
          </div>
          <div v-if="trace.failed_at_stage" class="summary-item">
            <span class="label">{{ t('trace.detail.failedAtStage') }}</span>
            <strong>{{ stageName(trace.failed_at_stage) }}</strong>
          </div>
          <div class="summary-item">
            <span class="label">{{ t('trace.detail.eventCount') }}</span>
            <strong>{{ trace.events.length }}</strong>
          </div>
          <div v-if="failedCount > 0" class="summary-item">
            <span class="label">{{ t('trace.modal.failedCount') }}</span>
            <strong class="danger">{{ failedCount }}</strong>
          </div>
          <div v-if="trace.total_duration_ms > 0" class="summary-item">
            <span class="label">{{ t('trace.detail.totalDuration') }}</span>
            <strong>{{ trace.total_duration_ms }} ms</strong>
          </div>
          <div v-if="sourceLabel" class="summary-item">
            <span class="label">{{ t('trace.detail.source') }}</span>
            <span class="source-pill">{{ sourceLabel }}</span>
          </div>
        </div>

        <!-- 时间轴 -->
        <ol v-if="trace.events.length" class="timeline">
          <li
            v-for="ev in trace.events"
            :key="ev.seq"
            class="timeline-item"
            :class="{ failed: ev.status === 'failed', expanded: isExpanded(ev.seq) }"
            :style="{ '--accent': eventColor(ev) }"
          >
            <button
              type="button"
              class="timeline-row"
              :aria-expanded="isExpanded(ev.seq)"
              @click="toggleDetails(ev.seq)"
            >
              <span class="seq" :style="{ background: eventColor(ev) }">{{ ev.seq }}</span>
              <span class="icon" :style="{ color: eventColor(ev) }" aria-hidden="true">{{ iconFor(ev.status) }}</span>
              <span class="stage">{{ stageName(ev.stage) }}</span>
              <span class="module">{{ moduleName(ev.module) }}</span>
              <span v-if="ev.duration_ms > 0" class="duration">{{ ev.duration_ms }} ms</span>
              <span class="caret" :class="{ open: isExpanded(ev.seq) }" aria-hidden="true">▾</span>
            </button>

            <div v-if="isExpanded(ev.seq)" class="timeline-body">
              <div class="muted small">{{ fmtTime(ev.timestamp) }}</div>
              <div v-if="ev.error" class="error-line">
                <span aria-hidden="true">⚠</span>
                <span>{{ ev.error }}</span>
              </div>

              <table v-if="ev.details && Object.keys(ev.details).length" class="kv-table">
                <tbody>
                  <tr v-for="(v, k) in ev.details" :key="k">
                    <td class="k">{{ k }}</td>
                    <td class="v">{{ truncStr(v, 240) }}</td>
                  </tr>
                </tbody>
              </table>

              <div v-if="ev.snapshot" class="snapshot">
                <div class="snapshot-title">📦 {{ t('trace.modal.snapshotTitle') }}</div>
                <div v-if="ev.snapshot.candidates && (ev.snapshot.candidates as unknown[]).length" class="snapshot-row">
                  <span class="label">{{ t('trace.modal.candidates') }}</span>
                  <span>{{ (ev.snapshot.candidates as unknown[]).length }} {{ t('trace.modal.entries') }}</span>
                </div>
                <div v-if="ev.snapshot.routing_state" class="snapshot-row">
                  <span class="label">{{ t('trace.modal.routingState') }}</span>
                  <span>{{ ev.snapshot.routing_state }}</span>
                </div>
                <div v-if="ev.snapshot.credential_mode" class="snapshot-row">
                  <span class="label">{{ t('trace.modal.credentialMode') }}</span>
                  <span>{{ ev.snapshot.credential_mode }}</span>
                </div>
                <div v-if="ev.snapshot.concurrency_slot" class="snapshot-row">
                  <span class="label">{{ t('trace.modal.concurrency') }}</span>
                  <span>
                    {{ ev.snapshot.concurrency_slot.in_use }} / {{ ev.snapshot.concurrency_slot.max_slots }}
                    <span v-if="ev.snapshot.concurrency_slot.blocked" class="danger-pill">
                      {{ t('trace.modal.blocked') }}: {{ ev.snapshot.concurrency_slot.reason || '—' }}
                    </span>
                  </span>
                </div>
                <table
                  v-if="ev.snapshot.node_probe_state && Object.keys(ev.snapshot.node_probe_state).length"
                  class="kv-table"
                >
                  <tbody>
                    <tr v-for="(v, k) in ev.snapshot.node_probe_state" :key="k">
                      <td class="k">{{ k }}</td>
                      <td class="v">{{ truncStr(v, 120) }}</td>
                    </tr>
                  </tbody>
                </table>
                <div v-if="ev.snapshot.failure_hint" class="failure-hint">
                  💡 {{ ev.snapshot.failure_hint }}
                </div>
              </div>
            </div>
          </li>
        </ol>
      </template>
    </div>

    <!-- AI 提示词内嵌展开面板 (在主面板下方继续展开) -->
    <div v-if="showAIPanel" class="ai-panel">
      <header class="ai-panel-header">
        <h4>{{ t('trace.aiPrompt.modalTitle') }}</h4>
        <button class="btn btn-ghost btn-sm" type="button" @click="closeAI">×</button>
      </header>
      <div class="ai-panel-body">
        <label class="ai-label">
          <span>{{ t('trace.aiPrompt.userQuestionLabel') }}</span>
          <textarea
            v-model="aiQuestion"
            :placeholder="t('trace.aiPrompt.userQuestionPlaceholder')"
            rows="2"
          />
        </label>
        <div class="ai-actions">
          <button
            class="btn btn-primary btn-sm"
            type="button"
            :disabled="aiGenerating"
            @click="generatePrompt"
          >
            {{ aiGenerating ? t('trace.aiPrompt.generating') : t('trace.aiPrompt.generate') }}
          </button>
        </div>
        <div v-if="aiError" class="trace-error">
          <span aria-hidden="true">⚠</span>
          <span>{{ aiError }}</span>
        </div>
        <div v-if="aiPrompt" class="ai-result">
          <div class="ai-result-header">
            <span>{{ t('trace.aiPrompt.resultLabel') }} · {{ t('trace.aiPrompt.tokenEstimate', { n: aiTokens }) }} · {{ aiEventCount }} {{ t('trace.modal.events') }}</span>
            <button class="btn btn-sm btn-primary" type="button" @click="copyPrompt">
              {{ aiCopySuccess ? t('trace.aiPrompt.copySuccess') : t('trace.aiPrompt.copy') }}
            </button>
          </div>
          <pre>{{ aiPrompt }}</pre>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
/*
 * 2026-07-17: 暗色内嵌面板 (替代此前的 modal)。
 * 关键约束:
 *   - 所有背景使用 var(--bg) / var(--card) / var(--bg-subtle), 严禁 #fff / rgba 红黄绿大块;
 *   - 状态色仅用于小尺寸元素: 序号圆点 / 文字 / 左侧细条;
 *   - 与所在 drawer / 详情面板 视觉一体, 不出现独立"亮色卡片"。
 */

.trace-panel {
  border: 1px solid var(--border, #30363d);
  border-radius: var(--radius, 8px);
  background: var(--bg-subtle, #161b22);
  /* 内嵌在 drawer 中, 不使用 fixed 定位, 不使用遮罩 */
  margin: 8px 0;
  overflow: hidden;
}

.trace-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border, #30363d);
  background: var(--card, #1c2128);
}

.trace-panel-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--text, #e6edf3);
}
.trace-panel-title .icon {
  font-size: 14px;
}
.trace-panel-title .title {
  white-space: nowrap;
}
.trace-panel-title .rid {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 11px;
  padding: 1px 6px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 3px;
  color: var(--muted, #8b949e);
  font-weight: 400;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trace-panel-actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.trace-panel-body {
  padding: 10px 12px;
  max-height: 360px;
  overflow-y: auto;
}

.trace-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 120px;
  color: var(--muted, #8b949e);
  font-size: 12px;
}
.spinner {
  width: 18px;
  height: 18px;
  border: 2px solid var(--border, #30363d);
  border-top-color: var(--accent, #6366f1);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 空状态 / 错误 — 暗色背景, 仅文字色不同 */
.trace-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 18px 12px;
  color: var(--muted, #8b949e);
  font-size: 12px;
}
.trace-empty .empty-icon {
  font-size: 28px;
  margin-bottom: 6px;
  opacity: 0.55;
}
.trace-empty h4 {
  margin: 0 0 4px;
  font-size: 13px;
  color: var(--text, #e6edf3);
  font-weight: 600;
}
.trace-empty p {
  margin: 0 0 8px;
}
.empty-hints {
  list-style: none;
  padding: 6px 10px;
  margin: 0;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-left: 2px solid var(--warning, #d29922);
  border-radius: 4px;
  font-size: 11px;
  max-width: 100%;
  text-align: left;
  color: var(--muted, #8b949e);
}
.empty-hints li {
  padding: 1px 0;
}
.empty-hints li::before {
  content: '·';
  margin-right: 6px;
  color: var(--warning, #d29922);
}

/* 错误条 — 暗色背景, 仅左边 2px 细条 + 文字色变化, 严禁亮红填充 */
.trace-error {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  padding: 8px 10px;
  margin: 4px 0;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-left: 2px solid var(--danger, #f85149);
  border-radius: 4px;
  font-size: 12px;
  color: var(--text, #e6edf3);
}

/* 摘要条 — 暗色, 纯文字 + 小 chip, 无亮色背景 */
.trace-summary {
  display: flex;
  gap: 18px;
  flex-wrap: wrap;
  padding: 6px 8px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  margin-bottom: 10px;
  font-size: 12px;
}
.summary-item {
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.summary-item .label {
  font-size: 10px;
  color: var(--muted, #8b949e);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.summary-item strong {
  font-size: 12px;
  font-weight: 600;
  color: var(--text, #e6edf3);
}
.summary-item strong.danger {
  color: var(--danger, #f85149);
}
.source-pill {
  display: inline-block;
  padding: 1px 6px;
  font-size: 10px;
  border-radius: 8px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  color: var(--muted, #8b949e);
}

/* 时间轴 */
.timeline {
  list-style: none;
  padding: 0;
  margin: 0;
  border-left: 1px solid var(--border, #30363d);
  margin-left: 10px;
}
.timeline-item {
  position: relative;
  padding: 2px 0 2px 18px;
}
.timeline-item::before {
  content: '';
  position: absolute;
  left: -5px;
  top: 0;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--bg-subtle, #161b22);
  border: 2px solid var(--accent, var(--muted, #8b949e));
}
.timeline-item.failed::before {
  border-color: var(--accent, var(--danger, #f85149));
}
.timeline-row {
  display: flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  border: 1px solid transparent;
  padding: 4px 6px;
  border-radius: 4px;
  cursor: pointer;
  width: 100%;
  text-align: left;
  font: inherit;
  color: inherit;
}
.timeline-row:hover {
  background: var(--bg, #0f1117);
}
.timeline-row:focus-visible {
  outline: 1px solid var(--accent, #6366f1);
  outline-offset: -1px;
}
.timeline-row .seq {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  color: var(--bg, #0f1117);
  font-size: 10px;
  font-weight: 600;
  flex-shrink: 0;
}
.timeline-row .icon {
  width: 14px;
  text-align: center;
  font-size: 11px;
  flex-shrink: 0;
}
.timeline-row .stage {
  font-weight: 500;
  font-size: 12px;
  color: var(--text, #e6edf3);
}
.timeline-row .module {
  font-size: 10px;
  padding: 0 5px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 3px;
  color: var(--muted, #8b949e);
}
.timeline-row .duration {
  font-family: ui-monospace, monospace;
  font-size: 10px;
  color: var(--muted, #8b949e);
  margin-left: auto;
}
.timeline-row .caret {
  margin-left: 4px;
  font-size: 10px;
  color: var(--muted, #8b949e);
  transition: transform 0.15s;
}
.timeline-row .caret.open {
  transform: rotate(180deg);
}

.timeline-body {
  padding: 4px 6px 8px 8px;
  font-size: 11px;
}
.muted { color: var(--muted, #8b949e); }
.small { font-size: 10px; margin-bottom: 4px; }

/* 错误行 — 暗色背景, 仅左侧 2px 细条, 严禁亮红填充 */
.error-line {
  display: flex;
  gap: 6px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-left: 2px solid var(--danger, #f85149);
  color: var(--text, #e6edf3);
  padding: 4px 8px;
  border-radius: 3px;
  margin-bottom: 6px;
  font-size: 11px;
}

.kv-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
  margin-top: 4px;
}
.kv-table td {
  padding: 3px 6px;
  border-bottom: 1px dashed var(--border, #30363d);
  vertical-align: top;
}
.kv-table td.k {
  color: var(--muted, #8b949e);
  width: 32%;
  font-weight: 500;
  white-space: nowrap;
}
.kv-table td.v {
  font-family: ui-monospace, SFMono-Regular, monospace;
  word-break: break-word;
  max-width: 0;
  overflow-wrap: anywhere;
  color: var(--text, #e6edf3);
}

/* snapshot — 暗色背景, 仅左侧 2px 细条, 严禁亮黄填充 */
.snapshot {
  margin-top: 6px;
  padding: 6px 10px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-left: 2px solid var(--warning, #d29922);
  border-radius: 3px;
  font-size: 11px;
}
.snapshot-title {
  font-weight: 600;
  margin-bottom: 4px;
  color: var(--text, #e6edf3);
}
.snapshot-row {
  display: flex;
  gap: 6px;
  padding: 1px 0;
}
.snapshot-row .label {
  flex-shrink: 0;
  color: var(--muted, #8b949e);
  min-width: 90px;
}
.danger-pill {
  display: inline-block;
  padding: 0 5px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--danger, #f85149);
  color: var(--danger, #f85149);
  border-radius: 3px;
  font-size: 9px;
  margin-left: 4px;
}
.failure-hint {
  margin-top: 4px;
  font-size: 11px;
  color: var(--muted, #8b949e);
}

/* AI 面板 — 暗色, 与主面板一体 */
.ai-panel {
  border-top: 1px solid var(--border, #30363d);
  background: var(--card, #1c2128);
}
.ai-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border, #30363d);
}
.ai-panel-header h4 {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--text, #e6edf3);
}
.ai-panel-body {
  padding: 8px 12px;
}
.ai-label {
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: 11px;
  color: var(--muted, #8b949e);
  margin-bottom: 8px;
}
.ai-label textarea {
  border: 1px solid var(--border, #30363d);
  border-radius: 3px;
  padding: 5px 7px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-family: inherit;
  font-size: 12px;
  resize: vertical;
}
.ai-actions {
  display: flex;
  gap: 6px;
  margin-bottom: 8px;
}
.ai-result {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 6px;
}
.ai-result-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 11px;
  color: var(--muted, #8b949e);
}
.ai-result pre {
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 3px;
  padding: 8px 10px;
  font-size: 11px;
  font-family: ui-monospace, monospace;
  white-space: pre-wrap;
  max-height: 200px;
  overflow-y: auto;
  margin: 0;
  color: var(--text, #e6edf3);
}
</style>