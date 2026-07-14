<script setup lang="ts">
// ActivationWizard.vue — Customer-facing multi-step activation flow (2026-07-13)
//
// Step 1 — Welcome & current status preview
// Step 2 — Online activation (paste license_key, optional device name)
// Step 3 — Offline activation (paste signed_license + activation_code)
// Step 4 — Confirmation + next-steps
//
// Step 2 and 3 are alternative flows; the user picks one. The wizard is
// designed to be reachable when state === 'none' from any other page.

import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import {
  getLicenseStatus,
  activateLicense,
  requestTrial,
  offlineActivate,
  createOfflineRequest,
  type CustomerLicenseStatus,
  type ActivationResult,
} from '../api/customer'

const { t } = useI18n()

const step = ref(1)
const status = ref<CustomerLicenseStatus | null>(null)
const loading = ref(false)
const onlineForm = ref({ license_key: '', device_name: '' })
const trialEmail = ref('')
const offlineForm = ref({ signed_license: '', request_id: '', activation_code: '' })
const lastResult = ref<ActivationResult | null>(null)
const offlineRequestResult = ref<{ request_id: string; signed_request: string } | null>(null)

const stateLabel = computed(() => {
  switch (status.value?.state) {
    case 'active': return t('customer.wizard.states.active', '已激活')
    case 'grace': return t('customer.wizard.states.grace', '宽限期')
    case 'expired': return t('customer.wizard.states.expired', '已过期')
    case 'revoked': return t('customer.wizard.states.revoked', '已吊销')
    case 'none':
    default:
      return t('customer.wizard.states.none', '未激活')
  }
})

const stateColor = computed(() => {
  switch (status.value?.state) {
    case 'active': return 'success'
    case 'grace': return 'warning'
    case 'expired':
    case 'revoked': return 'danger'
    default: return 'info'
  }
})

async function refresh() {
  loading.value = true
  try {
    status.value = await getLicenseStatus()
    if (status.value?.state === 'active') {
      step.value = 4
    }
  } catch (err) {
    ElMessage.error(`查询授权状态失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
  }
}

async function handleActivate() {
  if (!onlineForm.value.license_key.trim()) {
    ElMessage.warning('请输入 License Key')
    return
  }
  loading.value = true
  try {
    lastResult.value = await activateLicense(onlineForm.value)
    if (lastResult.value.success) {
      ElMessage.success('激活成功！')
      await refresh()
      step.value = 4
    } else if (lastResult.value.need_deactivate) {
      ElMessageBox.confirm(
        '设备数量已达上限，请先在管理后台停用一个设备后再激活。',
        '设备已达上限',
        { confirmButtonText: '我知道了' },
      )
    } else {
      ElMessage.error(lastResult.value.message || '激活失败')
    }
  } catch (err) {
    ElMessage.error(`激活失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
  }
}

async function handleTrial() {
  if (!trialEmail.value.trim() || !trialEmail.value.includes('@')) {
    ElMessage.warning('请输入有效邮箱')
    return
  }
  loading.value = true
  try {
    const result = await requestTrial({ email: trialEmail.value.trim() })
    if (!result.success || !result.license_key) {
      ElMessage.error(result.message || '试用申请失败')
      return
    }
    onlineForm.value.license_key = result.license_key
    ElMessage.success('试用 License 已创建，请继续完成激活')
    step.value = 2
  } catch (err) {
    ElMessage.error(`试用申请失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
  }
}

async function handleOfflineActivate() {
  if (!offlineForm.value.signed_license.trim() || !offlineForm.value.request_id.trim() || !offlineForm.value.activation_code.trim()) {
    ElMessage.warning('请填写签名 License、Request ID 和 Activation Code')
    return
  }
  loading.value = true
  try {
    const result = await offlineActivate(offlineForm.value)
    if (result.success) {
      ElMessage.success('离线激活成功！')
      await refresh()
      step.value = 4
    } else {
      ElMessage.error(result.message || '离线激活失败')
    }
  } catch (err) {
    ElMessage.error(`离线激活失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
  }
}

async function handleCreateOfflineRequest() {
  if (!onlineForm.value.license_key.trim()) {
    ElMessage.warning('请输入 License Key 以生成离线请求')
    return
  }
  loading.value = true
  try {
    const result = await createOfflineRequest({
      license_key: onlineForm.value.license_key,
      device_name: onlineForm.value.device_name,
    })
    offlineRequestResult.value = {
      request_id: result.request_id,
      signed_request: result.signed_request,
    }
    ElMessage.success('已生成离线请求，请提交给 License Authority 审批')
    step.value = 3
  } catch (err) {
    ElMessage.error(`生成离线请求失败: ${(err as Error).message}`)
  } finally {
    loading.value = false
  }
}

function copyToClipboard(text: string) {
  if (!text) return
  navigator.clipboard.writeText(text).then(
    () => ElMessage.success('已复制到剪贴板'),
    () => ElMessage.error('复制失败，请手动选择'),
  )
}

function resetWizard() {
  step.value = 1
  lastResult.value = null
  offlineRequestResult.value = null
  onlineForm.value = { license_key: '', device_name: '' }
  offlineForm.value = { signed_license: '', request_id: '', activation_code: '' }
  refresh()
}

onMounted(refresh)
</script>

<template>
  <div class="activation-wizard" v-loading="loading">
    <el-card class="wizard-header" shadow="never">
      <div class="header-row">
        <div>
          <h2>License 激活向导</h2>
          <p class="subtitle">通过 4 步完成首次激活或重新激活</p>
        </div>
        <el-tag :type="stateColor as any" size="large" effect="dark">
          {{ stateLabel }}
        </el-tag>
      </div>
      <div v-if="status?.state === 'active'" class="status-summary">
        <el-descriptions :column="3" border size="small">
          <el-descriptions-item label="客户">{{ status.customer_name || '—' }}</el-descriptions-item>
          <el-descriptions-item label="订阅层级">{{ status.subscription_tier || '—' }}</el-descriptions-item>
          <el-descriptions-item label="到期时间">{{ status.expires_at || '—' }}</el-descriptions-item>
          <el-descriptions-item label="剩余天数">{{ status.days_remaining ?? '—' }}</el-descriptions-item>
          <el-descriptions-item label="License Key" :span="2">
            <code>{{ status.license_key || '—' }}</code>
          </el-descriptions-item>
        </el-descriptions>
      </div>
    </el-card>

    <el-steps :active="step - 1" finish-status="success" class="wizard-steps">
      <el-step title="当前状态" description="查看授权情况" />
      <el-step title="输入 License" description="在线激活" />
      <el-step title="离线激活" description="无网络环境" />
      <el-step title="完成" description="激活成功" />
    </el-steps>

    <!-- Step 1: Status overview -->
    <el-card v-if="step === 1" class="step-card">
      <template #header>
        <span class="card-title">第 1 步：当前授权状态</span>
      </template>
      <div v-if="status?.state === 'none'">
        <el-alert
          type="info"
          :closable="false"
          show-icon
          title="尚未激活 License"
          description="本机首次启动后，需要完成激活才能解锁全部功能。在线激活需要 License Authority 可达；离线激活适用于隔离网络环境。"
        />
        <div class="cta-row">
          <el-button type="primary" size="large" @click="handleTrial">
            申请试用
          </el-button>
          <el-button type="primary" size="large" @click="step = 2">
            在线激活
          </el-button>
          <el-button size="large" @click="step = 3">
            离线激活
          </el-button>
        </div>
        <el-input
          v-model="trialEmail"
          class="trial-email"
          type="email"
          placeholder="用于接收试用信息的邮箱"
          clearable
        />
      </div>
      <div v-else-if="status?.state === 'expired'">
        <el-alert
          type="error"
          :closable="false"
          show-icon
          title="License 已过期"
          description="服务已进入受限模式（仅 /api/system/license/* 与健康检查可用）。请联系您的 License Authority 续期。"
        />
        <div class="cta-row">
          <el-button type="primary" @click="step = 2">重新激活</el-button>
          <el-button @click="step = 3">离线激活</el-button>
        </div>
      </div>
      <div v-else-if="status?.state === 'grace'">
        <el-alert
          type="warning"
          :closable="false"
          show-icon
          title="License 处于宽限期"
          :description="`还剩 ${status.grace_days_left ?? 0} 天，请尽快续期`"
        />
        <div class="cta-row">
          <el-button type="primary" @click="step = 2">立即续期</el-button>
          <el-button @click="resetWizard">稍后处理</el-button>
        </div>
      </div>
      <div v-else-if="status?.state === 'active'">
        <el-alert type="success" :closable="false" show-icon title="License 有效" description="无需再次激活。" />
      </div>
    </el-card>

    <!-- Step 2: Online activation -->
    <el-card v-if="step === 2" class="step-card">
      <template #header>
        <span class="card-title">第 2 步：输入 License Key 进行在线激活</span>
      </template>
      <el-form :model="onlineForm" label-position="top">
        <el-form-item label="License Key" required>
          <el-input
            v-model="onlineForm.license_key"
            placeholder="LIC-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
            clearable
          />
        </el-form-item>
        <el-form-item label="设备名称（可选）">
          <el-input v-model="onlineForm.device_name" placeholder="例如：production-cluster-01" />
        </el-form-item>
      </el-form>
      <div class="cta-row">
        <el-button @click="step = 1">上一步</el-button>
        <el-button type="primary" :loading="loading" @click="handleActivate">
          立即激活
        </el-button>
        <el-button @click="handleCreateOfflineRequest" :loading="loading">
          生成离线请求
        </el-button>
      </div>
    </el-card>

    <!-- Step 3: Offline activation -->
    <el-card v-if="step === 3" class="step-card">
      <template #header>
        <span class="card-title">第 3 步：离线激活</span>
      </template>

      <div v-if="offlineRequestResult" class="offline-request-block">
        <el-alert type="success" :closable="false" show-icon>
          <template #title>离线请求已生成</template>
          <template #default>
            请将以下内容提交给 License Authority 审批：
            <pre class="signed-request">{{ offlineRequestResult.signed_request }}</pre>
            <div class="cta-row">
              <el-button size="small" @click="copyToClipboard(offlineRequestResult.signed_request)">
                复制请求内容
              </el-button>
              <el-button size="small" @click="copyToClipboard(offlineRequestResult.request_id)">
                复制 Request ID
              </el-button>
            </div>
          </template>
        </el-alert>
      </div>

      <el-divider content-position="left">输入审批结果</el-divider>

      <el-form :model="offlineForm" label-position="top">
        <el-form-item label="签名后的 License（必填）">
          <el-input
            v-model="offlineForm.signed_license"
            type="textarea"
            :rows="6"
            placeholder="粘贴 License Authority 审批后返回的 base64 签名 License"
          />
        </el-form-item>
        <el-form-item label="Request ID（必填）">
          <el-input v-model="offlineForm.request_id" placeholder="粘贴审批对应的 Request ID" />
        </el-form-item>
        <el-form-item label="Activation Code（必填）">
          <el-input v-model="offlineForm.activation_code" placeholder="例如：ABCD2345" />
        </el-form-item>
      </el-form>
      <div class="cta-row">
        <el-button @click="step = 2">上一步</el-button>
        <el-button type="primary" :loading="loading" @click="handleOfflineActivate">
          应用离线 License
        </el-button>
      </div>
    </el-card>

    <!-- Step 4: Done -->
    <el-card v-if="step === 4" class="step-card">
      <template #header>
        <span class="card-title">第 4 步：激活完成</span>
      </template>
      <el-result
        icon="success"
        title="License 激活成功"
        sub-title="您现在可以开始使用本机的全部功能。"
      >
        <template #extra>
          <el-button type="primary" @click="resetWizard">重新检查</el-button>
          <el-button @click="$router.push('/')">返回首页</el-button>
        </template>
      </el-result>
    </el-card>
  </div>
</template>

<style scoped>
.activation-wizard {
  max-width: 960px;
  margin: 0 auto;
  padding: 24px;
}
.wizard-header { margin-bottom: 24px; }
.header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
}
.subtitle { color: #909399; margin: 4px 0 0; }
.status-summary { margin-top: 16px; }
.wizard-steps { margin: 24px 0; }
.step-card { margin-bottom: 16px; }
.card-title { font-weight: 600; }
.cta-row {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 16px;
}
.signed-request {
  background: #f5f7fa;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  padding: 12px;
  font-size: 12px;
  word-break: break-all;
  max-height: 160px;
  overflow: auto;
  margin: 12px 0;
}
</style>
