<script setup lang="ts">
// RequestLogDrawer.vue — 独立请求详情抽屉组件。
//
// 2026-07-02: 补齐附件展示（与 RequestLogsView.vue 同步实现，对齐参考文档
// REQUEST_LOGS_ATTACHMENT_SYNC.md）：
//   - 「📎 附件 (N)」Tab 按钮，仅在 attachments.length > 0 时显示
//   - 附件网格：图片缩略图 / 文件图标 + 类型/大小/路径/SHA256(截断) + 下载/放大按钮
//   - 大图预览 lightbox：Teleport 到 body，全局 ESC 关闭（点击遮罩/图片/按钮亦可关闭）
//
// 该组件被 DashboardViewV2 / TenantDashboardView / DashboardViewLegacy 等调用，
// 作为请求详情抽屉入口（会话上下文跳转已随 SessionContextDetailView 迁移至 plugin）。

import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { localeRef } from '../i18n'
import { useI18n } from 'vue-i18n'
import {
  getRequestLogDetail,
  attachmentURL,
  type RequestLogDetail,
  type AttachmentInfo,
} from '../api'
import {
  getSessionTags,
  addSessionTag,
  updateSessionTag,
  deleteSessionTag,
  type SessionTag,
} from '../api/sessionAnalytics'
import { isDefaultTenant } from '../store'
import RequestTracePanel from './RequestTracePanel.vue'
import RoutingAttemptsTimeline from './RoutingAttemptsTimeline.vue'
import ModelIdentityChip from './model/ModelIdentityChip.vue'
import SessionMetaTitleRow from './SessionMetaTitleRow.vue'
import SessionSummaryDrawer from './SessionSummaryDrawer.vue'
import { useRouter } from 'vue-router'

const props = withDefaults(defineProps<{
  requestId: string | null
  mode?: 'default' | 'request-logs'
  initialTraceOpen?: boolean
  /** Nested under NodeDetailDrawer (z-index 3001); raises backdrop above it. */
  stackLevel?: 'default' | 'nested'
}>(), {
  mode: 'default',
  initialTraceOpen: false,
  stackLevel: 'default',
})

const emit = defineEmits<{
  close: []
  /** @deprecated 使用 filterSession */
  generateSessionSummary: [sessionId: string]
  filterSession: [sessionId: string]
  openRequest: [requestId: string]
  sessionTitleChanged: [{ taskId: string; sessionId: string | null; title: string | null }]
}>()

const router = useRouter()

const loading = ref(false)
const bodyLoading = ref(false)
const detail = ref<RequestLogDetail | null>(null)
const error = ref('')
const tab = ref<'request' | 'outbound' | 'response' | 'attachments' | 'routing'>('request')
const showTrace = ref(false)

const sessionTags = ref<SessionTag[]>([])
const sessionTagsLoading = ref(false)
const sessionTagsError = ref<string | null>(null)
const addingTag = ref(false)
const newTagKey = ref('')
const newTagValue = ref('')
const newTagSaving = ref(false)
const editingTagId = ref<number | null>(null)
const editTagDraftKey = ref('')
const editTagDraftValue = ref('')
const editTagSaving = ref(false)
let detailLoadSeq = 0
let sessionTagsLoadSeq = 0

// 2026-07-02: 附件 lightbox 状态。Teleport 到 body 后由 handleKeydown 全局监听 ESC。
const attachmentsLightbox = ref(false)
const attachmentsLightboxSrc = ref('')

const summaryDrawerOpen = ref(false)
const summaryDrawerSessionId = ref<string | null>(null)

// 2026-07-02: i18n 接入；附件文案走 t() 键（键定义见 web/src/locales/*.ts）。
const { t } = useI18n()

watch(
  () => props.requestId,
  async (id) => {
    const loadSeq = ++detailLoadSeq
    detail.value = null
    error.value = ''
    loading.value = false
    bodyLoading.value = false
    tab.value = 'request'
    showTrace.value = props.initialTraceOpen
    closeLightbox()
    resetSessionMetaState()
    if (!id) return
    loading.value = true
    try {
      // 2026-07-04 / 2026-07-20: live-stream 落库延迟 → 404 指数退避重试。
      // 2026-08-21: 分阶段加载 — 先 omit_body 出 meta，再全量补 body。
      const retryMs = [200, 500, 1500]
      let metaDetail: RequestLogDetail | null = null
      for (let i = 0; i <= retryMs.length; i++) {
        try {
          metaDetail = await getRequestLogDetail(id, { omitBody: true })
          break
        } catch (err: unknown) {
          const isNotFound = err instanceof Error &&
            (err.message.includes('not found') ||
             err.message.includes('404'))
          if (!isNotFound || i === retryMs.length) {
            throw err
          }
          await new Promise(r => setTimeout(r, retryMs[i]))
        }
      }
      if (loadSeq !== detailLoadSeq || props.requestId !== id) return
      detail.value = metaDetail
      loading.value = false
      if (props.mode === 'request-logs' && metaDetail?.gw_session_id) {
        void loadSessionTags(metaDetail.gw_session_id)
      }
      bodyLoading.value = true
      try {
        const fullDetail = await getRequestLogDetail(id)
        if (loadSeq !== detailLoadSeq || props.requestId !== id) return
        detail.value = {
          ...metaDetail!,
          ...fullDetail,
          request_body: fullDetail.request_body ?? metaDetail?.request_body ?? null,
          response_body: fullDetail.response_body ?? metaDetail?.response_body ?? null,
          outbound_body: fullDetail.outbound_body ?? metaDetail?.outbound_body,
          attachments: fullDetail.attachments ?? metaDetail?.attachments,
          routing_attempts: fullDetail.routing_attempts ?? metaDetail?.routing_attempts,
          routing_summary: fullDetail.routing_summary ?? metaDetail?.routing_summary,
        }
      } catch (bodyErr: unknown) {
        if (loadSeq === detailLoadSeq && props.requestId === id) {
          // meta 已可用；body 失败不盖掉整页，仅提示。
          error.value = bodyErr instanceof Error
            ? `元数据已加载，正文加载失败：${bodyErr.message}`
            : '元数据已加载，正文加载失败'
        }
      } finally {
        if (loadSeq === detailLoadSeq && props.requestId === id) {
          bodyLoading.value = false
        }
      }
    } catch (e: unknown) {
      if (loadSeq === detailLoadSeq && props.requestId === id) {
        error.value = e instanceof Error ? e.message : '加载失败'
        loading.value = false
      }
    }
  },
  { immediate: true },
)

function resetSessionMetaState() {
  sessionTagsLoadSeq++
  sessionTags.value = []
  sessionTagsLoading.value = false
  sessionTagsError.value = null
  addingTag.value = false
  newTagKey.value = ''
  newTagValue.value = ''
  newTagSaving.value = false
  editingTagId.value = null
  editTagDraftKey.value = ''
  editTagDraftValue.value = ''
  editTagSaving.value = false
}

async function loadSessionTags(sessionId: string) {
  const loadSeq = ++sessionTagsLoadSeq
  sessionTagsLoading.value = true
  sessionTagsError.value = null
  try {
    const response = await getSessionTags(sessionId)
    if (loadSeq === sessionTagsLoadSeq && detail.value?.gw_session_id === sessionId) {
      sessionTags.value = response.tags ?? []
    }
  } catch (e: unknown) {
    if (loadSeq === sessionTagsLoadSeq) {
      sessionTagsError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (loadSeq === sessionTagsLoadSeq) sessionTagsLoading.value = false
  }
}

function isCurrentDetail(requestId: string, loadSeq: number): boolean {
  return detailLoadSeq === loadSeq && detail.value?.request_id === requestId
}

function onSessionTitleChanged(title: string | null) {
  if (!detail.value?.gw_task_id) return
  detail.value.session_title = title
  emit('sessionTitleChanged', {
    taskId: detail.value.gw_task_id,
    sessionId: detail.value.gw_session_id,
    title,
  })
}

function openSessionSummaryDrawer(sessionId: string) {
  summaryDrawerSessionId.value = sessionId
  summaryDrawerOpen.value = true
}

function closeSessionSummaryDrawer() {
  summaryDrawerOpen.value = false
}

function onSummaryFilterSession(sessionId: string) {
  emit('filterSession', sessionId)
  closeSessionSummaryDrawer()
}

function onSummaryOpenRequest(requestId: string) {
  closeSessionSummaryDrawer()
  emit('openRequest', requestId)
}

function startAddTag() {
  addingTag.value = true
  newTagKey.value = ''
  newTagValue.value = ''
}

function cancelAddTag() {
  addingTag.value = false
  newTagKey.value = ''
  newTagValue.value = ''
}

async function submitAddTag() {
  if (!detail.value?.gw_session_id) return
  const requestId = detail.value.request_id
  const loadSeq = detailLoadSeq
  const sessionId = detail.value.gw_session_id
  const key = newTagKey.value.trim()
  const value = newTagValue.value.trim()
  if (!key || !value) return
  newTagSaving.value = true
  try {
    await addSessionTag(sessionId, key, value)
    if (!isCurrentDetail(requestId, loadSeq)) return
    cancelAddTag()
    await loadSessionTags(sessionId)
  } catch (e: unknown) {
    if (isCurrentDetail(requestId, loadSeq)) {
      sessionTagsError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (isCurrentDetail(requestId, loadSeq)) newTagSaving.value = false
  }
}

function startEditTag(tag: SessionTag) {
  editingTagId.value = tag.id
  editTagDraftKey.value = tag.tag_key
  editTagDraftValue.value = tag.tag_value
}

function cancelEditTag() {
  editingTagId.value = null
  editTagDraftKey.value = ''
  editTagDraftValue.value = ''
}

async function submitEditTag() {
  if (!detail.value?.gw_session_id || editingTagId.value === null) return
  const requestId = detail.value.request_id
  const loadSeq = detailLoadSeq
  const sessionId = detail.value.gw_session_id
  const tagId = editingTagId.value
  const key = editTagDraftKey.value.trim()
  const value = editTagDraftValue.value.trim()
  if (!key || !value) return
  editTagSaving.value = true
  try {
    await updateSessionTag(sessionId, tagId, { tag_key: key, tag_value: value })
    if (!isCurrentDetail(requestId, loadSeq)) return
    cancelEditTag()
    await loadSessionTags(sessionId)
  } catch (e: unknown) {
    if (isCurrentDetail(requestId, loadSeq)) {
      sessionTagsError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (isCurrentDetail(requestId, loadSeq)) editTagSaving.value = false
  }
}

async function removeTag(tag: SessionTag) {
  if (!detail.value?.gw_session_id || !confirm(`删除标签 ${tag.tag_key}: ${tag.tag_value}？`)) return
  const requestId = detail.value.request_id
  const loadSeq = detailLoadSeq
  const sessionId = detail.value.gw_session_id
  try {
    await deleteSessionTag(sessionId, tag.id)
    if (isCurrentDetail(requestId, loadSeq)) await loadSessionTags(sessionId)
  } catch (e: unknown) {
    if (isCurrentDetail(requestId, loadSeq)) {
      sessionTagsError.value = e instanceof Error ? e.message : String(e)
    }
  }
}

// ── 附件辅助 (与 RequestLogsView.vue 保持一致；不抽 composable 以避免引入新依赖) ──

// detailAttachments 派生当前详情行的附件数组，空安全。
// 后端 ListByRequest 在 detail 接口中已返回完整 attachments JSONB 数组；
// 即便后端将来返回 null/undefined，此处的 ?? [] 也保证前端不抛。
function detailAttachments(): AttachmentInfo[] {
  return (detail.value?.attachments as AttachmentInfo[] | null | undefined) ?? []
}

// isImageAttachment 判断附件是否为图片（用于缩略图 vs 文件图标）。
function isImageAttachment(a: AttachmentInfo): boolean {
  return a.type === 'image' || a.content_type.startsWith('image/')
}

// formatBytes 把字节数格式化为人类可读（KB/MB）。
function formatBytes(n: number | undefined): string {
  if (!n || n <= 0) return '—'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / (1024 * 1024)).toFixed(2) + ' MB'
}

// fileExt 从附件 path/content_type 推断显示用的扩展名。
function fileExt(a: AttachmentInfo): string {
  if (a.path) {
    const m = a.path.match(/\.([a-z0-9]+)$/i)
    if (m) return m[1].toUpperCase()
  }
  if (a.content_type && a.content_type.includes('/')) {
    return a.content_type.split('/')[1].toUpperCase()
  }
  return 'FILE'
}

// openLightbox 点击图片缩略图放大查看。
function openLightbox(a: AttachmentInfo) {
  if (!isImageAttachment(a)) return
  attachmentsLightboxSrc.value = attachmentURL(a.path)
  attachmentsLightbox.value = true
}

// closeLightbox 主动关闭 lightbox（被 ESC handler、点击遮罩/关闭按钮调用）。
function closeLightbox() {
  attachmentsLightbox.value = false
  attachmentsLightboxSrc.value = ''
}

// downloadAttachment 触发浏览器下载（Content-Disposition 由后端控制）。
// 图片走 attachment; 非图片走 attachment; 浏览器均按 download 提示处理。
function downloadAttachment(a: AttachmentInfo) {
  const url = attachmentURL(a.path)
  const link = document.createElement('a')
  link.href = url
  link.download = a.path ? a.path.split('/').pop() || 'attachment' : 'attachment'
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
}

// 2026-07-02: 全局 ESC 关闭 lightbox（参考文档 §5.2）。
// 抽屉打开后焦点可能不在 lightbox 内，故绑到 window 而非抽屉节点。
// 监听注册在 onMounted，清理在 onBeforeUnmount。
function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && attachmentsLightbox.value) {
    e.stopPropagation()
    closeLightbox()
  }
}

onMounted(() => {
  window.addEventListener('keydown', handleKeydown)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleKeydown)
})

function fmtTs(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString(localeRef.value)
}

function hasOutboundBody(row: RequestLogDetail | null): boolean {
  if (!row?.outbound_body) return false
  return row.outbound_msg_count == null || row.outbound_msg_count > 0
}

function outboundEqualsRequest(row: RequestLogDetail): boolean {
  return JSON.stringify(row.request_body ?? '') === JSON.stringify(row.outbound_body ?? '')
}

function outboundMsgDelta(row: RequestLogDetail): string {
  const outbound = row.outbound_msg_count ?? extractMessagesFromBody(row.outbound_body).length
  const delta = outbound - extractMessagesFromBody(row.request_body).length
  return delta > 0 ? `+${delta}` : String(delta)
}

function outboundSummaryMarker(row: RequestLogDetail): string {
  const meta = row.compression_meta
  return meta && typeof meta === 'object' && typeof meta.summary_marker === 'string'
    ? meta.summary_marker
    : ''
}

function isSummaryMarkerMessage(message: Record<string, unknown>): boolean {
  const content = message.content
  if (typeof content === 'string') return content.startsWith('[smm_v1:')
  return Array.isArray(content) && content.some(
    (part) => typeof part?.text === 'string' && part.text.startsWith('[smm_v1:'),
  )
}

function bodyBytes(value: unknown): number {
  if (!value) return 0
  return new Blob([typeof value === 'string' ? value : JSON.stringify(value)]).size
}

function compressionSavings(row: RequestLogDetail) {
  const requestBytes = bodyBytes(row.request_body)
  const outboundBytes = bodyBytes(row.outbound_body)
  const savedBytes = requestBytes - outboundBytes
  const requestMessages = extractMessagesFromBody(row.request_body).length
  const outboundMessages = row.outbound_msg_count ?? extractMessagesFromBody(row.outbound_body).length
  return {
    savedBytes: savedBytes > 0 ? `-${savedBytes > 1024 ? `${(savedBytes / 1024).toFixed(1)}KB` : `${savedBytes}B`}` : '≈0',
    messageDelta: outboundMessages - requestMessages,
  }
}

function formatJson(obj: unknown): string {
  if (obj == null) return '(无数据)'
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
}

function summaryEnvelopeMessage(summary: Record<string, unknown> | undefined): Record<string, unknown>[] | null {
  if (!summary || typeof summary !== 'object') return null
  const bytes = typeof summary.bytes === 'number' ? summary.bytes : Number(summary.bytes || 0)
  const truncated = summary.head_truncated === true
  return [{
    role: 'gateway',
    content: truncated
      ? `[已摘要化: 原始 ${bytes} bytes, head 已截断]`
      : `[已摘要化: 原始 ${bytes} bytes]`,
  }]
}

function extractMessagesFromBody(body: unknown): Record<string, unknown>[] {
  if (body == null) return []
  let parsed: unknown = body
  if (typeof parsed === 'string') {
    try { parsed = JSON.parse(parsed) } catch { return [] }
  }
  if (Array.isArray(parsed)) return parsed as Record<string, unknown>[]
  if (typeof parsed === 'object' && parsed !== null) {
    const o = parsed as Record<string, unknown>
    const summaryMessage = summaryEnvelopeMessage(o._gw_body_summary as Record<string, unknown> | undefined)
    if (summaryMessage) return summaryMessage
    if (Array.isArray(o.messages)) return o.messages as Record<string, unknown>[]
    if (Array.isArray(o.choices)) {
      const msgs: Record<string, unknown>[] = []
      for (const c of o.choices as Record<string, unknown>[]) {
        if (c.message) msgs.push(c.message as Record<string, unknown>)
      }
      return msgs
    }
    return [o]
  }
  return []
}

function roleColor(role: string): string {
  switch (role) {
    case 'user': return 'var(--info)'
    case 'assistant': return 'var(--success)'
    case 'system': return 'var(--warning)'
    case 'tool': return 'var(--muted)'
    default: return 'inherit'
  }
}

function statusLabel(row: RequestLogDetail): string {
  if (row.request_status === 'in_progress') return '请求中'
  if (row.request_status === 'failure') {
    const ek = row.error_kind || ''
    if (!ek) return '失败'
    // 尝试 i18n 翻译：先查 gwErrorKind（带 gw_ 前缀的网关错误），再查 errorKind
    const gwKey = `requests.gwErrorKind.${ek}`
    const translated = t(gwKey)
    if (translated !== gwKey) return translated
    const ekKey = `requests.errorKind.${ek}`
    const ekTranslated = t(ekKey)
    if (ekTranslated !== ekKey) return ekTranslated
    // fallback: 下划线替换为空格
    return ek.replace(/_/g, ' ')
  }
  return row.request_status === 'rate_limited' ? '限流' : (row.success ? '成功' : '失败')
}

// 失败阶段标签
function failureStageLabel(stage: string | null | undefined): string {
  if (!stage) return ''
  switch (stage) {
    case 'gateway': return '网关'
    case 'upstream': return '上游'
    default: return stage
  }
}

function outboundModelDisplay(row: RequestLogDetail | null): string {
  if (!row) return '—'
  return row.provider_model || row.outbound_model || '—'
}

// 2026-07-13: 错误触发的主动探测元数据辅助函数
function isProbeRequest(row: RequestLogDetail | null): boolean {
  return row?.task_type === 'probe_triggered'
}

function probeOriginLabel(origin: string | null | undefined): string {
  switch (origin) {
    case 'probe_direct': return '直连上游'
    case 'probe_gateway': return '网关路径'
    case 'probe_scheduled': return '定时探测'
    default: return '主动探测'
  }
}

function probeAttempt(row: RequestLogDetail | null): number | null {
  if (!row?.auto_decision) return null
  const meta = row.auto_decision as Record<string, unknown> | null
  if (meta && typeof meta === 'object' && typeof meta.probe_attempt === 'number') {
    return meta.probe_attempt as number
  }
  // 回退：从 quality_flags 中尝试读取 "attempt_N" 标记（兼容老数据）
  if (Array.isArray(row.quality_flags)) {
    for (const flag of row.quality_flags) {
      const match = /^attempt_(\d+)$/.exec(flag)
      if (match) return parseInt(match[1], 10)
    }
  }
  return null
}

function routingAttempts(): RequestLogDetail['routing_attempts'] {
  return detail.value?.routing_attempts
}
</script>

<template>
  <div
    v-if="requestId"
    class="drawer-backdrop"
    :class="{ 'drawer-backdrop--nested': stackLevel === 'nested' }"
    @click="emit('close')"
  >
    <div class="drawer-panel card drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <h3 style="margin:0">原始请求详情</h3>
        <button class="btn btn-sm" type="button" @click="emit('close')">关闭</button>
      </div>

      <div v-if="loading" class="drawer-loading">
        <div class="drawer-skel" aria-hidden="true" />
        <p>正在加载请求元数据…</p>
      </div>
      <div v-else-if="error && !detail" class="drawer-error">{{ error }}</div>

      <template v-else-if="detail">
        <div v-if="error" class="drawer-error drawer-error-inline">{{ error }}</div>
        <div class="drawer-section">
          <div class="meta-line">
            <span><strong>请求ID:</strong> <code>{{ detail.request_id }}</code></span>
            <span><strong>时间:</strong> {{ fmtTs(detail.ts) }}</span>
            <span class="meta-model">
              <strong>模型:</strong>
              <ModelIdentityChip
                compact
                :client-model="detail.client_model"
                :canonical-name="detail.canonical_name || detail.canonical_model"
                :outbound-model="detail.provider_model || detail.outbound_model"
                :raw-model="detail.provider_model"
                @click-canonical="detail.canonical_name && router.push({ path: '/models', query: { q: detail.canonical_name } })"
              />
            </span>
            <span><strong>状态:</strong>
              <span :style="{ color: detail.request_status === 'rate_limited' ? 'var(--warning)' : detail.success ? 'var(--success)' : 'var(--danger)' }">
                {{ detail.success ? '成功' : statusLabel(detail) }}
              </span>
            </span>
            <span v-if="!detail.success && detail.failure_stage"><strong>失败阶段:</strong>
              <span style="color: var(--warning)">{{ failureStageLabel(detail.failure_stage) }}</span>
            </span>
            <span v-if="!detail.success && detail.failure_detail_code"><strong>失败代码:</strong>
              <code>{{ detail.failure_detail_code }}</code>
            </span>
            <span><strong>延迟:</strong> {{ detail.latency_ms ?? '—' }}ms</span>
            <span><strong>Token:</strong> {{ detail.prompt_tokens ?? '—' }} / {{ detail.completion_tokens ?? '—' }}</span>
            <span v-if="detail.gw_session_id"><strong>Session:</strong> {{ detail.gw_session_id }}</span>
            <span v-if="detail.gw_task_id"><strong>Task:</strong> {{ detail.gw_task_id }}</span>
            <span><strong>供应商:</strong> {{ detail.provider_name ?? '—' }}</span>
            <span><strong>Key:</strong> {{ detail.api_key_prefix ?? (detail.api_key_id != null ? `key#${detail.api_key_id}` : '—') }}</span>
            <span v-if="detail.application_code"><strong>应用:</strong> {{ detail.application_code }}</span>
            <span v-if="detail.upstream_finish_reason"><strong>结束原因:</strong> {{ detail.upstream_finish_reason }}</span>
            <span v-if="!isDefaultTenant()"><strong>积分消耗:</strong> {{ detail.credits_charged ?? '—' }}</span>
          </div>
        </div>

        <section v-if="props.mode === 'request-logs'" class="drawer-section session-meta-section">
          <SessionMetaTitleRow
            :key="`${detail.gw_session_id ?? ''}:${detail.gw_task_id ?? ''}`"
            :task-id="detail.gw_task_id"
            :session-id="detail.gw_session_id"
            :title="detail.session_title"
            @title-changed="onSessionTitleChanged"
            @open-summary="openSessionSummaryDrawer"
          />

          <div class="session-meta-tags">
            <div class="tags-header">
              <strong>项目 / 任务等标签:</strong>
              <button v-if="detail.gw_session_id && !addingTag" class="btn btn-sm" @click="startAddTag">+ 添加标签</button>
              <button v-if="addingTag" class="btn btn-sm" :disabled="newTagSaving" @click="cancelAddTag">取消</button>
            </div>
            <div v-if="!detail.gw_session_id" class="text-muted">此请求未绑定会话，无法关联标签。</div>
            <div v-else-if="sessionTagsLoading" class="text-muted">加载中…</div>
            <div v-else-if="sessionTagsError" class="meta-error">{{ sessionTagsError }}</div>
            <div v-else-if="!sessionTags.length && !addingTag" class="text-muted">暂无标签。可添加 project / task / client 等维度。</div>
            <div v-else class="tags-list">
              <div v-for="sessionTag in sessionTags" :key="sessionTag.id" class="tag-row">
                <template v-if="editingTagId === sessionTag.id">
                  <input v-model="editTagDraftKey" class="tag-input" maxlength="50" placeholder="key" @keydown.enter="submitEditTag" @keydown.esc="cancelEditTag" />
                  <span>:</span>
                  <input v-model="editTagDraftValue" class="tag-input tag-input-value" placeholder="value" @keydown.enter="submitEditTag" @keydown.esc="cancelEditTag" />
                  <button class="btn btn-sm btn-primary" :disabled="editTagSaving" @click="submitEditTag">{{ editTagSaving ? '保存中…' : '保存' }}</button>
                  <button class="btn btn-sm" :disabled="editTagSaving" @click="cancelEditTag">取消</button>
                </template>
                <template v-else>
                  <span class="tag-key">{{ sessionTag.tag_key }}</span>
                  <span>:</span>
                  <span>{{ sessionTag.tag_value }}</span>
                  <span v-if="sessionTag.tag_source" class="tag-source">{{ sessionTag.tag_source }}</span>
                  <span class="tag-actions">
                    <button class="btn btn-sm" :disabled="editingTagId !== null || addingTag" @click="startEditTag(sessionTag)">编辑</button>
                    <button class="btn btn-sm btn-danger-ghost" :disabled="editingTagId !== null || addingTag" @click="removeTag(sessionTag)">删除</button>
                  </span>
                </template>
              </div>
              <div v-if="addingTag" class="tag-row">
                <input v-model="newTagKey" class="tag-input" maxlength="50" placeholder="key" @keydown.enter="submitAddTag" @keydown.esc="cancelAddTag" />
                <span>:</span>
                <input v-model="newTagValue" class="tag-input tag-input-value" placeholder="value" @keydown.enter="submitAddTag" @keydown.esc="cancelAddTag" />
                <button class="btn btn-sm btn-primary" :disabled="newTagSaving || !newTagKey.trim() || !newTagValue.trim()" @click="submitAddTag">{{ newTagSaving ? '添加中…' : '添加' }}</button>
              </div>
            </div>
          </div>
        </section>

        <!-- 2026-07-17: 流程详情内嵌面板 — 直接在「请求详情」与「Tabs/按钮」之间
             展开一层 (不再弹窗), 暗色背景不抢抽屉主视觉。 -->
        <RequestTracePanel
          v-if="showTrace"
          :request-id="detail?.request_id ?? props.requestId"
          @close="showTrace = false"
        />

        <div class="drawer-section">
          <div class="tab-row">
            <button class="btn btn-sm" type="button" :class="{ 'btn-primary': tab === 'request' }" @click="tab = 'request'">请求消息</button>
            <button
              v-if="props.mode === 'request-logs' && hasOutboundBody(detail)"
              class="btn btn-sm"
              type="button"
              :class="{ 'btn-primary': tab === 'outbound' }"
              @click="tab = 'outbound'"
            >
              转发消息
              <span class="tab-badge">{{ outboundEqualsRequest(detail) ? '=' : `Δ${outboundMsgDelta(detail)}` }}</span>
            </button>
            <button class="btn btn-sm" type="button" :class="{ 'btn-primary': tab === 'response' }" @click="tab = 'response'">响应内容</button>
            <!-- 2026-07-02: 附件 Tab，仅在 attachments.length > 0 时显示，
                 与 RequestLogsView.vue 行为一致；文案走 i18n。 -->
            <button
              v-if="detailAttachments().length"
              class="btn btn-sm"
              type="button"
              :class="{ 'btn-primary': tab === 'attachments' }"
              @click="tab = 'attachments'"
            >
              {{ t('requests.detail_extra.attachmentsTab') }}
              <span class="tab-badge">{{ detailAttachments().length }}</span>
            </button>
            <button
              v-if="routingAttempts()?.attempts?.length"
              class="btn btn-sm"
              type="button"
              :class="{ 'btn-primary': tab === 'routing' }"
              @click="tab = 'routing'"
            >
              路由尝试
              <span class="tab-badge">{{ routingAttempts()?.attempts?.length }}</span>
            </button>
            <!-- 2026-07-17: 流程详情按钮 — 展开 RequestTracePanel (内嵌面板),
                 在「请求详情」与「Tabs」之间显示端到端链路 + 失败快照 + AI 提示词。
                 不再弹窗, 避免被亮色块 modal 抢戏;super_admin 限定,
                 开启与否不依赖 detail 字段, 因此即使 attachments=0 也显示。 -->
            <button
              class="btn btn-sm btn-trace"
              type="button"
              @click="showTrace = !showTrace"
              :title="t('trace.modal.tooltip')"
            >
              {{ showTrace ? '✕ ' + t('trace.modal.close') : t('trace.modal.openButton') }}
            </button>
          </div>
        </div>

        <div class="drawer-body-scroll">
          <p v-if="bodyLoading" class="body-loading-hint" role="status">正文与响应异步加载中…</p>
          <template v-if="tab === 'request'">
            <template v-if="extractMessagesFromBody(detail.request_body).length">
              <div v-for="(msg, i) in extractMessagesFromBody(detail.request_body)" :key="i" class="msg-block">
                <div class="msg-role" :style="{ color: roleColor(String(msg.role || '')) }">[{{ msg.role || 'unknown' }}]</div>
                <pre class="msg-pre">{{ formatJson(msg.content ?? msg) }}</pre>
                <div v-if="msg.tool_calls" class="tool-block">
                  <div class="tool-label">工具调用:</div>
                  <pre v-for="(tc, j) in (msg.tool_calls as unknown[])" :key="j" class="tool-pre">{{ formatJson(tc) }}</pre>
                </div>
              </div>
            </template>
            <div v-else class="text-muted">(无请求数据)</div>
          </template>

          <template v-else-if="tab === 'outbound'">
            <div class="outbound-summary">
              <strong>转发体</strong>
              <span>消息数 {{ detail.outbound_msg_count ?? '—' }}</span>
              <span>估算 {{ detail.outbound_token_est ?? '—' }} tokens</span>
              <span>节约 {{ compressionSavings(detail).savedBytes }}</span>
              <span>消息变化 {{ compressionSavings(detail).messageDelta }}</span>
              <span v-if="outboundSummaryMarker(detail)" class="summary-marker-badge">含 LLM 摘要</span>
            </div>
            <template v-if="extractMessagesFromBody(detail.outbound_body).length">
              <div v-for="(msg, i) in extractMessagesFromBody(detail.outbound_body)" :key="i" class="msg-block">
                <div class="msg-role" :style="{ color: roleColor(String(msg.role || '')) }">
                  [{{ msg.role || 'unknown' }}]
                  <span v-if="isSummaryMarkerMessage(msg)" class="summary-boundary">smm_v1 摘要边界</span>
                </div>
                <pre class="msg-pre">{{ formatJson(msg.content ?? msg) }}</pre>
                <div v-if="msg.tool_calls" class="tool-block">
                  <div class="tool-label">工具调用:</div>
                  <pre v-for="(tc, j) in (msg.tool_calls as unknown[])" :key="j" class="tool-pre">{{ formatJson(tc) }}</pre>
                </div>
              </div>
            </template>
            <div v-else class="text-muted">(无转发数据)</div>
          </template>

          <template v-else-if="tab === 'response'">
            <template v-if="detail.response_body">
              <template v-if="(detail.response_body as Record<string, unknown>).choices">
                <div
                  v-for="(choice, i) in ((detail.response_body as Record<string, unknown>).choices as Record<string, unknown>[])"
                  :key="i"
                  class="msg-block"
                >
                  <div class="msg-role">Choice {{ i }}
                    <span v-if="choice.finish_reason" class="text-muted"> · finish: {{ choice.finish_reason }}</span>
                  </div>
                  <template v-if="choice.message">
                    <div :style="{ color: roleColor(String((choice.message as Record<string, unknown>).role || '')) }">
                      [{{ (choice.message as Record<string, unknown>).role || 'unknown' }}]
                    </div>
                    <pre v-if="(choice.message as Record<string, unknown>).content" class="msg-pre">{{ (choice.message as Record<string, unknown>).content }}</pre>
                    <div v-if="(choice.message as Record<string, unknown>).tool_calls" class="tool-block">
                      <div class="tool-label">工具调用:</div>
                      <pre
                        v-for="(tc, j) in ((choice.message as Record<string, unknown>).tool_calls as unknown[])"
                        :key="j"
                        class="tool-pre"
                      >{{ formatJson(tc) }}</pre>
                    </div>
                  </template>
                </div>
              </template>
              <pre v-else class="msg-pre">{{ formatJson(detail.response_body) }}</pre>
            </template>
            <div v-else class="text-muted">(无响应数据)</div>
          </template>

          <!-- 2026-07-02: 附件面板。结构与 RequestLogsView.vue 同步。 -->
          <template v-else-if="tab === 'attachments'">
            <div v-if="detailAttachments().length" class="attachments-grid">
              <div
                v-for="(att, idx) in detailAttachments()"
                :key="(att.path || '') + idx"
                class="attachment-card"
              >
                <div
                  class="attachment-thumb"
                  :title="t('requests.detail_extra.clickToPreviewTitle')"
                  @click="isImageAttachment(att) ? openLightbox(att) : downloadAttachment(att)"
                >
                  <img
                    v-if="isImageAttachment(att)"
                    :src="attachmentURL(att.path)"
                    :alt="fileExt(att)"
                    loading="lazy"
                    class="attachment-img"
                  />
                  <div v-else class="attachment-file-icon">
                    <span>{{ fileExt(att) }}</span>
                  </div>
                </div>
                <div class="attachment-meta">
                  <div class="attachment-line1">
                    <span class="attachment-type">{{ att.content_type || att.type }}</span>
                    <span class="attachment-size">{{ formatBytes(att.size) }}</span>
                  </div>
                  <div class="attachment-line2" :title="att.path">{{ att.path }}</div>
                  <div v-if="att.hash" class="attachment-line2" :title="att.hash">SHA256: {{ att.hash.substring(0, 12) }}…</div>
                  <div class="attachment-actions">
                    <button class="btn btn-sm" type="button" @click="downloadAttachment(att)">
                      {{ t('requests.detail_extra.download') }}
                    </button>
                    <button
                      v-if="isImageAttachment(att)"
                      class="btn btn-sm"
                      type="button"
                      @click="openLightbox(att)"
                    >放大</button>
                  </div>
                </div>
              </div>
            </div>
            <div v-else class="text-muted">{{ t('requests.detail_extra.noAttachments') }}</div>
          </template>

          <RoutingAttemptsTimeline
            v-else-if="tab === 'routing'"
            :summary="detail.routing_summary"
            :attempts="routingAttempts()?.attempts"
          />
        </div>
      </template>

      <!-- 2026-07-02: 附件大图预览。Teleport to body 避免被父抽屉裁剪，
           ESC 由 handleKeydown 全局监听。 -->
      <Teleport to="body">
        <div v-if="attachmentsLightbox" class="lightbox-backdrop" @click="closeLightbox">
          <img :src="attachmentsLightboxSrc" class="lightbox-img" @click.stop alt="attachment preview" />
          <button class="btn btn-sm lightbox-close" type="button" @click="closeLightbox">
            {{ t('requests.detail_extra.closePreview') }}
          </button>
        </div>
      </Teleport>

      <!-- 2026-07-17: 流程详情面板改为内嵌, 不再使用 Teleport modal。
           RequestTracePanel 已在抽屉「请求详情」与「Tabs」之间展开。 -->
    </div>

    <SessionSummaryDrawer
      :open="summaryDrawerOpen"
      :session-id="summaryDrawerSessionId"
      :session-title="detail?.session_title"
      :task-id="detail?.gw_task_id ?? null"
      @close="closeSessionSummaryDrawer"
      @filter-session="onSummaryFilterSession"
      @open-request="onSummaryOpenRequest"
    />
  </div>
</template>

<style scoped>
.drawer-loading, .drawer-error {
  padding: 32px;
  text-align: center;
  font-size: 13px;
}
.drawer-error { color: var(--danger); }
.drawer-error-inline {
  padding: 10px 16px;
  margin: 0 0 8px;
  text-align: left;
  font-size: 12px;
  color: var(--warning);
  background: color-mix(in srgb, var(--warning) 10%, transparent);
}
.drawer-skel {
  width: min(420px, 80%);
  height: 10px;
  margin: 0 auto 14px;
  border-radius: 999px;
  background: linear-gradient(90deg, var(--kx-border), color-mix(in srgb, var(--kx-muted) 25%, transparent), var(--kx-border));
  background-size: 200% 100%;
  animation: drawer-skel 1.2s ease-in-out infinite;
}
@keyframes drawer-skel {
  0% { background-position: 100% 0; }
  100% { background-position: -100% 0; }
}
.body-loading-hint {
  margin: 0 0 10px;
  font-size: 12px;
  color: var(--kx-muted);
}
.meta-line {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 16px;
  font-size: 12px;
  margin-bottom: 8px;
}

/* 2026-07-13: 错误触发的主动探测元数据样式 */
.probe-info {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 8px;
  background: rgba(64, 158, 255, 0.12);
  border: 1px solid rgba(64, 158, 255, 0.4);
  border-radius: 4px;
  font-size: 12px;
  color: #1890ff;
}
.probe-info .probe-origin {
  font-weight: 500;
}
.probe-info .probe-attempt {
  padding: 0 6px;
  background: rgba(64, 158, 255, 0.2);
  border-radius: 3px;
  font-weight: 600;
}
.tab-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; flex-wrap: wrap; }
/* 2026-07-17: 流程详情按钮 — 暗色调, 与全局 btn-ghost 风格一致,
   hover 时轻微 accent 高亮, 但不引入亮色块背景。 */
.btn-trace {
  margin-left: auto;
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text);
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.btn-trace:hover {
  border-color: var(--accent);
  color: var(--accent-h);
}
.tab-badge {
  display: inline-block;
  margin-left: 4px;
  min-width: 18px;
  padding: 0 5px;
  height: 16px;
  line-height: 16px;
  border-radius: 8px;
  background: var(--accent);
  color: #fff;
  font-size: 10px;
  text-align: center;
  vertical-align: middle;
}
.drawer-body-scroll {
  flex: 1;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  background: var(--bg-subtle);
  font-size: 12px;
  max-height: calc(100vh - 220px);
}
.msg-block { margin-bottom: 12px; }
.msg-role { font-weight: 600; margin-bottom: 4px; }
.msg-pre {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
  font-size: 11px;
  line-height: 1.5;
}
.tool-block { margin-top: 6px; }
.tool-label { color: var(--muted); font-size: 11px; margin-bottom: 4px; }
.tool-pre {
  margin: 0 0 4px;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 11px;
  padding: 4px;
  background: var(--card);
  border-radius: 4px;
}
.text-muted { color: var(--muted); }

/* ── 2026-07-02: 附件面板 + 大图预览（与 RequestLogsView.vue 同款样式） ── */
.attachments-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}
.attachment-card {
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  background: var(--surface-primary);
  display: flex;
  flex-direction: column;
}
.attachment-thumb {
  width: 100%;
  height: 140px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #0d1b2a;
  cursor: pointer;
  overflow: hidden;
}
.attachment-thumb:hover {
  background: #122438;
}
.attachment-img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}
.attachment-file-icon {
  font-size: 14px;
  font-weight: 700;
  color: var(--muted);
  padding: 16px 20px;
  border: 1px dashed var(--border);
  border-radius: 6px;
}
.attachment-meta {
  padding: 8px;
  font-size: 11px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.attachment-line1 {
  display: flex;
  justify-content: space-between;
  gap: 8px;
}
.attachment-type {
  color: var(--accent);
  font-weight: 600;
}
.attachment-size {
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.attachment-line2 {
  color: var(--text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.attachment-actions {
  display: flex;
  gap: 6px;
  margin-top: 4px;
}

/* Lightbox（Teleport 到 body，需独立样式以覆盖全局） */
.lightbox-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.85);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 9999;
  backdrop-filter: blur(4px);
}
.lightbox-img {
  max-width: 92vw;
  max-height: 88vh;
  object-fit: contain;
  border-radius: 4px;
  background: #000;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.6);
}
.session-meta-section {
  background: var(--surface-primary);
  border-radius: 6px;
  padding: 8px 12px;
  margin-top: 8px;
  overflow: visible;
}
.tags-header,
.tag-row,
.outbound-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.session-meta-tags { border-top: 1px dashed var(--border); padding-top: 8px; }
.tags-list { display: flex; flex-direction: column; gap: 4px; }
.tag-row { padding: 4px 8px; border: 1px solid var(--border); border-radius: 4px; }
.tag-key { font-family: ui-monospace, SFMono-Regular, monospace; font-weight: 600; }
.tag-source, .summary-marker-badge, .summary-boundary {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  color: var(--accent);
}
.tag-actions { display: flex; gap: 4px; margin-left: auto; }
.title-input, .tag-input {
  min-width: 80px;
  padding: 3px 6px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
  font-size: 12px;
}
.title-input, .tag-input-value { flex: 1 1 160px; }
.meta-error, .btn-danger-ghost { color: var(--danger); }
.outbound-summary {
  margin-bottom: 10px;
  padding: 6px 10px;
  border-radius: 4px;
  background: var(--card);
  color: var(--text-secondary);
}
.lightbox-close {
  position: fixed;
  top: 16px;
  right: 16px;
  z-index: 10000;
}
/* Above NodeDetailDrawer (3000/3001); global .drawer-backdrop is only 100. */
.drawer-backdrop--nested {
  z-index: 3200;
}
</style>
