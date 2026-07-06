<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// 健康评分配置（默认值，暂无后端API）
const config = ref({
  error_ended_penalty: 30,
  abandoned_penalty: 15,
  per_error_penalty: 3,
  per_error_cap: 30,
  per_compliance_penalty: 10,
  per_compliance_cap: 30,
  high_latency_threshold_ms: 5000,
  high_latency_penalty: 15,
  model_switch_threshold: 3,
  model_switch_penalty: 10,
  prompt_injection_penalty: 20,
  pii_penalty: 15,
  toxic_output_penalty: 15,
  sensitive_penalty_cap: 30,
})

const showInfo = ref(false)
</script>

<template>
  <div class="health-score-config-panel">
    <el-alert
      type="info"
      :closable="false"
      show-icon
      style="margin-bottom: 20px;"
    >
      <template #title>
        {{ t('sessions.config.healthInfo') }}
      </template>
    </el-alert>

    <el-card shadow="hover" style="margin-bottom: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>{{ t('sessions.config.outcomeRules') }}</span>
          <el-button type="primary" size="small" disabled>
            {{ t('common.save') }} ({{ t('sessions.config.comingSoon') }})
          </el-button>
        </div>
      </template>

      <el-form label-width="200px">
        <el-form-item :label="t('sessions.config.errorEndedPenalty')">
          <el-input-number
            v-model="config.error_ended_penalty"
            :min="0"
            :max="100"
            disabled
          />
          <span class="form-hint">{{ t('sessions.config.errorEndedHint') }}</span>
        </el-form-item>

        <el-form-item :label="t('sessions.config.abandonedPenalty')">
          <el-input-number
            v-model="config.abandoned_penalty"
            :min="0"
            :max="100"
            disabled
          />
          <span class="form-hint">{{ t('sessions.config.abandonedHint') }}</span>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card shadow="hover" style="margin-bottom: 20px;">
      <template #header>
        <span>{{ t('sessions.config.errorRules') }}</span>
      </template>

      <el-form label-width="200px">
        <el-form-item :label="t('sessions.config.perErrorPenalty')">
          <el-input-number
            v-model="config.per_error_penalty"
            :min="0"
            :max="50"
            disabled
          />
          <span class="form-hint">{{ t('sessions.config.perErrorHint') }}</span>
        </el-form-item>

        <el-form-item :label="t('sessions.config.perErrorCap')">
          <el-input-number
            v-model="config.per_error_cap"
            :min="0"
            :max="100"
            disabled
          />
        </el-form-item>
      </el-form>
    </el-card>

    <el-card shadow="hover" style="margin-bottom: 20px;">
      <template #header>
        <span>{{ t('sessions.config.performanceRules') }}</span>
      </template>

      <el-form label-width="200px">
        <el-form-item :label="t('sessions.config.highLatencyThreshold')">
          <el-input-number
            v-model="config.high_latency_threshold_ms"
            :min="1000"
            :max="60000"
            :step="1000"
            disabled
          />
          <span class="form-hint">ms</span>
        </el-form-item>

        <el-form-item :label="t('sessions.config.highLatencyPenalty')">
          <el-input-number
            v-model="config.high_latency_penalty"
            :min="0"
            :max="50"
            disabled
          />
        </el-form-item>

        <el-form-item :label="t('sessions.config.modelSwitchThreshold')">
          <el-input-number
            v-model="config.model_switch_threshold"
            :min="1"
            :max="20"
            disabled
          />
        </el-form-item>

        <el-form-item :label="t('sessions.config.modelSwitchPenalty')">
          <el-input-number
            v-model="config.model_switch_penalty"
            :min="0"
            :max="50"
            disabled
          />
        </el-form-item>
      </el-form>
    </el-card>

    <el-card shadow="hover">
      <template #header>
        <span>{{ t('sessions.config.securityRules') }}</span>
      </template>

      <el-form label-width="200px">
        <el-form-item :label="t('sessions.config.promptInjectionPenalty')">
          <el-input-number
            v-model="config.prompt_injection_penalty"
            :min="0"
            :max="100"
            disabled
          />
        </el-form-item>

        <el-form-item :label="t('sessions.config.piiPenalty')">
          <el-input-number
            v-model="config.pii_penalty"
            :min="0"
            :max="100"
            disabled
          />
        </el-form-item>

        <el-form-item :label="t('sessions.config.toxicOutputPenalty')">
          <el-input-number
            v-model="config.toxic_output_penalty"
            :min="0"
            :max="100"
            disabled
          />
        </el-form-item>

        <el-form-item :label="t('sessions.config.sensitivePenaltyCap')">
          <el-input-number
            v-model="config.sensitive_penalty_cap"
            :min="0"
            :max="100"
            disabled
          />
          <span class="form-hint">{{ t('sessions.config.sensitivePenaltyCapHint') }}</span>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.health-score-config-panel {
  max-width: 900px;
}

.form-hint {
  margin-left: 12px;
  font-size: 12px;
  color: #909399;
}

:deep(.el-form-item) {
  margin-bottom: 20px;
}

:deep(.el-card__header) {
  padding: 12px 20px;
  font-weight: 500;
}
</style>
