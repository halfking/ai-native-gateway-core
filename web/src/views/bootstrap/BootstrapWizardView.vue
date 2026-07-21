<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  bootstrapApi,
  markBootstrapActivated,
  type BootstrapStatus,
  type FingerprintInfo,
} from '../../api/bootstrap'
import { maintainLifecycleApi } from '../../api/maintainLifecycle'
import {
  collectClientFingerprint,
  ensureInstanceId,
  resolveHardwareHash,
} from '../../utils/deviceFingerprint'

const AGREEMENT_VERSION = '2026-07-17'
const AGREEMENT_STORAGE_KEY = `llmgw_product_terms_${AGREEMENT_VERSION}`

const router = useRouter()

const step = ref(0)
const steps = ['用户协议', '设备指纹', '激活 License', '注册中心', '完成']

const agreed = ref(!!localStorage.getItem(AGREEMENT_STORAGE_KEY))
const loading = ref(false)
const error = ref('')
const message = ref('')

const status = ref<BootstrapStatus | null>(null)
const fingerprint = ref<FingerprintInfo | null>(null)
const hardwareHash = ref('')
const networkSummary = ref('')
const instanceId = ref(ensureInstanceId())
const licenseKey = ref('')
const deviceName = ref('')
const offlinePayload = ref('')
const activateMode = ref<'online' | 'offline'>('online')
const registerResult = ref<{ registered: boolean; deferred?: boolean; message?: string } | null>(null)

const canNextFromAgreement = computed(() => agreed.value)
const canActivate = computed(() => {
  if (!hardwareHash.value.trim() || !instanceId.value.trim()) return false
  if (activateMode.value === 'online') return !!licenseKey.value.trim()
  return !!offlinePayload.value.trim()
})

async function loadStatus() {
  try {
    status.value = await bootstrapApi.status(instanceId.value.trim() || undefined)
    if (status.value.hardware_hash) {
      hardwareHash.value = await resolveHardwareHash(status.value.hardware_hash)
    }
    if (status.value.activated) {
      markBootstrapActivated()
      step.value = Math.max(step.value, 4)
    }
  } catch {
    status.value = null
  }
}

async function loadFingerprint() {
  loading.value = true
  error.value = ''
  try {
    let serverHash = ''
    try {
      fingerprint.value = await bootstrapApi.fingerprint()
      serverHash = fingerprint.value.hardware_hash || ''
      if (fingerprint.value.network_summary) {
        networkSummary.value = fingerprint.value.network_summary
      }
    } catch {
      fingerprint.value = null
    }
    if (!networkSummary.value) {
      const client = await collectClientFingerprint()
      networkSummary.value = client.network_summary
      if (!serverHash) serverHash = client.hardware_hash
    }
    hardwareHash.value = await resolveHardwareHash(serverHash)
    if (!instanceId.value) instanceId.value = ensureInstanceId()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function acceptAgreement() {
  if (!agreed.value) {
    error.value = '请先阅读并勾选同意用户协议。'
    return
  }
  loading.value = true
  error.value = ''
  try {
    localStorage.setItem(AGREEMENT_STORAGE_KEY, new Date().toISOString())
    try {
      await maintainLifecycleApi.consent({
        subject_id: instanceId.value || 'anonymous',
        instance_id: instanceId.value || undefined,
        agreement_type: 'product_terms',
        agreement_version: AGREEMENT_VERSION,
        granted: true,
        source: 'gateway-web/bootstrap_wizard',
      })
    } catch {
      // offline-friendly: local consent is enough to proceed
    }
    step.value = 1
    await loadFingerprint()
  } finally {
    loading.value = false
  }
}

async function goActivateStep() {
  if (!hardwareHash.value) await loadFingerprint()
  if (!hardwareHash.value) {
    error.value = '无法采集设备指纹，请刷新后重试。'
    return
  }
  error.value = ''
  step.value = 2
}

async function performActivate() {
  if (!canActivate.value) {
    error.value = activateMode.value === 'online'
      ? '请填写实例 ID 与 License Key。'
      : '请粘贴离线激活码或签名 License。'
    return
  }
  loading.value = true
  error.value = ''
  message.value = ''
  try {
    localStorage.setItem('llmgw_instance_id', instanceId.value.trim())
    localStorage.setItem('maintain_instance_id', instanceId.value.trim())

    if (activateMode.value === 'online') {
      const result = await bootstrapApi.activate({
        instance_id: instanceId.value.trim(),
        license_key: licenseKey.value.trim(),
        hardware_hash: hardwareHash.value.trim(),
        device_name: deviceName.value.trim() || undefined,
      })
      if (!result.activated) {
        throw new Error(result.error || result.online_error || '激活失败')
      }
      message.value = result.mode === 'online'
        ? '在线激活成功。'
        : '本地激活成功（中心不可达时仍可离线使用）。'
      registerResult.value = {
        registered: !!result.registered,
        deferred: !result.registered,
        message: result.registered ? '已向中心注册' : '中心不可达，将后台补注册',
      }
    } else {
      const raw = offlinePayload.value.trim()
      const result = await bootstrapApi.importOffline({
        instance_id: instanceId.value.trim(),
        hardware_hash: hardwareHash.value.trim(),
        signed_license: raw,
        activation_code: raw,
      })
      if (!result.activated) {
        throw new Error(result.error || '离线激活失败')
      }
      if (result.license_key) licenseKey.value = result.license_key
      message.value = result.message || '离线激活成功。'
      registerResult.value = {
        registered: false,
        deferred: true,
        message: '离线模式；联网后将自动向中心补注册',
      }
    }

    markBootstrapActivated()
    step.value = 3
    await tryRegisterCenter()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function tryRegisterCenter() {
  loading.value = true
  error.value = ''
  try {
    const result = await bootstrapApi.registerCenter({
      instance_id: instanceId.value.trim(),
      license_key: licenseKey.value.trim(),
      hardware_hash: hardwareHash.value.trim(),
    })
    registerResult.value = {
      registered: result.registered,
      deferred: result.deferred,
      message: result.message || (result.registered ? '已注册到中心' : '已安排后台补注册'),
    }
    step.value = 4
  } catch (e) {
    // Never block local use on center registration failure
    registerResult.value = {
      registered: false,
      deferred: true,
      message: `中心注册稍后重试：${(e as Error).message}`,
    }
    step.value = 4
  } finally {
    loading.value = false
    await loadStatus()
  }
}

function finish() {
  markBootstrapActivated()
  router.push({ path: '/', query: { login: '1' } })
}

function skipToLogin() {
  router.push({ path: '/', query: { login: '1' } })
}

onMounted(async () => {
  await loadStatus()
  if (status.value?.activated) {
    step.value = 4
    return
  }
  if (agreed.value) {
    step.value = 1
    await loadFingerprint()
  }
})
</script>

<template>
  <div class="lifecycle-page bootstrap-wizard">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">FIRST BOOT</p>
        <h1>本地网关安装激活</h1>
        <p>本机通过 IP:端口访问即可激活；可完全离线完成。联网时将自动向中心注册，不阻塞本地使用。</p>
      </div>
      <div class="lifecycle-page__actions">
        <RouterLink class="btn btn-ghost" to="/customer/update-activate">高级激活页</RouterLink>
        <button type="button" class="btn btn-ghost" @click="skipToLogin">稍后再说</button>
      </div>
    </header>

    <nav class="wizard-steps" aria-label="激活步骤">
      <div
        v-for="(label, i) in steps"
        :key="label"
        class="wizard-steps__item"
        :class="{
          'is-active': step === i,
          'is-done': step > i,
        }"
      >
        <span class="wizard-steps__index">{{ i + 1 }}</span>
        <span class="wizard-steps__label">{{ label }}</span>
      </div>
    </nav>

    <el-alert v-if="error" type="error" :title="error" show-icon closable class="mb" @close="error = ''" />
    <el-alert v-if="message" type="success" :title="message" show-icon class="mb" />

    <!-- Step 0: Agreement -->
    <el-card v-if="step === 0" shadow="never" class="wizard-card">
      <template #header>用户协议与数据采集说明</template>
      <p class="muted">激活前请确认：</p>
      <ul class="agree-list">
        <li>业务数据与配置默认保存在你的基础设施中。</li>
        <li>本向导仅采集设备指纹哈希（hardware_hash）与网络摘要，不明文上传硬件序列号。</li>
        <li>离线环境可完成本地激活；联网后自动向中心补注册与心跳。</li>
        <li>软件按“现状”提供，请遵守适用的开源与商业授权条款。</li>
      </ul>
      <label class="check-row">
        <input v-model="agreed" type="checkbox" />
        <span>我已阅读并同意 <a href="/user-agreement.html" target="_blank" rel="noopener">用户许可协议</a></span>
      </label>
      <div class="wizard-actions">
        <el-button type="primary" :loading="loading" :disabled="!canNextFromAgreement" @click="acceptAgreement">
          同意并继续
        </el-button>
      </div>
    </el-card>

    <!-- Step 1: Fingerprint -->
    <el-card v-else-if="step === 1" shadow="never" class="wizard-card">
      <template #header>设备指纹</template>
      <el-skeleton v-if="loading && !hardwareHash" :rows="3" animated />
      <template v-else>
        <el-descriptions :column="1" size="small" border>
          <el-descriptions-item label="实例 ID">
            <span class="mono">{{ instanceId }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="硬件哈希">
            <span class="mono">{{ hardwareHash || '—' }}</span>
          </el-descriptions-item>
          <el-descriptions-item v-if="fingerprint?.os" label="系统">
            {{ fingerprint.os }} / {{ fingerprint.arch || '—' }}
          </el-descriptions-item>
          <el-descriptions-item label="网络摘要">
            {{ networkSummary || '—' }}
          </el-descriptions-item>
          <el-descriptions-item label="中心连通">
            <el-tag :type="status?.center_online ? 'success' : 'info'" size="small">
              {{ status?.center_online ? '在线（可自动注册）' : '离线（不阻塞激活）' }}
            </el-tag>
          </el-descriptions-item>
        </el-descriptions>
        <p class="hint">指纹仅用于绑定本机 License，不会上传原始硬件标识。</p>
      </template>
      <div class="wizard-actions">
        <el-button @click="step = 0">上一步</el-button>
        <el-button :loading="loading" @click="loadFingerprint">重新采集</el-button>
        <el-button type="primary" :disabled="!hardwareHash" @click="goActivateStep">下一步</el-button>
      </div>
    </el-card>

    <!-- Step 2: Activate -->
    <el-card v-else-if="step === 2" shadow="never" class="wizard-card">
      <template #header>激活 License</template>
      <p class="muted mb">
        请先在云端 <a href="https://llm.kxpms.cn/maintain/license" target="_blank" rel="noopener">领取/查看激活码</a>，再回到本机填写。
      </p>
      <el-radio-group v-model="activateMode" class="mb">
        <el-radio-button value="online">在线 / 填码激活</el-radio-button>
        <el-radio-button value="offline">离线导入</el-radio-button>
      </el-radio-group>

      <el-form label-position="top" @submit.prevent="performActivate">
        <el-form-item label="实例 ID">
          <el-input v-model="instanceId" placeholder="gw-prod-01" />
        </el-form-item>
        <el-form-item label="硬件哈希">
          <el-input :model-value="hardwareHash" readonly class="mono-input" />
        </el-form-item>
        <template v-if="activateMode === 'online'">
          <el-form-item label="License Key">
            <el-input v-model="licenseKey" placeholder="LIC-••••••••" />
          </el-form-item>
          <el-form-item label="设备名称（可选）">
            <el-input v-model="deviceName" placeholder="生产网关 01" />
          </el-form-item>
        </template>
        <template v-else>
          <el-form-item label="离线激活码 / 签名 License">
            <el-input
              v-model="offlinePayload"
              type="textarea"
              :rows="5"
              placeholder="粘贴审批通过后的激活响应或 signed_license"
            />
          </el-form-item>
          <p class="hint">
            也可在
            <RouterLink to="/customer/offline-activation">离线激活页</RouterLink>
            提交申请，审批后再导入。
          </p>
        </template>
      </el-form>
      <div class="wizard-actions">
        <el-button @click="step = 1">上一步</el-button>
        <el-button type="primary" :loading="loading" :disabled="!canActivate" @click="performActivate">
          激活
        </el-button>
      </div>
    </el-card>

    <!-- Step 3: Register center -->
    <el-card v-else-if="step === 3" shadow="never" class="wizard-card">
      <template #header>注册中心</template>
      <el-skeleton v-if="loading" :rows="2" animated />
      <template v-else-if="registerResult">
        <p>
          <el-tag :type="registerResult.registered ? 'success' : 'warning'" size="small">
            {{ registerResult.registered ? '已注册' : '待补注册' }}
          </el-tag>
          <span class="ml muted">{{ registerResult.message }}</span>
        </p>
        <p class="hint">中心不可达不会影响本机使用；恢复网络后将自动补注册与心跳。</p>
      </template>
      <div class="wizard-actions">
        <el-button :loading="loading" @click="tryRegisterCenter">重试注册</el-button>
        <el-button type="primary" @click="step = 4">继续</el-button>
      </div>
    </el-card>

    <!-- Step 4: Done -->
    <el-card v-else shadow="never" class="wizard-card">
      <template #header>激活完成</template>
      <p><strong>本机网关已就绪。</strong></p>
      <p class="muted">
        {{ status?.message || '可以登录本地后台开始使用。联网后中心将自动同步实例状态。' }}
      </p>
      <el-descriptions v-if="hardwareHash" :column="1" size="small" class="mt" border>
        <el-descriptions-item label="实例 ID">{{ instanceId }}</el-descriptions-item>
        <el-descriptions-item label="硬件哈希">
          <span class="mono">{{ hardwareHash }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="中心">
          {{ registerResult?.registered ? '已注册' : (registerResult?.message || '后台补注册') }}
        </el-descriptions-item>
      </el-descriptions>
      <div class="wizard-actions">
        <el-button type="primary" @click="finish">进入登录</el-button>
        <RouterLink class="btn btn-ghost" to="/customer/update-activate">查看更新与激活</RouterLink>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.lifecycle-page { padding: 24px clamp(16px, 3vw, 32px) 40px; max-width: 880px; margin: 0 auto; }
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

.wizard-steps {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 18px;
}
.wizard-steps__item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 8px;
  border: 1px solid #d8e0ec;
  color: #5b6b82;
  font-size: 13px;
}
.wizard-steps__item.is-active {
  border-color: #1e4fd6;
  color: #1e4fd6;
  background: rgba(30, 79, 214, 0.06);
}
.wizard-steps__item.is-done {
  border-color: #86b7a0;
  color: #1f6b4a;
}
.wizard-steps__index {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 700;
  background: #eef2f8;
}
.wizard-steps__item.is-active .wizard-steps__index {
  background: #1e4fd6;
  color: #fff;
}
.wizard-steps__item.is-done .wizard-steps__index {
  background: #1f6b4a;
  color: #fff;
}

.wizard-card { margin-bottom: 16px; }
.wizard-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 18px;
}
.agree-list {
  margin: 8px 0 16px;
  padding-left: 1.2em;
  color: #3d4d66;
  line-height: 1.7;
  font-size: 14px;
}
.check-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 14px;
  color: #243044;
}
.check-row input { margin-top: 3px; }
.muted { color: #5b6b82; font-size: 14px; line-height: 1.6; }
.hint { margin: 10px 0 0; color: #7a879c; font-size: 12px; line-height: 1.5; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; word-break: break-all; }
.mb { margin-bottom: 14px; }
.ml { margin-left: 8px; }
.mt { margin-top: 12px; }

@media (max-width: 640px) {
  .lifecycle-page__header { flex-direction: column; }
  .wizard-steps__label { display: none; }
  .wizard-steps__item.is-active .wizard-steps__label,
  .wizard-steps__item.is-done .wizard-steps__label { display: inline; }
}
</style>
