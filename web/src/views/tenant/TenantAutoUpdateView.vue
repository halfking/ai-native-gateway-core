<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { getTenantUpdates, type Release } from '../../api/ops'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'

const { t } = useI18n()
const { tenantLabel, tenantCode } = useMaasTenantContext()

const loading = ref(true)
const loadFailed = ref(false)
const tenantName = ref('')
const currentVersion = ref('')
const releases = ref<Release[]>([])

const tenantDisplay = computed(() => tenantName.value || tenantCode.value || '—')

async function load() {
  loading.value = true
  loadFailed.value = false
  try {
    const data = await getTenantUpdates()
    tenantName.value = data.tenant_name || ''
    currentVersion.value = data.current_version || t('tenants.tenantOps.autoUpdate.unknownVersion')
    releases.value = data.items || []
  } catch (error) {
    loadFailed.value = true
    ElMessage.error(t('tenants.tenantOps.autoUpdate.loadError'))
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
        <h1>{{ t('tenants.tenantOps.autoUpdate.title') }}</h1>
        <p class="muted">{{ t('tenants.tenantOps.autoUpdate.subtitle', { tenant: tenantDisplay }) }}</p>
      </div>
      <span class="tenant-badge">{{ tenantLabel }}</span>
    </div>
    <el-alert v-if="loadFailed" :title="t('tenants.tenantOps.autoUpdate.loadFailed')" type="error" :closable="false" />
    <el-alert v-else :title="t('tenants.tenantOps.autoUpdate.infoAlert')" type="info" :closable="false" />
    <el-card class="main-card current-version" shadow="never">
      <span>{{ t('tenants.tenantOps.autoUpdate.currentVersion') }}</span>
      <strong>{{ currentVersion }}</strong>
    </el-card>
    <el-card class="main-card" shadow="never">
      <el-table v-loading="loading" :data="releases" :empty-text="t('tenants.tenantOps.autoUpdate.emptyReleases')">
        <el-table-column prop="version" :label="t('tenants.tenantOps.autoUpdate.table.version')" width="140" />
        <el-table-column prop="title" :label="t('tenants.tenantOps.autoUpdate.table.title')" min-width="220" />
        <el-table-column prop="channel" :label="t('tenants.tenantOps.autoUpdate.table.channel')" width="110" />
        <el-table-column prop="mandatory" :label="t('tenants.tenantOps.autoUpdate.table.mandatory')" width="110">
          <template #default="{ row }">
            {{ row.mandatory ? t('tenants.tenantOps.autoUpdate.table.yes') : t('tenants.tenantOps.autoUpdate.table.no') }}
          </template>
        </el-table-column>
        <el-table-column prop="published_at" :label="t('tenants.tenantOps.autoUpdate.table.publishedAt')" width="190" />
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
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
</style>
