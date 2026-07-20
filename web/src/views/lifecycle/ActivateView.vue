<script setup lang="ts">
import { onMounted, ref } from 'vue'
import OperationAgreementDialog from '../../components/lifecycle/OperationAgreementDialog.vue'
import { bootstrapApi, markBootstrapActivated } from '../../api/bootstrap'
import {
  licenseStateLabel,
  maintainLifecycleApi,
  type LicenseStatus,
} from '../../api/maintainLifecycle'
import {
  ensureInstanceId,
  resolveHardwareHash,
} from '../../utils/deviceFingerprint'

const AGREEMENT_VERSION = '2026-07-17'
const AGREEMENT_STORAGE_KEY = `llmgw_op_agreement_activate_${AGREEMENT_VERSION}`

const instanceId = ref(ensureInstanceId())
const licenseKey = ref('')
const deviceName = ref('')
const hardwareHash = ref('')
const loading = ref(false)
const checking = ref(false)
const message = ref('')
const error = ref('')
const status = ref<LicenseStatus | null>(null)
const showAgreement = ref(false)
const pendingActivation = ref(false)

async function ensureHash() {
  if (hardwareHash.value) return hardwareHash.value
  let serverHash = ''
  try {
    const fp = await bootstrapApi.fingerprint()
    serverHash = fp.hardware_hash || ''
  } catch {
    /* fall back to client hash */
  }
  hardwareHash.value = await resolveHardwareHash(serverHash)
  return hardwareHash.value
}

async function loadStatus() {
  if (!instanceId.value.trim()) return
  checking.value = true
  try {
    const hash = await ensureHash()
    const boot = await bootstrapApi.status(instanceId.value.trim(), licenseKey.value.trim() || undefined)
    if (boot.activated) {
      status.value = {
        state: 'active',
        license_key: boot.license_key,
        device_name: deviceName.value || undefined,
      }
      return
    }
    try {
      status.value = await maintainLifecycleApi.licenseStatus(instanceId.value.trim())
    } catch {
      status.value = { state: 'none' }
    }
  } catch (e) {
    status.value = null
    error.value = (e as Error).message
  } finally {
    checking.value = false
  }
}

async function activate() {
  if (!instanceId.value.trim() || !licenseKey.value.trim()) {
    error.value = '请输入实例 ID 和 License Key。'
    return
  }
  if (!localStorage.getItem(AGREEMENT_STORAGE_KEY)) {
    pendingActivation.value = true
    showAgreement.value = true
    return
  }
  await performActivation()
}

async function performActivation() {
  localStorage.setItem('maintain_instance_id', instanceId.value.trim())
  localStorage.setItem('llmgw_instance_id', instanceId.value.trim())
  loading.value = true
  error.value = ''
  message.value = ''
  try {
    const hash = await ensureHash()
    if (!hash) {
      throw new Error('无法采集设备指纹（hardware_hash），请刷新后重试。')
    }
    const result = await bootstrapApi.activate({
      instance_id: instanceId.value.trim(),
      license_key: licenseKey.value.trim(),
      hardware_hash: hash,
      device_name: deviceName.value.trim() || undefined,
    })
    if (!result.activated) {
      throw new Error(result.error || result.online_error || '激活失败')
    }
    status.value = {
      state: 'active',
      license_key: licenseKey.value.trim(),
      device_name: deviceName.value.trim() || undefined,
    }
    markBootstrapActivated()
    message.value = result.mode === 'online'
      ? 'License 已激活，并已尝试向中心注册。'
      : 'License 已激活（本地/离线模式可用）。'

    // Best-effort center register — never block local success.
    try {
      const reg = await bootstrapApi.registerCenter({
        instance_id: instanceId.value.trim(),
        license_key: licenseKey.value.trim(),
        hardware_hash: hash,
      })
      if (reg.registered) {
        message.value = 'License 已激活，并已向中心注册。'
      } else if (reg.deferred) {
        message.value = `License 已激活。${reg.message || '中心不可达，将后台补注册。'}`
      }
    } catch {
      message.value = 'License 已激活。中心注册稍后自动重试。'
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function onAgreed() {
  showAgreement.value = false
  if (pendingActivation.value) {
    performActivation()
    pendingActivation.value = false
  }
}

function onCancelled() {
  showAgreement.value = false
  pendingActivation.value = false
}

onMounted(async () => {
  await ensureHash()
  await loadStatus()
})
</script>

<template>
  <div class="lifecycle-page">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">LICENSE ACTIVATION</p>
        <h1>激活实例 License</h1>
        <p>完成用户协议确认后，将 License 绑定到当前实例。离线环境可走离线激活流程。</p>
      </div>
      <div class="lifecycle-page__actions">
        <RouterLink class="btn btn-ghost" to="/bootstrap">安装向导</RouterLink>
        <RouterLink class="btn btn-ghost" to="/customer/offline-activation">离线激活</RouterLink>
        <RouterLink class="btn btn-ghost" to="/customer/license">查看状态</RouterLink>
        <RouterLink class="btn btn-ghost" to="/customer/agreement">用户协议</RouterLink>
      </div>
    </header>

    <el-alert v-if="error" type="error" :title="error" show-icon closable class="mb" @close="error = ''" />
    <el-alert v-if="message" type="success" :title="message" show-icon class="mb" />

    <div class="lifecycle-grid">
      <el-card shadow="never">
        <template #header>当前状态</template>
        <el-skeleton v-if="checking" :rows="3" animated />
        <template v-else-if="status && status.state !== 'none'">
          <p><strong>{{ licenseStateLabel(status.state) }}</strong>
            <el-tag v-if="status.subscription_tier" size="small" class="ml">{{ status.subscription_tier }}</el-tag>
          </p>
          <p v-if="status.license_key" class="mono">{{ status.license_key }}</p>
          <p v-if="status.expires_at" class="muted">有效期至 {{ new Date(status.expires_at).toLocaleString() }}</p>
          <p v-if="status.device_name" class="muted">设备：{{ status.device_name }}</p>
        </template>
        <el-empty v-else description="尚未激活" />
      </el-card>

      <el-card shadow="never">
        <template #header>在线激活</template>
        <el-form label-position="top" @submit.prevent="activate">
          <el-form-item label="实例 ID">
            <el-input v-model="instanceId" placeholder="gw-prod-01" @change="loadStatus" />
          </el-form-item>
          <el-form-item label="License Key">
            <el-input v-model="licenseKey" placeholder="LIC-••••••••" />
          </el-form-item>
          <el-form-item label="硬件哈希">
            <el-input :model-value="hardwareHash" readonly placeholder="自动采集中…" />
          </el-form-item>
          <el-form-item label="设备名称（可选）">
            <el-input v-model="deviceName" placeholder="生产网关 01" />
          </el-form-item>
          <el-button type="primary" :loading="loading" @click="activate">激活 License</el-button>
        </el-form>
      </el-card>
    </div>

    <OperationAgreementDialog
      v-model="showAgreement"
      scope="activate"
      :version="AGREEMENT_VERSION"
      :subject-id="instanceId"
      @agreed="onAgreed"
      @cancelled="onCancelled"
    />
  </div>
</template>

<style scoped>
.lifecycle-page { padding: 24px clamp(16px, 3vw, 32px) 40px; max-width: 1100px; }
.lifecycle-page__header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  margin-bottom: 20px;
}
.eyebrow {
  margin: 0 0 6px;
  color: #1e4fd6;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
}
.lifecycle-page__header h1 { margin: 0 0 8px; font-size: 24px; letter-spacing: -0.03em; }
.lifecycle-page__header p { margin: 0; color: #5b6b82; font-size: 14px; line-height: 1.6; max-width: 60ch; }
.lifecycle-page__actions { display: flex; flex-wrap: wrap; gap: 8px; }
.lifecycle-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
}
.mb { margin-bottom: 14px; }
.ml { margin-left: 8px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.muted { color: #5b6b82; font-size: 13px; }
</style>
