<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getTenantUpdates, type Release } from '../../api/ops'
import { useMaasTenantContext } from '../../composables/useMaasTenantContext'

// 与其它 /tenant/* 页面保持一致的租户上下文（侧栏徽标 / 管理员视角）。
const { tenantLabel, tenantCode } = useMaasTenantContext()

const loading = ref(true)
const loadFailed = ref(false)
const tenantName = ref('')
const currentVersion = ref('未知')
const releases = ref<Release[]>([])

// 优先展示后端解析出的租户名称，缺失时回退到租户编码。
const tenantDisplay = computed(() => tenantName.value || tenantCode.value || '—')

async function load() {
  loading.value = true
  loadFailed.value = false
  try {
    const data = await getTenantUpdates()
    tenantName.value = data.tenant_name || ''
    currentVersion.value = data.current_version || '未知'
    releases.value = data.items || []
  } catch (error) {
    loadFailed.value = true
    ElMessage.error('无法加载可用更新')
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
        <h1>我的更新</h1>
        <p class="muted">租户 {{ tenantDisplay }} 的只读更新信息</p>
      </div>
      <span class="tenant-badge">{{ tenantLabel }}</span>
    </div>
    <el-alert v-if="loadFailed" title="更新信息加载失败，请稍后重试或联系平台管理员。" type="error" :closable="false" />
    <el-alert v-else title="平台会根据发布策略为你的实例提供更新，此页面不提供发布或回滚操作。" type="info" :closable="false" />
    <el-card class="main-card current-version" shadow="never"><span>当前版本</span><strong>{{ currentVersion }}</strong></el-card>
    <el-card class="main-card" shadow="never">
      <el-table v-loading="loading" :data="releases" empty-text="当前没有可用更新">
        <el-table-column prop="version" label="版本" width="140" />
        <el-table-column prop="title" label="标题" min-width="220" />
        <el-table-column prop="channel" label="渠道" width="110" />
        <el-table-column prop="mandatory" label="必须更新" width="110"><template #default="{ row }">{{ row.mandatory ? '是' : '否' }}</template></el-table-column>
        <el-table-column prop="published_at" label="发布时间" width="190" />
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
