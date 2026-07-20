<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  licenseStateLabel,
  maintainLifecycleApi,
  type LicenseStatus,
} from '../../api/maintainLifecycle'
import { bootstrapApi } from '../../api/bootstrap'
import { ensureInstanceId, resolveHardwareHash } from '../../utils/deviceFingerprint'

const instanceId = ref(ensureInstanceId())
const status = ref<LicenseStatus | null>(null)
const loading = ref(false)
const error = ref('')

async function load() {
  if (!instanceId.value.trim()) {
    error.value = '请先填写实例 ID。'
    status.value = null
    return
  }
  loading.value = true
  error.value = ''
  localStorage.setItem('maintain_instance_id', instanceId.value.trim())
  localStorage.setItem('llmgw_instance_id', instanceId.value.trim())
  try {
    let serverHash = ''
    try {
      const fp = await bootstrapApi.fingerprint()
      serverHash = fp.hardware_hash || ''
    } catch { /* client fallback */ }
    await resolveHardwareHash(serverHash)

    const boot = await bootstrapApi.status(instanceId.value.trim())
    if (boot.activated) {
      status.value = {
        state: 'active',
        license_key: boot.license_key,
      }
      return
    }

    try {
      status.value = await maintainLifecycleApi.licenseStatus(instanceId.value.trim())
    } catch {
      status.value = { state: 'none' }
    }
  } catch (e) {
    error.value = (e as Error).message
    status.value = null
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="lifecycle-page">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">LICENSE STATUS</p>
        <h1>License 状态</h1>
        <p>查看当前实例的授权状态、等级与有效期。</p>
      </div>
      <div class="lifecycle-page__actions">
        <RouterLink class="btn btn-primary btn-sm" to="/customer/activate">去激活</RouterLink>
        <RouterLink class="btn btn-ghost btn-sm" to="/customer/site">站点信息</RouterLink>
        <el-button :loading="loading" @click="load">刷新</el-button>
      </div>
    </header>

    <div class="lifecycle-grid">
      <el-card shadow="never">
        <template #header>实例查询</template>
        <el-form label-position="top" @submit.prevent="load">
          <el-form-item label="实例 ID">
            <el-input v-model="instanceId" placeholder="gw-prod-01" @keyup.enter="load" />
          </el-form-item>
          <el-button type="primary" :loading="loading" @click="load">查询状态</el-button>
        </el-form>
      </el-card>

      <el-card shadow="never">
        <template #header>授权详情</template>
        <el-skeleton v-if="loading" :rows="4" animated />
        <el-alert v-else-if="error" type="error" :title="error" show-icon :closable="false" />
        <el-empty v-else-if="!status || status.state === 'none'" description="未找到有效 License">
          <RouterLink class="btn btn-primary btn-sm" to="/customer/activate">去激活</RouterLink>
        </el-empty>
        <template v-else>
          <p>
            <strong>{{ licenseStateLabel(status.state) }}</strong>
            <el-tag v-if="status.subscription_tier" size="small" class="ml">{{ status.subscription_tier }}</el-tag>
          </p>
          <el-descriptions :column="1" size="small" class="mt">
            <el-descriptions-item v-if="status.license_key" label="License">{{ status.license_key }}</el-descriptions-item>
            <el-descriptions-item v-if="status.customer_name" label="客户">{{ status.customer_name }}</el-descriptions-item>
            <el-descriptions-item v-if="status.device_name" label="设备">{{ status.device_name }}</el-descriptions-item>
            <el-descriptions-item v-if="status.expires_at" label="有效期">{{ new Date(status.expires_at).toLocaleString() }}</el-descriptions-item>
            <el-descriptions-item v-if="status.last_heartbeat" label="心跳">{{ new Date(status.last_heartbeat).toLocaleString() }}</el-descriptions-item>
          </el-descriptions>
        </template>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.lifecycle-page { padding: 24px clamp(16px, 3vw, 32px) 40px; max-width: 1100px; }
.lifecycle-page__header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}
.eyebrow {
  margin: 0 0 6px;
  color: #1e4fd6;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
}
.lifecycle-page__header h1 { margin: 0 0 8px; font-size: 24px; }
.lifecycle-page__header p { margin: 0; color: #5b6b82; font-size: 14px; }
.lifecycle-page__actions { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.lifecycle-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
}
.ml { margin-left: 8px; }
.mt { margin-top: 12px; }
</style>
