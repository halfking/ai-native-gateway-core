<script setup lang="ts">
// RequestLogDrawer.vue — 独立请求详情抽屉组件。
//
// 2026-07-02: 补齐附件展示（与 RequestLogsView.vue 同步实现，对齐参考文档
// REQUEST_LOGS_ATTACHMENT_SYNC.md）：
//   - 「📎 附件 (N)」Tab 按钮，仅在 attachments.length > 0 时显示
//   - 附件网格：图片缩略图 / 文件图标 + 类型/大小/路径/SHA256(截断) + 下载/放大按钮
//   - 大图预览 lightbox：Teleport 到 body，全局 ESC 关闭（点击遮罩/图片/按钮亦可关闭）
//
// 该组件被 web/src/views/session-context/SessionContextDetailView.vue:435 调用，
// 作为从 Session 上下文跳转请求详情的入口。

import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getRequestLogDetail,
  attachmentURL,
  type RequestLogDetail,
  type AttachmentInfo,
} from '../api'

const props = defineProps<{
  requestId: string | null
}>()

const emit = defineEmits<{
  close: []
}>()

const loading = ref(false)
const detail = ref<RequestLogDetail | null>(null)
const error = ref('')
const tab = ref<'request' | 'response' | 'attachments'>('request')

// 2026-07-02: 附件 lightbox 状态。Teleport 到 body 后由 handleKeydown 全局监听 ESC。
const attachmentsLightbox = ref(false)
const attachmentsLightboxSrc = ref('')

// 2026-07-02: i18n 接入；附件文案走 t() 键（键定义见 web/src/locales/*.ts）。
const { t } = useI18n()

watch(
  () => props.requestId,
  async (id) => {
    detail.value = null
    error.value = ''
    tab.value = 'request'
    closeLightbox()
    if (!id) return
    loading.value = true
    try {
      detail.value = await getRequestLogDetail(id)
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '加载失败'
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

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
  return new Date(v).toLocaleString('zh-CN')
}

function formatJson(obj: unknown): string {
  if (obj == null) return '(无数据)'
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
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
    case 'user': return 'var(--info, #3b82f6)'
    case 'assistant': return 'var(--success, #22c55e)'
    case 'system': return 'var(--warning, #f59e0b)'
    case 'tool': return 'var(--muted, #94a3b8)'
    default: return 'inherit'
  }
}

function statusLabel(row: RequestLogDetail): string {
  if (row.request_status === 'in_progress') return '请求中'
  if (row.request_status === 'failure') return row.error_kind || '失败'
  return row.success ? '成功' : '失败'
}

function outboundModelDisplay(row: RequestLogDetail | null): string {
  if (!row) return '—'
  return row.provider_model || row.outbound_model || '—'
}
</script>

<template>
  <div v-if="requestId" class="drawer-backdrop" @click="emit('close')">
    <div class="drawer-panel card drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <h3 style="margin:0">原始请求详情</h3>
        <button class="btn btn-sm" type="button" @click="emit('close')">关闭</button>
      </div>

      <div v-if="loading" class="drawer-loading">加载中…</div>
      <div v-else-if="error" class="drawer-error">{{ error }}</div>

      <template v-else-if="detail">
        <div class="drawer-section">
          <div class="meta-line">
            <span><strong>请求ID:</strong> <code>{{ detail.request_id }}</code></span>
            <span><strong>时间:</strong> {{ fmtTs(detail.ts) }}</span>
            <span><strong>模型:</strong> {{ detail.client_model ?? '—' }}</span>
            <span><strong>出站:</strong> {{ outboundModelDisplay(detail) }}</span>
            <span><strong>状态:</strong>
              <span :style="{ color: detail.success ? 'var(--success)' : 'var(--danger)' }">
                {{ detail.success ? '成功' : statusLabel(detail) }}
              </span>
            </span>
            <span><strong>延迟:</strong> {{ detail.latency_ms ?? '—' }}ms</span>
            <span><strong>Token:</strong> {{ detail.prompt_tokens ?? '—' }} / {{ detail.completion_tokens ?? '—' }}</span>
            <span v-if="detail.gw_session_id"><strong>Session:</strong> {{ detail.gw_session_id }}</span>
            <span v-if="detail.gw_task_id"><strong>Task:</strong> {{ detail.gw_task_id }}</span>
          </div>
        </div>

        <div class="drawer-section">
          <div class="tab-row">
            <button class="btn btn-sm" type="button" :class="{ 'btn-primary': tab === 'request' }" @click="tab = 'request'">请求消息</button>
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
          </div>
        </div>

        <div class="drawer-body-scroll">
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
    </div>
  </div>
</template>

<style scoped>
.drawer-loading, .drawer-error {
  padding: 32px;
  text-align: center;
  font-size: 13px;
}
.drawer-error { color: var(--danger); }
.meta-line {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 16px;
  font-size: 12px;
  margin-bottom: 8px;
}
.tab-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.tab-badge {
  display: inline-block;
  margin-left: 4px;
  min-width: 18px;
  padding: 0 5px;
  height: 16px;
  line-height: 16px;
  border-radius: 8px;
  background: var(--accent, #3b82f6);
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
  border: 1px solid var(--border, #333);
  border-radius: 6px;
  overflow: hidden;
  background: var(--surface-primary, #16213e);
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
  border: 1px dashed var(--border, #333);
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
  color: var(--accent, #3b82f6);
  font-weight: 600;
}
.attachment-size {
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.attachment-line2 {
  color: var(--text-secondary, #6b7280);
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
.lightbox-close {
  position: fixed;
  top: 16px;
  right: 16px;
  z-index: 10000;
}
</style>