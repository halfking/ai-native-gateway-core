<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { getMaasModels } from '../../api'
import type { MaasModel } from '../../api'
import { isSuperAdmin, isDefaultTenant, isPlatformOpsView, getCurrentTenantId } from '../../store'

const pageTitle = computed(() =>
  isPlatformOpsView() ? 'MaaS 模型清单' : '模型清单',
)

const models = ref<MaasModel[]>([])
const loading = ref(false)
const error = ref('')
const search = ref('')

const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  if (isSuperAdmin() && isDefaultTenant()) return '整站数据'
  if (isDefaultTenant()) return '默认租户'
  return `租户: ${tenantId}`
})

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter(
    (m) =>
      m.canonical_name.toLowerCase().includes(q) ||
      m.display_name.toLowerCase().includes(q),
  )
})

function fmtCredits(n: number) {
  return n.toLocaleString('zh-CN')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await getMaasModels()
    models.value = res.items ?? []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>{{ pageTitle }}</h2>
      <div class="page-header-actions">
        <span
          class="tenant-badge"
          :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }"
        >
          {{ tenantLabel }}
        </span>
        <input
          v-model="search"
          class="search-input"
          placeholder="搜索模型…"
        />
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <p class="page-desc">各模型按每百万 Token 计费的积分单价（输入 / 输出）。</p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div class="card">
      <table class="table" style="width:100%">
        <thead>
          <tr>
            <th>模型名称</th>
            <th style="text-align:right">积分 / 1M 输入</th>
            <th style="text-align:right">积分 / 1M 输出</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in filtered" :key="m.canonical_name">
            <td>
              <div class="model-name">{{ m.display_name }}</div>
              <code class="model-code">{{ m.canonical_name }}</code>
            </td>
            <td class="num">{{ fmtCredits(m.credits_per_1m_in) }}</td>
            <td class="num">{{ fmtCredits(m.credits_per_1m_out) }}</td>
          </tr>
          <tr v-if="!loading && filtered.length === 0">
            <td colspan="3" class="empty">暂无模型数据</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.page-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.page-desc {
  font-size: 13px;
  color: var(--muted);
  margin: -8px 0 16px;
}
.search-input {
  padding: 6px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 13px;
  width: 180px;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 0;
  overflow: hidden;
}
.model-name {
  font-weight: 600;
  font-size: 13px;
}
.model-code {
  font-size: 11px;
  color: var(--muted);
}
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 13px;
}
.empty {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}
.tenant-badge--admin {
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}
.tenant-badge--default {
  background: rgba(34, 197, 94, 0.1);
  color: #22c55e;
}
</style>
