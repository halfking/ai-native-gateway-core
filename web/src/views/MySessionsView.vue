<script setup lang="ts">
// MySessionsView.vue — /my/sessions 页面（数据权限审计 P2，2026-11-26）。
// 调用用户级端点 GET /api/sessions/my：
//   · role=user        → 只看自己的会话
//   · role=tenant_admin → 看本租户全部会话
//   · role=super_admin  → 看全平台会话
// 权限过滤在后端生效（admin/my_endpoints.go），前端只呈现返回的数据。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { formatDateTime } from '../utils/datetime'
import { getMySessions, type MySession } from '../api/admin'
import { getUserRole } from '../store'
// el-* 组件显式注册（element-import-audit 门要求；写法同 ClientAnalyticsView）
import { ElAlert, ElCard, ElEmpty, ElTable, ElTableColumn, ElTag } from 'element-plus'

const { t } = useI18n()

const loading = ref(false)
const errorMessage = ref('')
const sessions = ref<MySession[]>([])
const total = ref(0)
const role = ref('')

const roleLabel = computed(() => {
  const r = role.value || getUserRole()
  switch (r) {
    case 'super_admin': return t('my.roleSuperAdmin')
    case 'tenant_admin': return t('my.roleTenantAdmin')
    case 'user': return t('my.roleUser')
    default: return r
  }
})

const scopeHint = computed(() => {
  switch (role.value) {
    case 'super_admin': return t('my.scopeHintSuperAdmin')
    case 'tenant_admin': return t('my.scopeHintTenantAdmin')
    case 'user': return t('my.scopeHintUser')
    default: return ''
  }
})

function fmtCost(v: number): string {
  return '$' + v.toFixed(4)
}

async function load() {
  loading.value = true
  errorMessage.value = ''
  try {
    const resp = await getMySessions()
    sessions.value = resp.sessions ?? []
    total.value = resp.total ?? sessions.value.length
    role.value = resp.role ?? ''
  } catch (e: unknown) {
    console.error('Failed to load my sessions:', e)
    errorMessage.value = surfaceError(e, { key: 'my.loadError' })
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div v-loading="loading" class="my-sessions-view">
    <div class="page-header">
      <h2>{{ t('my.sessionsTitle') }}</h2>
      <el-tag :type="role === 'user' ? 'info' : role === 'tenant_admin' ? 'warning' : 'danger'" size="small">
        {{ roleLabel }}
      </el-tag>
    </div>

    <el-alert v-if="scopeHint" :title="scopeHint" type="info" :closable="false" style="margin-bottom: 16px;" />
    <el-alert v-if="errorMessage" type="error" :closable="false" style="margin-bottom: 16px;">
      {{ errorMessage }}
    </el-alert>

    <el-card shadow="hover">
      <template #header>
        <span>{{ t('my.sessionsCard') }}（{{ total }}）</span>
      </template>
      <el-table :data="sessions" stripe style="width: 100%;">
        <el-table-column prop="title" :label="t('my.colTitle')" min-width="200">
          <template #default="{ row }">
            {{ row.title || t('my.untitled') }}
          </template>
        </el-table-column>
        <el-table-column prop="session_key" :label="t('my.colSessionKey')" min-width="180" show-overflow-tooltip />
        <el-table-column v-if="role === 'super_admin' || role === 'tenant_admin'" prop="tenant_id" :label="t('my.colTenant')" width="140" />
        <el-table-column prop="request_count" :label="t('my.colRequests')" width="110" align="right" />
        <el-table-column prop="total_cost_usd" :label="t('my.colCost')" width="120" align="right">
          <template #default="{ row }">
            {{ fmtCost(row.total_cost_usd) }}
          </template>
        </el-table-column>
        <el-table-column prop="updated_at" :label="t('my.colUpdatedAt')" width="180">
          <template #default="{ row }">
            {{ formatDateTime(row.updated_at) }}
          </template>
        </el-table-column>
        <template #empty>
          <el-empty :description="t('my.empty')" />
        </template>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.my-sessions-view {
  padding: 16px;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}

.page-header h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

:deep(.el-card__header) {
  padding: 12px 16px;
  font-weight: 500;
}
</style>
