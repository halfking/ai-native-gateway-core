<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getTenantLicenseStatus, type License } from '../../api/ops'

const loading = ref(true)
const tenantID = ref('')
const licenses = ref<License[]>([])

function status(license: License) {
  if (license.revoked_at) return '已撤销'
  if (new Date(license.expires_at) < new Date()) return '已过期'
  return '有效'
}

async function load() {
  loading.value = true
  try {
    const data = await getTenantLicenseStatus()
    tenantID.value = data.tenant_id
    licenses.value = data.licenses || []
  } catch (error) {
    ElMessage.error('无法加载当前租户授权')
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
      <div><h1>我的授权</h1><p class="muted">租户 {{ tenantID || '...' }} 的只读授权状态</p></div>
    </div>
    <el-alert title="此页面仅显示当前租户信息，授权变更请联系平台管理员。" type="info" :closable="false" />
    <el-card class="main-card" shadow="never">
      <el-table v-loading="loading" :data="licenses" empty-text="当前租户暂无授权记录">
        <el-table-column prop="subscription_tier" label="套餐" width="140" />
        <el-table-column prop="max_devices" label="设备上限" width="110" />
        <el-table-column prop="expires_at" label="到期时间" width="190" />
        <el-table-column label="状态" width="110"><template #default="{ row = {} } = {}"><el-tag :type="status(row) === '有效' ? 'success' : 'warning'">{{ status(row) }}</el-tag></template></el-table-column>
        <el-table-column prop="features" label="功能" min-width="240" />
      </el-table>
    </el-card>
  </div>
</template>
