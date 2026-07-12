<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { getTenantUpdates, type Release } from '../../api/ops'

const route = useRoute()
const loading = ref(true)
const tenantID = ref('')
const currentVersion = ref('未知')
const releases = ref<Release[]>([])

async function load() {
  loading.value = true
  try {
    const data = await getTenantUpdates(typeof route.query.tenant === 'string' ? route.query.tenant : undefined)
    tenantID.value = data.tenant_id
    currentVersion.value = data.current_version || '未知'
    releases.value = data.items || []
  } catch (error) {
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
    <div class="page-header"><div><h1>我的更新</h1><p class="muted">租户 {{ tenantID || '...' }} 的只读更新信息</p></div></div>
    <el-alert title="平台会根据发布策略为你的实例提供更新，此页面不提供发布或回滚操作。" type="info" :closable="false" />
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
