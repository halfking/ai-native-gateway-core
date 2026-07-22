<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  ElAlert,
  ElButton,
  ElCard,
  ElDescriptions,
  ElDescriptionsItem,
  ElDivider,
  ElForm,
  ElFormItem,
  ElInput,
  ElRadio,
  ElRadioGroup,
  ElResult,
  ElSkeleton,
  ElTag,
} from 'element-plus'
import {
  bootstrapApi,
  markBootstrapActivated,
  type BootstrapStatus,
  type FingerprintInfo,
} from '../../api/bootstrap'
import { maintainLifecycleApi } from '../../api/maintainLifecycle'
import OperationAgreementDialog from '../../components/lifecycle/OperationAgreementDialog.vue'
import {
  collectClientFingerprint,
  ensureInstanceId,
  resolveHardwareHash,
  setInstanceId,
} from '../../utils/deviceFingerprint'

const AGREEMENT_VERSION = '2026-07-17'
const AGREEMENT_STORAGE_KEY = `llmgw_op_agreement_activate_${AGREEMENT_VERSION}`
const PRODUCT_TERMS_STORAGE_KEY = `llmgw_product_terms_${AGREEMENT_VERSION}`

const router = useRouter()

const step = ref(0)
const steps = ['用户协议', '设备指纹', '激活 License', '注册中心', '完成']

const agreementAccepted = ref(!!localStorage.getItem(AGREEMENT_STORAGE_KEY))
const productTermsAccepted = ref(!!localStorage.getItem(PRODUCT_TERMS_STORAGE_KEY))
const showAgreementDialog = ref(false)
const agreementDialogResolved = ref<'agreed' | 'cancelled' | null>(null)
const pendingActivation = ref(false)

const loading = ref(false)
const error = ref('')
const message = ref('')
const errorKey = ref(0)
const messageKey = ref(0)
const ERROR_AUTO_HIDE_MS = 8000        // 错误 8 秒后自动消失
const SUCCESS_AUTO_HIDE_MS = 3000     // 成功 3 秒后自动消失
const cooldownTimer = ref<number>(0)  // 控制 cooldown 自动重试计时

// 错误提示：自动消失（红色，8秒），并用 key 触发过渡动画
watch(error, (val, old) => {
  if (old && !val) return // 用户手动关闭，不重置
  if (val) {
    errorKey.value++
    if (errorTimer) window.clearTimeout(errorTimer)
    errorTimer = window.setTimeout(() => { error.value = '' }, ERROR_AUTO_HIDE_MS)
  }
})

// 成功提示：自动消失（绿色，3秒）
watch(message, (val, old) => {
  if (old && !val) return
  if (val) {
    messageKey.value++
    if (messageTimer) window.clearTimeout(messageTimer)
    messageTimer = window.setTimeout(() => { message.value = '' }, SUCCESS_AUTO_HIDE_MS)
  }
})

let errorTimer: number | null = null
let messageTimer: number | null = null

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
const instanceIdCopied = ref(false)

const canActivate = computed(() => {
  if (!hardwareHash.value.trim() || !instanceId.value.trim()) return false
  if (activateMode.value === 'online') return true  // 在线激活不需要输入，总是可以点击
  return !!offlinePayload.value.trim()  // 离线激活需要粘贴激活响应码
})

async function loadStatus() {
  try {
    status.value = await bootstrapApi.status()
    if (status.value.instance_id) {
      setInstanceId(status.value.instance_id)
      instanceId.value = status.value.instance_id
    }
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
    let serverInstanceId = ''
    try {
      fingerprint.value = await bootstrapApi.fingerprint()
      serverHash = fingerprint.value.hardware_hash || ''
      serverInstanceId = fingerprint.value.instance_id || ''
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
    if (serverInstanceId) {
      setInstanceId(serverInstanceId)
      instanceId.value = serverInstanceId
    }
    await loadStatus()
    if (!instanceId.value) {
      error.value = '无法生成本机实例 ID，请检查 /var/lib/kx-gateway 目录权限后重试。'
      return
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function recordProductTermsConsent() {
  if (productTermsAccepted.value) return
  try {
    await maintainLifecycleApi.consent({
      subject_id: instanceId.value || 'anonymous',
      instance_id: instanceId.value || undefined,
      agreement_type: 'product_terms',
      agreement_version: AGREEMENT_VERSION,
      granted: true,
      source: 'gateway-web/bootstrap_wizard',
    })
    productTermsAccepted.value = true
  } catch {
    // offline-friendly: local cache still unlocks UX
  }
}

function openAgreementDialog() {
  // 强制弹窗：未勾选 + 未确认前不能继续。每一次启动流程都要再次确认。
  agreementDialogResolved.value = null
  showAgreementDialog.value = true
}

async function onAgreementAgreed() {
  agreementAccepted.value = true
  localStorage.setItem(AGREEMENT_STORAGE_KEY, new Date().toISOString())
  agreementDialogResolved.value = 'agreed'
  showAgreementDialog.value = false
  // 同意后自动进入步骤1（设备指纹）
  step.value = 1
  await loadFingerprint()
  if (pendingActivation.value) {
    pendingActivation.value = false
    await continueActivate()
  }
}

function onAgreementCancelled() {
  agreementDialogResolved.value = 'cancelled'
  // 取消即视为拒绝：清掉本机任何残留的"已同意"标记，确保下次启动仍强制弹窗。
  try {
    localStorage.removeItem(AGREEMENT_STORAGE_KEY)
  } catch { /* ignore */ }
  agreementAccepted.value = false
  showAgreementDialog.value = false
  if (pendingActivation.value) {
    pendingActivation.value = false
    error.value = '未同意用户协议，无法继续激活。'
  }
}

async function recordAgreementConsent() {
  // 同意弹窗后异步上报一次 consent（best effort），用户授权过的协议记录可以留底。
  try {
    await maintainLifecycleApi.consent({
      subject_id: instanceId.value || 'anonymous',
      instance_id: instanceId.value || undefined,
      agreement_type: 'activation_terms',
      agreement_version: AGREEMENT_VERSION,
      granted: true,
      source: 'gateway-web/operation_dialog',
    })
  } catch { /* ignore */ }
}

async function ensureAgreementThenActivate() {
  // 强制：未同意 → 弹窗 → 取消 → 阻断。
  if (!agreementAccepted.value) {
    pendingActivation.value = true
    openAgreementDialog()
    return
  }
  await continueActivate()
}

async function continueActivate() {
  // 这里是协议已确认后的真正激活分支。
  await performActivate()
}

async function goActivateStep() {
  if (!hardwareHash.value) await loadFingerprint()
  if (!hardwareHash.value) {
    error.value = '无法采集设备指纹，请刷新后重试。'
    return
  }
  if (!instanceId.value) {
    error.value = '无法生成本机实例 ID，请稍后再试或检查服务器权限。'
    return
  }
  error.value = ''
  step.value = 2
}

async function copyInstanceId() {
  if (!instanceId.value) return
  try {
    await navigator.clipboard.writeText(instanceId.value)
    instanceIdCopied.value = true
    setTimeout(() => { instanceIdCopied.value = false }, 2000)
  } catch { /* ignore */ }
}

  // 把 result 的所有字段都串起来，给用户看最完整的错误信息
  const buildErrorDetail = (result: any): string => {
    return [
      result.error && `${result.error}`,
      result.body && result.body.message && `${result.body.message}`,
      result.body && result.body.code && `[${result.body.code}]`,
    ].filter(Boolean).join(' - ')
  }

  async function performActivate() {
    if (!canActivate.value) {
      error.value = activateMode.value === 'online'
        ? '设备指纹信息不完整，请返回上一步重新采集。'
        : '请粘贴离线激活码或签名 License。'
      return
    }
    // 二次保险：进 activate 之前再确认一次。
    if (!agreementAccepted.value) {
      pendingActivation.value = true
      openAgreementDialog()
      return
    }
    loading.value = true
    error.value = ''
    message.value = ''
    try {
      setInstanceId(instanceId.value.trim())

      if (activateMode.value === 'online') {
        const result = await bootstrapApi.activateQuick({
          instance_id: instanceId.value.trim(),
          hardware_hash: hardwareHash.value.trim(),
          device_name: deviceName.value.trim() || undefined,
        })
        if (!result.activated) {
          // 详细错误：cooldown 等业务错误让用户看清楚可以做什么
          const detail = buildErrorDetail(result)
          if (result.body && result.body.code === 'issue.cooldown') {
            throw new Error(`中心冷却中（${result.body.message || ''}）。请稍后重试，每次失败都会清除冷却。`)
          }
          throw new Error(detail || '在线激活失败')
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
        const detail = buildErrorDetail(result)
        if (result.body && result.body.code === 'issue.cooldown') {
          throw new Error(`中心冷却中（${result.body.message || ''}）。请稍后重试。`)
        }
        throw new Error(detail || '离线激活失败')
      }
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
  // 已激活且未点"重新激活"→ 直接到结束页，不要再弹协议。
  if (status.value?.activated) {
    step.value = 4
    return
  }
  // 未同意协议 → 弹窗；已同意 → 进入步骤1（设备指纹）
  if (!agreementAccepted.value) {
    openAgreementDialog()
  } else {
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
        <RouterLink class="btn btn-ghost btn-no-arrow" to="/customer/update-activate">高级激活页</RouterLink>
        <button type="button" class="btn btn-ghost btn-no-arrow" @click="skipToLogin">稍后再说</button>
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

    <transition name="el-alert-fade">
      <el-alert
        v-if="error"
        :key="errorKey"
        type="error"
        :title="error"
        show-icon
        closable
        class="mb"
        @close="error = ''"
      />
    </transition>
    <transition name="el-alert-fade">
      <el-alert v-if="message" :key="messageKey" type="success" :title="message" show-icon class="mb" />
    </transition>

    <!-- 已同意协议但未完成激活：显示后续步骤；否则提示未同意 -->
    <template v-if="agreementAccepted || agreementDialogResolved === 'agreed'">
      <!-- Step 1: Fingerprint + 选择激活方式 -->
      <el-card v-if="step === 1" shadow="never" class="wizard-card">
        <template #header>设备指纹</template>
        <el-skeleton v-if="loading && !hardwareHash" :rows="3" animated />
        <template v-else>
          <el-descriptions :column="1" size="small" border class="mb">
            <el-descriptions-item label="实例 ID">
              <div class="instance-id-row">
                <span class="mono">{{ instanceId || '—' }}</span>
                <el-button class="btn-no-arrow" size="small" type="primary" link :disabled="!instanceId" @click="copyInstanceId">
                  {{ instanceIdCopied ? '✓ 已复制' : '复制实例 ID' }}
                </el-button>
              </div>
            </el-descriptions-item>
            <el-descriptions-item label="设备指纹">
              <span class="mono">{{ hardwareHash || '—' }}</span>
            </el-descriptions-item>
            <el-descriptions-item v-if="fingerprint?.os" label="系统信息">
              <span class="mono">{{ fingerprint.os }} / {{ fingerprint.arch || '—' }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="网络状态">
              <el-tag :type="status?.center_online ? 'success' : 'info'" size="small">
                {{ status?.center_online ? '在线（可自动注册）' : '离线（不阻塞激活）' }}
              </el-tag>
            </el-descriptions-item>
          </el-descriptions>

          <p class="hint mb">
            实例 ID 由本机在安装时自动生成（每台物理设备唯一）。设备指纹仅用于绑定本机 License，不会上传原始硬件标识。
          </p>

          <el-divider content-position="left">选择激活方式</el-divider>

          <el-radio-group v-model="activateMode" class="mb">
            <el-radio value="online" size="large">
              <span style="font-weight: 500;">在线激活</span>
              <span class="muted" style="font-size: 12px; margin-left: 8px;">需要 License Key</span>
            </el-radio>
            <el-radio value="offline" size="large">
              <span style="font-weight: 500;">离线激活</span>
              <span class="muted" style="font-size: 12px; margin-left: 8px;">生成激活申请码，到公网站点获取激活响应</span>
            </el-radio>
          </el-radio-group>
        </template>
        <div class="wizard-actions">
          <button type="button" class="btn btn-secondary btn-no-arrow" :disabled="loading" @click="loadFingerprint">重新采集</button>
          <button type="button" class="btn btn-primary" :disabled="!hardwareHash || !instanceId" @click="goActivateStep">下一步</button>
        </div>
      </el-card>

      <!-- Step 2: Activate -->
      <el-card v-else-if="step === 2" shadow="never" class="wizard-card">
        <template #header>激活 License</template>

        <!-- 在线激活模式 -->
        <template v-if="activateMode === 'online'">
          <p class="muted mb">
            点击"激活"按钮，系统将自动向中心 <code>llm.kxpms.cn</code> 申请 License 并完成本地激活（无需手动填写 License Key）。
          </p>
          <el-form label-position="top" @submit.prevent="ensureAgreementThenActivate" style="max-width: 600px; min-height: 0;" :show-message="false" :inline-message="false">
            <el-form-item label="设备名称（可选）" error="">
              <el-input v-model="deviceName" placeholder="例如：生产网关 01" clearable style="max-height: 40px;" />
            </el-form-item>
          </el-form>
        </template>

        <!-- 离线激活模式 -->
        <template v-else>
          <p class="muted mb">
            复制实例ID到 <a href="https://llm.kxpms.cn/maintain/license" target="_blank" rel="noopener">公网激活站点</a> 获取激活码，然后粘贴到下方。
          </p>

          <el-form label-position="top" @submit.prevent="ensureAgreementThenActivate" style="max-width: 600px;">
            <el-form-item label="实例 ID">
              <div style="display: flex; gap: 8px; align-items: center;">
                <el-input v-model="instanceId" readonly class="mono" size="small" />
                <el-button size="small" type="primary" :disabled="!instanceId" @click="copyInstanceId">
                  {{ instanceIdCopied ? '✓ 已复制' : '复制' }}
                </el-button>
              </div>
            </el-form-item>

            <el-form-item label="激活响应码">
              <el-input
                v-model="offlinePayload"
                type="textarea"
                :rows="3"
                placeholder="粘贴从公网站点获取的激活响应码"
              />
            </el-form-item>
          </el-form>
        </template>

        <div class="wizard-actions">
          <button type="button" class="btn btn-secondary btn-no-arrow" @click="step = 1">上一步</button>
          <button
            type="button"
            class="btn btn-primary"
            :disabled="loading || !canActivate"
            @click="ensureAgreementThenActivate"
          >
            {{ activateMode === 'online' ? '激活' : '导入激活' }}
          </button>
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
          <button type="button" class="btn btn-secondary btn-no-arrow" :disabled="loading" @click="tryRegisterCenter">重试注册</button>
          <button type="button" class="btn btn-primary" @click="step = 4">继续</button>
        </div>
      </el-card>

      <!-- Step 4: Done -->
      <el-card v-else-if="step === 4" shadow="never" class="wizard-card">
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
          <button type="button" class="btn btn-primary" @click="finish">进入登录</button>
          <RouterLink class="btn btn-ghost btn-no-arrow" to="/customer/license">查看 License 状态</RouterLink>
        </div>
      </el-card>
    </template>

    <template v-else>
      <el-card shadow="never" class="wizard-card agreement-card">
        <template #header>未同意用户协议</template>
        <p class="muted">
          激活本机 License 必须先阅读并同意 <a href="/user-agreement.html" target="_blank" rel="noopener">用户协议</a>。
          点击下方按钮弹出协议确认窗口，勾选并"同意并继续"后才能进入后续步骤。
        </p>
        <div class="wizard-actions">
          <button type="button" class="btn btn-primary" @click="openAgreementDialog">阅读并同意用户协议</button>
        </div>
      </el-card>
    </template>

    <OperationAgreementDialog
      v-model="showAgreementDialog"
      scope="activate"
      :version="AGREEMENT_VERSION"
      :subject-id="instanceId"
      @agreed="onAgreementAgreed"
      @cancelled="onAgreementCancelled"
    />
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
.agreement-card { border: 1px solid #f59e0b; background: #fffaf0; }
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
.muted { color: #5b6b82; font-size: 14px; line-height: 1.6; }
.hint { margin: 10px 0 0; color: #7a879c; font-size: 12px; line-height: 1.5; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; word-break: break-all; }
.mono-input :deep(.el-input__inner),
.mono-input input { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.instance-id-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
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
