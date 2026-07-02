<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { getApprovalConfig, updateApprovalConfig, type ApprovalConfig } from '../api/approval'
import ApproverManager from '../components/ApproverManager.vue'
import NotificationChannels from '../components/NotificationChannels.vue'
import ApprovalRules from '../components/ApprovalRules.vue'

const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const successMessage = ref<string | null>(null)

const config = ref<ApprovalConfig>({
  enabled: false,
  mode: 'manual',
  timeout_seconds: 3600,
  timeout_action: 'reject',
  approvers: [],
  notification_channels: {},
  rules: [],
})

const modeOptions = [
  { value: 'disabled', label: '禁用', description: '完全关闭审批功能' },
  { value: 'automatic', label: '自动审批', description: '根据规则自动处理' },
  { value: 'manual', label: '人工审批', description: '需要审批人手动审批' },
]

const timeoutActionOptions = [
  { value: 'approve', label: '自动通过', description: '超时后自动批准请求' },
  { value: 'reject', label: '自动拒绝', description: '超时后自动拒绝请求' },
]

async function loadConfig() {
  loading.value = true
  error.value = null
  try {
    config.value = await getApprovalConfig()
  } catch (e: any) {
    error.value = e.message || '加载配置失败'
  } finally {
    loading.value = false
  }
}

async function saveConfig() {
  saving.value = true
  error.value = null
  successMessage.value = null
  
  try {
    await updateApprovalConfig(config.value)
    successMessage.value = '保存成功'
    setTimeout(() => {
      successMessage.value = null
    }, 3000)
  } catch (e: any) {
    error.value = e.message || '保存失败'
  } finally {
    saving.value = false
  }
}

function formatTimeout(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`
  return `${Math.floor(seconds / 3600)} 小时`
}

onMounted(() => {
  loadConfig()
})
</script>

<template>
  <div class="approval-config-view">
    <!-- Header -->
    <div class="page-header">
      <div>
        <h1>审批配置</h1>
        <p class="page-description">配置审批流程、审批人和通知渠道</p>
      </div>
      <button 
        class="btn btn-primary btn-large"
        @click="saveConfig"
        :disabled="saving || loading"
      >
        {{ saving ? '保存中...' : '💾 保存配置' }}
      </button>
    </div>

    <!-- Messages -->
    <div v-if="error" class="message message-error">
      <span class="message-icon">❌</span>
      {{ error }}
    </div>
    
    <div v-if="successMessage" class="message message-success">
      <span class="message-icon">✅</span>
      {{ successMessage }}
    </div>

    <!-- Loading -->
    <div v-if="loading" class="loading-container">
      <div class="loading-spinner">加载中...</div>
    </div>

    <!-- Content -->
    <div v-else class="content">
      <!-- Basic Settings -->
      <section class="section">
        <div class="section-header">
          <h2>基本设置</h2>
        </div>
        
        <div class="settings-grid">
          <div class="setting-item">
            <div class="setting-label">
              <span>启用审批流程</span>
              <span class="setting-hint">开启后，符合规则的请求将进入审批流程</span>
            </div>
            <label class="switch-label">
              <input 
                type="checkbox"
                v-model="config.enabled"
                class="switch-input"
              />
              <span class="switch-track"></span>
              <span class="switch-text">{{ config.enabled ? '已启用' : '已禁用' }}</span>
            </label>
          </div>

          <div class="setting-item">
            <div class="setting-label">
              <span>审批模式</span>
              <span class="setting-hint">选择审批的工作模式</span>
            </div>
            <select v-model="config.mode" class="form-select">
              <option v-for="opt in modeOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }} - {{ opt.description }}
              </option>
            </select>
          </div>

          <div class="setting-item">
            <div class="setting-label">
              <span>审批超时时间</span>
              <span class="setting-hint">当前设置: {{ formatTimeout(config.timeout_seconds) }}</span>
            </div>
            <div class="timeout-input-group">
              <input 
                type="number"
                v-model.number="config.timeout_seconds"
                class="form-input"
                min="60"
                max="86400"
                step="60"
              />
              <span class="input-suffix">秒</span>
            </div>
          </div>

          <div class="setting-item">
            <div class="setting-label">
              <span>超时后行为</span>
              <span class="setting-hint">审批超时后的处理方式</span>
            </div>
            <select v-model="config.timeout_action" class="form-select">
              <option v-for="opt in timeoutActionOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }} - {{ opt.description }}
              </option>
            </select>
          </div>
        </div>
      </section>

      <!-- Approvers -->
      <section class="section">
        <div class="section-header">
          <h2>审批人管理</h2>
          <p class="section-description">配置审批人员及其优先级</p>
        </div>
        <ApproverManager v-model="config.approvers" />
      </section>

      <!-- Notification Channels -->
      <section class="section">
        <div class="section-header">
          <h2>通知渠道</h2>
          <p class="section-description">配置审批通知的发送渠道</p>
        </div>
        <NotificationChannels v-model="config.notification_channels" />
      </section>

      <!-- Rules -->
      <section class="section">
        <div class="section-header">
          <h2>审批规则</h2>
          <p class="section-description">定义哪些请求需要审批</p>
        </div>
        <ApprovalRules v-model="config.rules" />
      </section>

      <!-- Save Button (Bottom) -->
      <div class="bottom-actions">
        <button 
          class="btn btn-primary btn-large"
          @click="saveConfig"
          :disabled="saving || loading"
        >
          {{ saving ? '保存中...' : '💾 保存配置' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.approval-config-view {
  padding: 20px;
  max-width: 1400px;
  margin: 0 auto;
  color: var(--text-primary, #e6edf3);
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0 0 8px;
  font-size: 28px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

.page-description {
  margin: 0;
  font-size: 14px;
  color: var(--text-secondary, #8b949e);
}

.message {
  padding: 12px 16px;
  border-radius: 6px;
  margin-bottom: 16px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
}

.message-icon {
  font-size: 16px;
}

.message-error {
  background: rgba(248, 113, 113, 0.1);
  border: 1px solid rgba(248, 113, 113, 0.3);
  color: #f87171;
}

.message-success {
  background: rgba(52, 211, 153, 0.1);
  border: 1px solid rgba(52, 211, 153, 0.3);
  color: #34d399;
}

.loading-container {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 64px;
}

.loading-spinner {
  font-size: 16px;
  color: var(--text-secondary, #8b949e);
}

.content {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.section {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  padding: 20px;
}

.section-header {
  margin-bottom: 16px;
}

.section-header h2 {
  margin: 0 0 4px;
  font-size: 18px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

.section-description {
  margin: 0;
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
}

.settings-grid {
  display: grid;
  gap: 20px;
}

.setting-item {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 16px;
  align-items: center;
  padding: 16px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}

.setting-label {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.setting-label > span:first-child {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary, #e6edf3);
}

.setting-hint {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
}

.switch-label {
  display: flex;
  align-items: center;
  gap: 12px;
  cursor: pointer;
  user-select: none;
}

.switch-input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}

.switch-track {
  position: relative;
  width: 44px;
  height: 24px;
  background: var(--border, #30363d);
  border-radius: 12px;
  transition: background 0.2s;
  flex-shrink: 0;
}

.switch-track::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 20px;
  height: 20px;
  background: white;
  border-radius: 50%;
  transition: transform 0.2s;
}

.switch-input:checked + .switch-track {
  background: var(--accent, #6366f1);
}

.switch-input:checked + .switch-track::after {
  transform: translateX(20px);
}

.switch-text {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary, #e6edf3);
  min-width: 60px;
}

.form-select,
.form-input {
  padding: 8px 12px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 14px;
  min-width: 280px;
}

.form-select:focus,
.form-input:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}

.timeout-input-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.timeout-input-group .form-input {
  min-width: 120px;
}

.input-suffix {
  font-size: 14px;
  color: var(--text-secondary, #8b949e);
}

.bottom-actions {
  display: flex;
  justify-content: center;
  padding: 20px 0;
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

.btn-large {
  padding: 12px 24px;
  font-size: 15px;
}

.btn-primary {
  background: var(--accent, #6366f1);
  color: #fff;
}

.btn-primary:hover:not(:disabled) {
  background: #5558e3;
}

.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    gap: 16px;
  }

  .setting-item {
    grid-template-columns: 1fr;
  }

  .form-select,
  .form-input {
    width: 100%;
    min-width: 0;
  }
}
</style>
