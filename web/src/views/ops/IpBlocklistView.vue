<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  listIPBlocklist,
  createIPBlocklist,
  updateIPBlocklist,
  deleteIPBlocklist,
  reloadIPBlocklist,
  type IPBlocklistEntry,
} from '../../api/security'

const { t } = useI18n()
const loading = ref(false)
const items = ref<IPBlocklistEntry[]>([])
const total = ref(0)
const scopeFilter = ref('')
const form = ref({ ip_or_cidr: '', reason: '', scope: 'ops' })

async function load() {
  loading.value = true
  try {
    const res = await listIPBlocklist({ scope: scopeFilter.value || undefined, limit: 100 })
    items.value = res.items
    total.value = res.total
  } catch (e) {
    ElMessage.error(t('ops.blocklist.loadFailed'))
    console.error(e)
  } finally {
    loading.value = false
  }
}

async function onCreate() {
  if (!form.value.ip_or_cidr.trim()) {
    ElMessage.warning(t('ops.blocklist.ipRequired'))
    return
  }
  try {
    await createIPBlocklist(form.value)
    form.value.ip_or_cidr = ''
    form.value.reason = ''
    ElMessage.success(t('ops.blocklist.createSuccess'))
    await load()
  } catch (e) {
    ElMessage.error(t('ops.blocklist.createFailed'))
  }
}

async function toggleEnabled(row: IPBlocklistEntry) {
  try {
    await updateIPBlocklist(row.id, { enabled: !row.enabled })
    await load()
  } catch {
    ElMessage.error(t('ops.blocklist.updateFailed'))
  }
}

async function onDelete(row: IPBlocklistEntry) {
  try {
    await ElMessageBox.confirm(t('ops.blocklist.deleteConfirm', { ip: row.ip_or_cidr }), t('common.confirm'))
    await deleteIPBlocklist(row.id)
    ElMessage.success(t('ops.blocklist.deleteSuccess'))
    await load()
  } catch {
    // cancelled
  }
}

async function onReload() {
  try {
    await reloadIPBlocklist()
    ElMessage.success(t('ops.blocklist.reloadSuccess'))
    await load()
  } catch {
    ElMessage.error(t('ops.blocklist.reloadFailed'))
  }
}

onMounted(load)
</script>

<template>
  <div class="ip-blocklist-view">
    <div class="page-header">
      <h1>{{ t('ops.blocklist.title') }}</h1>
      <div class="actions">
        <el-select v-model="scopeFilter" clearable :placeholder="t('ops.blocklist.scope')" style="width: 140px" @change="load">
          <el-option value="global" label="global" />
          <el-option value="ops" label="ops" />
          <el-option value="collect" label="collect" />
        </el-select>
        <el-button @click="onReload">{{ t('ops.blocklist.reloadCache') }}</el-button>
        <el-button type="primary" :loading="loading" @click="load">{{ t('common.refresh') }}</el-button>
      </div>
    </div>

    <el-card shadow="never" class="create-card">
      <template #header>{{ t('ops.blocklist.add') }}</template>
      <div class="create-row">
        <el-input v-model="form.ip_or_cidr" :placeholder="t('ops.blocklist.ipPlaceholder')" style="width: 220px" />
        <el-select v-model="form.scope" style="width: 120px">
          <el-option value="global" label="global" />
          <el-option value="ops" label="ops" />
          <el-option value="collect" label="collect" />
        </el-select>
        <el-input v-model="form.reason" :placeholder="t('ops.blocklist.reason')" style="flex: 1" />
        <el-button type="primary" @click="onCreate">{{ t('common.add') }}</el-button>
      </div>
    </el-card>

    <el-table v-loading="loading" :data="items" size="small">
      <el-table-column prop="ip_or_cidr" :label="t('ops.blocklist.ip')" width="180" />
      <el-table-column prop="scope" label="scope" width="90" />
      <el-table-column prop="source" label="source" width="110" />
      <el-table-column prop="reason" :label="t('ops.blocklist.reason')" show-overflow-tooltip />
      <el-table-column prop="hit_count" :label="t('ops.blocklist.hits')" width="80" />
      <el-table-column prop="enabled" :label="t('common.table.status')" width="100">
        <template #default="{ row }">
          <el-switch :model-value="row.enabled" @change="toggleEnabled(row)" />
        </template>
      </el-table-column>
      <el-table-column :label="t('common.table.actions')" width="100">
        <template #default="{ row }">
          <el-button link type="danger" @click="onDelete(row)">{{ t('common.delete') }}</el-button>
        </template>
      </el-table-column>
    </el-table>
    <div class="footer-meta">{{ t('ops.blocklist.total', { n: total }) }}</div>
  </div>
</template>

<style scoped>
.ip-blocklist-view { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.page-header h1 { margin: 0; font-size: 22px; }
.actions { display: flex; gap: 8px; align-items: center; }
.create-card { margin-bottom: 16px; }
.create-row { display: flex; gap: 8px; align-items: center; }
.footer-meta { margin-top: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
</style>
