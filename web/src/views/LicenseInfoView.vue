<script setup lang="ts">
// LicenseInfoView.vue — Read-only license detail view (2026-07-13)
//
// Shows the customer's currently-bound license. Different from
// ActivationWizard which is interactive.

import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import {
  getLicenseInfo,
  sendHeartbeat,
  type CustomerLicenseInfo,
} from '../api/customer'

const { t } = useI18n()

const info = ref<CustomerLicenseInfo | null>(null)
const loading = ref(false)
const heartbeatSending = ref(false)
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
    case 'active': return '已激活'
    case 'grace': return '宽限期'
    case 'expired': return '已过期'
    case 'revoked': return '已吊销'
    default: return '未激活'
  }
})

async function refresh() {
  loading.value = true
  try {
    info.value = await getLicenseInfo()
  } catch (err) {
    ElMessage.error(`查询 License 信息失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
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
  <div class="license-info-view" v-loading="loading">
    <el-card>
      <template #header>
        <div class="header-row">
          <span class="card-title">License 信息</span>
          <div class="header-actions">
            <el-tag :type="stateColor as any" size="large">{{ stateLabel }}</el-tag>
            <el-button @click="refresh" :loading="loading" size="small">刷新</el-button>
          </div>
        </div>
      </template>

      <el-empty v-if="info?.state === 'none'" description="本机尚未激活 License">
        <el-button type="primary" @click="$router.push('/activate')">前往激活向导</el-button>
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
    </el-card>
  </div>
</template>

<style scoped>
.license-info-view {
  max-width: 960px;
  margin: 0 auto;
  padding: 24px;
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
.heartbeat-row {
  margin-top: 16px;
  display: flex;
  gap: 12px;
  align-items: center;
}
.warn { color: #e6a23c; font-weight: 600; }
.muted { color: #909399; font-size: 13px; }
</style>