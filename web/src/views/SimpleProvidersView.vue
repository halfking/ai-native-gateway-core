<script setup lang="ts">
// SimpleProvidersView.vue — 简易模式供应商页（2026-11-17）
//
// 设计稿：docs/03-design/02-feature-design/simple-mode-concept-v2.html
//
// ★ L2 落点：供应商页（两字段快速接入 + 关停/零流量折叠 + 遮蔽账号）
//   + 模型管理弹层（三段选择器 + 启停供应商的 model_offers）
//
// ★ 数据口径**与专家面完全一致**（同一批 API、同一张表）：
//   /api/providers ⇒ providers 表；/api/catalog ⇒ provider_catalog 表。
//   简易模式不新建任何表、不新造任何统计口径。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getProviders,
  getCatalog,
  createProvider,
  addCredential,
  type Provider,
  type CatalogEntry,
} from '../api'
import {
  ElMessage,
  ElCollapse,
  ElCollapseItem,
  ElButton,
  ElCheckbox,
  ElDialog,
  ElInput,
  ElOption,
  ElSelect,
} from 'element-plus'
import PageHeader from '../components/ui/PageHeader.vue'
import { useWorkMode } from '../composables/useWorkMode'
import { surfaceError } from '../i18n/errorSurface'
import { fmtDateTimeShort } from '../i18n/useFormat'

const { t } = useI18n()
const { isSimple } = useWorkMode()

// ─── 状态 ────────────────────────────────────────────────────────────────────

const providers = ref<Provider[]>([])
const catalog = ref<CatalogEntry[]>([])
const loading = ref(false)
const error = ref('')

// 快速接入表单
const quickAddVisible = ref(false)
const selectedCatalogCode = ref('')
const apiKeyInput = ref('')
const adding = ref(false)

// 遮蔽账号开关
const maskAccounts = ref(false)

// 折叠状态
const activeCollapse = ref<string[]>(['active'])

// ─── 数据加载 ────────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [p, c] = await Promise.all([
      getProviders(),
      getCatalog(),
    ])
    providers.value = p
    catalog.value = c.filter(e => !e.hidden) // 过滤掉隐藏的目录项
  } catch (e) {
    // ★ 2026-11-17：原来直接展示原始 error.message ⇒ 用户看到的是后端英文串
    //   （i18n:error-outlet 门判红）。改走 surfaceError：本地化主文案 + 可选原因。
    error.value = surfaceError(e, { key: 'simpleMode.providers.loadFailed' })
  } finally {
    loading.value = false
  }
}

onMounted(load)

// ─── 分组逻辑 ────────────────────────────────────────────────────────────────

interface ProviderRow {
  provider: Provider
  name: string
  stateKey: 'ok' | 'warn' | 'off' | 'unknown'
  stateLabel: string
  credentialCount: number
  requestCount: number
  masked: boolean
}

/** 活跃供应商：enabled=true 且有健康凭据 */
const activeProviders = computed<ProviderRow[]>(() =>
  providers.value
    .filter(p => p.enabled && (p.healthy_credential_count ?? 0) > 0)
    .map(toRow)
)

/** 关停/零流量供应商：disabled 或 零健康凭据 */
const offProviders = computed<ProviderRow[]>(() =>
  providers.value
    .filter(p => !p.enabled || (p.healthy_credential_count ?? 0) === 0)
    .map(toRow)
)

function toRow(p: Provider): ProviderRow {
  const masked = maskAccounts.value
  return {
    provider: p,
    name: masked ? maskText(p.display_name) : p.display_name,
    stateKey: stateKeyOf(p),
    stateLabel: stateLabelOf(p),
    credentialCount: p.active_credential_count,
    requestCount: 0, // TODO: 从用量API获取（本轮暂无后端聚合）
    masked,
  }
}

function stateKeyOf(p: Provider): ProviderRow['stateKey'] {
  if (!p.enabled) return 'off'
  if (p.health_status === 'healthy') return 'ok'
  if (p.health_status === 'warning') return 'warn'
  return 'unknown'
}

function stateLabelOf(p: Provider): string {
  const key = stateKeyOf(p)
  switch (key) {
    case 'ok': return t('common.status.healthy')
    case 'warn': return t('common.status.warning')
    case 'off': return t('common.status.disabled')
    default: return t('common.status.unknown')
  }
}

/** 遮蔽文本：保留前2字符 + 中间用 *** 替换 */
function maskText(text: string): string {
  if (!text || text.length <= 2) return '***'
  return text.slice(0, 2) + '***'
}

// ─── 快速接入 ────────────────────────────────────────────────────────────────

function showQuickAdd() {
  quickAddVisible.value = true
  selectedCatalogCode.value = ''
  apiKeyInput.value = ''
}

const selectedCatalog = computed(() =>
  catalog.value.find(c => c.code === selectedCatalogCode.value)
)

const canAdd = computed(() =>
  selectedCatalogCode.value && apiKeyInput.value.trim()
)

async function handleQuickAdd() {
  if (!canAdd.value) return

  adding.value = true
  try {
    const cat = selectedCatalog.value!
    
    // 1. 创建供应商（使用目录的默认值）
    const provider = await createProvider({
      catalog_code: cat.code,
      display_name: cat.display_name,
      base_url: cat.base_url_template,
      protocol: cat.protocol,
    })

    // 2. 添加凭据
    await addCredential(provider.id, {
      api_key: apiKeyInput.value.trim(),
      label: t('simpleMode.providers.primaryCredentialLabel', { name: cat.display_name }),
    })

    ElMessage.success(t('simpleMode.providers.added'))
    quickAddVisible.value = false

    // 3. 重新加载列表
    await load()
  } catch (e) {
    // ★ 2026-11-17：同 load()，原始 error.message 不直接上屏（error-outlet 门）。
    ElMessage.error(surfaceError(e, { key: 'simpleMode.providers.addFailed' }))
  } finally {
    adding.value = false
  }
}

// ─── 目录选项 ────────────────────────────────────────────────────────────────

const catalogOptions = computed(() =>
  catalog.value.map(c => ({
    value: c.code,
    label: c.display_name,
    description: c.category,
  }))
)

// ─── 状态样式 ────────────────────────────────────────────────────────────────

function stateClass(key: ProviderRow['stateKey']): string {
  switch (key) {
    case 'ok': return 'state-ok'
    case 'warn': return 'state-warn'
    case 'off': return 'state-off'
    default: return 'state-unknown'
  }
}
</script>

<template>
  <div class="simple-providers-view">
    <PageHeader
      :title="t('simpleMode.providers.title')"
      :subtitle="t('simpleMode.providers.subtitle')"
    >
      <template #actions>
        <el-checkbox v-model="maskAccounts" class="mask-toggle">
          {{ t('simpleMode.providers.maskAccounts') }}
        </el-checkbox>
        <el-button type="primary" @click="showQuickAdd">
          {{ t('simpleMode.providers.quickAdd') }}
        </el-button>
      </template>
    </PageHeader>

    <div v-if="loading" class="loading">
      {{ t('common.loading') }}
    </div>

    <div v-else-if="error" class="error">
      {{ error }}
    </div>

    <div v-else class="provider-list">
      <!-- 活跃供应商 -->
      <div v-if="activeProviders.length > 0" class="section">
        <div
          v-for="row in activeProviders"
          :key="row.provider.id"
          class="provider-card"
        >
          <div class="card-header">
            <div class="name-group">
              <span class="name">{{ row.name }}</span>
              <span :class="['state-badge', stateClass(row.stateKey)]">
                {{ row.stateLabel }}
              </span>
            </div>
          </div>
          <div class="card-body">
            <div class="info-item">
              <span class="label">Base URL</span>
              <span class="value mono">{{ maskAccounts ? '***' : row.provider.base_url || '—' }}</span>
            </div>
            <div class="info-item">
              <span class="label">{{ t('common.credentials') }}</span>
              <span class="value">{{ row.credentialCount }}</span>
            </div>
            <div class="info-item">
              <span class="label">{{ t('simpleMode.providers.last30Days') }}</span>
              <span class="value">{{ row.requestCount }} {{ t('simpleMode.providers.requests') }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 关停/零流量折叠 -->
      <el-collapse v-model="activeCollapse" class="off-section">
        <el-collapse-item name="off">
          <template #title>
            <div class="collapse-title">
              <span>{{ t('simpleMode.providers.offLabel') }}</span>
              <span class="count-badge">{{ offProviders.length }}</span>
            </div>
          </template>
          <div v-if="offProviders.length === 0" class="empty-hint">
            {{ t('simpleMode.providers.offEmpty') }}
          </div>
          <div
            v-for="row in offProviders"
            :key="row.provider.id"
            class="provider-card off"
          >
            <div class="card-header">
              <div class="name-group">
                <span class="name">{{ row.name }}</span>
                <span :class="['state-badge', stateClass(row.stateKey)]">
                  {{ row.stateLabel }}
                </span>
              </div>
            </div>
            <div class="card-body">
              <div class="info-item">
                <span class="label">{{ t('common.credentials') }}</span>
                <span class="value">{{ row.credentialCount }}</span>
              </div>
            </div>
          </div>
        </el-collapse-item>
      </el-collapse>

      <!-- 空状态 -->
      <div v-if="providers.length === 0" class="empty-state">
        <div class="empty-icon">📦</div>
        <div class="empty-text">{{ t('simpleMode.providers.noProvider') }}</div>
        <el-button type="primary" @click="showQuickAdd">
          {{ t('simpleMode.providers.quickAdd') }}
        </el-button>
      </div>
    </div>

    <!-- 快速接入对话框 -->
    <el-dialog
      v-model="quickAddVisible"
      :title="t('simpleMode.providers.quickAdd')"
      width="540px"
      :close-on-click-modal="false"
      style="max-height: 80vh; overflow-y: auto;"
    >
      <div class="quick-add-form">
        <p class="hint">{{ t('simpleMode.providers.quickAddHint') }}</p>

        <div class="form-item">
          <label class="form-label">{{ t('simpleMode.providers.catalog') }}</label>
          <el-select
            v-model="selectedCatalogCode"
            :placeholder="t('simpleMode.providers.catalogPlaceholder')"
            filterable
            class="full-width"
          >
            <el-option
              v-for="opt in catalogOptions"
              :key="opt.value"
              :value="opt.value"
              :label="opt.label"
            >
              <div class="catalog-option">
                <span class="opt-label">{{ opt.label }}</span>
                <span class="opt-desc">{{ opt.description }}</span>
              </div>
            </el-option>
          </el-select>
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('simpleMode.providers.apiKey') }}</label>
          <el-input
            v-model="apiKeyInput"
            type="textarea"
            :rows="3"
            :placeholder="t('simpleMode.providers.apiKeyPlaceholder')"
          />
        </div>

        <!-- 自动填充的默认值提示 -->
        <div v-if="selectedCatalog" class="auto-fill-hint">
          <div class="hint-title">{{ t('common.autoFilled') }}:</div>
          <ul class="hint-list">
            <li><strong>Base URL:</strong> {{ selectedCatalog.base_url_template }}</li>
            <li><strong>{{ t('common.protocol') }}:</strong> {{ selectedCatalog.protocol }}</li>
            <li><strong>{{ t('simpleMode.providers.models') }}:</strong> {{ selectedCatalog.models_manifest_json?.length || 0 }} {{ t('simpleMode.providers.modelsUnit') }}</li>
          </ul>
        </div>
      </div>

      <template #footer>
        <el-button @click="quickAddVisible = false">
          {{ t('common.cancel') }}
        </el-button>
        <el-button
          type="primary"
          :disabled="!canAdd"
          :loading="adding"
          @click="handleQuickAdd"
        >
          {{ adding ? t('simpleMode.providers.adding') : t('simpleMode.providers.add') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.simple-providers-view {
  padding: var(--kx-space-5);
}

.mask-toggle {
  margin-right: var(--kx-space-3);
}

.loading,
.error {
  padding: var(--kx-space-6);
  text-align: center;
  color: var(--kx-muted);
}

.error {
  color: var(--kx-danger);
}

.provider-list {
  max-width: 1200px;
}

.section {
  display: grid;
  gap: var(--kx-space-4);
  margin-bottom: var(--kx-space-5);
}

.provider-card {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: var(--radius-card);
  padding: var(--kx-space-4);
  transition: border-color 0.2s;
}

.provider-card:hover {
  border-color: var(--kx-border-strong);
}

.provider-card.off {
  opacity: 0.6;
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--kx-space-3);
}

.name-group {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
}

.name {
  font-size: var(--kx-text-lg);
  font-weight: 600;
}

.state-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: var(--kx-text-xs);
  font-weight: 500;
}

.state-ok {
  background: var(--kx-success-soft);
  color: var(--kx-success);
}

.state-warn {
  background: var(--kx-warning-soft);
  color: var(--kx-warning);
}

.state-off {
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
}

.state-unknown {
  background: var(--kx-surface-2);
  color: var(--kx-muted);
}

.card-body {
  display: flex;
  gap: var(--kx-space-5);
  flex-wrap: wrap;
}

.info-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.info-item .label {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.info-item .value {
  font-size: var(--kx-text-md);
  font-weight: 500;
}

.info-item .value.mono {
  font-family: var(--kx-font-mono);
  font-size: var(--kx-text-sm);
}

/* 折叠区 */
.off-section {
  margin-top: var(--kx-space-5);
}

.collapse-title {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  font-weight: 500;
}

.count-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  background: var(--kx-surface-2);
  border-radius: 10px;
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.empty-hint {
  padding: var(--kx-space-4);
  text-align: center;
  color: var(--kx-muted);
  font-size: var(--kx-text-sm);
}

/* 空状态 */
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: var(--kx-space-8);
  text-align: center;
}

.empty-icon {
  font-size: 48px;
  margin-bottom: var(--kx-space-3);
}

.empty-text {
  margin-bottom: var(--kx-space-4);
  color: var(--kx-muted);
  font-size: var(--kx-text-md);
}

/* 快速接入表单 */
.quick-add-form {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-4);
}

.hint {
  padding: var(--kx-space-3);
  background: var(--kx-surface-2);
  border-radius: var(--radius);
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
  margin: 0;
}

.form-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.form-label {
  font-weight: 500;
  font-size: var(--kx-text-sm);
}

.full-width {
  width: 100%;
}

.catalog-option {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.opt-label {
  font-weight: 500;
}

.opt-desc {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.auto-fill-hint {
  padding: var(--kx-space-3);
  background: var(--kx-primary-soft);
  border-radius: var(--radius);
  font-size: var(--kx-text-sm);
}

.hint-title {
  font-weight: 500;
  margin-bottom: 8px;
}

.hint-list {
  margin: 0;
  padding-left: 20px;
}

.hint-list li {
  margin-bottom: 4px;
  color: var(--kx-muted);
}

.hint-list strong {
  color: var(--kx-text);
}
</style>
