<script setup lang="ts">
// MyUsageView.vue — /my/usage 页面（数据权限审计 P2，2026-11-26）。
// 调用用户级端点 GET /api/usage/my（近 30 天用量统计）：
//   · role=user        → 只统计自己的用量
//   · role=tenant_admin → 统计本租户用量
//   · role=super_admin  → 统计全平台用量
// 权限过滤在后端生效（admin/my_endpoints.go），前端只呈现返回的数据。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { getMyUsage, type MyUsageStats } from '../api/admin'
import { getUserRole } from '../store'

const { t } = useI18n()

const loading = ref(false)
const errorMessage = ref('')
const usage = ref<MyUsageStats | null>(null)
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
    case 'super_admin': return t('my.scopeHintUsageSuperAdmin')
    case 'tenant_admin': return t('my.scopeHintUsageTenantAdmin')
    case 'user': return t('my.scopeHintUsageUser')
    default: return ''
  }
})

const totalTokens = computed(() =>
  usage.value ? usage.value.total_prompt_tokens + usage.value.total_completion_tokens : 0,
)

function fmtCost(v: number): string {
  return '$' + v.toFixed(4)
}

async function load() {
  loading.value = true
  errorMessage.value = ''
  try {
    const resp = await getMyUsage()
    usage.value = resp.usage ?? null
    role.value = resp.role ?? ''
  } catch (e: unknown) {
    console.error('Failed to load my usage:', e)
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
  <div v-loading="loading" class="my-usage-view">
    <div class="page-header">
      <h2>{{ t('my.usageTitle') }}</h2>
      <el-tag :type="role === 'user' ? 'info' : role === 'tenant_admin' ? 'warning' : 'danger'" size="small">
        {{ roleLabel }}
      </el-tag>
    </div>

    <el-alert v-if="scopeHint" :title="scopeHint" type="info" :closable="false" style="margin-bottom: 16px;" />
    <el-alert v-if="errorMessage" type="error" :closable="false" style="margin-bottom: 16px;">
      {{ errorMessage }}
    </el-alert>

    <template v-if="usage">
      <el-alert :title="t('my.usagePeriod')" type="info" :closable="false" style="margin-bottom: 16px;" />

      <!-- 统计卡片（写法同 ClientAnalyticsView 的 stat-item 范式） -->
      <el-row :gutter="16" style="margin-bottom: 16px;">
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('my.statRequests') }}</div>
              <div class="stat-value">{{ usage.request_count.toLocaleString() }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('my.statCost') }}</div>
              <div class="stat-value">{{ fmtCost(usage.total_cost_usd) }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('my.statPromptTokens') }}</div>
              <div class="stat-value">{{ usage.total_prompt_tokens.toLocaleString() }}</div>
            </div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card shadow="hover">
            <div class="stat-item">
              <div class="stat-label">{{ t('my.statCompletionTokens') }}</div>
              <div class="stat-value">{{ usage.total_completion_tokens.toLocaleString() }}</div>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <el-card shadow="hover">
        <template #header>
          <span>{{ t('my.statTotalTokens') }}</span>
        </template>
        <div class="total-tokens">{{ totalTokens.toLocaleString() }}</div>
      </el-card>
    </template>

    <el-empty v-else-if="!loading && !errorMessage" :description="t('my.empty')" />
  </div>
</template>

<style scoped>
.my-usage-view {
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

.stat-item {
  text-align: center;
}

.stat-label {
  font-size: 14px;
  color: var(--text-secondary);
  margin-bottom: 8px;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
  color: var(--text);
}

.total-tokens {
  font-size: 32px;
  font-weight: 600;
  text-align: center;
  padding: 24px 0;
  color: var(--text);
}

:deep(.el-card__header) {
  padding: 12px 16px;
  font-weight: 500;
}
</style>
