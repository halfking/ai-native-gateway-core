<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  isCoreNode,
  isMaintainProbed,
  onMaintainAvailabilityChange,
  probeMaintainAvailable,
} from '../config/edition'
import {
  updateActivateApi,
  type ModuleCatalogItem,
} from '../api/updateActivate'
import {
  moduleEntitlementApi,
  type ModuleEntitlement,
} from '../api/moduleEntitlements'

type Tab = 'catalog' | 'opened' | 'mine'
const tab = ref<Tab>('catalog')

const visible = ref(false)
const catalog = ref<ModuleCatalogItem[]>([])
const entitlements = ref<ModuleEntitlement[]>([])
const myEntitlements = ref<ModuleEntitlement[]>([])
const loading = ref(false)
const applying = ref<string | null>(null)
const error = ref('')
let unsubscribe: (() => void) | null = null

async function refresh() {
  if (!visible.value) return
  loading.value = true
  error.value = ''
  try {
    const [cat, ent, mine] = await Promise.all([
      updateActivateApi.modulesCatalog().catch(() => ({ items: [] as ModuleCatalogItem[] })),
      moduleEntitlementApi.list().catch(() => ({ items: [] as ModuleEntitlement[] })),
      moduleEntitlementApi.listMine().catch(() => ({ items: [] as ModuleEntitlement[] })),
    ])
    catalog.value = cat.items || []
    entitlements.value = ent.items || []
    myEntitlements.value = mine.items || []
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

function openedAtOf(moduleId: string): string {
  const hit = entitlements.value.find(
    (e) => e.module_id === moduleId && e.status === 'opened',
  )
  return hit?.opened_at || ''
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

async function applyPending(row: ModuleCatalogItem) {
  applying.value = row.module_id
  try {
    await moduleEntitlementApi.apply({
      module_id: row.module_id,
      display_name: row.display_name,
      auto_open: false,
      note: 'awaiting super admin approval',
    })
    ElMessage.info(`已提交申请：${row.display_name || row.module_id}`)
    await refresh()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    applying.value = null
  }
}

onMounted(async () => {
  if (!isMaintainProbed()) {
    await probeMaintainAvailable()
  }
  visible.value = isCoreNode()
  unsubscribe = onMaintainAvailabilityChange(() => {
    visible.value = isCoreNode()
    void refresh()
  })
  await refresh()
})

onBeforeUnmount(() => {
  unsubscribe?.()
  unsubscribe = null
})
</script>

<template>
  <el-card v-if="visible" shadow="never" class="ent-panel">
    <template #header>
      <div class="ent-panel__head">
        <div>
          <strong>发版模块开通</strong>
          <span class="muted">（核心节点）清单来自中心发版目录</span>
        </div>
        <el-button size="small" :loading="loading" @click="refresh">刷新</el-button>
      </div>
    </template>
    <el-tabs v-model="tab" class="ent-tabs">
      <el-tab-pane label="模块清单" name="catalog">
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
              {{ row.opened_at || openedAtOf(row.module_id) || '—' }}
            </template>
          </el-table-column>
          <el-table-column label="操作" width="180" fixed="right">
            <template #default="{ row }">
              <el-button
                v-if="statusOf(row.module_id) !== 'opened' && !row.opened_at"
                type="primary"
                size="small"
                link
                :loading="applying === row.module_id"
                @click="applyOpen(row)"
              >
                直开
              </el-button>
              <el-button
                v-if="statusOf(row.module_id) !== 'opened' && !row.opened_at"
                size="small"
                link
                :loading="applying === row.module_id"
                @click="applyPending(row)"
              >
                提交申请
              </el-button>
              <span v-else class="muted">—</span>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
      <el-tab-pane label="已开通" name="opened">
        <el-table v-loading="loading" :data="entitlements.filter((e) => e.status === 'opened')" size="small" stripe empty-text="暂无已开通记录">
          <el-table-column label="模块" min-width="160">
            <template #default="{ row }">{{ row.display_name || row.module_id }}</template>
          </el-table-column>
          <el-table-column label="开通时间" min-width="180">
            <template #default="{ row }">{{ row.opened_at || '—' }}</template>
          </el-table-column>
          <el-table-column label="操作人" min-width="120">
            <template #default="{ row }">{{ row.opened_by || 'system' }}</template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
      <el-tab-pane label="我的申请" name="mine">
        <el-table v-loading="loading" :data="myEntitlements" size="small" stripe empty-text="暂无我的申请">
          <el-table-column label="模块" min-width="160">
            <template #default="{ row }">{{ row.display_name || row.module_id }}</template>
          </el-table-column>
          <el-table-column label="状态" width="120">
            <template #default="{ row }">
              <el-tag v-if="row.status === 'opened'" type="success" size="small">已开通</el-tag>
              <el-tag v-else-if="row.status === 'pending'" type="warning" size="small">待审核</el-tag>
              <el-tag v-else size="small" type="info">已拒绝</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="申请时间" min-width="180">
            <template #default="{ row }">{{ row.requested_at }}</template>
          </el-table-column>
          <el-table-column label="说明" min-width="180">
            <template #default="{ row }">{{ row.note || '—' }}</template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>
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
.ent-tabs :deep(.el-tabs__header) { margin-bottom: 12px; }
</style>