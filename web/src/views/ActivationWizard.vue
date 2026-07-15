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
import PublicPortalLayout from '../components/PublicPortalLayout.vue'
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
const trialAgreed = ref(false)
const offlineForm = ref({ signed_license: '', request_id: '', activation_code: '' })
const lastResult = ref<ActivationResult | null>(null)
const offlineRequestResult = ref<{ request_id: string; signed_request: string } | null>(null)
const trialAgreementVersion = '2026-07-15'

const comparisonRows = computed(() => [
  {
    feature: t('customer.wizard.compare.rows.console'),
    inactive: t('customer.wizard.compare.inactiveLimited'),
    active: t('customer.wizard.compare.activeFull'),
  },
  {
    feature: t('customer.wizard.compare.rows.api'),
    inactive: t('customer.wizard.compare.inactiveHealth'),
    active: t('customer.wizard.compare.activeAll'),
  },
  {
    feature: t('customer.wizard.compare.rows.trial'),
    inactive: t('customer.wizard.compare.inactiveTrial'),
    active: t('customer.wizard.compare.activeTrial'),
  },
])

const showDeviceLimit = computed(() =>
  lastResult.value?.need_deactivate === true
  || lastResult.value?.error_code === 'device_limit_exceeded',
)

function activationErrorMessage(result: ActivationResult): string {
  const code = result.error_code
  if (code) {
    const key = `customer.wizard.errorCodes.${code}`
    const translated = t(key)
    if (translated !== key) return translated
  }
  return result.message || t('customer.wizard.messages.activateFailed')
}

function deviceLimitBody(): string {
  const active = lastResult.value?.active_devices?.length ?? 0
  const max = lastResult.value?.max_devices ?? active
  return t('customer.wizard.messages.deviceLimitBody', { active, max })
}

async function handleActivationResult(result: ActivationResult) {
  lastResult.value = result
  if (result.success) {
    ElMessage.success(t('customer.wizard.messages.activateSuccess'))
    await refresh()
    step.value = 4
    return
  }
  if (result.need_deactivate || result.error_code === 'device_limit_exceeded') {
    await ElMessageBox.confirm(
      deviceLimitBody(),
      t('customer.wizard.messages.deviceLimitTitle'),
      { confirmButtonText: t('customer.wizard.messages.deviceLimitOk') },
    )
    return
  }
  ElMessage.error(activationErrorMessage(result))
}

const trialReady = computed(() => trialEmail.value.includes('@') && trialAgreed.value)

const stateLabel = computed(() => {
  switch (status.value?.state) {
    case 'active': return t('customer.wizard.states.active')
    case 'grace': return t('customer.wizard.states.grace')
    case 'expired': return t('customer.wizard.states.expired')
    case 'revoked': return t('customer.wizard.states.revoked')
    case 'none':
    default:
      return t('customer.wizard.states.none')
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
    ElMessage.error(t('customer.wizard.messages.statusLoadFailed', { msg: (err as Error).message }))
  } finally {
    loading.value = false
  }
}

async function handleActivate() {
  if (!onlineForm.value.license_key.trim()) {
    ElMessage.warning(t('customer.wizard.messages.enterLicenseKey'))
    return
  }
  loading.value = true
  try {
    await handleActivationResult(await activateLicense(onlineForm.value))
  } catch (err) {
    ElMessage.error(t('customer.wizard.messages.activateFailedWithMsg', { msg: (err as Error).message }))
  } finally {
    loading.value = false
  }
}

async function handleTrial() {
  if (!trialEmail.value.trim() || !trialEmail.value.includes('@')) {
    ElMessage.warning(t('customer.wizard.messages.trialEmailInvalid'))
    return
  }
  if (!trialAgreed.value) {
    ElMessage.warning(t('customer.wizard.messages.trialConsentRequired'))
    return
  }
  loading.value = true
  try {
    const result = await requestTrial({ email: trialEmail.value.trim(), agree: true })
    if (!result.success || !result.license_key) {
      ElMessage.error(result.message || t('customer.wizard.messages.trialFailed'))
      return
    }
    onlineForm.value.license_key = result.license_key
    ElMessage.success(t('customer.wizard.messages.trialCreated'))
    await handleActivationResult(await activateLicense({
      license_key: result.license_key,
      device_name: onlineForm.value.device_name || undefined,
    }))
    if (lastResult.value?.success) {
      return
    }
    if (lastResult.value?.need_deactivate || lastResult.value?.error_code === 'device_limit_exceeded') {
      step.value = 2
      return
    }
    ElMessage.warning(t('customer.wizard.messages.trialActivateFailed'))
    step.value = 2
  } catch (err) {
    ElMessage.error(t('customer.wizard.messages.trialFailedWithMsg', { msg: (err as Error).message }))
  } finally {
    loading.value = false
  }
}

async function handleOfflineActivate() {
  if (!offlineForm.value.signed_license.trim() || !offlineForm.value.request_id.trim() || !offlineForm.value.activation_code.trim()) {
    ElMessage.warning(t('customer.wizard.messages.fillOfflineFields'))
    return
  }
  loading.value = true
  try {
    const result = await offlineActivate(offlineForm.value)
    if (result.success) {
      ElMessage.success(t('customer.wizard.messages.offlineSuccess'))
      await refresh()
      step.value = 4
    } else {
      ElMessage.error(result.message || t('customer.wizard.messages.offlineFailed'))
    }
  } catch (err) {
    ElMessage.error(t('customer.wizard.messages.offlineFailedWithMsg', { msg: (err as Error).message }))
  } finally {
    loading.value = false
  }
}

async function handleCreateOfflineRequest() {
  if (!onlineForm.value.license_key.trim()) {
    ElMessage.warning(t('customer.wizard.messages.enterKeyForOffline'))
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
    ElMessage.success(t('customer.wizard.messages.offlineRequestCreated'))
    step.value = 3
  } catch (err) {
    ElMessage.error(t('customer.wizard.messages.offlineRequestFailed', { msg: (err as Error).message }))
  } finally {
    loading.value = false
  }
}

function copyToClipboard(text: string) {
  if (!text) return
  navigator.clipboard.writeText(text).then(
    () => ElMessage.success(t('customer.wizard.messages.copied')),
    () => ElMessage.error(t('customer.wizard.messages.copyFailed')),
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
  <PublicPortalLayout
    :title="t('customer.wizard.headerTitle')"
    :subtitle="t('customer.wizard.headerSubtitle')"
    :kicker="t('public.download.activateLink')"
  >
  <div class="activation-wizard" v-loading="loading">
    <el-card class="wizard-header pub-card" shadow="never">
      <div class="header-row">
        <el-tag :type="stateColor as any" size="large" effect="dark">
          {{ stateLabel }}
        </el-tag>
      </div>
      <div v-if="status?.hardware_hash" class="device-id-row">
        <span>{{ t('customer.wizard.deviceId.label') }}</span>
        <code>{{ status.hardware_hash }}</code>
        <el-button link size="small" @click="copyToClipboard(status.hardware_hash!)">
          {{ t('customer.wizard.deviceId.copy') }}
        </el-button>
      </div>
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="flow-alert"
        :title="t('customer.wizard.flow.title')"
        :description="t('customer.wizard.flow.desc')"
      />
      <el-table :data="comparisonRows" size="small" class="compare-table">
        <el-table-column :label="t('customer.wizard.compare.feature')" prop="feature" />
        <el-table-column :label="t('customer.wizard.compare.inactive')" prop="inactive" />
        <el-table-column :label="t('customer.wizard.compare.active')" prop="active" />
      </el-table>
      <div v-if="status?.state === 'active'" class="status-summary">
        <el-descriptions :column="3" border size="small">
          <el-descriptions-item :label="t('customer.wizard.status.customer')">{{ status.customer_name || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('customer.wizard.status.tier')">{{ status.subscription_tier || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('customer.wizard.status.expiresAt')">{{ status.expires_at || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('customer.wizard.status.daysRemaining')">{{ status.days_remaining ?? '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('customer.wizard.status.licenseKey')" :span="2">
            <code>{{ status.license_key || '—' }}</code>
          </el-descriptions-item>
        </el-descriptions>
      </div>
    </el-card>

    <el-steps :active="step - 1" finish-status="success" class="wizard-steps">
      <el-step :title="t('customer.wizard.steps.status.title')" :description="t('customer.wizard.steps.status.description')" />
      <el-step :title="t('customer.wizard.steps.online.title')" :description="t('customer.wizard.steps.online.description')" />
      <el-step :title="t('customer.wizard.steps.offline.title')" :description="t('customer.wizard.steps.offline.description')" />
      <el-step :title="t('customer.wizard.steps.done.title')" :description="t('customer.wizard.steps.done.description')" />
    </el-steps>

    <!-- Step 1: Status overview -->
    <el-card v-if="step === 1" class="step-card">
      <template #header>
        <span class="card-title">{{ t('customer.wizard.step1.title') }}</span>
      </template>
      <div v-if="status?.state === 'none'">
        <el-alert
          type="info"
          :closable="false"
          show-icon
          :title="t('customer.wizard.step1.noneTitle')"
          :description="t('customer.wizard.step1.noneDesc')"
        />
        <div class="cta-row">
          <el-button type="primary" size="large" :disabled="!trialReady" :loading="loading" @click="handleTrial">
            {{ t('customer.wizard.step1.trialCta') }}
          </el-button>
          <el-button type="primary" size="large" @click="step = 2">
            {{ t('customer.wizard.step1.onlineActivate') }}
          </el-button>
          <el-button size="large" @click="step = 3">
            {{ t('customer.wizard.step1.offlineActivate') }}
          </el-button>
        </div>
        <el-input
          v-model="trialEmail"
          class="trial-email"
          type="email"
          :placeholder="t('customer.wizard.step1.trialEmail')"
          clearable
        />
        <el-checkbox v-model="trialAgreed" class="trial-consent">
          {{ t('customer.wizard.step1.trialConsent') }}
          <a href="/user-agreement.html" target="_blank" rel="noopener">{{ t('customer.wizard.step1.trialAgreement') }}</a>
        </el-checkbox>
        <p class="trial-version">{{ t('customer.wizard.step1.trialAgreementVersion', { version: trialAgreementVersion }) }}</p>
      </div>
      <div v-else-if="status?.state === 'expired'">
        <el-alert
          type="error"
          :closable="false"
          show-icon
          :title="t('customer.wizard.step1.expiredTitle')"
          :description="t('customer.wizard.step1.expiredDesc')"
        />
        <div class="cta-row">
          <el-button type="primary" @click="step = 2">{{ t('customer.wizard.step1.reactivate') }}</el-button>
          <el-button @click="step = 3">{{ t('customer.wizard.step1.offlineActivate') }}</el-button>
        </div>
      </div>
      <div v-else-if="status?.state === 'grace'">
        <el-alert
          type="warning"
          :closable="false"
          show-icon
          :title="t('customer.wizard.step1.graceTitle')"
          :description="t('customer.wizard.step1.graceDesc', { days: status.grace_days_left ?? 0 })"
        />
        <div class="cta-row">
          <el-button type="primary" @click="step = 2">{{ t('customer.wizard.step1.renewNow') }}</el-button>
          <el-button @click="resetWizard">{{ t('customer.wizard.step1.later') }}</el-button>
        </div>
      </div>
      <div v-else-if="status?.state === 'active'">
        <el-alert type="success" :closable="false" show-icon :title="t('customer.wizard.step1.activeTitle')" :description="t('customer.wizard.step1.activeDesc')" />
      </div>
    </el-card>

    <!-- Step 2: Online activation -->
    <el-card v-if="step === 2" class="step-card">
      <template #header>
        <span class="card-title">{{ t('customer.wizard.step2.title') }}</span>
      </template>
      <el-form :model="onlineForm" label-position="top">
        <el-form-item :label="t('customer.wizard.step2.licenseKey')" required>
          <el-input
            v-model="onlineForm.license_key"
            :placeholder="t('customer.wizard.step2.licenseKeyPlaceholder')"
            clearable
          />
        </el-form-item>
        <el-form-item :label="t('customer.wizard.step2.deviceName')">
          <el-input v-model="onlineForm.device_name" :placeholder="t('customer.wizard.step2.deviceNamePlaceholder')" />
        </el-form-item>
      </el-form>
      <div class="cta-row">
        <el-button @click="step = 1">{{ t('customer.wizard.step2.prev') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleActivate">
          {{ t('customer.wizard.step2.activate') }}
        </el-button>
        <el-button @click="handleCreateOfflineRequest" :loading="loading">
          {{ t('customer.wizard.step2.createOfflineRequest') }}
        </el-button>
      </div>

      <el-alert
        v-if="showDeviceLimit"
        type="warning"
        :closable="false"
        show-icon
        class="device-limit-alert"
        :title="t('customer.wizard.messages.deviceLimitTitle')"
        :description="deviceLimitBody()"
      />
      <el-table
        v-if="showDeviceLimit && lastResult?.active_devices?.length"
        :data="lastResult.active_devices"
        size="small"
        class="device-limit-table"
      >
        <el-table-column :label="t('customer.wizard.deviceTable.deviceName')" prop="device_name" />
        <el-table-column :label="t('customer.wizard.deviceTable.instanceId')" prop="instance_id" />
        <el-table-column :label="t('customer.wizard.deviceTable.lastHeartbeat')" prop="last_heartbeat" />
      </el-table>
    </el-card>

    <!-- Step 3: Offline activation -->
    <el-card v-if="step === 3" class="step-card">
      <template #header>
        <span class="card-title">{{ t('customer.wizard.step3.title') }}</span>
      </template>

      <div v-if="offlineRequestResult" class="offline-request-block">
        <el-alert type="success" :closable="false" show-icon>
          <template #title>{{ t('customer.wizard.step3.requestGenerated') }}</template>
          <template #default>
            {{ t('customer.wizard.step3.submitHint') }}
            <pre class="signed-request">{{ offlineRequestResult.signed_request }}</pre>
            <div class="cta-row">
              <el-button size="small" @click="copyToClipboard(offlineRequestResult.signed_request)">
                {{ t('customer.wizard.step3.copyRequest') }}
              </el-button>
              <el-button size="small" @click="copyToClipboard(offlineRequestResult.request_id)">
                {{ t('customer.wizard.step3.copyRequestId') }}
              </el-button>
            </div>
          </template>
        </el-alert>
      </div>

      <el-divider content-position="left">{{ t('customer.wizard.step3.inputResult') }}</el-divider>

      <el-form :model="offlineForm" label-position="top">
        <el-form-item :label="t('customer.wizard.step3.signedLicense')">
          <el-input
            v-model="offlineForm.signed_license"
            type="textarea"
            :rows="6"
            :placeholder="t('customer.wizard.step3.signedLicensePlaceholder')"
          />
        </el-form-item>
        <el-form-item :label="t('customer.wizard.step3.requestId')">
          <el-input v-model="offlineForm.request_id" :placeholder="t('customer.wizard.step3.requestIdPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('customer.wizard.step3.activationCode')">
          <el-input v-model="offlineForm.activation_code" :placeholder="t('customer.wizard.step3.activationCodePlaceholder')" />
        </el-form-item>
      </el-form>
      <div class="cta-row">
        <el-button @click="step = 2">{{ t('customer.wizard.step3.prev') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleOfflineActivate">
          {{ t('customer.wizard.step3.apply') }}
        </el-button>
      </div>
    </el-card>

    <!-- Step 4: Done -->
    <el-card v-if="step === 4" class="step-card">
      <template #header>
        <span class="card-title">{{ t('customer.wizard.step4.title') }}</span>
      </template>
      <el-result
        icon="success"
        :title="t('customer.wizard.step4.successTitle')"
        :sub-title="t('customer.wizard.step4.successSubtitle')"
      >
        <template #extra>
          <el-button type="primary" @click="resetWizard">{{ t('customer.wizard.step4.recheck') }}</el-button>
          <el-button @click="$router.push('/')">{{ t('customer.wizard.step4.goHome') }}</el-button>
        </template>
      </el-result>
    </el-card>
  </div>
  </PublicPortalLayout>
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
.trial-email { margin-top: 16px; }
.trial-consent { margin-top: 12px; display: block; }
.trial-version {
  margin: 8px 0 0;
  font-size: 12px;
  color: #909399;
}
.device-limit-alert { margin-top: 16px; }
.device-limit-table { margin-top: 12px; }
.device-id-row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 12px 0; font-size: 13px; color: #94a3b8; }
.device-id-row code { word-break: break-all; color: #cbd5e1; }
.flow-alert { margin: 12px 0; }
.compare-table { margin-top: 8px; }
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
