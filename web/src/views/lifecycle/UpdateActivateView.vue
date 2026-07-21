<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import OperationAgreementDialog from '../../components/OperationAgreementDialog.vue'
import UpdateActivateSiteCard from '../../components/lifecycle/UpdateActivateSiteCard.vue'
import UpdateActivateVersionsCard from '../../components/lifecycle/UpdateActivateVersionsCard.vue'
import UpdateActivateModulesCard from '../../components/lifecycle/UpdateActivateModulesCard.vue'
import {
  bootstrapApi,
  markBootstrapActivated,
  type BootstrapStatus,
} from '../../api/bootstrap'
import {
  updateActivateApi,
  type ModuleCatalogItem,
  type UpgradeStatus,
} from '../../api/updateActivate'
import {
  ensureInstanceId,
  resolveHardwareHash,
  setInstanceId,
} from '../../utils/deviceFingerprint'
import { SITE_TITLE } from '../../config/brand'

const AGREEMENT_VERSION = '2026-07-17'
const AGREEMENT_SCOPE = 'activate'
const AGREEMENT_KEY = `llmgw_op_agreement_${AGREEMENT_SCOPE}_${AGREEMENT_VERSION}`
const DEVICE_NAME_KEY = 'llmgw_device_name'

const instanceId = ref(ensureInstanceId())
const deviceName = ref(readDeviceName())
const hardwareHash = ref('')
const status = ref<BootstrapStatus | null>(null)
const upgrade = ref<UpgradeStatus | null>(null)
const modules = ref<ModuleCatalogItem[]>([])
const loading = ref(false)
const upgrading = ref(false)
const checkingUpgrade = ref(false)
const modulesLoading = ref(false)
const modulesError = ref('')
const activating = ref(false)
const error = ref('')
const message = ref('')
const showAgreement = ref(false)
const agreementAccepted = ref(!!localStorage.getItem(AGREEMENT_KEY))

const canActivate = computed(
  () => !!deviceName.value.trim() && !!instanceId.value.trim() && !!hardwareHash.value,
)

function readDeviceName(): string {
  try {
    return localStorage.getItem(DEVICE_NAME_KEY) || ''
  } catch {
    return ''
  }
}

function persistDeviceName() {
  const name = deviceName.value.trim()
  if (!name) return
  try {
    localStorage.setItem(DEVICE_NAME_KEY, name)
  } catch {
    /* ignore */
  }
}

async function ensureHash() {
  if (hardwareHash.value) return hardwareHash.value
  let serverHash = ''
  let serverInstanceId = ''
  try {
    const fp = await bootstrapApi.fingerprint()
    serverHash = fp.hardware_hash || ''
    serverInstanceId = fp.instance_id || ''
  } catch {
    /* fall back */
  }
  hardwareHash.value = await resolveHardwareHash(serverHash)
  if (serverInstanceId) {
    setInstanceId(serverInstanceId)
    instanceId.value = serverInstanceId
  }
  return hardwareHash.value
}

async function loadStatus() {
  loading.value = true
  error.value = ''
  try {
    await ensureHash()
    status.value = await bootstrapApi.status()
    if (status.value?.instance_id) {
      setInstanceId(status.value.instance_id)
      instanceId.value = status.value.instance_id
    }
    if (status.value?.device_name && !deviceName.value) {
      deviceName.value = status.value.device_name
    }
  } catch (e) {
    error.value = (e as Error).message
    status.value = null
  } finally {
    loading.value = false
  }
}

async function loadUpgrade() {
  upgrading.value = true
  try {
    upgrade.value = await updateActivateApi.upgradeStatus()
  } catch {
    upgrade.value = null
  } finally {
    upgrading.value = false
  }
}

async function loadModules() {
  modulesLoading.value = true
  modulesError.value = ''
  try {
    const res = await updateActivateApi.modulesCatalog()
    modules.value = Array.isArray(res.items) ? res.items : []
  } catch (e) {
    modules.value = []
    modulesError.value = (e as Error).message || '无法加载模块清单'
  } finally {
    modulesLoading.value = false
  }
}

async function refreshAll() {
  await Promise.all([loadStatus(), loadUpgrade(), loadModules()])
}

function onActivateClick() {
  if (!deviceName.value.trim()) {
    error.value = '请先填写注册名称（站点显示名）。'
    return
  }
  if (!instanceId.value.trim()) {
    error.value = '实例 ID 尚未生成，请稍后再试。'
    return
  }
  if (!hardwareHash.value.trim()) {
    error.value = '设备指纹尚未采集，请稍后再试。'
    return
  }
  persistDeviceName()
  // 强制：弹窗确认。未同意 → 不能激活。
  agreementAccepted.value = !!localStorage.getItem(AGREEMENT_KEY)
  if (!agreementAccepted.value) {
    showAgreement.value = true
    return
  }
  void performQuickActivate()
}

async function onAgreementAgreed() {
  agreementAccepted.value = true
  localStorage.setItem(AGREEMENT_KEY, new Date().toISOString())
  showAgreement.value = false
  await performQuickActivate()
}

function onAgreementCancelled() {
  showAgreement.value = false
  // 取消即视为拒绝：清掉残留，确保下次仍要求确认。
  try {
    localStorage.removeItem(AGREEMENT_KEY)
  } catch { /* ignore */ }
  agreementAccepted.value = false
  error.value = '未同意用户协议，无法继续激活。'
}

async function performQuickActivate() {
  activating.value = true
  error.value = ''
  message.value = ''
  try {
    const hash = await ensureHash()
    const result = await bootstrapApi.activateQuick({
      instance_id: instanceId.value.trim(),
      hardware_hash: hash,
      device_name: deviceName.value.trim(),
    })
    if (!result.activated) {
      throw new Error(result.error || result.message || '激活失败')
    }
    markBootstrapActivated()
    message.value = result.message || '已同意并完成激活'
    ElMessage.success(message.value)
    await loadStatus()
  } catch (e) {
    error.value = (e as Error).message
    ElMessage.error(error.value)
  } finally {
    activating.value = false
  }
}

async function onCheckUpgrade() {
  checkingUpgrade.value = true
  try {
    upgrade.value = await updateActivateApi.upgradeCheck()
    if (upgrade.value.has_update) {
      ElMessage.success(`检测到新版本 ${upgrade.value.latest_version}`)
    } else {
      ElMessage.info('当前已是最新版本')
    }
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    checkingUpgrade.value = false
  }
}

function onUpgrade() {
  // Prefer Maintain upgrade panel when co-deployed; else public download.
  window.location.assign('/maintain/upgrade')
}

onMounted(async () => {
  await refreshAll()
  // 未激活场景下，下次访问仍强制弹窗。
  if (!status.value?.activated) {
    await nextTick()
  }
})
</script>

<template>
  <div class="ua-page">
    <header class="ua-page__header">
      <div>
        <p class="eyebrow">UPDATE &amp; ACTIVATE</p>
        <h1>更新与激活</h1>
        <p>{{ SITE_TITLE }} — 站点信息、一键激活、版本升级与已开通模块。</p>
      </div>
      <div class="ua-page__actions">
        <RouterLink class="btn btn-ghost btn-no-arrow" to="/customer/offline-activation">离线激活</RouterLink>
        <button type="button" class="btn btn-secondary btn-sm btn-no-arrow" :disabled="loading" @click="refreshAll">
          刷新
        </button>
      </div>
    </header>

    <el-alert v-if="error" type="error" :title="error" show-icon class="mb" closable @close="error = ''" />
    <el-alert v-if="message" type="success" :title="message" show-icon class="mb" closable @close="message = ''" />

    <div class="ua-grid">
      <UpdateActivateSiteCard
        :status="status"
        :loading="loading"
        :instance-id="instanceId"
        :device-name="deviceName"
      />

      <el-card shadow="never" class="ua-card">
        <template #header>
          <span class="ua-card__title">激活与用户协议</span>
        </template>
        <template v-if="status?.activated">
          <el-result icon="success" title="本机已激活" :sub-title="status.message || '可继续查看版本与模块'" />
        </template>
        <template v-else>
          <p class="hint">
            默认激活只需填写注册名称、点击"同意协议并激活"，系统将弹出用户协议窗口，
            勾选并确认后即可向中心 <code>llm.kxpms.cn</code> 申请 license 并完成本地激活（无需手填 License Key）。
          </p>
          <el-form label-position="top" @submit.prevent="onActivateClick">
            <el-form-item label="注册名称" required>
              <el-input v-model="deviceName" maxlength="64" placeholder="例如：华东机房-网关-01" />
            </el-form-item>
            <el-form-item>
              <button type="button" class="btn btn-primary" :disabled="activating" @click="onActivateClick">
                同意协议并激活
              </button>
            </el-form-item>
          </el-form>
        </template>
      </el-card>

      <UpdateActivateVersionsCard
        :status="upgrade"
        :loading="upgrading"
        :checking="checkingUpgrade"
        @check="onCheckUpgrade"
        @upgrade="onUpgrade"
      />

      <UpdateActivateModulesCard
        :items="modules"
        :loading="modulesLoading"
        :error="modulesError"
      />
    </div>

    <OperationAgreementDialog
      v-model="showAgreement"
      :scope="AGREEMENT_SCOPE"
      :version="AGREEMENT_VERSION"
      :subject-id="instanceId"
      @agreed="onAgreementAgreed"
      @cancelled="onAgreementCancelled"
    />
  </div>
</template>

<style scoped>
.ua-page { padding: 24px clamp(16px, 3vw, 32px) 40px; max-width: 1100px; }
.ua-page__header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  margin-bottom: 20px;
}
.eyebrow {
  margin: 0 0 6px;
  color: var(--accent-h);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
}
.ua-page__header h1 { margin: 0 0 8px; font-size: 24px; }
.ua-page__header p { margin: 0; color: var(--muted); font-size: 14px; line-height: 1.6; }
.ua-page__actions { display: flex; gap: 8px; flex-wrap: wrap; }
.ua-grid {
  display: grid;
  gap: 16px;
  grid-template-columns: 1fr;
}
.ua-card__title { font-weight: 600; }
.hint { margin: 0 0 16px; color: var(--muted); font-size: 13px; line-height: 1.6; }
.mb { margin-bottom: 12px; }
@media (min-width: 900px) {
  .ua-grid { grid-template-columns: 1fr 1fr; }
  .ua-grid > :nth-child(3),
  .ua-grid > :nth-child(4) { grid-column: 1 / -1; }
}
</style>
