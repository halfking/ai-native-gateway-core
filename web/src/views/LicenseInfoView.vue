<script setup lang="ts">
// LicenseInfoView.vue — Read-only license detail view (2026-07-13)
//
// Shows the customer's currently-bound license. Different from
// ActivationWizard which is interactive.

import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PublicPortalLayout from '../components/PublicPortalLayout.vue'
import {
  getLicenseInfo,
  getRuntimeTelemetryPreference,
  sendHeartbeat,
  setRuntimeTelemetryPreference,
  type CustomerLicenseInfo,
  type RuntimeTelemetryPreference,
} from '../api/customer'

const { t } = useI18n()

const info = ref<CustomerLicenseInfo | null>(null)
const loading = ref(false)
const heartbeatSending = ref(false)
const telemetryPreference = ref<RuntimeTelemetryPreference | null>(null)
const telemetrySaving = ref(false)
const telemetryAvailable = ref(false)
let heartbeatTimer: ReturnType<typeof setInterval> | null = null

const stateColor = computed(() => {
  switch (info.value?.state) {
    case 'active': return 'success'
    case 'grace': return 'warning'
    case 'expired':
    case 'revoked': return 'danger'
    default: return 'info'
  }
})

const stateLabel = computed(() => {
  switch (info.value?.state) {
    case 'active': return t('customer.info.states.active')
    case 'grace': return t('customer.info.states.grace')
    case 'expired': return t('customer.info.states.expired')
    case 'revoked': return t('customer.info.states.revoked')
    default: return t('customer.info.states.none')
  }
})

async function refresh() {
  loading.value = true
  try {
    info.value = await getLicenseInfo()
    try {
      telemetryPreference.value = await getRuntimeTelemetryPreference()
      telemetryAvailable.value = true
    } catch {
      telemetryPreference.value = null
      telemetryAvailable.value = false
    }
  } catch (err) {
    ElMessage.error(t('customer.info.loadFailed', { msg: (err as Error).message }))
  } finally {
    loading.value = false
  }
}

async function updateTelemetryPreference(enabled: boolean) {
  telemetrySaving.value = true
  try {
    telemetryPreference.value = await setRuntimeTelemetryPreference(enabled)
    ElMessage.success(enabled ? '已启用运行状态统计' : '已停止运行状态统计')
  } catch (err) {
    ElMessage.error(`更新运行状态统计授权失败: ${(err as Error).message}`)
  } finally {
    telemetrySaving.value = false
  }
}

async function manualHeartbeat() {
  heartbeatSending.value = true
  try {
    await sendHeartbeat()
    ElMessage.success('心跳已发送')
    await refresh()
  } catch (err) {
    ElMessage.error(`心跳发送失败: ${(err as Error).message}`)
  } finally {
    heartbeatSending.value = false
  }
}

function startHeartbeatTimer() {
  if (heartbeatTimer) return
  heartbeatTimer = setInterval(() => {
    if (info.value?.state === 'active' || info.value?.state === 'grace') {
      manualHeartbeat()
    }
  }, 6 * 60 * 60 * 1000)
}

onMounted(async () => {
  await refresh()
  startHeartbeatTimer()
})

onBeforeUnmount(() => {
  if (heartbeatTimer) {
    clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
})
</script>

<template>
  <PublicPortalLayout
    :title="t('customer.info.title')"
    :subtitle="t('public.license.subtitle')"
    :kicker="t('public.layout.offline')"
  >
    <div v-loading="loading" class="license-info-view">
      <el-card shadow="never" class="pub-card">
        <template #header>
          <div class="header-row">
            <span class="card-title">{{ t('customer.info.title') }}</span>
            <div class="header-actions">
              <el-tag :type="stateColor as any" size="large">{{ stateLabel }}</el-tag>
              <el-button @click="refresh" :loading="loading" size="small">{{ t('customer.info.refresh') }}</el-button>
            </div>
          </div>
        </template>

      <el-alert
        v-if="info?.state === 'expired'"
        type="error"
        :closable="false"
        show-icon
        class="state-banner"
        :title="t('customer.info.expiredBanner')"
      >
        <template #default>
          <el-button type="primary" size="small" @click="$router.push('/activate')">
            {{ t('customer.info.reactivate') }}
          </el-button>
        </template>
      </el-alert>

      <el-alert
        v-if="info?.state === 'revoked'"
        type="error"
        :closable="false"
        show-icon
        class="state-banner"
        :title="t('customer.info.revokedBanner')"
      >
        <template #default>
          <el-button type="primary" size="small" @click="$router.push('/activate')">
            {{ t('customer.info.reactivate') }}
          </el-button>
        </template>
      </el-alert>

      <el-empty v-if="info?.state === 'none'" :description="t('customer.info.notActivated')">
        <el-button type="primary" @click="$router.push('/activate')">{{ t('customer.info.gotoActivate') }}</el-button>
      </el-empty>

      <el-descriptions v-else :column="2" border>
        <el-descriptions-item label="客户名称">
          {{ info?.customer_name || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="客户邮箱">
          {{ info?.customer_email || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="License Key">
          <code>{{ info?.license_key || '—' }}</code>
        </el-descriptions-item>
        <el-descriptions-item label="订阅层级">
          {{ info?.subscription_tier || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="到期时间">
          {{ info?.expires_at || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="剩余天数">
          <span :class="{ 'warn': (info?.days_remaining ?? 30) < 7 }">
            {{ info?.days_remaining ?? '—' }}
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="最大设备数">
          {{ info?.max_devices ?? '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="已激活设备数">
          {{ info?.active_devices ?? 0 }}
        </el-descriptions-item>
        <el-descriptions-item label="激活时间">
          {{ info?.activated_at || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="上次心跳">
          {{ info?.last_heartbeat || '—' }}
        </el-descriptions-item>
        <el-descriptions-item label="功能特性" :span="2">
          <el-tag
            v-for="f in info?.features || []"
            :key="f"
            type="info"
            effect="plain"
            style="margin-right: 6px;"
          >
            {{ f }}
          </el-tag>
          <span v-if="!info?.features?.length" class="muted">—</span>
        </el-descriptions-item>
        <el-descriptions-item label="运行模式">
          <el-tag size="small">{{ info?.mode || '—' }}</el-tag>
        </el-descriptions-item>
      </el-descriptions>

      <div class="heartbeat-row" v-if="info?.state === 'active' || info?.state === 'grace'">
        <el-button
          type="primary"
          :loading="heartbeatSending"
          @click="manualHeartbeat"
        >
          立即发送心跳
        </el-button>
        <span class="muted">
          系统会每 6 小时自动发送一次心跳；您也可以手动触发。
        </span>
      </div>

      <section v-if="telemetryAvailable && (info?.state === 'active' || info?.state === 'grace')" class="telemetry-preference">
        <div>
          <h3>运行状态统计</h3>
          <p>
            仅采集软件版本、资源使用、服务可用性及聚合请求指标，用于可靠性与产品改良；
            不采集原始 AI 会话、提示词、模型回复、文件或凭据。
          </p>
        </div>
        <el-switch
          :model-value="telemetryPreference?.enabled ?? false"
          :loading="telemetrySaving"
          active-text="已启用"
          inactive-text="未启用"
          @update:model-value="updateTelemetryPreference"
        />
      </section>
      </el-card>
    </div>
  </PublicPortalLayout>
</template>

<style scoped>
.license-info-view {
  width: 100%;
}
.header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.header-actions {
  display: flex;
  gap: 12px;
  align-items: center;
}
.card-title {
  font-weight: 600;
}
.state-banner { margin-bottom: 16px; }
.heartbeat-row {
  margin-top: 16px;
  display: flex;
  gap: 12px;
  align-items: center;
}
.warn { color: #e6a23c; font-weight: 600; }
.muted { color: #909399; font-size: 13px; }
.telemetry-preference {
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid var(--el-border-color-lighter);
  display: flex;
  justify-content: space-between;
  gap: 24px;
  align-items: center;
}
.telemetry-preference h3 { margin: 0 0 4px; font-size: 15px; }
.telemetry-preference p { margin: 0 0 4px; color: #606266; font-size: 13px; max-width: 680px; }
@media (max-width: 640px) {
  .telemetry-preference { align-items: flex-start; flex-direction: column; gap: 12px; }
}
</style>
