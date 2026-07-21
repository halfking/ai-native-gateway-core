<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useRouter } from 'vue-router'
import OperationAgreementDialog from '../../components/OperationAgreementDialog.vue'
import { bootstrapApi, markBootstrapActivated } from '../../api/bootstrap'
import {
  ensureInstanceId,
  resolveHardwareHash,
  setInstanceId,
} from '../../utils/deviceFingerprint'

const AGREEMENT_VERSION = '2026-07-17'
const AGREEMENT_SCOPE = 'activate'
const AGREEMENT_KEY = `llmgw_op_agreement_${AGREEMENT_SCOPE}_${AGREEMENT_VERSION}`

const router = useRouter()

const instanceId = ref(ensureInstanceId())
const hardwareHash = ref('')
const offlinePayload = ref('')
const deviceName = ref('')
const loading = ref(false)
const message = ref('')
const error = ref('')
const showAgreement = ref(false)
const agreementAccepted = ref(!!localStorage.getItem(AGREEMENT_KEY))
const copied = ref(false)

const canSubmit = computed(
  () => !!instanceId.value.trim() && !!hardwareHash.value.trim() && !!offlinePayload.value.trim(),
)

async function ensureReady() {
  try {
    const fp = await bootstrapApi.fingerprint()
    if (fp.instance_id) {
      setInstanceId(fp.instance_id)
      instanceId.value = fp.instance_id
    }
    hardwareHash.value = await resolveHardwareHash(fp.hardware_hash)
  } catch {
    hardwareHash.value = await resolveHardwareHash(null)
  }
}

async function copyInstanceId() {
  if (!instanceId.value) return
  try {
    await navigator.clipboard.writeText(instanceId.value)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch { /* ignore */ }
}

function openPublicSite() {
  // 用户需在公网站点 https://llm.kxpms.cn 粘贴实例 ID，生成激活码。
  window.open('https://llm.kxpms.cn/maintain/license', '_blank', 'noopener,noreferrer')
}

function openAgreement() {
  agreementAccepted.value = !!localStorage.getItem(AGREEMENT_KEY)
  if (!agreementAccepted.value) {
    showAgreement.value = true
  }
}

function onAgreed() {
  agreementAccepted.value = true
  localStorage.setItem(AGREEMENT_KEY, new Date().toISOString())
  showAgreement.value = false
}

function onCancelled() {
  showAgreement.value = false
  try {
    localStorage.removeItem(AGREEMENT_KEY)
  } catch { /* ignore */ }
  agreementAccepted.value = false
}

async function importLicense() {
  // 强制：未同意协议 → 弹窗 → 取消 → 阻断。
  if (!agreementAccepted.value) {
    showAgreement.value = true
    return
  }
  if (!canSubmit.value) {
    error.value = '请先复制实例 ID、在公网生成激活码，粘贴回此处后再导入。'
    return
  }
  loading.value = true
  error.value = ''
  message.value = ''
  try {
    const result = await bootstrapApi.importOffline({
      instance_id: instanceId.value.trim(),
      hardware_hash: hardwareHash.value.trim(),
      signed_license: offlinePayload.value.trim(),
      activation_code: offlinePayload.value.trim(),
    })
    if (!result.activated) {
      throw new Error(result.error || '离线激活失败')
    }
    message.value = result.message || '离线激活成功；联网后将自动向中心补注册。'
    ElMessage.success(message.value)
    markBootstrapActivated()
    setTimeout(() => router.push({ path: '/', query: { login: '1' } }), 800)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(ensureReady)
</script>

<template>
  <div class="lifecycle-page">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">OFFLINE ACTIVATION</p>
        <h1>离线激活</h1>
        <p>本机无法联网访问中心时使用：复制实例 ID 到公网激活站点，生成激活码后粘贴回来即可。</p>
      </div>
      <RouterLink class="btn btn-ghost" to="/customer/update-activate">返回更新与激活</RouterLink>
    </header>

    <el-alert v-if="error" type="error" :title="error" show-icon class="mb" closable @close="error = ''" />
    <el-alert v-if="message" type="success" :title="message" show-icon class="mb" closable @close="message = ''" />

    <div class="offline-grid">
      <el-card shadow="never" class="step-card">
        <template #header>
          <span class="step-num">1</span>
          <span>本机：复制实例 ID</span>
        </template>
        <p class="muted">实例 ID 由本机自动生成（一机一实例），离线激活必须把此 ID 复制到公网激活站点。</p>
        <el-form-item label="实例 ID（一机一实例，自动锁定）">
          <div class="copy-row">
            <el-input :model-value="instanceId" readonly class="mono-input" />
            <button type="button" class="btn btn-secondary btn-sm btn-no-arrow" :disabled="!instanceId" @click="copyInstanceId">
              {{ copied ? '已复制' : '复制实例 ID' }}
            </button>
          </div>
        </el-form-item>
        <el-form-item label="硬件哈希（自动采集）">
          <el-input :model-value="hardwareHash" readonly class="mono-input" />
        </el-form-item>
        <el-form-item label="设备名称（可选，公网站点可读）">
          <el-input v-model="deviceName" placeholder="例如：华东机房-网关-01" />
        </el-form-item>
      </el-card>

      <el-card shadow="never" class="step-card">
        <template #header>
          <span class="step-num">2</span>
          <span>公网：到 llm.kxpms.cn 生成激活码</span>
        </template>
        <p class="muted">
          在公网打开 <code>https://llm.kxpms.cn/maintain/license</code> → 离线激活 →
          粘贴上面复制的实例 ID → 提交审批。审批通过后，将激活响应或 <code>license.dat</code> 内容复制到下方输入框。
        </p>
        <div class="step-actions">
          <button type="button" class="btn btn-primary" @click="openPublicSite">打开公网激活站点</button>
        </div>
      </el-card>

      <el-card shadow="never" class="step-card">
        <template #header>
          <span class="step-num">3</span>
          <span>本机：粘贴激活码 / signed_license / license.dat</span>
        </template>
        <el-form label-position="top" @submit.prevent="importLicense">
          <el-form-item label="激活响应 / signed_license / license.dat">
            <el-input
              v-model="offlinePayload"
              type="textarea"
              :rows="6"
              placeholder="粘贴公网生成的激活响应 JSON、signed_license 字符串或 license.dat 内容"
            />
          </el-form-item>
          <div class="step-actions">
            <button type="button" class="btn btn-primary" :disabled="loading || !canSubmit" @click="importLicense">
              同意协议并导入
            </button>
            <button type="button" class="btn btn-ghost btn-no-arrow" @click="openAgreement">查看协议</button>
          </div>
          <p v-if="!agreementAccepted" class="hint warn">
            ⚠ 首次导入会弹出用户协议窗口，未勾选同意则无法激活。
          </p>
        </el-form>
      </el-card>
    </div>

    <OperationAgreementDialog
      v-model="showAgreement"
      :scope="AGREEMENT_SCOPE"
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
.lifecycle-page__header h1 { margin: 0 0 8px; font-size: 24px; }
.lifecycle-page__header p { margin: 0; color: #5b6b82; font-size: 14px; }
.offline-grid {
  display: grid;
  gap: 16px;
  grid-template-columns: 1fr;
}
.step-card :deep(.el-card__header) {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}
.step-num {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: #1e4fd6;
  color: #fff;
  font-size: 13px;
  font-weight: 700;
}
.copy-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.copy-row .el-input { flex: 1; min-width: 240px; }
.step-actions { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.mono-input :deep(.el-input__inner),
.mono-input input { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.muted { color: #5b6b82; font-size: 14px; line-height: 1.6; }
.hint { margin-top: 8px; font-size: 12px; color: #7a879c; }
.hint.warn { color: #c2413b; }
.mb { margin-bottom: 14px; }
</style>
