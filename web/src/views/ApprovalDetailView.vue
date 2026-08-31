<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { fmtDateMedium } from '../i18n/useFormat'
import { useRouter, useRoute } from 'vue-router'
import { getApprovalDetail, approveApproval, rejectApproval, type ApprovalDetail } from '../api/approval'
import PageBackLink from '../components/PageBackLink.vue'

const router = useRouter()
const route = useRoute()
const { t } = useI18n()

const requestId = computed(() => route.params.id as string)

// State
const loading = ref(false)
const approval = ref<ApprovalDetail | null>(null)
const error = ref<string | null>(null)
const successMessage = ref<string | null>(null)

// Action state
const actionLoading = ref(false)
const showApproveForm = ref(false)
const showRejectForm = ref(false)
const actionReason = ref('')

// UI state
const showSessionSummary = ref(false)
const showContext = ref(false)
const showFullMessages = ref(false)

// Real-time updates
let refreshInterval: number | undefined

function getRiskLevelColor(level: string): string {
  switch (level?.toUpperCase()) {
    case 'LOW': return 'green'
    case 'MEDIUM': return 'yellow'
    case 'HIGH': return 'orange'
    case 'CRITICAL': return 'red'
    default: return 'gray'
  }
}

function getRiskLevelLabel(level: string): string {
  switch (level?.toUpperCase()) {
    case 'LOW': return '低风险'
    case 'MEDIUM': return '中风险'
    case 'HIGH': return '高风险'
    case 'CRITICAL': return '严重风险'
    default: return level || '-'
  }
}

function getStatusColor(status: string): string {
  switch (status) {
    case 'pending': return 'yellow'
    case 'approved': return 'green'
    case 'rejected': return 'red'
    case 'timeout': return 'gray'
    default: return 'gray'
  }
}

function getStatusLabel(status: string): string {
  switch (status) {
    case 'pending': return '待审批'
    case 'approved': return '已批准'
    case 'rejected': return '已拒绝'
    case 'timeout': return '已超时'
    default: return status
  }
}

function formatCost(cost?: number): string {
  if (!cost) return '-'
  return `¥${cost.toFixed(4)}`
}

const isPending = computed(() => approval.value?.status === 'pending')
const isDecided = computed(() => ['approved', 'rejected', 'timeout'].includes(approval.value?.status || ''))

async function loadApproval() {
  loading.value = true
  error.value = null

  try {
    approval.value = await getApprovalDetail(requestId.value)
  } catch (e: any) {
    error.value = e.message || '加载审批详情失败'
  } finally {
    loading.value = false
  }
}

async function handleApprove() {
  if (actionLoading.value) return

  actionLoading.value = true
  error.value = null
  successMessage.value = null

  try {
    await approveApproval(requestId.value, actionReason.value || undefined)
    successMessage.value = '审批请求已批准'
    showApproveForm.value = false
    actionReason.value = ''
    await loadApproval()
  } catch (e: any) {
    error.value = e.message || '批准失败'
  } finally {
    actionLoading.value = false
  }
}

async function handleReject() {
  if (actionLoading.value) return

  if (!actionReason.value.trim()) {
    error.value = '请输入拒绝原因'
    return
  }

  actionLoading.value = true
  error.value = null
  successMessage.value = null

  try {
    await rejectApproval(requestId.value, actionReason.value)
    successMessage.value = '审批请求已拒绝'
    showRejectForm.value = false
    actionReason.value = ''
    await loadApproval()
  } catch (e: any) {
    error.value = e.message || '拒绝失败'
  } finally {
    actionLoading.value = false
  }
}

function cancelAction() {
  showApproveForm.value = false
  showRejectForm.value = false
  actionReason.value = ''
  error.value = null
}

function startAutoRefresh() {
  if (isPending.value) {
    refreshInterval = window.setInterval(() => {
      loadApproval()
    }, 15000) // Refresh every 15 seconds
  }
}

function stopAutoRefresh() {
  if (refreshInterval) {
    clearInterval(refreshInterval)
    refreshInterval = undefined
  }
}

onMounted(() => {
  loadApproval()
  startAutoRefresh()
})

onBeforeUnmount(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div class="approval-detail-view">
    <!-- Header -->
    <div class="page-header">
      <div>
        <PageBackLink :to="'/admin/approvals'" :label="t('approval.detail.back')" />
        <h1>{{ t('approval.detail.title') }}</h1>
        <p class="page-description">Request ID: {{ requestId }}</p>
      </div>
    </div>

    <!-- Messages -->
    <div v-if="error" class="message message-error">
      <span class="message-icon">❌</span>
      {{ error }}
      <button class="message-close" @click="error = null">×</button>
    </div>

    <div v-if="successMessage" class="message message-success">
      <span class="message-icon">✅</span>
      {{ successMessage }}
      <button class="message-close" @click="successMessage = null">×</button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner">加载中...</div>
    </div>

    <!-- Content -->
    <div v-else-if="approval" class="content">
      <!-- Basic Info -->
      <section class="section">
        <div class="section-header">
          <h2>{{ t('approval.detail.sections.basic') }}</h2>
          <span class="badge" :class="`badge-${getStatusColor(approval.status)}`">
            {{ getStatusLabel(approval.status) }}
          </span>
        </div>

        <div class="info-grid">
          <div class="info-item">
            <div class="info-label">会话 ID</div>
            <div class="info-value text-mono">{{ approval.session_id }}</div>
          </div>

          <div class="info-item">
            <div class="info-label">请求 ID</div>
            <div class="info-value text-mono">{{ approval.request_id }}</div>
          </div>

          <div class="info-item">
            <div class="info-label">租户 ID</div>
            <div class="info-value">{{ approval.tenant_id }}</div>
          </div>

          <div class="info-item">
            <div class="info-label">风险等级</div>
            <div class="info-value">
              <span class="badge badge-large" :class="`badge-${getRiskLevelColor(approval.risk_level)}`">
                {{ getRiskLevelLabel(approval.risk_level) }}
              </span>
            </div>
          </div>

          <div class="info-item">
            <div class="info-label">触发原因</div>
            <div class="info-value">{{ approval.trigger_type || '-' }}</div>
          </div>

          <div class="info-item">
            <div class="info-label">预估成本</div>
            <div class="info-value">{{ formatCost(approval.detect_result?.cost_estimation) }}</div>
          </div>

          <div class="info-item">
            <div class="info-label">创建时间</div>
            <div class="info-value">{{ fmtDateMedium(approval.created_at) }}</div>
          </div>

          <div class="info-item">
            <div class="info-label">过期时间</div>
            <div class="info-value">
              {{ fmtDateMedium(approval.expires_at) }}
              <span v-if="approval.time_left && isPending" class="time-left">
                (剩余: {{ approval.time_left }})
              </span>
            </div>
          </div>

          <div v-if="approval.approved_by" class="info-item">
            <div class="info-label">审批人</div>
            <div class="info-value">{{ approval.approved_by }}</div>
          </div>

          <div v-if="approval.approved_at" class="info-item">
            <div class="info-label">审批时间</div>
            <div class="info-value">{{ fmtDateMedium(approval.approved_at) }}</div>
          </div>

          <div v-if="approval.reason" class="info-item info-item-full">
            <div class="info-label">审批说明</div>
            <div class="info-value">{{ approval.reason }}</div>
          </div>
        </div>
      </section>

      <!-- Sensitive Info -->
      <section v-if="approval.detect_result?.sensitive_info?.length" class="section">
        <div class="section-header">
          <h2>{{ t('approval.detail.sections.sensitive') }}</h2>
          <span class="badge badge-red">{{ approval.detect_result.sensitive_info.length }} 项</span>
        </div>

        <div class="sensitive-list">
          <div v-for="(item, idx) in approval.detect_result.sensitive_info" :key="idx" class="sensitive-item">
            <span class="sensitive-icon">⚠️</span>
            <span class="sensitive-text">{{ item }}</span>
          </div>
        </div>
      </section>

      <!-- Session Summary -->
      <section v-if="approval.snapshot?.session_summary" class="section collapsible-section">
        <div class="section-header clickable" @click="showSessionSummary = !showSessionSummary">
          <h2>
            <span class="collapse-icon">{{ showSessionSummary ? '▼' : '▶' }}</span>
            会话摘要
          </h2>
        </div>

        <div v-if="showSessionSummary" class="section-content">
          <div class="summary-text">{{ approval.snapshot.session_summary }}</div>
        </div>
      </section>

      <!-- Messages -->
      <section v-if="approval.snapshot?.messages?.length" class="section collapsible-section">
        <div class="section-header clickable" @click="showFullMessages = !showFullMessages">
          <h2>
            <span class="collapse-icon">{{ showFullMessages ? '▼' : '▶' }}</span>
            用户消息
          </h2>
          <span class="badge badge-gray">{{ approval.snapshot.messages.length }} 条</span>
        </div>

        <div v-if="showFullMessages" class="section-content">
          <div class="messages-list">
            <div v-for="(msg, idx) in approval.snapshot.messages" :key="idx" class="message-item">
              <div class="message-header">
                <span class="message-role" :class="`role-${msg.role}`">{{ msg.role }}</span>
                <span v-if="msg.redacted" class="message-redacted">已脱敏</span>
              </div>
              <div class="message-content">{{ msg.content }}</div>
            </div>
          </div>
        </div>
      </section>

      <!-- Full Context (Admin Only) -->
      <section v-if="approval.snapshot?.context" class="section collapsible-section">
        <div class="section-header clickable" @click="showContext = !showContext">
          <h2>
            <span class="collapse-icon">{{ showContext ? '▼' : '▶' }}</span>
            完整上下文
          </h2>
          <span class="badge badge-orange">管理员权限</span>
        </div>

        <div v-if="showContext" class="section-content">
          <pre class="context-data">{{ JSON.stringify(approval.snapshot.context, null, 2) }}</pre>
        </div>
      </section>

      <!-- Action Forms -->
      <section v-if="isPending" class="section action-section">
        <div class="section-header">
          <h2>{{ t('approval.detail.sections.actions') }}</h2>
        </div>

        <div v-if="!showApproveForm && !showRejectForm" class="action-buttons-main">
          <button class="btn btn-success btn-large" @click="showApproveForm = true">
            ✓ 批准
          </button>
          <button class="btn btn-danger btn-large" @click="showRejectForm = true">
            ✕ 拒绝
          </button>
        </div>

        <!-- Approve Form -->
        <div v-if="showApproveForm" class="action-form">
          <h3>批准审批请求</h3>
          <div class="form-group">
            <label>备注（可选）</label>
            <textarea
              v-model="actionReason"
              class="form-textarea"
              placeholder="输入批准原因或备注..."
              rows="3"
            ></textarea>
          </div>
          <div class="form-actions">
            <button
              class="btn btn-success"
              @click="handleApprove"
              :disabled="actionLoading"
            >
              {{ actionLoading ? '处理中...' : '确认批准' }}
            </button>
            <button
              class="btn btn-secondary"
              @click="cancelAction"
              :disabled="actionLoading"
            >
              取消
            </button>
          </div>
        </div>

        <!-- Reject Form -->
        <div v-if="showRejectForm" class="action-form">
          <h3>拒绝审批请求</h3>
          <div class="form-group">
            <label>拒绝原因 <span class="required">*</span></label>
            <textarea
              v-model="actionReason"
              class="form-textarea"
              placeholder="请输入拒绝原因（必填）..."
              rows="3"
            ></textarea>
          </div>
          <div class="form-actions">
            <button
              class="btn btn-danger"
              @click="handleReject"
              :disabled="actionLoading || !actionReason.trim()"
            >
              {{ actionLoading ? '处理中...' : '确认拒绝' }}
            </button>
            <button
              class="btn btn-secondary"
              @click="cancelAction"
              :disabled="actionLoading"
            >
              取消
            </button>
          </div>
        </div>
      </section>

      <!-- Decision History (if decided) -->
      <section v-if="isDecided" class="section">
        <div class="section-header">
          <h2>{{ t('approval.detail.sections.history') }}</h2>
        </div>

        <div class="timeline">
          <div class="timeline-item">
            <div class="timeline-dot timeline-dot-blue"></div>
            <div class="timeline-content">
              <div class="timeline-title">请求创建</div>
              <div class="timeline-time">{{ fmtDateMedium(approval.created_at) }}</div>
            </div>
          </div>

          <div v-if="approval.approved_at" class="timeline-item">
            <div class="timeline-dot" :class="`timeline-dot-${getStatusColor(approval.status)}`"></div>
            <div class="timeline-content">
              <div class="timeline-title">{{ getStatusLabel(approval.status) }}</div>
              <div class="timeline-time">{{ fmtDateMedium(approval.approved_at) }}</div>
              <div v-if="approval.approved_by" class="timeline-detail">审批人: {{ approval.approved_by }}</div>
              <div v-if="approval.reason" class="timeline-detail">说明: {{ approval.reason }}</div>
            </div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.approval-detail-view {
  padding: 20px;
  max-width: 1200px;
  margin: 0 auto;
  color: var(--text-primary);
}

.page-header {
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 8px 0;
  font-size: 28px;
  font-weight: 600;
}

.page-description {
  margin: 0;
  font-size: 14px;
  color: var(--text-secondary);
  font-family: 'Monaco', 'Menlo', monospace;
}

.message {
  padding: 12px 16px;
  border-radius: 6px;
  margin-bottom: 16px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  position: relative;
}

.message-icon {
  font-size: 16px;
}

.message-close {
  margin-left: auto;
  background: none;
  border: none;
  color: inherit;
  font-size: 20px;
  cursor: pointer;
  padding: 0 4px;
  opacity: 0.7;
}

.message-close:hover {
  opacity: 1;
}

.message-error {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
}

.message-success {
  background: var(--success-bg);
  border: 1px solid var(--success-bd);
  color: var(--success);
}

.loading-container {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 64px;
}

.loading-spinner {
  font-size: 16px;
  color: var(--text-secondary);
}

.content {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.section {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 20px;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.section-header h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
}

.clickable {
  cursor: pointer;
  user-select: none;
}

.clickable:hover h2 {
  color: var(--accent);
}

.collapse-icon {
  font-size: 14px;
  color: var(--text-secondary);
}

.section-content {
  margin-top: 16px;
}

.info-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
}

.info-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.info-item-full {
  grid-column: 1 / -1;
}

.info-label {
  font-size: 13px;
  color: var(--text-secondary);
  font-weight: 500;
}

.info-value {
  font-size: 14px;
  color: var(--text-primary);
}

.text-mono {
  font-family: 'Monaco', 'Menlo', monospace;
  font-size: 13px;
  word-break: break-all;
}

.time-left {
  color: var(--warning);
  font-size: 12px;
  margin-left: 8px;
}

.badge {
  display: inline-block;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  text-transform: uppercase;
}

.badge-large {
  padding: 6px 14px;
  font-size: 13px;
}

.badge-green {
  background: var(--success-bg);
  color: var(--success);
}

.badge-yellow {
  background: var(--warning-bg);
  color: var(--warning);
}

.badge-orange {
  background: var(--warning-bd);
  color: var(--warning);
}

.badge-red {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
}

.badge-gray {
  background: var(--neutral-bg);
  color: var(--muted);
}

.sensitive-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.sensitive-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--danger) 12%, transparent);
  border-radius: 6px;
}

.sensitive-icon {
  font-size: 18px;
  flex-shrink: 0;
}

.sensitive-text {
  font-size: 14px;
  color: var(--danger);
}

.summary-text {
  padding: 16px;
  background: var(--bg);
  border-radius: 6px;
  font-size: 14px;
  line-height: 1.6;
  color: var(--text-primary);
  white-space: pre-wrap;
}

.messages-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.message-item {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
}

.message-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}

.message-role {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  padding: 3px 8px;
  border-radius: 4px;
  background: var(--border);
  color: var(--text-primary);
}

.role-user {
  background: color-mix(in srgb, var(--accent) 20%, transparent);
  color: var(--accent);
}

.role-assistant {
  background: rgba(52, 211, 153, 0.2);
  color: var(--success);
}

.role-system {
  background: color-mix(in srgb, var(--muted) 14%, transparent);
  color: var(--muted);
}

.message-redacted {
  font-size: 11px;
  color: var(--warning);
  padding: 2px 6px;
  background: var(--warning-bg);
  border-radius: 3px;
}

.message-content {
  font-size: 14px;
  line-height: 1.6;
  color: var(--text-primary);
  white-space: pre-wrap;
}

.context-data {
  padding: 16px;
  background: var(--bg);
  border-radius: 6px;
  font-size: 12px;
  font-family: 'Monaco', 'Menlo', monospace;
  color: var(--text-primary);
  overflow-x: auto;
  max-height: 500px;
  overflow-y: auto;
}

.action-section {
  border: 2px solid var(--accent);
}

.action-buttons-main {
  display: flex;
  gap: 16px;
  justify-content: center;
}

.action-form {
  background: var(--bg);
  border-radius: 6px;
  padding: 20px;
}

.action-form h3 {
  margin: 0 0 16px;
  font-size: 16px;
  font-weight: 600;
}

.form-group {
  margin-bottom: 16px;
}

.form-group label {
  display: block;
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
  margin-bottom: 8px;
}

.required {
  color: var(--danger);
}

.form-textarea {
  width: 100%;
  padding: 10px 12px;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text-primary);
  font-size: 14px;
  font-family: inherit;
  resize: vertical;
}

.form-textarea:focus {
  outline: none;
  border-color: var(--accent);
}

.form-actions {
  display: flex;
  gap: 12px;
}

.timeline {
  display: flex;
  flex-direction: column;
  gap: 0;
}

.timeline-item {
  display: flex;
  gap: 16px;
  padding: 12px 0;
  position: relative;
}

.timeline-item:not(:last-child)::after {
  content: '';
  position: absolute;
  left: 7px;
  top: 32px;
  bottom: -12px;
  width: 2px;
  background: var(--border);
}

.timeline-dot {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  flex-shrink: 0;
  margin-top: 4px;
}

.timeline-dot-blue {
  background: var(--accent);
}

.timeline-dot-green {
  background: var(--success);
}

.timeline-dot-red {
  background: var(--danger);
}

.timeline-dot-yellow {
  background: var(--warning);
}

.timeline-dot-gray {
  background: var(--muted);
}

.timeline-content {
  flex: 1;
}

.timeline-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 4px;
}

.timeline-time {
  font-size: 13px;
  color: var(--text-secondary);
  margin-bottom: 4px;
}

.timeline-detail {
  font-size: 13px;
  color: var(--text-primary);
  margin-top: 4px;
}

.btn {
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 14px;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.2s;
  font-weight: 500;
}

.btn:hover:not(:disabled) {
  opacity: 0.9;
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-large {
  padding: 12px 32px;
  font-size: 16px;
}

.btn-success {
  background: var(--success);
  color: var(--kx-text);
  border-color: var(--success);
}

.btn-success:hover:not(:disabled) {
  background: #2cc189;
}

.btn-danger {
  background: var(--danger);
  color: var(--on-primary);
  border-color: var(--danger);
}

.btn-danger:hover:not(:disabled) {
  background: #f65e5e;
}

.btn-secondary {
  background: var(--bg-card);
  color: var(--text-primary);
  border-color: var(--border);
}

.btn-secondary:hover:not(:disabled) {
  border-color: var(--accent);
}

@media (max-width: 768px) {
  .info-grid {
    grid-template-columns: 1fr;
  }

  .action-buttons-main {
    flex-direction: column;
  }

  .form-actions {
    flex-direction: column;
  }
}
</style>
