<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// 压缩策略配置（示例，复用现有CompressionView逻辑）
const strategies = ref([
  { name: 'sliding_window_token', enabled: true, description: t('sessions.config.strategyToken') },
  { name: 'llm_summary', enabled: false, description: t('sessions.config.strategySummary') },
  { name: 'memora_l1_inject', enabled: true, description: t('sessions.config.strategyMemora') },
])
</script>

<template>
  <div class="compression-config-panel">
    <el-alert
      type="info"
      :closable="false"
      show-icon
      style="margin-bottom: 20px;"
    >
      <template #title>
        {{ t('sessions.config.compressionInfo') }}
      </template>
    </el-alert>

    <el-card shadow="hover">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>{{ t('sessions.config.compressionStrategies') }}</span>
          <el-button type="text" @click="$router.push('/compression')">
            {{ t('sessions.config.viewDetails') }} →
          </el-button>
        </div>
      </template>

      <el-table :data="strategies" style="width: 100%">
        <el-table-column prop="name" :label="t('sessions.config.strategyName')" width="200" />
        <el-table-column prop="description" :label="t('sessions.config.strategyDescription')" />
        <el-table-column :label="t('sessions.config.status')" width="100" align="center">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? t('sessions.config.enabled') : t('sessions.config.disabled') }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>

      <div style="margin-top: 20px; padding: 16px; background: #f5f7fa; border-radius: 4px;">
        <p style="margin: 0; font-size: 14px; color: #606266;">
          💡 {{ t('sessions.config.compressionTip') }}
        </p>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.compression-config-panel {
  max-width: 900px;
}

:deep(.el-card__header) {
  padding: 12px 20px;
  font-weight: 500;
}
</style>
