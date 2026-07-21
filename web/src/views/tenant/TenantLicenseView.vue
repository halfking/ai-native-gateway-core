<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { getTenantLicenseStatus, type License } from '../../api/ops'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'

const { t } = useI18n()
const { tenantLabel, tenantCode } = useMaasTenantContext()

const loading = ref(true)
const loadFailed = ref(false)
const tenantName = ref('')
const licenses = ref<License[]>([])

const tenantDisplay = computed(() => tenantName.value || tenantCode.value || '—')

function status(license: License): string {
  if (license.revoked_at) return t('tenants.tenantOps.license.status.revoked')
  if (license.expires_at && new Date(license.expires_at) < new Date()) {
    return t('tenants.tenantOps.license.status.expired')
  }
  return t('tenants.tenantOps.license.status.active')
}

function featuresText(features?: string[] | unknown[]) {
  if (!Array.isArray(features) || features.length === 0) return '—'
  return features.join('、')
}

async function load() {
  loading.value = true
  loadFailed.value = false
  try {
    const data = await getTenantLicenseStatus()
    tenantName.value = data.tenant_name || ''
    licenses.value = data.licenses || []
  } catch (error) {
    loadFailed.value = true
    ElMessage.error(t('tenants.tenantOps.license.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="tenant-ops-view">
    <div class="page-header">
      <div>
        <h1>{{ t('tenants.tenantOps.license.title') }}</h1>
        <p class="muted">{{ t('tenants.tenantOps.license.subtitle', { tenant: tenantDisplay }) }}</p>
      </div>
      <span class="tenant-badge">{{ tenantLabel }}</span>
    </div>
    <el-alert v-if="loadFailed" :title="t('tenants.tenantOps.license.loadFailed')" type="error" :closable="false" />
    <el-alert v-else :title="t('tenants.tenantOps.license.infoAlert')" type="info" :closable="false" />
    <el-card class="main-card" shadow="never">
      <el-table v-loading="loading" :data="licenses" :empty-text="t('tenants.tenantOps.license.empty')">
        <el-table-column prop="subscription_tier" :label="t('tenants.tenantOps.license.table.tier')" width="140" />
        <el-table-column prop="max_devices" :label="t('tenants.tenantOps.license.table.maxDevices')" width="110" />
        <el-table-column prop="expires_at" :label="t('tenants.tenantOps.license.table.expiresAt')" width="190" />
        <el-table-column :label="t('tenants.tenantOps.license.table.status')" width="110">
          <template #default="{ row }">
            <el-tag :type="status(row) === t('tenants.tenantOps.license.status.active') ? 'success' : 'warning'">
              {{ status(row) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('tenants.tenantOps.license.table.features')" min-width="240">
          <template #default="{ row }">{{ featuresText(row.features) }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.tenant-badge {
  display: inline-flex;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  background: var(--surface-secondary);
  color: var(--text-secondary);
}
</style>
