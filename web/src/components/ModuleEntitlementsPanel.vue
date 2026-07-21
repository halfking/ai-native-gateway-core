<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { isCoreNode } from '../config/edition'
import {
  updateActivateApi,
  type ModuleCatalogItem,
} from '../api/updateActivate'
import {
  moduleEntitlementApi,
  type ModuleEntitlement,
} from '../api/moduleEntitlements'

const visible = isCoreNode()
const catalog = ref<ModuleCatalogItem[]>([])
const entitlements = ref<ModuleEntitlement[]>([])
const loading = ref(false)
const applying = ref<string | null>(null)
const error = ref('')

async function refresh() {
  if (!visible) return
  loading.value = true
  error.value = ''
  try {
    const [cat, ent] = await Promise.all([
      updateActivateApi.modulesCatalog().catch(() => ({ items: [] as ModuleCatalogItem[] })),
      moduleEntitlementApi.list().catch(() => ({ items: [] as ModuleEntitlement[] })),
    ])
    catalog.value = cat.items || []
    entitlements.value = ent.items || []
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function statusOf(moduleId: string): string {
  const hit = entitlements.value.find((e) => e.module_id === moduleId)
  return hit?.status || ''
}

async function applyOpen(row: ModuleCatalogItem) {
  applying.value = row.module_id
  try {
    await moduleEntitlementApi.apply({
      module_id: row.module_id,
      display_name: row.display_name,
      auto_open: true,
      note: 'core-admin direct open',
    })
    ElMessage.success(`已开通 ${row.display_name || row.module_id}`)
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    applying.value = null
  }
}

onMounted(refresh)
</script>

<template>
  <el-card v-if="visible" shadow="never" class="ent-panel">
    <template #header>
      <div class="ent-panel__head">
        <div>
          <strong>发版模块开通</strong>
          <span class="muted">（核心节点）清单来自中心发版目录，超管可直接开通</span>
        </div>
        <el-button size="small" :loading="loading" @click="refresh">刷新</el-button>
      </div>
    </template>
    <el-alert v-if="error" type="warning" :title="error" show-icon :closable="false" class="mb" />
    <el-table v-loading="loading" :data="catalog" size="small" stripe empty-text="暂无发版模块">
      <el-table-column label="模块" min-width="160">
        <template #default="{ row }">{{ row.display_name || row.module_id }}</template>
      </el-table-column>
      <el-table-column prop="version" label="版本" width="100" />
      <el-table-column label="开通状态" width="120">
        <template #default="{ row }">
          <el-tag v-if="statusOf(row.module_id) === 'opened' || row.opened_at" type="success" size="small">
            已开通
          </el-tag>
          <el-tag v-else-if="statusOf(row.module_id) === 'pending'" type="warning" size="small">
            待审核
          </el-tag>
          <el-tag v-else size="small">未开通</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="开通时间" min-width="160">
        <template #default="{ row }">
          {{ row.opened_at || entitlements.find((e) => e.module_id === row.module_id && e.status === 'opened')?.opened_at || '—' }}
        </template>
      </el-table-column>
      <el-table-column label="操作" width="120" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="statusOf(row.module_id) !== 'opened' && !row.opened_at"
            type="primary"
            size="small"
            link
            :loading="applying === row.module_id"
            @click="applyOpen(row)"
          >
            开通
          </el-button>
          <span v-else class="muted">—</span>
        </template>
      </el-table-column>
    </el-table>
  </el-card>
</template>

<style scoped>
.ent-panel { margin-bottom: 16px; }
.ent-panel__head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}
.muted { color: var(--muted); font-size: 12px; margin-left: 8px; }
.mb { margin-bottom: 12px; }
</style>
