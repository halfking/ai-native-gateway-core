<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getLicenses,
  createLicense,
  updateLicense,
  revokeLicense,
  getLicenseDevices,
  getOfflineActivationRequests,
  approveOfflineActivation,
  rejectOfflineActivation,
  deactivateDevice,
  getProductModules,
  getLicenseModuleOverrides,
  upsertLicenseModule,
  type License,
  type LicenseDevice,
  type OfflineActivationRequest,
  type ProductModule,
  type LicenseModuleOverride,
} from '../../api/ops'

const { t } = useI18n()

const licenses = ref<License[]>([])
const devices = ref<Record<number, LicenseDevice[]>>({})
const offlineRequests = ref<OfflineActivationRequest[]>([])
const loading = ref(false)
const expandedRows = ref<number[]>([])
const total = ref(0)
const searchQuery = ref('')
const statusFilter = ref('')
const pagination = ref({ offset: 0, limit: 20 })

// Create dialog state
const showCreateDialog = ref(false)
const createForm = ref({
  customer: '',
  customer_email: '',
  max_devices: 5,
  subscription_tier: 'starter',
  features: '',
  expires_at: '',
})

// Edit dialog state
const showEditDialog = ref(false)
const editingLicense = ref<License | null>(null)
const editForm = ref({
  customer_name: '',
  customer_email: '',
  max_devices: 5,
  subscription_tier: 'starter',
  features: '',
  expires_at: '',
})

function licenseStatus(lic: License): string {
  if (lic.revoked_at) return 'revoked'
  if (new Date(lic.expires_at) < new Date()) return 'expired'
  return 'active'
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

async function load() {
  loading.value = true
  try {
    const res = await getLicenses({
      offset: pagination.value.offset,
      limit: pagination.value.limit,
      query: searchQuery.value || undefined,
      status: statusFilter.value || undefined,
    })
    licenses.value = res.licenses || []
    total.value = res.total
  } catch (error) {
    ElMessage.error(t('ops.license.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  pagination.value.offset = 0
  load()
}

function handlePageChange(page: number) {
  pagination.value.offset = (page - 1) * pagination.value.limit
  load()
}

function handleSizeChange(size: number) {
  pagination.value.limit = size
  pagination.value.offset = 0
  load()
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

  const features: string[] = createForm.value.features
    ? createForm.value.features.split(',').map((s: string) => s.trim()).filter(Boolean)
    : []

  loading.value = true
  try {
    await createLicense({
      customer: createForm.value.customer,
      customer_email: createForm.value.customer_email || undefined,
      max_devices: createForm.value.max_devices,
      subscription_tier: createForm.value.subscription_tier || undefined,
      features: features.length > 0 ? features : undefined,
      expires_at: createForm.value.expires_at,
    })
    ElMessage.success(t('ops.license.createSuccess'))
    showCreateDialog.value = false
    createForm.value = { customer: '', customer_email: '', max_devices: 5, subscription_tier: 'starter', features: '', expires_at: '' }
    await load()
  } catch (error) {
    ElMessage.error(t('ops.license.createFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function handleEditLicense(row: License) {
  editingLicense.value = row
  editForm.value = {
    customer_name: row.customer_name,
    customer_email: row.customer_email || '',
    max_devices: row.max_devices,
    subscription_tier: row.subscription_tier || 'starter',
    features: (row.features || []).join(', '),
    expires_at: row.expires_at,
  }
  showEditDialog.value = true
}

async function handleSaveEdit() {
  if (!editingLicense.value) return
  const features = editForm.value.features
    ? editForm.value.features.split(',').map((s: string) => s.trim()).filter(Boolean)
    : []
  loading.value = true
  try {
    await updateLicense(editingLicense.value.id, {
      customer_name: editForm.value.customer_name || undefined,
      customer_email: editForm.value.customer_email || undefined,
      max_devices: editForm.value.max_devices,
      subscription_tier: editForm.value.subscription_tier || undefined,
      features: features.length > 0 ? features : undefined,
      expires_at: editForm.value.expires_at || undefined,
    })
    ElMessage.success(t('common.updateSuccess'))
    showEditDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(t('common.operationFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleRevoke(license: License) {
  try {
    await ElMessageBox.confirm(
      t('ops.license.revokeConfirm', { customer: license.customer_name }),
      t('common.warning'),
      { type: 'warning' }
    )
    await revokeLicense(license.id)
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
      devices.value[row.id] = await getLicenseDevices(row.id)
    } catch (error) {
      ElMessage.error(t('ops.license.loadDevicesFailed'))
      console.error(error)
    }
  }
}

async function handleApproveOffline(request: OfflineActivationRequest) {
  loading.value = true
  try {
    const result = await approveOfflineActivation(request.request_id)
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
    await rejectOfflineActivation(request.request_id, reason)
    ElMessage.success(t('ops.license.rejectSuccess'))
    await loadOfflineRequests()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.license.rejectFailed'))
      console.error(error)
    }
  }
}

// Module management state
const catalogModules = ref<ProductModule[]>([])
const catalogLoaded = ref(false)
const showModuleDialog = ref(false)
const moduleLicense = ref<License | null>(null)
const licenseModuleOverrides = ref<Map<string, LicenseModuleOverride>>(new Map())
const moduleSaving = ref(false)

function getModuleOverride(moduleKey: string): LicenseModuleOverride | undefined {
  return licenseModuleOverrides.value.get(moduleKey)
}

function isModuleEnabled(moduleKey: string): boolean {
  const ov = getModuleOverride(moduleKey)
  if (ov !== undefined) return ov.enabled
  return false
}

async function loadCatalog() {
  if (catalogLoaded.value) return
  try {
    catalogModules.value = await getProductModules()
    catalogLoaded.value = true
  } catch (e) {
    console.error('Failed to load module catalog', e)
  }
}

async function handleManageModules(row: License) {
  moduleLicense.value = row
  licenseModuleOverrides.value = new Map()
  await loadCatalog()
  try {
    const overrides = await getLicenseModuleOverrides(row.id)
    for (const ov of overrides) {
      licenseModuleOverrides.value.set(ov.module_key, ov)
    }
  } catch (e) {
    console.error('Failed to load module overrides', e)
  }
  showModuleDialog.value = true
}

async function handleToggleModule(moduleKey: string, enabled: boolean) {
  if (!moduleLicense.value) return
  moduleSaving.value = true
  try {
    await upsertLicenseModule(moduleLicense.value.id, { module_key: moduleKey, enabled })
    licenseModuleOverrides.value.set(moduleKey, {
      license_id: moduleLicense.value.id,
      module_key: moduleKey,
      enabled,
    })
    ElMessage.success(`${moduleKey}: ${enabled ? 'enabled' : 'disabled'}`)
  } catch (e) {
    ElMessage.error(`Failed to update module ${moduleKey}`)
    console.error(e)
  } finally {
    moduleSaving.value = false
  }
}

function groupedModules() {
  const groups = new Map<string, ProductModule[]>()
  for (const m of catalogModules.value) {
    const g = groups.get(m.category) || []
    g.push(m)
    groups.set(m.category, g)
  }
  return groups
}

async function handleDeactivateDevice(row: License, device: LicenseDevice) {
  try {
    const { value: reason } = await ElMessageBox.prompt(
      t('ops.license.deactivateReason'),
      t('ops.license.deactivateConfirm'),
      { inputType: 'textarea' }
    )
    await deactivateDevice(row.id, device.hardware_hash, reason)
    ElMessage.success(t('ops.license.deactivateSuccess'))
    devices.value[row.id] = await getLicenseDevices(row.id)
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.license.deactivateFailed'))
      console.error(error)
    }
  }
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
        <el-table-column prop="instance_id" :label="t('ops.license.deviceId')" width="150" />
        <el-table-column prop="request_id" :label="t('ops.license.requestCode')" />
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="row.status === 'pending' ? 'warning' : 'success'" size="small">
              {{ t(`ops.license.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="timestamp" :label="t('common.createdAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.timestamp) }}</template>
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
      <div class="toolbar">
        <el-input
          v-model="searchQuery"
          :placeholder="t('common.button.search')"
          clearable
          style="width: 260px"
          @clear="handleSearch"
          @keyup.enter="handleSearch"
        />
        <el-select v-model="statusFilter" clearable :placeholder="t('common.status')" style="width: 140px" @change="handleSearch">
          <el-option :label="t('ops.license.status.active')" value="active" />
          <el-option :label="t('ops.license.status.expired')" value="expired" />
          <el-option :label="t('ops.license.status.revoked')" value="revoked" />
        </el-select>
        <el-button type="primary" @click="handleSearch">{{ t('common.button.search') }}</el-button>
      </div>
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
                <el-table-column prop="instance_id" :label="t('ops.license.deviceId')" />
                <el-table-column prop="device_name" :label="t('ops.license.hostname')" />
                <el-table-column prop="status" :label="t('common.status')" width="90">
                  <template #default="{ row: device }">
                    <el-tag :type="device.status === 'active' ? 'success' : 'info'" size="small">
                      {{ device.status }}
                    </el-tag>
                  </template>
                </el-table-column>
                <el-table-column prop="activated_at" :label="t('ops.license.activatedAt')">
                  <template #default="{ row: device }">{{ formatDate(device.activated_at) }}</template>
                </el-table-column>
                <el-table-column prop="last_heartbeat" :label="t('ops.license.lastSeen')">
                  <template #default="{ row: device }">{{ formatDate(device.last_heartbeat || '') }}</template>
                </el-table-column>
                <el-table-column :label="t('common.actions')" width="100">
                  <template #default="{ row: device }">
                    <el-button
                      v-if="device.status === 'active'"
                      type="danger"
                      size="small"
                      @click="handleDeactivateDevice(row, device)"
                    >
                      {{ t('ops.license.deactivate') }}
                    </el-button>
                  </template>
                </el-table-column>
              </el-table>
              <div v-else class="loading-devices">{{ t('common.loading') }}</div>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="license_key" :label="t('ops.license.licenseKey')" width="200" />
        <el-table-column :label="t('ops.license.customer')" width="180">
          <template #default="{ row }">
            <div>{{ row.customer_name }}</div>
            <div v-if="row.customer_email" class="cell-sub">{{ row.customer_email }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.license.devices')" width="100">
          <template #default="{ row }">
            {{ (devices[row.id] || []).filter((d: LicenseDevice) => d.status === 'active').length }} / {{ row.max_devices }}
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.license.tier')" width="100">
          <template #default="{ row }">
            <el-tag v-if="row.subscription_tier" size="small" effect="plain">
              {{ row.subscription_tier }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.license.features')" min-width="120">
          <template #default="{ row }">
            <el-tag
              v-for="f in (row.features || [])"
              :key="f"
              size="small"
              style="margin-right: 4px; margin-bottom: 2px"
            >
              {{ f }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="expires_at" :label="t('ops.license.expiresAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.expires_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(licenseStatus(row))" size="small">
              {{ t(`ops.license.status.${licenseStatus(row)}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('common.createdAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="240" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click="handleEditLicense(row)">
              {{ t('common.edit') }}
            </el-button>
            <el-button size="small" @click="handleManageModules(row)">
              {{ t('ops.license.modules') }}
            </el-button>
            <el-button
              v-if="licenseStatus(row) === 'active'"
              type="danger"
              size="small"
              @click="handleRevoke(row)"
            >
              {{ t('ops.license.revoke') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="pagination-wrapper">
        <el-pagination
          v-model:page-size="pagination.limit"
          :page-sizes="[10, 20, 50, 100]"
          :total="total"
          :current-page="Math.floor(pagination.offset / pagination.limit) + 1"
          layout="total, sizes, prev, pager, next"
          @current-change="handlePageChange"
          @size-change="handleSizeChange"
        />
      </div>
    </el-card>

    <!-- Create License Dialog -->
    <el-dialog
      v-model="showCreateDialog"
      :title="t('ops.license.createTitle')"
      width="500px"
    >
      <el-form :model="createForm" label-width="140px">
        <el-form-item :label="t('ops.license.customer')" required>
          <el-input v-model="createForm.customer" :placeholder="t('ops.license.customerPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.license.customerEmail')">
          <el-input v-model="createForm.customer_email" :placeholder="t('ops.license.customerEmailPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.license.maxDevices')" required>
          <el-input-number v-model="createForm.max_devices" :min="1" :max="100" />
        </el-form-item>
        <el-form-item :label="t('ops.license.subscriptionTier')">
          <el-select v-model="createForm.subscription_tier" style="width: 100%">
            <el-option label="starter" value="starter" />
            <el-option label="pro" value="pro" />
            <el-option label="enterprise" value="enterprise" />
            <el-option label="custom" value="custom" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.license.features')">
          <el-input v-model="createForm.features" :placeholder="t('ops.license.featuresPlaceholder')" />
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

    <!-- Edit License Dialog -->
    <el-dialog
      v-model="showEditDialog"
      :title="t('common.edit') + ': ' + (editingLicense?.customer_name || '')"
      width="500px"
    >
      <el-form :model="editForm" label-width="140px">
        <el-form-item :label="t('ops.license.customer')">
          <el-input v-model="editForm.customer_name" />
        </el-form-item>
        <el-form-item :label="t('ops.license.customerEmail')">
          <el-input v-model="editForm.customer_email" />
        </el-form-item>
        <el-form-item :label="t('ops.license.maxDevices')">
          <el-input-number v-model="editForm.max_devices" :min="1" :max="1000" />
        </el-form-item>
        <el-form-item :label="t('ops.license.subscriptionTier')">
          <el-select v-model="editForm.subscription_tier" style="width: 100%">
            <el-option label="starter" value="starter" />
            <el-option label="pro" value="pro" />
            <el-option label="enterprise" value="enterprise" />
            <el-option label="custom" value="custom" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.license.features')">
          <el-input v-model="editForm.features" />
        </el-form-item>
        <el-form-item :label="t('ops.license.expiresAt')">
          <el-date-picker
            v-model="editForm.expires_at"
            type="datetime"
            placeholder="YYYY-MM-DDTHH:mm:ssZ"
            value-format="YYYY-MM-DDTHH:mm:ssZ"
            style="width: 100%"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showEditDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleSaveEdit">
          {{ t('common.save') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Module Management Dialog -->
    <el-dialog
      v-model="showModuleDialog"
      :title="t('ops.license.modulesTitle') + (moduleLicense ? ': ' + moduleLicense.customer_name : '')"
      width="700px"
    >
      <div v-loading="moduleSaving" class="module-dialog-body">
        <div v-for="[category, mods] in groupedModules()" :key="category" class="module-category">
          <h4 class="category-title">{{ category }}</h4>
          <div class="module-list">
            <div v-for="m in mods" :key="m.key" class="module-item">
              <div class="module-info">
                <span class="module-name">{{ m.name }}</span>
                <span class="module-key">{{ m.key }}</span>
                <span v-if="m.is_base" class="module-base-tag">base</span>
              </div>
              <div class="module-desc">{{ m.description }}</div>
              <div class="module-controls">
                <el-switch
                  :model-value="isModuleEnabled(m.key)"
                  :disabled="m.is_base || moduleSaving"
                  @change="(val: boolean) => handleToggleModule(m.key, val)"
                />
              </div>
            </div>
          </div>
        </div>
        <el-empty v-if="catalogModules.length === 0" :description="t('common.feedback.noData')" />
      </div>
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

.toolbar {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 16px;
}

.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
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

.cell-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.4;
}

.module-dialog-body {
  max-height: 60vh;
  overflow-y: auto;
}

.module-category {
  margin-bottom: 20px;
}

.category-title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 8px 0;
  text-transform: capitalize;
  color: var(--el-text-color-primary);
}

.module-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.module-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  width: calc(50% - 4px);
  box-sizing: border-box;
}

.module-info {
  display: flex;
  align-items: center;
  gap: 6px;
}

.module-name {
  font-weight: 500;
  font-size: 13px;
}

.module-key {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  font-family: monospace;
}

.module-base-tag {
  font-size: 10px;
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
  padding: 1px 6px;
  border-radius: 4px;
}

.module-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.3;
}

.module-controls {
  margin-top: 4px;
}
</style>
