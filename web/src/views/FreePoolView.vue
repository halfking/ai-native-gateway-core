<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { getFreePoolStatus, registerFreeProvider, type FreePoolStatusResponse, type FreePoolEntry } from '../api'

const poolData  = ref<FreePoolStatusResponse | null>(null)
const loading   = ref(false)
const error     = ref('')
const message   = ref('')

const showAddForm = ref(false)
const submitting   = ref(false)
const newProvider = ref({
  catalog_code: '',
  display_name: '',
  base_url: '',
  protocol: 'openai-completions',
  api_key: '',
  models: '',
})

const knownProviders = [
  {
    code: 'zhipu-free',
    name: 'Zhipu GLM (Free Tier)',
    url: 'https://open.bigmodel.cn/api/paas/v4',
    models: 'GLM-4-Flash,GLM-4.7-Flash,GLM-4V-Flash',
  },
  {
    code: 'siliconflow-free',
    name: 'SiliconFlow (Free Tier)',
    url: 'https://api.siliconflow.cn/v1',
    models: 'Qwen/Qwen2.5-7B-Instruct,deepseek-ai/DeepSeek-R1-Distill-Qwen-7B',
  },
  {
    code: 'free-chatgpt',
    name: 'Free ChatGPT API (Community)',
    url: 'https://free.v36.cm/v1',
    models: 'gpt-4o-mini,gpt-3.5-turbo',
  },
]

async function load() {
  loading.value = true
  error.value = ''
  try {
    poolData.value = await getFreePoolStatus()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function useTemplate(idx: number) {
  const tpl = knownProviders[idx]
  newProvider.value.catalog_code = tpl.code
  newProvider.value.display_name = tpl.name
  newProvider.value.base_url = tpl.url
  newProvider.value.models = tpl.models
  showAddForm.value = true
}

async function submitNew() {
  if (!newProvider.value.catalog_code || !newProvider.value.base_url) {
    error.value = '请填写 Catalog Code 和 Base URL'
    return
  }
  submitting.value = true
  error.value = ''
  message.value = ''
  try {
    const models = newProvider.value.models
      .split(',')
      .map(s => s.trim())
      .filter(Boolean)

    const res = await registerFreeProvider({
      catalog_code: newProvider.value.catalog_code,
      display_name: newProvider.value.display_name || newProvider.value.catalog_code,
      base_url: newProvider.value.base_url,
      protocol: newProvider.value.protocol,
      api_key: newProvider.value.api_key || undefined,
      models: models.length > 0 ? models : undefined,
    })
    message.value = `Provider 注册成功 (ID: ${res.provider_id})`
    showAddForm.value = false
    newProvider.value = {
      catalog_code: '',
      display_name: '',
      base_url: '',
      protocol: 'openai-completions',
      api_key: '',
      models: '',
    }
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '注册失败'
  } finally {
    submitting.value = false
  }
}

function statusBadgeClass(entry: FreePoolEntry): string {
  if (entry.credential_status !== 'active') return 'badge-red'
  if (entry.availability_state === 'rate_limited' || entry.availability_state === 'cooling') return 'badge-orange'
  if (entry.quota_state === 'exhausted' || entry.quota_state === 'balance_exhausted') return 'badge-orange'
  return 'badge-green'
}

function statusLabel(entry: FreePoolEntry): string {
  if (entry.credential_status !== 'active') return '已禁用'
  if (entry.availability_state === 'rate_limited') return '限流'
  if (entry.availability_state === 'cooling') return '冷却'
  if (entry.quota_state === 'exhausted') return '配额用尽'
  return '可用'
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>免费资源池</h2>
      <div style="display:flex;gap:8px">
        <button class="btn btn-ghost" @click="load" :disabled="loading">刷新</button>
        <button class="btn btn-primary" @click="showAddForm = !showAddForm">
          {{ showAddForm ? '取消' : '添加 Provider' }}
        </button>
      </div>
    </div>
    <p style="color:var(--muted);margin-bottom:20px">
      管理免费模型资源池。免费模型在路由时优先级最高（composite_score = 0）。当前上限 20 个 Provider。
    </p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="message" class="alert alert-success">{{ message }}</div>

    <div v-if="poolData" class="stat-grid" style="margin-bottom:20px">
      <div class="stat-card">
        <div class="stat-label">Provider 总数</div>
        <div class="stat-value">{{ poolData.stats.total_providers }}</div>
        <div class="stat-sub" style="font-size:11px;color:var(--muted)">上限 20</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">可用 Provider</div>
        <div class="stat-value" style="color:#16a34a">{{ poolData.stats.available_providers }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">模型总数</div>
        <div class="stat-value">{{ poolData.stats.total_models }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">免费模型</div>
        <div class="stat-value" style="color:#16a34a">{{ poolData.stats.free_models }}</div>
      </div>
    </div>

    <div v-if="showAddForm" class="card" style="margin-bottom:20px">
      <h3 style="margin-top:0">添加免费 Provider</h3>
      <div class="form-grid">
        <div class="form-item">
          <label>Catalog Code *</label>
          <input v-model="newProvider.catalog_code" class="input" placeholder="例如: zhipu-free" />
        </div>
        <div class="form-item">
          <label>显示名称</label>
          <input v-model="newProvider.display_name" class="input" placeholder="例如: Zhipu GLM Free" />
        </div>
        <div class="form-item" style="grid-column: 1 / -1">
          <label>Base URL *</label>
          <input v-model="newProvider.base_url" class="input" placeholder="https://api.example.com/v1" />
        </div>
        <div class="form-item">
          <label>协议</label>
          <select v-model="newProvider.protocol" class="input">
            <option value="openai-completions">openai-completions</option>
            <option value="openai-responses">openai-responses</option>
            <option value="anthropic-messages">anthropic-messages</option>
          </select>
        </div>
        <div class="form-item">
          <label>API Key</label>
          <input v-model="newProvider.api_key" class="input" type="password" placeholder="可选" />
        </div>
        <div class="form-item" style="grid-column: 1 / -1">
          <label>模型列表 (逗号分隔)</label>
          <input v-model="newProvider.models" class="input" placeholder="例如: gpt-4o-mini, gpt-3.5-turbo" />
        </div>
      </div>

      <div style="margin-top:16px">
        <h4 style="font-size:13px;margin-bottom:8px;color:var(--muted)">已知免费 Provider 模板：</h4>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button
            v-for="(tpl, idx) in knownProviders"
            :key="tpl.code"
            class="btn btn-ghost btn-sm"
            @click="useTemplate(idx)"
            style="font-size:12px"
          >
            {{ tpl.name }}
          </button>
        </div>
      </div>

      <div style="margin-top:16px;display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" @click="submitNew" :disabled="submitting">
          {{ submitting ? '注册中…' : '注册 Provider' }}
        </button>
        <button class="btn btn-ghost" @click="showAddForm = false" :disabled="submitting">取消</button>
      </div>
    </div>

    <div v-if="loading" class="empty">加载中…</div>

    <div v-else-if="poolData" class="card">
      <h3 style="margin-top:0">Provider 列表 ({{ poolData.pool.length }})</h3>
      <div v-if="poolData.pool.length === 0" class="empty">暂无免费 Provider，点击「添加 Provider」开始</div>
      <table v-else>
        <thead>
          <tr>
            <th>Catalog Code</th>
            <th>显示名称</th>
            <th>凭据标签</th>
            <th>状态</th>
            <th>模型</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in poolData.pool" :key="entry.credential_id">
            <td><code style="font-size:11px">{{ entry.catalog_code }}</code></td>
            <td>{{ entry.provider_name }}</td>
            <td>
              <div>{{ entry.credential_label }}</div>
              <div class="cell-muted">#{{ entry.credential_id }} · {{ entry.availability_state || 'ready' }}</div>
            </td>
            <td>
              <span class="badge" :class="statusBadgeClass(entry)">
                {{ statusLabel(entry) }}
              </span>
            </td>
            <td>
              <div>总数: <strong>{{ entry.total_offers }}</strong></div>
              <div class="cell-muted">免费: {{ entry.free_offers }} / 可用: {{ entry.available_offers }}</div>
            </td>
            <td>
              <button class="btn btn-ghost btn-sm" @click="load" style="font-size:11px">刷新</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.cell-muted { color: var(--muted); font-size: 11px; margin-top: 3px; }

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.form-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.form-item label {
  font-size: 12px;
  font-weight: 600;
  color: var(--muted);
}
.badge-orange {
  background: #fed7aa;
  color: #9a3412;
}
</style>
