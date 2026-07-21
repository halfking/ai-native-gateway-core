<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getApprovalConfig, updateApprovalConfig, type ApprovalConfig } from '../api/approval'
import ApproverManager from '../components/ApproverManager.vue'
import NotificationChannels from '../components/NotificationChannels.vue'
import ApprovalRules from '../components/ApprovalRules.vue'

const { t } = useI18n()
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

const modeOptions = computed(() => [
  { value: 'disabled', label: t('approval.config.mode.disabled'), description: t('approval.config.mode.disabledDesc') },
  { value: 'automatic', label: t('approval.config.mode.automatic'), description: t('approval.config.mode.automaticDesc') },
  { value: 'manual', label: t('approval.config.mode.manual'), description: t('approval.config.mode.manualDesc') },
])

const timeoutActionOptions = computed(() => [
  { value: 'approve', label: t('approval.config.timeoutAction.approve'), description: t('approval.config.timeoutAction.approveDesc') },
  { value: 'reject', label: t('approval.config.timeoutAction.reject'), description: t('approval.config.timeoutAction.rejectDesc') },
])

async function loadConfig() {
  loading.value = true
  error.value = null
  try {
    const loadedConfig = await getApprovalConfig()
    // Ensure notification_channels is always an object (2026-07-08: fix undefined access)
    config.value = {
      ...loadedConfig,
      notification_channels: loadedConfig.notification_channels || {},
      rules: loadedConfig.rules || [],
      approvers: loadedConfig.approvers || [],
    }
  } catch (e: any) {
    error.value = e.message || t('approval.config.errors.loadFailed')
    console.error('Failed to load approval config:', e)
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
    successMessage.value = t('approval.config.success.saved')
    setTimeout(() => {
      successMessage.value = null
    }, 3000)
  } catch (e: any) {
    error.value = e.message || t('approval.config.errors.saveFailed')
  } finally {
    saving.value = false
  }
}

function formatTimeout(seconds: number): string {
  if (seconds < 60) return t('approval.config.format.seconds', { n: seconds })
  if (seconds < 3600) return t('approval.config.format.minutes', { n: Math.floor(seconds / 60) })
  return t('approval.config.format.hours', { n: Math.floor(seconds / 3600) })
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
        <h1>{{ t('approval.config.title') }}</h1>
        <p class="page-description">{{ t('approval.config.description') }}</p>
      </div>
      <button 
        class="btn btn-primary btn-large"
        @click="saveConfig"
        :disabled="saving || loading"
      >
        {{ saving ? t('approval.config.saving') : t('approval.config.save') }}
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
      <div class="loading-spinner">{{ t('approval.config.loading') }}</div>
    </div>

    <!-- Content -->
    <div v-else class="content">
      <!-- Basic Settings -->
      <section class="section">
        <div class="section-header">
          <h2>{{ t('approval.config.sections.basic') }}</h2>
        </div>
        
        <div class="settings-grid">
          <div class="setting-item">
            <div class="setting-label">
              <span>{{ t('approval.config.enabled.label') }}</span>
              <span class="setting-hint">{{ t('approval.config.enabled.hint') }}</span>
            </div>
            <label class="switch-label">
              <input 
                type="checkbox"
                v-model="config.enabled"
                class="switch-input"
              />
              <span class="switch-track"></span>
              <span class="switch-text">{{ config.enabled ? t('approval.config.enabled.on') : t('approval.config.enabled.off') }}</span>
            </label>
          </div>

          <div class="setting-item">
            <div class="setting-label">
              <span>{{ t('approval.config.mode.label') }}</span>
              <span class="setting-hint">{{ t('approval.config.mode.hint') }}</span>
            </div>
            <select v-model="config.mode" class="form-select">
              <option v-for="opt in modeOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }} - {{ opt.description }}
              </option>
            </select>
          </div>

          <div class="setting-item">
            <div class="setting-label">
              <span>{{ t('approval.config.timeout.label') }}</span>
              <span class="setting-hint">{{ t('approval.config.timeout.hint', { value: formatTimeout(config.timeout_seconds) }) }}</span>
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
              <span class="input-suffix">{{ t('approval.config.timeout.suffix') }}</span>
            </div>
          </div>

          <div class="setting-item">
            <div class="setting-label">
              <span>{{ t('approval.config.timeoutAction.label') }}</span>
              <span class="setting-hint">{{ t('approval.config.timeoutAction.hint') }}</span>
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
          <h2>{{ t('approval.config.sections.approvers') }}</h2>
          <p class="section-description">{{ t('approval.config.sectionsDesc.approvers') }}</p>
        </div>
        <ApproverManager v-model="config.approvers" />
      </section>

      <!-- Notification Channels -->
      <section class="section">
        <div class="section-header">
          <h2>{{ t('approval.config.sections.channels') }}</h2>
          <p class="section-description">{{ t('approval.config.sectionsDesc.channels') }}</p>
        </div>
        <NotificationChannels v-model="config.notification_channels" />
      </section>

      <!-- Rules -->
      <section class="section">
        <div class="section-header">
          <h2>{{ t('approval.config.sections.rules') }}</h2>
          <p class="section-description">{{ t('approval.config.sectionsDesc.rules') }}</p>
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
          {{ saving ? t('approval.config.saving') : t('approval.config.save') }}
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
  color: var(--text-primary);
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
  color: var(--text-primary);
}

.page-description {
  margin: 0;
  font-size: 14px;
  color: var(--text-secondary);
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
  color: var(--text-secondary);
}

.content {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.section {
  background: var(--bg-card);
  border: 1px solid var(--border);
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
  color: var(--text-primary);
}

.section-description {
  margin: 0;
  font-size: 13px;
  color: var(--text-secondary);
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
  background: var(--bg);
  border: 1px solid var(--border);
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
  color: var(--text-primary);
}

.setting-hint {
  font-size: 12px;
  color: var(--text-secondary);
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
  background: var(--border);
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
  background: var(--accent);
}

.switch-input:checked + .switch-track::after {
  transform: translateX(20px);
}

.switch-text {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
  min-width: 60px;
}

.form-select,
.form-input {
  padding: 8px 12px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text-primary);
  font-size: 14px;
  min-width: 280px;
}

.form-select:focus,
.form-input:focus {
  outline: none;
  border-color: var(--accent);
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
  color: var(--text-secondary);
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
  background: var(--accent);
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
