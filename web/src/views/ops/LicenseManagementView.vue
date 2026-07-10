<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getLicenses,
  createLicense,
  revokeLicense,
  getLicenseDevices,
  getOfflineActivationRequests,
  approveOfflineActivation,
  rejectOfflineActivation,
  type License,
  type LicenseDevice,
  type OfflineActivationRequest,
} from '../../api/ops'

const { t } = useI18n()

const licenses = ref<License[]>([])
const devices = ref<Record<number, LicenseDevice[]>>({})
const offlineRequests = ref<OfflineActivationRequest[]>([])
const loading = ref(false)
const expandedRows = ref<number[]>([])

// Create dialog state
const showCreateDialog = ref(false)
const createForm = ref({
  customer: '',
  max_devices: 5,
  expires_at: '',
})

async function load() {
  loading.value = true
  try {
    licenses.value = await getLicenses()
  } catch (error) {
    ElMessage.error(t('ops.license.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function loadOfflineRequests() {
  try {
    offlineRequests.value = await getOfflineActivationRequests()
  } catch (error) {
    console.error('Failed to load offline requests:', error)
  }
}

async function handleCreate() {
  if (!createForm.value.customer || !createForm.value.expires_at) {
    ElMessage.warning(t('ops.license.fillRequired'))
    return
  }

  loading.value = true
  try {
    // Element Plus el-date-picker 返回 RFC3339 格式（value-format）
    await createLicense({
      customer: createForm.value.customer,
      max_devices: createForm.value.max_devices,
      expires_at: createForm.value.expires_at,
    })
    ElMessage.success(t('ops.license.createSuccess'))
    showCreateDialog.value = false
    createForm.value = { customer: '', max_devices: 5, expires_at: '' }
    await load()
  } catch (error) {
    ElMessage.error(t('ops.license.createFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleRevoke(license: License) {
  try {
    await ElMessageBox.confirm(
      t('ops.license.revokeConfirm', { customer: license.customer }),
      t('common.warning'),
      { type: 'warning' }
    )
    await revokeLicense(license.license_key)
    ElMessage.success(t('ops.license.revokeSuccess'))
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.license.revokeFailed'))
      console.error(error)
    }
  }
}

async function handleExpandChange(row: License) {
  const idx = expandedRows.value.indexOf(row.id)
  if (idx > -1) {
    expandedRows.value.splice(idx, 1)
    return
  }

  expandedRows.value.push(row.id)
  if (!devices.value[row.id]) {
    try {
      devices.value[row.id] = await getLicenseDevices(row.license_key)
    } catch (error) {
      ElMessage.error(t('ops.license.loadDevicesFailed'))
      console.error(error)
    }
  }
}

async function handleApproveOffline(request: OfflineActivationRequest) {
  loading.value = true
  try {
    const result = await approveOfflineActivation(request.request_code)
    ElMessage.success(t('ops.license.approveSuccess'))
    ElMessageBox.alert(
      `${t('ops.license.activationCode')}: ${result.activation_code}`,
      t('ops.license.offlineActivation'),
      { type: 'success' }
    )
    await loadOfflineRequests()
  } catch (error) {
    ElMessage.error(t('ops.license.approveFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleRejectOffline(request: OfflineActivationRequest) {
  try {
    const { value: reason } = await ElMessageBox.prompt(
      t('ops.license.rejectReason'),
      t('ops.license.rejectTitle'),
      { inputType: 'textarea' }
    )
    await rejectOfflineActivation(request.request_code, reason)
    ElMessage.success(t('ops.license.rejectSuccess'))
    await loadOfflineRequests()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.license.rejectFailed'))
      console.error(error)
    }
  }
}

function statusType(status: string) {
  const map: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
    active: 'success',
    expired: 'warning',
    revoked: 'danger',
  }
  return map[status] || 'info'
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}

onMounted(() => {
  load()
  loadOfflineRequests()
})
</script>

<template>
  <div class="license-management-view">
    <div class="page-header">
      <h1>🔑 {{ t('ops.license.title') }}</h1>
      <el-button type="primary" @click="showCreateDialog = true">
        + {{ t('ops.license.create') }}
      </el-button>
    </div>

    <!-- Offline Activation Requests -->
    <el-card v-if="offlineRequests.length > 0" class="offline-requests-card" shadow="never">
      <template #header>
        <div class="card-header">
          <span>{{ t('ops.license.offlineRequests') }}</span>
          <el-badge :value="offlineRequests.filter(r => r.status === 'pending').length" />
        </div>
      </template>
      <el-table :data="offlineRequests" size="small">
        <el-table-column prop="license_key" :label="t('ops.license.licenseKey')" width="200" />
        <el-table-column prop="device_id" :label="t('ops.license.deviceId')" width="150" />
        <el-table-column prop="request_code" :label="t('ops.license.requestCode')" />
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="row.status === 'pending' ? 'warning' : 'success'" size="small">
              {{ t(`ops.license.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('common.createdAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="180" fixed="right">
          <template #default="{ row }">
            <template v-if="row.status === 'pending'">
              <el-button type="success" size="small" @click="handleApproveOffline(row)">
                {{ t('ops.license.approve') }}
              </el-button>
              <el-button type="danger" size="small" @click="handleRejectOffline(row)">
                {{ t('ops.license.reject') }}
              </el-button>
            </template>
            <el-tag v-else type="info" size="small">{{ t(`ops.license.status.${row.status}`) }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Licenses Table -->
    <el-card class="main-card" shadow="never">
      <el-table
        v-loading="loading"
        :data="licenses"
        :row-key="(row: License) => row.id"
        :expand-row-keys="expandedRows"
        @expand-change="handleExpandChange"
      >
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="expanded-content">
              <h4>{{ t('ops.license.devices') }}</h4>
              <el-table v-if="devices[row.id]" :data="devices[row.id]" size="small">
                <el-table-column prop="device_id" :label="t('ops.license.deviceId')" />
                <el-table-column prop="hostname" :label="t('ops.license.hostname')" />
                <el-table-column prop="activated_at" :label="t('ops.license.activatedAt')">
                  <template #default="{ row: device }">{{ formatDate(device.activated_at) }}</template>
                </el-table-column>
                <el-table-column prop="last_seen" :label="t('ops.license.lastSeen')">
                  <template #default="{ row: device }">{{ formatDate(device.last_seen) }}</template>
                </el-table-column>
              </el-table>
              <div v-else class="loading-devices">{{ t('common.loading') }}</div>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="license_key" :label="t('ops.license.licenseKey')" width="200" />
        <el-table-column prop="customer" :label="t('ops.license.customer')" width="150" />
        <el-table-column :label="t('ops.license.devices')" width="120">
          <template #default="{ row }">
            {{ row.active_devices }} / {{ row.max_devices }}
          </template>
        </el-table-column>
        <el-table-column prop="expires_at" :label="t('ops.license.expiresAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.expires_at) }}</template>
        </el-table-column>
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.license.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('common.createdAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="120" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'active'"
              type="danger"
              size="small"
              @click="handleRevoke(row)"
            >
              {{ t('ops.license.revoke') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Create License Dialog -->
    <el-dialog
      v-model="showCreateDialog"
      :title="t('ops.license.createTitle')"
      width="500px"
    >
      <el-form :model="createForm" label-width="120px">
        <el-form-item :label="t('ops.license.customer')" required>
          <el-input v-model="createForm.customer" :placeholder="t('ops.license.customerPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.license.maxDevices')" required>
          <el-input-number v-model="createForm.max_devices" :min="1" :max="100" />
        </el-form-item>
        <el-form-item :label="t('ops.license.expiresAt')" required>
          <el-date-picker
            v-model="createForm.expires_at"
            type="datetime"
            :placeholder="t('ops.license.selectDate')"
            value-format="YYYY-MM-DDTHH:mm:ssZ"
            style="width: 100%"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreateDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreate">
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.license-management-view {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.page-header h1 {
  font-size: 24px;
  margin: 0;
}

.offline-requests-card {
  margin-bottom: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.main-card {
  margin-top: 20px;
}

.expanded-content {
  padding: 20px;
  background-color: var(--el-fill-color-light);
}

.expanded-content h4 {
  margin: 0 0 12px 0;
  font-size: 14px;
  font-weight: 600;
}

.loading-devices {
  text-align: center;
  padding: 20px;
  color: var(--el-text-color-secondary);
}
</style>
