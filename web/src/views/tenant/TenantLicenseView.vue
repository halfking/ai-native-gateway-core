<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getTenantLicenseStatus, type License } from '../../api/ops'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'

// 与其它 /tenant/* 页面保持一致的租户上下文（侧栏徽标 / 管理员视角）。
const { tenantLabel, tenantCode } = useMaasTenantContext()

const loading = ref(true)
const loadFailed = ref(false)
const tenantName = ref('')
const licenses = ref<License[]>([])

// 优先展示后端解析出的租户名称，缺失时回退到租户编码。
const tenantDisplay = computed(() => tenantName.value || tenantCode.value || '—')

function status(license: License) {
  if (license.revoked_at) return '已撤销'
  if (license.expires_at && new Date(license.expires_at) < new Date()) return '已过期'
  return '有效'
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
      <div>
        <h1>我的授权</h1>
        <p class="muted">租户 {{ tenantDisplay }} 的只读授权状态</p>
      </div>
      <span class="tenant-badge">{{ tenantLabel }}</span>
    </div>
    <el-alert v-if="loadFailed" title="授权信息加载失败，请稍后重试或联系平台管理员。" type="error" :closable="false" />
    <el-alert v-else title="此页面仅显示当前租户信息，授权变更请联系平台管理员。" type="info" :closable="false" />
    <el-card class="main-card" shadow="never">
      <el-table v-loading="loading" :data="licenses" empty-text="当前租户暂无授权记录">
        <el-table-column prop="subscription_tier" label="套餐" width="140" />
        <el-table-column prop="max_devices" label="设备上限" width="110" />
        <el-table-column prop="expires_at" label="到期时间" width="190" />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="status(row) === '有效' ? 'success' : 'warning'">{{ status(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="功能" min-width="240">
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
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
</style>
