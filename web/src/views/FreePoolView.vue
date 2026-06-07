<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import {
  getFreePoolStatus,
  registerFreeProvider,
  getFreePoolMethods,
  getFreePoolKeys,
  addFreePoolKey,
  getFreePoolSignupHub,
  importFreePoolEnv,
  discoverFreePool,
  bootstrapFreePool,
  type FreePoolStatusResponse,
  type FreePoolEntry,
} from '../api'

const poolData = ref<FreePoolStatusResponse | null>(null)
const poolKeys = ref<any[]>([])
const methodsData = ref<any>(null)
const loading = ref(false)
const error = ref('')
const message = ref('')
const activeTab = ref<'models' | 'providers' | 'catalog' | 'keys' | 'guide' | 'assistant'>('assistant')

// Add provider form
const showAddForm = ref(false)
const submitting = ref(false)
const newProvider = ref({
  catalog_code: '',
  display_name: '',
  base_url: '',
  protocol: 'openai-completions',
  api_key: '',
  models: '',
})

const knownProviders = [
  { code: 'zhipu-free', name: 'Zhipu GLM (Free Tier)', url: 'https://open.bigmodel.cn/api/paas/v4', models: 'GLM-4-Flash,GLM-4.7-Flash,GLM-4V-Flash' },
  { code: 'siliconflow-free', name: 'SiliconFlow (Free Tier)', url: 'https://api.siliconflow.cn/v1', models: 'Qwen/Qwen2.5-7B-Instruct,deepseek-ai/DeepSeek-R1-Distill-Qwen-7B' },
  { code: 'free-chatgpt', name: 'Free ChatGPT API (Community)', url: 'https://free.v36.cm/v1', models: 'gpt-4o-mini,gpt-3.5-turbo' },
]

// Add key form
const showKeyForm = ref(false)
const newKey = ref({ catalog_code: '', api_key: '', label: '' })
const keySubmitting = ref(false)

// Quick entry
const quickEntry = ref({ catalog_code: '', api_key: '', label: '' })
const quickProbing = ref(false)
const quickSaving = ref(false)
const probeResult = ref<any>(null)

// Signup hub
const signupHub = ref<any>(null)
const hubCategory = ref('all')
const hubQuery = ref('')

// Sync
const syncing = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [status, methods, keys, hub] = await Promise.all([
      getFreePoolStatus(),
      getFreePoolMethods(),
      getFreePoolKeys(),
      getFreePoolSignupHub(),
    ])
    poolData.value = status
    methodsData.value = methods
    poolKeys.value = keys
    signupHub.value = hub
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function sync() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    await load()
    message.value = '同步完成'
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '同步失败'
  } finally {
    syncing.value = false
  }
}

async function runBootstrap() {
  if (!confirm('确认启动免费池引导？')) return
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await bootstrapFreePool()
    message.value = res.status || '引导已启动'
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '引导失败'
  } finally {
    syncing.value = false
  }
}

async function runDiscover() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await discoverFreePool()
    message.value = res.status || '发现已启动'
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '发现失败'
  } finally {
    syncing.value = false
  }
}

async function runImportEnv() {
  syncing.value = true
  error.value = ''
  message.value = ''
  try {
    const res = await importFreePoolEnv()
    message.value = `导入 ${res.imported} 个环境变量`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '导入失败'
  } finally {
    syncing.value = false
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
    const models = newProvider.value.models.split(',').map(s => s.trim()).filter(Boolean)
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
    newProvider.value = { catalog_code: '', display_name: '', base_url: '', protocol: 'openai-completions', api_key: '', models: '' }
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '注册失败'
  } finally {
    submitting.value = false
  }
}

async function submitNewKey() {
  if (!newKey.value.catalog_code || !newKey.value.api_key) {
    error.value = '请填写 Catalog Code 和 API Key'
    return
  }
  keySubmitting.value = true
  error.value = ''
  message.value = ''
  try {
    await addFreePoolKey(newKey.value)
    message.value = '密钥添加成功'
    showKeyForm.value = false
    newKey.value = { catalog_code: '', api_key: '', label: '' }
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '添加失败'
  } finally {
    keySubmitting.value = false
  }
}

function statusClass(entry: FreePoolEntry) {
  if (entry.credential_status !== 'active') return 'badge-red'
  if (entry.availability_state === 'rate_limited' || entry.availability_state === 'cooling' || entry.quota_state === 'exhausted') return 'badge-amber'
  return 'badge-green'
}

function statusLabel(entry: FreePoolEntry) {
  if (entry.credential_status !== 'active') return '已禁用'
  if (entry.availability_state === 'rate_limited') return '限流'
  if (entry.availability_state === 'cooling') return '冷却'
  if (entry.quota_state === 'exhausted') return '配额用尽'
  return '可用'
}

const filteredPool = computed(() => {
  if (!poolData.value) return []
  return poolData.value.pool
})

const stats = computed(() => poolData.value?.stats)

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2>免费资源池</h2>
      <div style="display:flex;gap:8px">
        <button class="btn btn-ghost btn-sm" @click="sync" :disabled="syncing">
          {{ syncing ? '同步中...' : '刷新' }}
        </button>
        <button class="btn btn-primary btn-sm" @click="showAddForm = !showAddForm">
          {{ showAddForm ? '取消' : '添加 Provider' }}
        </button>
      </div>
    </div>

    <p style="color:var(--muted);font-size:13px;margin-bottom:20px">
      管理免费模型资源池。免费模型在路由时优先级最高（composite_score = 0）。当前上限 20 个 Provider。
    </p>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>
    <div v-if="message" class="alert alert-success" style="margin-bottom:12px">{{ message }}</div>

    <!-- Stats -->
    <div v-if="stats" class="stat-grid" style="margin-bottom:20px">
      <div class="stat-card">
        <div class="stat-label">Provider 总数</div>
        <div class="stat-value">{{ stats.total_providers ?? 0 }}</div>
        <div style="font-size:11px;color:var(--muted)">上限 20</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">可用 Provider</div>
        <div class="stat-value" style="color:var(--success)">{{ stats.available_providers ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">模型总数</div>
        <div class="stat-value">{{ stats.total_models ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">免费模型</div>
        <div class="stat-value" style="color:var(--success)">{{ stats.free_models ?? 0 }}</div>
      </div>
    </div>

    <!-- Tab Bar -->
    <div style="display:flex;gap:4px;margin-bottom:16px;border-bottom:1px solid var(--border);padding-bottom:8px">
      <button v-for="tab in (['assistant', 'models', 'providers', 'catalog', 'keys', 'guide'] as const)"
              :key="tab"
              :class="['btn btn-ghost btn-sm', { 'btn-primary': activeTab === tab }]"
              @click="activeTab = tab">
        {{ { assistant: '助手', models: '模型', providers: '提供商', catalog: '目录', keys: '密钥', guide: '指南' }[tab] }}
      </button>
    </div>

    <!-- Assistant Tab -->
    <div v-if="activeTab === 'assistant'">
      <div class="card" style="margin-bottom:16px">
        <h3 style="margin:0 0 12px">快速入门</h3>
        <div style="display:flex;gap:12px;flex-wrap:wrap">
          <button class="btn btn-primary btn-sm" @click="runBootstrap">一键引导</button>
          <button class="btn btn-ghost btn-sm" @click="runDiscover">自动发现</button>
          <button class="btn btn-ghost btn-sm" @click="runImportEnv">导入环境变量</button>
        </div>
      </div>

      <div class="card">
        <h3 style="margin:0 0 12px">Provider 列表 ({{ filteredPool.length }})</h3>
        <div v-if="filteredPool.length === 0" style="text-align:center;padding:24px;color:var(--muted)">
          暂无免费 Provider，点击「添加 Provider」开始
        </div>
        <table v-else class="data-table" style="width:100%;font-size:12px">
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
            <tr v-for="p in filteredPool" :key="p.credential_id">
              <td><code style="font-size:11px">{{ p.catalog_code }}</code></td>
              <td>{{ p.provider_name }}</td>
              <td>
                <div>{{ p.credential_label }}</div>
                <div style="font-size:11px;color:var(--muted)">#{{ p.credential_id }} · {{ p.availability_state || 'ready' }}</div>
              </td>
              <td>
                <span :class="['badge', statusClass(p)]">{{ statusLabel(p) }}</span>
              </td>
              <td>
                <div>总数: <strong>{{ p.total_offers }}</strong></div>
                <div style="font-size:11px;color:var(--muted)">免费: {{ p.free_offers }} / 可用: {{ p.available_offers }}</div>
              </td>
              <td>
                <button class="btn btn-ghost btn-sm" @click="sync">刷新</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Models Tab -->
    <div v-if="activeTab === 'models'">
      <div class="card">
        <h3 style="margin:0 0 12px">免费模型</h3>
        <div v-if="poolData?.models?.length === 0" style="text-align:center;padding:24px;color:var(--muted)">
          暂无免费模型
        </div>
        <div v-else style="overflow-x:auto">
          <table class="data-table" style="width:100%;font-size:12px">
            <thead>
              <tr>
                <th>模型</th>
                <th>提供商</th>
                <th>凭据</th>
                <th>可用</th>
                <th>免费</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in poolData?.models ?? []" :key="m.raw_model_name">
                <td><code style="font-size:11px">{{ m.raw_model_name }}</code></td>
                <td>{{ m.provider_name || '—' }}</td>
                <td>{{ m.credential_id || '—' }}</td>
                <td>
                  <span :class="['badge', m.available ? 'badge-green' : 'badge-gray']">
                    {{ m.available ? '可用' : '不可用' }}
                  </span>
                </td>
                <td>
                  <span :class="['badge', m.free ? 'badge-green' : 'badge-gray']">
                    {{ m.free ? '免费' : '付费' }}
                  </span>
                </td>
                <td>{{ m.status || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Providers Tab -->
    <div v-if="activeTab === 'providers'">
      <div class="card">
        <h3 style="margin:0 0 12px">Provider 管理</h3>
        <p style="color:var(--muted);font-size:13px">Provider 管理功能开发中...</p>
      </div>
    </div>

    <!-- Catalog Tab -->
    <div v-if="activeTab === 'catalog'">
      <div class="card">
        <h3 style="margin:0 0 12px">目录模板</h3>
        <div v-if="poolData?.catalog?.length === 0" style="text-align:center;padding:24px;color:var(--muted)">
          暂无目录模板
        </div>
        <div v-else style="overflow-x:auto">
          <table class="data-table" style="width:100%;font-size:12px">
            <thead>
              <tr>
                <th>代码</th>
                <th>显示名称</th>
                <th>类型</th>
                <th>模型数</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in poolData?.catalog ?? []" :key="c.code">
                <td><code style="font-size:11px">{{ c.code }}</code></td>
                <td>{{ c.display_name }}</td>
                <td>{{ c.type || '—' }}</td>
                <td>{{ c.model_count ?? 0 }}</td>
                <td>
                  <span :class="['badge', c.active ? 'badge-green' : 'badge-gray']">
                    {{ c.active ? '活跃' : '未配置' }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Keys Tab -->
    <div v-if="activeTab === 'keys'">
      <div class="card">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
          <h3 style="margin:0">密钥管理 ({{ poolKeys.length }})</h3>
          <button class="btn btn-primary btn-sm" @click="showKeyForm = !showKeyForm">
            {{ showKeyForm ? '取消' : '+ 添加密钥' }}
          </button>
        </div>
        <div v-if="showKeyForm" class="card" style="margin-bottom:12px;background:var(--surface-secondary)">
          <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:12px">
            <div>
              <label style="font-size:12px;display:block;margin-bottom:4px">Catalog Code *</label>
              <input class="input" v-model="newKey.catalog_code" placeholder="zhipu-free" />
            </div>
            <div>
              <label style="font-size:12px;display:block;margin-bottom:4px">API Key *</label>
              <input class="input" type="password" v-model="newKey.api_key" placeholder="sk-..." />
            </div>
            <div>
              <label style="font-size:12px;display:block;margin-bottom:4px">标签（可选）</label>
              <input class="input" v-model="newKey.label" placeholder="生产密钥" />
            </div>
          </div>
          <div style="margin-top:12px">
            <button class="btn btn-primary btn-sm" @click="submitNewKey" :disabled="keySubmitting">
              {{ keySubmitting ? '添加中...' : '添加' }}
            </button>
          </div>
        </div>
        <div v-if="poolKeys.length === 0" style="text-align:center;padding:24px;color:var(--muted)">
          暂无密钥
        </div>
        <div v-else style="overflow-x:auto">
          <table class="data-table" style="width:100%;font-size:12px">
            <thead>
              <tr>
                <th>Catalog Code</th>
                <th>标签</th>
                <th>状态</th>
                <th>最后使用</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="k in poolKeys" :key="k.id">
                <td><code style="font-size:11px">{{ k.catalog_code }}</code></td>
                <td>{{ k.label || '—' }}</td>
                <td>
                  <span :class="['badge', k.status === 'active' ? 'badge-green' : 'badge-gray']">
                    {{ k.status || '—' }}
                  </span>
                </td>
                <td>{{ k.last_used_at ? new Date(k.last_used_at).toLocaleString('zh-CN') : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Guide Tab -->
    <div v-if="activeTab === 'guide'">
      <div class="card">
        <h3 style="margin:0 0 12px">使用指南</h3>
        <div style="font-size:13px;line-height:1.8">
          <p><strong>1. 一键引导</strong> - 自动注册所有已知的免费 Provider</p>
          <p><strong>2. 自动发现</strong> - 扫描现有 Provider 中的免费模型</p>
          <p><strong>3. 导入环境变量</strong> - 从环境变量导入 API Key</p>
          <p><strong>4. 手动添加</strong> - 点击「添加 Provider」手动注册</p>
          <p><strong>5. 密钥管理</strong> - 在「密钥」标签页管理 API Key</p>
          <p style="color:var(--muted);margin-top:16px">免费模型在路由时优先级最高（composite_score = 0），会优先使用。</p>
        </div>
      </div>
    </div>

    <!-- Add Provider Modal -->
    <div v-if="showAddForm" class="modal-overlay" @click.self="showAddForm = false">
      <div class="modal" style="max-width:600px">
        <h3>添加免费 Provider</h3>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:16px">
          <div>
            <label style="font-size:12px;display:block;margin-bottom:4px">Catalog Code *</label>
            <input class="input" v-model="newProvider.catalog_code" placeholder="zhipu-free" />
          </div>
          <div>
            <label style="font-size:12px;display:block;margin-bottom:4px">显示名称</label>
            <input class="input" v-model="newProvider.display_name" placeholder="Zhipu GLM Free" />
          </div>
          <div style="grid-column:1/-1">
            <label style="font-size:12px;display:block;margin-bottom:4px">Base URL *</label>
            <input class="input" v-model="newProvider.base_url" placeholder="https://api.example.com/v1" />
          </div>
          <div>
            <label style="font-size:12px;display:block;margin-bottom:4px">协议</label>
            <select class="input" v-model="newProvider.protocol">
              <option value="openai-completions">openai-completions</option>
              <option value="openai-responses">openai-responses</option>
              <option value="anthropic-messages">anthropic-messages</option>
            </select>
          </div>
          <div>
            <label style="font-size:12px;display:block;margin-bottom:4px">API Key（可选）</label>
            <input class="input" type="password" v-model="newProvider.api_key" placeholder="sk-..." />
          </div>
          <div style="grid-column:1/-1">
            <label style="font-size:12px;display:block;margin-bottom:4px">模型列表（逗号分隔，可选）</label>
            <input class="input" v-model="newProvider.models" placeholder="gpt-4o-mini, gpt-3.5-turbo" />
          </div>
        </div>
        <div style="font-size:12px;color:var(--muted);margin-bottom:12px">
          已知免费 Provider 模板：
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px">
          <button v-for="(p, idx) in knownProviders" :key="p.code" class="btn btn-ghost btn-sm" @click="useTemplate(idx)">
            {{ p.name }}
          </button>
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="showAddForm = false">取消</button>
          <button class="btn btn-primary" @click="submitNew" :disabled="submitting">
            {{ submitting ? '注册中...' : '注册 Provider' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
