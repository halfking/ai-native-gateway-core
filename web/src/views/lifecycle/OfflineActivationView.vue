<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { maintainLifecycleApi } from '../../api/maintainLifecycle'
import { resolveHardwareHash } from '../../utils/deviceFingerprint'
import { bootstrapApi } from '../../api/bootstrap'

const licenseKey = ref('')
const hardwareHash = ref('')
const deviceName = ref('')
const requestId = ref('')
const status = ref('')
const rejectReason = ref('')
const responseData = ref('')
const loading = ref(false)
const polling = ref(false)
const error = ref('')
const copied = ref(false)
let pollTimer: ReturnType<typeof setInterval> | null = null

const statusLabel: Record<string, string> = {
  pending: '待审批',
  approved: '已通过',
  rejected: '已驳回',
}

async function submit() {
  if (!licenseKey.value.trim() || !hardwareHash.value.trim()) {
    error.value = '请填写 License Key 与硬件标识。'
    return
  }
  loading.value = true
  error.value = ''
  try {
    const req = await maintainLifecycleApi.submitOfflineRequest({
      license_key: licenseKey.value.trim(),
      hardware_hash: hardwareHash.value.trim(),
      device_name: deviceName.value.trim() || undefined,
    })
    requestId.value = req.request_id
    status.value = req.status
    startPolling()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function checkStatus() {
  if (!requestId.value) return
  try {
    const req = await maintainLifecycleApi.offlineRequestStatus(requestId.value)
    status.value = req.status
    rejectReason.value = req.reject_reason || ''
    if (req.status === 'approved') {
      const resp = await maintainLifecycleApi.offlineResponse(requestId.value)
      responseData.value = JSON.stringify(resp, null, 2)
      stopPolling()
    } else if (req.status === 'rejected') {
      stopPolling()
    }
  } catch {
    // keep polling
  }
}

function startPolling() {
  if (pollTimer) return
  polling.value = true
  pollTimer = setInterval(checkStatus, 3000)
}

function stopPolling() {
  polling.value = false
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

async function copyResponse() {
  if (!responseData.value) return
  await navigator.clipboard.writeText(responseData.value)
  copied.value = true
  setTimeout(() => { copied.value = false }, 2000)
}

onBeforeUnmount(stopPolling)

onMounted(async () => {
  try {
    const fp = await bootstrapApi.fingerprint()
    hardwareHash.value = await resolveHardwareHash(fp.hardware_hash)
  } catch {
    const client = await import('../../utils/deviceFingerprint').then((m) => m.collectClientFingerprint())
    hardwareHash.value = client.hardware_hash
  }
})
</script>

<template>
  <div class="lifecycle-page">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">OFFLINE ACTIVATION</p>
        <h1>离线激活</h1>
        <p>提交离线请求，审批通过后复制响应到隔离环境完成激活。</p>
      </div>
      <RouterLink class="btn btn-ghost" to="/customer/update-activate">返回更新与激活</RouterLink>
    </header>

    <el-alert v-if="error" type="error" :title="error" show-icon class="mb" closable @close="error = ''" />

    <div class="lifecycle-grid">
      <el-card shadow="never">
        <template #header>提交请求</template>
        <el-form label-position="top">
          <el-form-item label="License Key">
            <el-input v-model="licenseKey" />
          </el-form-item>
          <el-form-item label="硬件标识">
            <el-input v-model="hardwareHash" />
          </el-form-item>
          <el-form-item label="设备名称（可选）">
            <el-input v-model="deviceName" />
          </el-form-item>
          <el-button type="primary" :loading="loading" @click="submit">提交请求</el-button>
        </el-form>
      </el-card>

      <el-card shadow="never">
        <template #header>请求状态</template>
        <el-empty v-if="!requestId" description="尚未提交请求" />
        <template v-else>
          <p class="mono">{{ requestId }}</p>
          <p>
            <strong>{{ statusLabel[status] || status || '待审批' }}</strong>
            <el-tag v-if="polling" size="small" class="ml">轮询中</el-tag>
          </p>
          <p v-if="rejectReason" class="error">驳回原因：{{ rejectReason }}</p>
          <el-button @click="checkStatus">立即刷新</el-button>
        </template>
      </el-card>

      <el-card shadow="never">
        <template #header>激活响应</template>
        <el-empty v-if="!responseData" description="审批通过后显示响应" />
        <template v-else>
          <pre class="response">{{ responseData }}</pre>
          <el-button type="primary" @click="copyResponse">{{ copied ? '已复制' : '复制响应' }}</el-button>
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
.lifecycle-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 16px;
}
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; word-break: break-all; }
.ml { margin-left: 8px; }
.mb { margin-bottom: 14px; }
.error { color: #c2413b; }
.response {
  max-height: 240px;
  overflow: auto;
  background: #152033;
  color: #dbe4f0;
  padding: 12px;
  border-radius: 8px;
  font-size: 12px;
}
</style>
