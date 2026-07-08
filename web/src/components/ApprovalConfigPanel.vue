<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { getApprovalConfig, updateApprovalConfig, type ApprovalConfig } from '../api/approval'
import ApproverManager from './ApproverManager.vue'
import NotificationChannels from './NotificationChannels.vue'
import ApprovalRules from './ApprovalRules.vue'

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

// 使用 computed 确保 i18n 完全初始化后再访问
const modeOptions = computed(() => [
  { value: 'disabled', label: t('sessions.config.modeDisabled'), description: t('sessions.config.modeDisabledDesc') },
  { value: 'automatic', label: t('sessions.config.modeAutomatic'), description: t('sessions.config.modeAutomaticDesc') },
  { value: 'manual', label: t('sessions.config.modeManual'), description: t('sessions.config.modeManualDesc') },
])

const timeoutActionOptions = computed(() => [
  { value: 'approve', label: t('sessions.config.timeoutApprove'), description: t('sessions.config.timeoutApproveDesc') },
  { value: 'reject', label: t('sessions.config.timeoutReject'), description: t('sessions.config.timeoutRejectDesc') },
])

async function loadConfig() {
  loading.value = true
  error.value = null
  try {
    config.value = await getApprovalConfig()
  } catch (e: any) {
    error.value = e.message || t('sessions.config.loadError')
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
    successMessage.value = t('sessions.config.saveSuccess')
    setTimeout(() => {
      successMessage.value = null
    }, 3000)
  } catch (e: any) {
    error.value = e.message || t('sessions.config.saveError')
  } finally {
    saving.value = false
  }
}

function formatTimeout(seconds: number): string {
  if (seconds < 60) return `${seconds} ${t('common.seconds')}`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} ${t('common.minutes')}`
  return `${Math.floor(seconds / 3600)} ${t('common.hours')}`
}

onMounted(() => {
  loadConfig()
})
</script>

<template>
  <div class="approval-config-panel" v-loading="loading">
    <div class="panel-actions">
      <el-button 
        type="primary"
        @click="saveConfig"
        :disabled="saving || loading"
      >
        {{ saving ? t('common.saving') : t('common.save') }}
      </el-button>
    </div>

    <el-alert v-if="error" type="error" :closable="false" style="margin-bottom: 20px;">
      {{ error }}
    </el-alert>
    
    <el-alert v-if="successMessage" type="success" :closable="false" style="margin-bottom: 20px;">
      {{ successMessage }}
    </el-alert>

    <el-card shadow="hover" style="margin-bottom: 20px;">
      <template #header>
        <span>{{ t('sessions.config.basicSettings') }}</span>
      </template>

      <el-form label-width="140px">
        <el-form-item :label="t('sessions.config.approvalMode')">
          <el-select v-model="config.mode" style="width: 300px;">
            <el-option
              v-for="opt in modeOptions"
              :key="opt.value"
              :label="opt.label"
              :value="opt.value"
            >
              <div>
                <div>{{ opt.label }}</div>
                <div style="font-size: 12px; color: #909399;">{{ opt.description }}</div>
              </div>
            </el-option>
          </el-select>
        </el-form-item>

        <el-form-item :label="t('sessions.config.timeout')">
          <el-input-number
            v-model="config.timeout_seconds"
            :min="60"
            :max="86400"
            :step="60"
            style="width: 200px;"
          />
          <span style="margin-left: 12px; color: #909399;">{{ formatTimeout(config.timeout_seconds) }}</span>
        </el-form-item>

        <el-form-item :label="t('sessions.config.timeoutAction')">
          <el-select v-model="config.timeout_action" style="width: 300px;">
            <el-option
              v-for="opt in timeoutActionOptions"
              :key="opt.value"
              :label="opt.label"
              :value="opt.value"
            >
              <div>
                <div>{{ opt.label }}</div>
                <div style="font-size: 12px; color: #909399;">{{ opt.description }}</div>
              </div>
            </el-option>
          </el-select>
        </el-form-item>
      </el-form>
    </el-card>

    <ApproverManager v-model="config.approvers" style="margin-bottom: 20px;" />
    <NotificationChannels v-model="config.notification_channels" style="margin-bottom: 20px;" />
    <ApprovalRules v-model="config.rules" />
  </div>
</template>

<style scoped>
.approval-config-panel {
  position: relative;
}

.panel-actions {
  position: absolute;
  top: 0;
  right: 0;
  z-index: 10;
}

:deep(.el-card__header) {
  padding: 12px 20px;
  font-weight: 500;
}
</style>
