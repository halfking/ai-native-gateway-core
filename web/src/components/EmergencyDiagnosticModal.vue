<script setup lang="ts">
// EmergencyDiagnosticModal.vue — 应急诊断弹窗
// 2026-07-12: 显示凭据诊断信息、智能分析、推荐恢复方案，支持一键强制恢复

import { ref, computed, watch } from 'vue'
import { authBearer } from '../store'

const props = defineProps<{
  visible: boolean
  credentialId: number
  model: string
  laneName: string
}>()

const emit = defineEmits<{
  close: []
  recovered: []
}>()

interface DiagFailure {
  request_id: string
  client_model: string
  ts: string
  error_kind: string
  status: string
  provider_id: number
}

interface DiagBinding {
  raw_model_name: string
  provider_id: number
  provider_code: string
  available: boolean
  unavailable_reason: string
  unavailable_recover_at?: string
}

interface DiagnosticData {
  credential_id: number
  label: string
  provider_id: number
  provider_code: string
  availability_state: string
  health_status: string
  state_reason_code: string
  state_reason_detail: string
  circuit_state: string
  consecutive_failures: number
  balance_usd?: number
  bindings: DiagBinding[]
  recent_failures: DiagFailure[]
  recent_failures_count: number
  analysis: string
  recommendation: string
  recoverable: boolean
}

const loading = ref(false)
const recovering = ref(false)
const diagnosticData = ref<DiagnosticData | null>(null)
const error = ref<string | null>(null)

const hasData = computed(() => !!diagnosticData.value)

async function fetchDiagnostic() {
  if (!props.visible || props.credentialId <= 0) return
  
  loading.value = true
  error.value = null
  
  try {
    const response = await fetch(
      `/api/admin/diagnostics/credential?id=${props.credentialId}&minutes=15`,
      {
        headers: {
          'Authorization': authBearer(),
        },
      }
    )
    
    if (!response.ok) {
      const errData = await response.json().catch(() => ({}))
      throw new Error(errData.error?.detail || `HTTP ${response.status}`)
    }
    
    diagnosticData.value = await response.json()
  } catch (err: any) {
    error.value = err.message || '诊断失败'
    diagnosticData.value = null
  } finally {
    loading.value = false
  }
}

async function handleForceRecover() {
  if (!props.credentialId || recovering.value) return
  
  if (!confirm(`确认强制恢复凭据 ${props.credentialId}？\n此操作将重置凭据状态、清空所有 binding 的不可用标记、重置探测状态。`)) {
    return
  }
  
  recovering.value = true
  error.value = null
  
  try {
    const response = await fetch(
      `/api/admin/diagnostics/credential/force-recover?id=${props.credentialId}`,
      {
        method: 'POST',
        headers: {
          'Authorization': authBearer(),
        },
      }
    )
    
    if (!response.ok) {
      const errData = await response.json().catch(() => ({}))
      throw new Error(errData.error?.detail || `HTTP ${response.status}`)
    }
    
    // 恢复成功，重新拉取诊断数据
    await fetchDiagnostic()
    emit('recovered')
  } catch (err: any) {
    error.value = `恢复失败: ${err.message}`
  } finally {
    recovering.value = false
  }
}

function handleClose() {
  emit('close')
}

function formatTimestamp(ts: string): string {
  const d = new Date(ts)
  return d.toLocaleString('zh-CN', { 
    month: '2-digit', 
    day: '2-digit', 
    hour: '2-digit', 
    minute: '2-digit', 
    second: '2-digit' 
  })
}

// 监听 visible 变化，打开时自动拉取
watch(() => props.visible, (visible) => {
  if (visible) {
    fetchDiagnostic()
  } else {
    // 关闭时清空数据
    diagnosticData.value = null
    error.value = null
  }
}, { immediate: true })
</script>

<template>
  <div v-if="visible" class="modal-backdrop" @click.self="handleClose">
    <div class="modal-content">
      <div class="modal-header">
        <h2 class="modal-title">⚠️ 应急诊断</h2>
        <button class="modal-close" @click="handleClose" title="关闭">✕</button>
      </div>

      <div class="modal-body">
        <!-- 上下文信息 -->
        <div class="diag-context">
          <div class="diag-context-item">
            <span class="diag-context-label">泳道:</span>
            <span class="diag-context-value">{{ laneName }}</span>
          </div>
          <div class="diag-context-item">
            <span class="diag-context-label">模型:</span>
            <span class="diag-context-value">{{ model }}</span>
          </div>
          <div class="diag-context-item">
            <span class="diag-context-label">凭据 ID:</span>
            <span class="diag-context-value">{{ credentialId }}</span>
          </div>
        </div>

        <!-- 加载状态 -->
        <div v-if="loading" class="diag-loading">
          <div class="spinner"></div>
          <p>正在诊断...</p>
        </div>

        <!-- 错误信息 -->
        <div v-if="error && !loading" class="diag-error">
          {{ error }}
        </div>

        <!-- 诊断数据 -->
        <div v-if="hasData && !loading" class="diag-data">
          <!-- 凭据状态 -->
          <section class="diag-section">
            <h3 class="diag-section-title">凭据状态</h3>
            <div class="diag-info-grid">
              <div class="diag-info-item">
                <span class="diag-info-label">标签:</span>
                <span class="diag-info-value">{{ diagnosticData!.label }}</span>
              </div>
              <div class="diag-info-item">
                <span class="diag-info-label">供应商:</span>
                <span class="diag-info-value">{{ diagnosticData!.provider_code }} ({{ diagnosticData!.provider_id }})</span>
              </div>
              <div class="diag-info-item">
                <span class="diag-info-label">可用状态:</span>
                <span 
                  class="diag-info-value" 
                  :class="diagnosticData!.availability_state === 'ready' ? 'status-ok' : 'status-warn'"
                >
                  {{ diagnosticData!.availability_state }}
                </span>
              </div>
              <div class="diag-info-item">
                <span class="diag-info-label">健康状态:</span>
                <span 
                  class="diag-info-value" 
                  :class="diagnosticData!.health_status === 'healthy' ? 'status-ok' : 'status-error'"
                >
                  {{ diagnosticData!.health_status }}
                </span>
              </div>
              <div class="diag-info-item">
                <span class="diag-info-label">熔断器:</span>
                <span 
                  class="diag-info-value" 
                  :class="diagnosticData!.circuit_state === 'closed' ? 'status-ok' : 'status-error'"
                >
                  {{ diagnosticData!.circuit_state }}
                </span>
              </div>
              <div class="diag-info-item">
                <span class="diag-info-label">连续失败:</span>
                <span 
                  class="diag-info-value" 
                  :class="diagnosticData!.consecutive_failures > 0 ? 'status-error' : 'status-ok'"
                >
                  {{ diagnosticData!.consecutive_failures }}
                </span>
              </div>
              <div v-if="diagnosticData!.balance_usd !== undefined" class="diag-info-item">
                <span class="diag-info-label">余额:</span>
                <span class="diag-info-value">${{ diagnosticData!.balance_usd.toFixed(2) }}</span>
              </div>
            </div>
          </section>

          <!-- Binding 状态 -->
          <section v-if="diagnosticData!.bindings.length > 0" class="diag-section">
            <h3 class="diag-section-title">模型绑定 ({{ diagnosticData!.bindings.length }})</h3>
            <div class="diag-bindings-table">
              <table>
                <thead>
                  <tr>
                    <th>模型</th>
                    <th>供应商</th>
                    <th>可用</th>
                    <th>不可用原因</th>
                    <th>恢复时间</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="b in diagnosticData!.bindings" :key="b.raw_model_name">
                    <td>{{ b.raw_model_name }}</td>
                    <td>{{ b.provider_code }}</td>
                    <td>
                      <span :class="b.available ? 'status-ok' : 'status-error'">
                        {{ b.available ? '✓' : '✗' }}
                      </span>
                    </td>
                    <td>{{ b.unavailable_reason || '-' }}</td>
                    <td>{{ b.unavailable_recover_at ? formatTimestamp(b.unavailable_recover_at) : '-' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <!-- 最近失败 -->
          <section v-if="diagnosticData!.recent_failures.length > 0" class="diag-section">
            <h3 class="diag-section-title">最近失败 ({{ diagnosticData!.recent_failures_count }})</h3>
            <div class="diag-failures-list">
              <div 
                v-for="f in diagnosticData!.recent_failures.slice(0, 5)" 
                :key="f.request_id" 
                class="diag-failure-item"
              >
                <div class="diag-failure-header">
                  <span class="diag-failure-model">{{ f.client_model }}</span>
                  <span class="diag-failure-time">{{ formatTimestamp(f.ts) }}</span>
                </div>
                <div class="diag-failure-detail">
                  <span class="diag-failure-kind">{{ f.error_kind }}</span>
                  <span class="diag-failure-status">{{ f.status }}</span>
                  <span class="diag-failure-request-id" :title="f.request_id">{{ f.request_id.slice(0, 12) }}...</span>
                </div>
              </div>
            </div>
          </section>

          <!-- 智能分析 -->
          <section class="diag-section">
            <h3 class="diag-section-title">智能分析</h3>
            <div class="diag-analysis">
              {{ diagnosticData!.analysis }}
            </div>
          </section>

          <!-- 推荐方案 -->
          <section class="diag-section">
            <h3 class="diag-section-title">推荐方案</h3>
            <div class="diag-recommendation">
              {{ diagnosticData!.recommendation }}
            </div>
          </section>
        </div>
      </div>

      <div class="modal-footer">
        <button 
          v-if="hasData && diagnosticData!.recoverable" 
          class="btn btn-primary" 
          :disabled="recovering"
          @click="handleForceRecover"
        >
          {{ recovering ? '恢复中...' : '强制恢复' }}
        </button>
        <button class="btn btn-secondary" @click="handleClose">关闭</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.75);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 9999;
  padding: 20px;
}

.modal-content {
  background: var(--bg, #0d1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  max-width: 900px;
  width: 100%;
  max-height: 90vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border, #30363d);
}

.modal-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: var(--text, #e6edf3);
}

.modal-close {
  background: none;
  border: none;
  color: var(--muted, #8b949e);
  font-size: 24px;
  cursor: pointer;
  padding: 0;
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
  transition: all 0.2s;
}

.modal-close:hover {
  background: var(--bg-subtle, #161b22);
  color: var(--text, #e6edf3);
}

.modal-body {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.modal-footer {
  display: flex;
  gap: 12px;
  justify-content: flex-end;
  padding: 16px 20px;
  border-top: 1px solid var(--border, #30363d);
}

/* 上下文信息 */
.diag-context {
  display: flex;
  gap: 16px;
  margin-bottom: 20px;
  padding: 12px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}

.diag-context-item {
  display: flex;
  gap: 6px;
  font-size: 13px;
}

.diag-context-label {
  color: var(--muted, #8b949e);
  font-weight: 500;
}

.diag-context-value {
  color: var(--text, #e6edf3);
  font-weight: 600;
}

/* 加载/错误 */
.diag-loading, .diag-error {
  padding: 40px;
  text-align: center;
  color: var(--muted, #8b949e);
}

.diag-error {
  color: var(--danger, #f85149);
}

.spinner {
  width: 40px;
  height: 40px;
  margin: 0 auto 16px;
  border: 3px solid var(--border, #30363d);
  border-top-color: var(--accent, #58a6ff);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 诊断数据 */
.diag-data {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.diag-section {
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  padding: 16px;
}

.diag-section-title {
  margin: 0 0 12px 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text, #e6edf3);
}

.diag-info-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
}

.diag-info-item {
  display: flex;
  gap: 8px;
  font-size: 13px;
}

.diag-info-label {
  color: var(--muted, #8b949e);
  font-weight: 500;
}

.diag-info-value {
  color: var(--text, #e6edf3);
  font-weight: 600;
}

.status-ok {
  color: var(--success, #3fb950) !important;
}

.status-warn {
  color: #d29922 !important;
}

.status-error {
  color: var(--danger, #f85149) !important;
}

/* Bindings 表格 */
.diag-bindings-table {
  overflow-x: auto;
}

.diag-bindings-table table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.diag-bindings-table th {
  text-align: left;
  padding: 8px;
  background: var(--bg, #0d1117);
  color: var(--muted, #8b949e);
  font-weight: 600;
  border-bottom: 1px solid var(--border, #30363d);
}

.diag-bindings-table td {
  padding: 8px;
  color: var(--text, #e6edf3);
  border-bottom: 1px solid var(--border, #30363d);
}

.diag-bindings-table tbody tr:last-child td {
  border-bottom: none;
}

/* 失败列表 */
.diag-failures-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.diag-failure-item {
  padding: 10px;
  background: var(--bg, #0d1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
  font-size: 12px;
}

.diag-failure-header {
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
}

.diag-failure-model {
  font-weight: 600;
  color: var(--text, #e6edf3);
}

.diag-failure-time {
  color: var(--muted, #8b949e);
}

.diag-failure-detail {
  display: flex;
  gap: 12px;
  color: var(--muted, #8b949e);
}

.diag-failure-kind {
  color: var(--danger, #f85149);
  font-weight: 500;
}

.diag-failure-request-id {
  font-family: monospace;
  font-size: 11px;
}

/* 分析和推荐 */
.diag-analysis {
  padding: 12px;
  background: rgba(248, 81, 73, 0.1);
  border-left: 3px solid var(--danger, #f85149);
  border-radius: 4px;
  font-size: 13px;
  color: var(--text, #e6edf3);
  line-height: 1.6;
}

.diag-recommendation {
  padding: 12px;
  background: rgba(88, 166, 255, 0.1);
  border-left: 3px solid var(--accent, #58a6ff);
  border-radius: 4px;
  font-size: 13px;
  color: var(--text, #e6edf3);
  line-height: 1.6;
}

/* 按钮 */
.btn {
  padding: 8px 16px;
  font-size: 14px;
  font-weight: 600;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
}

.btn-primary {
  background: var(--accent, #58a6ff);
  color: #ffffff;
}

.btn-primary:hover:not(:disabled) {
  background: #1f6feb;
}

.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-secondary {
  background: var(--bg-subtle, #161b22);
  color: var(--text, #e6edf3);
  border: 1px solid var(--border, #30363d);
}

.btn-secondary:hover {
  background: var(--bg, #0d1117);
  border-color: var(--muted, #8b949e);
}
</style>
