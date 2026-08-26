<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, onMounted, computed } from 'vue'
import { listSettings, updateSetting, SettingItem } from '../api/settings'
import { req } from '../api/_core'

const { t } = useI18n({ useScope: 'global' })

// ========== 标签页控制 ==========
const activeTab = ref<'overview' | 'config' | 'records'>('overview')

// ========== 统计数据类型 ==========
interface ComplianceStats {
  total_checks: number
  pii_hits: number
  secret_hits: number
  toxicity_hits: number
  jailbreak_hits: number
  avg_latency_ms: number
  last_updated: string
}

interface ComplianceRecord {
  id: number
  session_id: string
  tenant_id: string
  check_type: string
  hit_type: string
  severity: number
  redacted: boolean
  content_preview: string
  created_at: string
}

// ========== 配置数据类型 ==========
interface OutputComplianceConfig {
  enabled: boolean
  redaction_mode: string
  pii_engine: string
  toxicity_engine: string
  check_pii: boolean
  check_toxicity: boolean
  check_bias: boolean
  check_hallucination: boolean
  check_secrets: boolean
  check_internal_ip: boolean
  check_jailbreak_response: boolean
  pii_threshold: number
  toxicity_threshold: number
  auto_redact: boolean
  redact_email: boolean
  redact_phone: boolean
  redact_id_card: boolean
  redact_credit_card: boolean
  redact_bank_card: boolean
  redact_jwt: boolean
  redact_password: boolean
  block_message: string
  sampling_rate: number
  retention_days: number
}

// ========== 状态 ==========
const stats = ref<ComplianceStats | null>(null)
const statsLoading = ref(false)
const statsError = ref('')

const records = ref<ComplianceRecord[]>([])
const recordsLoading = ref(false)
const recordsPage = ref(1)
const recordsTotal = ref(0)
const recordsSize = ref(50)

const config = ref<OutputComplianceConfig>({
  enabled: true,
  redaction_mode: 'owner_mismatch',
  pii_engine: 'regex',
  toxicity_engine: 'keyword',
  check_pii: true,
  check_toxicity: false,
  check_bias: false,
  check_hallucination: false,
  check_secrets: true,
  check_internal_ip: true,
  check_jailbreak_response: true,
  pii_threshold: 0.7,
  toxicity_threshold: 0.8,
  auto_redact: true,
  redact_email: true,
  redact_phone: true,
  redact_id_card: true,
  redact_credit_card: true,
  redact_bank_card: false,
  redact_jwt: true,
  redact_password: true,
  block_message: 'Content blocked due to compliance policy',
  sampling_rate: 1.0,
  retention_days: 90,
})

const configLoading = ref(false)
const configSaving = ref(false)
const configError = ref('')
const configSuccess = ref('')

const filterTenantID = ref('')
const filterCheckType = ref('')
const filterHitType = ref('')

const totalPages = computed(() => Math.max(1, Math.ceil(recordsTotal.value / recordsSize.value)))

// ========== 加载统计数据 ==========
async function loadStats() {
  statsLoading.value = true
  statsError.value = ''
  try {
    const params = new URLSearchParams()
    if (filterTenantID.value) params.append('tenant_id', filterTenantID.value)
    const url = `/api/admin/output-compliance/stats${params.toString() ? '?' + params.toString() : ''}`
    const data = await req<ComplianceStats>('GET', url)
    stats.value = data
  } catch (e: unknown) {
    statsError.value = e instanceof Error ? e.message : String(e)
    // 如果API不存在，使用模拟数据
    stats.value = {
      total_checks: 0,
      pii_hits: 0,
      secret_hits: 0,
      toxicity_hits: 0,
      jailbreak_hits: 0,
      avg_latency_ms: 0,
      last_updated: new Date().toISOString(),
    }
  } finally {
    statsLoading.value = false
  }
}

// ========== 加载命中记录 ==========
async function loadRecords() {
  recordsLoading.value = true
  try {
    const params = new URLSearchParams()
    if (filterTenantID.value) params.append('tenant_id', filterTenantID.value)
    if (filterCheckType.value) params.append('check_type', filterCheckType.value)
    if (filterHitType.value) params.append('hit_type', filterHitType.value)
    params.append('limit', recordsSize.value.toString())
    params.append('offset', ((recordsPage.value - 1) * recordsSize.value).toString())
    const url = `/api/admin/output-compliance/records?${params.toString()}`
    const data = await req<{ records: ComplianceRecord[]; total: number }>('GET', url)
    records.value = data.records || []
    recordsTotal.value = data.total || 0
  } catch (e: unknown) {
    // API不存在时使用空数据
    records.value = []
    recordsTotal.value = 0
  } finally {
    recordsLoading.value = false
  }
}

// ========== 加载配置 ==========
async function loadConfig() {
  configLoading.value = true
  configError.value = ''
  try {
    const settings = await listSettings({ category: 'output_compliance' })
    const items = 'items' in settings ? settings.items : (settings as unknown as SettingItem[])

    // 映射配置
    const mapping: Record<string, keyof OutputComplianceConfig> = {
      'output_compliance.enabled': 'enabled',
      'output_compliance.redaction_mode': 'redaction_mode',
      'output_compliance.pii_engine': 'pii_engine',
      'output_compliance.toxicity_engine': 'toxicity_engine',
      'output_compliance.check_pii': 'check_pii',
      'output_compliance.check_toxicity': 'check_toxicity',
      'output_compliance.check_bias': 'check_bias',
      'output_compliance.check_hallucination': 'check_hallucination',
      'output_compliance.check_secrets': 'check_secrets',
      'output_compliance.check_internal_ip': 'check_internal_ip',
      'output_compliance.check_jailbreak_response': 'check_jailbreak_response',
      'output_compliance.pii_threshold': 'pii_threshold',
      'output_compliance.toxicity_threshold': 'toxicity_threshold',
      'output_compliance.auto_redact': 'auto_redact',
      'output_compliance.redact_email': 'redact_email',
      'output_compliance.redact_phone': 'redact_phone',
      'output_compliance.redact_id_card': 'redact_id_card',
      'output_compliance.redact_credit_card': 'redact_credit_card',
      'output_compliance.redact_bank_card': 'redact_bank_card',
      'output_compliance.redact_jwt': 'redact_jwt',
      'output_compliance.redact_password': 'redact_password',
      'output_compliance.block_message': 'block_message',
      'output_compliance.sampling_rate': 'sampling_rate',
      'output_compliance.retention_days': 'retention_days',
    }

    for (const item of items) {
      if (item && item.key && mapping[item.key]) {
        const configKey = mapping[item.key]
        const value = item.value
        if (typeof config.value[configKey] === 'boolean') {
          (config.value as any)[configKey] = value === 'true' || value === true
        } else if (typeof config.value[configKey] === 'number') {
          (config.value as any)[configKey] = Number(value)
        } else {
          (config.value as any)[configKey] = value
        }
      }
    }
  } catch (e: unknown) {
    configError.value = e instanceof Error ? e.message : String(e)
  } finally {
    configLoading.value = false
  }
}

// ========== 保存配置 ==========
async function saveConfig() {
  configSaving.value = true
  configError.value = ''
  configSuccess.value = ''
  try {
    const mapping: Record<keyof OutputComplianceConfig, string> = {
      enabled: 'output_compliance.enabled',
      redaction_mode: 'output_compliance.redaction_mode',
      pii_engine: 'output_compliance.pii_engine',
      toxicity_engine: 'output_compliance.toxicity_engine',
      check_pii: 'output_compliance.check_pii',
      check_toxicity: 'output_compliance.check_toxicity',
      check_bias: 'output_compliance.check_bias',
      check_hallucination: 'output_compliance.check_hallucination',
      check_secrets: 'output_compliance.check_secrets',
      check_internal_ip: 'output_compliance.check_internal_ip',
      check_jailbreak_response: 'output_compliance.check_jailbreak_response',
      pii_threshold: 'output_compliance.pii_threshold',
      toxicity_threshold: 'output_compliance.toxicity_threshold',
      auto_redact: 'output_compliance.auto_redact',
      redact_email: 'output_compliance.redact_email',
      redact_phone: 'output_compliance.redact_phone',
      redact_id_card: 'output_compliance.redact_id_card',
      redact_credit_card: 'output_compliance.redact_credit_card',
      redact_bank_card: 'output_compliance.redact_bank_card',
      redact_jwt: 'output_compliance.redact_jwt',
      redact_password: 'output_compliance.redact_password',
      block_message: 'output_compliance.block_message',
      sampling_rate: 'output_compliance.sampling_rate',
      retention_days: 'output_compliance.retention_days',
    }

    for (const [key, settingKey] of Object.entries(mapping)) {
      await updateSetting(settingKey, { value: (config.value as any)[key] })
    }
    configSuccess.value = t('outputCompliance.config.saveSuccess')
    setTimeout(() => { configSuccess.value = '' }, 3000)
  } catch (e: unknown) {
    configError.value = e instanceof Error ? e.message : String(e)
  } finally {
    configSaving.value = false
  }
}

// ========== 辅助函数 ==========
function fmtDate(s: string) {
  if (!s) return '-'
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

function changeRecordsPage(delta: number) {
  const next = recordsPage.value + delta
  if (next < 1 || next > totalPages.value) return
  recordsPage.value = next
  loadRecords()
}

function severityColor(severity: number): string {
  if (severity >= 8) return '#ef4444'
  if (severity >= 5) return '#f59e0b'
  return '#10b981'
}

onMounted(() => {
  loadStats()
  loadRecords()
  if (activeTab.value === 'config') {
    loadConfig()
  }
})
</script>

<template>
  <div class="output-compliance-view">
    <div class="view-header">
      <h2>{{ t('outputCompliance.title') }}</h2>
      <p class="view-subtitle">{{ t('outputCompliance.subtitle') }}</p>
    </div>

    <!-- 标签页 -->
    <div class="tab-bar">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'overview' }"
        @click="activeTab = 'overview'; loadStats()"
      >
        {{ t('outputCompliance.tabs.overview') }}
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'records' }"
        @click="activeTab = 'records'; loadRecords()"
      >
        {{ t('outputCompliance.tabs.records') }}
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'config' }"
        @click="activeTab = 'config'; loadConfig()"
      >
        {{ t('outputCompliance.tabs.config') }}
      </button>
    </div>

    <!-- 概览标签页 -->
    <div v-if="activeTab === 'overview'">
      <div v-if="statsLoading" class="loading-state">
        <span class="spinner"></span>
        {{ t('outputCompliance.loading') }}
      </div>
      <div v-else-if="statsError" class="error-banner">⚠️ {{ statsError }}</div>
      <div v-else-if="stats" class="stats-grid">
        <div class="stat-card">
          <div class="stat-label">{{ t('outputCompliance.stats.totalChecks') }}</div>
          <div class="stat-value">{{ stats.total_checks.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('outputCompliance.stats.piiHits') }}</div>
          <div class="stat-value stat-danger">{{ stats.pii_hits.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('outputCompliance.stats.secretHits') }}</div>
          <div class="stat-value stat-danger">{{ stats.secret_hits.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('outputCompliance.stats.toxicityHits') }}</div>
          <div class="stat-value stat-warn">{{ stats.toxicity_hits.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('outputCompliance.stats.jailbreakHits') }}</div>
          <div class="stat-value stat-warn">{{ stats.jailbreak_hits.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('outputCompliance.stats.avgLatency') }}</div>
          <div class="stat-value">{{ stats.avg_latency_ms.toFixed(0) }} ms</div>
        </div>
      </div>
    </div>

    <!-- 命中记录标签页 -->
    <div v-if="activeTab === 'records'">
      <!-- 筛选 -->
      <div class="filter-bar">
        <input
          v-model="filterTenantID"
          type="text"
          :placeholder="t('outputCompliance.filter.tenantID')"
          class="filter-input"
        />
        <select v-model="filterCheckType" class="filter-select">
          <option value="">{{ t('outputCompliance.filter.allCheckTypes') }}</option>
          <option value="pii">{{ t('outputCompliance.checkType.pii') }}</option>
          <option value="secret">{{ t('outputCompliance.checkType.secret') }}</option>
          <option value="toxicity">{{ t('outputCompliance.checkType.toxicity') }}</option>
          <option value="jailbreak">{{ t('outputCompliance.checkType.jailbreak') }}</option>
        </select>
        <button class="btn-primary" @click="recordsPage = 1; loadRecords()">
          {{ t('outputCompliance.search') }}
        </button>
      </div>

      <!-- 记录列表 -->
      <div class="table-container">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('outputCompliance.tableHeaders.id') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.session') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.tenant') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.checkType') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.hitType') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.severity') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.redacted') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.content') }}</th>
              <th>{{ t('outputCompliance.tableHeaders.createdAt') }}</th>
            </tr>
          </thead>
          <tbody v-if="recordsLoading">
            <tr>
              <td colspan="9" class="loading-cell">
                <span class="spinner"></span>
                {{ t('outputCompliance.loading') }}
              </td>
            </tr>
          </tbody>
          <tbody v-else-if="records.length === 0">
            <tr>
              <td colspan="9" class="empty-cell">{{ t('outputCompliance.empty') }}</td>
            </tr>
          </tbody>
          <tbody v-else>
            <tr v-for="rec in records" :key="rec.id">
              <td>{{ rec.id }}</td>
              <td class="session-id">{{ rec.session_id }}</td>
              <td>{{ rec.tenant_id }}</td>
              <td>
                <span class="badge-blue">{{ rec.check_type }}</span>
              </td>
              <td>
                <span class="badge-yellow">{{ rec.hit_type }}</span>
              </td>
              <td :style="{ color: severityColor(rec.severity) }">
                {{ rec.severity.toFixed(1) }}
              </td>
              <td>
                <span :class="rec.redacted ? 'badge-green' : 'badge-gray'">
                  {{ rec.redacted ? t('outputCompliance.yes') : t('outputCompliance.no') }}
                </span>
              </td>
              <td class="content-preview">{{ rec.content_preview }}</td>
              <td class="date-cell">{{ fmtDate(rec.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 分页 -->
      <div class="pagination">
        <button class="btn-secondary" :disabled="recordsPage <= 1" @click="changeRecordsPage(-1)">
          {{ t('outputCompliance.pagination.previous') }}
        </button>
        <span class="page-info">
          {{ t('outputCompliance.pagination.info', { current: recordsPage, total: totalPages, count: recordsTotal }) }}
        </span>
        <button class="btn-secondary" :disabled="recordsPage >= totalPages" @click="changeRecordsPage(1)">
          {{ t('outputCompliance.pagination.next') }}
        </button>
      </div>
    </div>

    <!-- 配置标签页 -->
    <div v-if="activeTab === 'config'" class="config-panel">
      <div v-if="configLoading" class="loading-state">
        <span class="spinner"></span>
        {{ t('outputCompliance.config.loading') }}
      </div>
      <div v-else-if="configError" class="error-banner">⚠️ {{ configError }}</div>
      <div v-else class="config-form">
        <div v-if="configSuccess" class="success-banner">✅ {{ configSuccess }}</div>

        <!-- 开关配置 -->
        <div class="config-section">
          <h3>{{ t('outputCompliance.config.moduleControl') }}</h3>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.enabled') }}</label>
            <input v-model="config.enabled" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactionMode') }}</label>
            <select v-model="config.redaction_mode" class="form-select">
              <option value="off">{{ t('outputCompliance.config.redactionOff') }}</option>
              <option value="always">{{ t('outputCompliance.config.redactionAlways') }}</option>
              <option value="owner_mismatch">{{ t('outputCompliance.config.redactionOwnerMismatch') }}</option>
            </select>
          </div>
        </div>

        <!-- 检测项配置 -->
        <div class="config-section">
          <h3>{{ t('outputCompliance.config.detectionItems') }}</h3>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.checkPII') }}</label>
            <input v-model="config.check_pii" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.checkToxicity') }}</label>
            <input v-model="config.check_toxicity" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.checkSecrets') }}</label>
            <input v-model="config.check_secrets" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.checkInternalIP') }}</label>
            <input v-model="config.check_internal_ip" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.checkJailbreak') }}</label>
            <input v-model="config.check_jailbreak_response" type="checkbox" class="form-checkbox" />
          </div>
        </div>

        <!-- 脱敏项配置 -->
        <div class="config-section">
          <h3>{{ t('outputCompliance.config.redactionItems') }}</h3>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.autoRedact') }}</label>
            <input v-model="config.auto_redact" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactEmail') }}</label>
            <input v-model="config.redact_email" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactPhone') }}</label>
            <input v-model="config.redact_phone" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactIDCard') }}</label>
            <input v-model="config.redact_id_card" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactCreditCard') }}</label>
            <input v-model="config.redact_credit_card" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactJWT') }}</label>
            <input v-model="config.redact_jwt" type="checkbox" class="form-checkbox" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.redactPassword') }}</label>
            <input v-model="config.redact_password" type="checkbox" class="form-checkbox" />
          </div>
        </div>

        <!-- 阈值配置 -->
        <div class="config-section">
          <h3>{{ t('outputCompliance.config.thresholds') }}</h3>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.piiThreshold') }} ({{ config.pii_threshold }})</label>
            <input v-model.number="config.pii_threshold" type="range" min="0" max="1" step="0.1" class="form-range" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.toxicityThreshold') }} ({{ config.toxicity_threshold }})</label>
            <input v-model.number="config.toxicity_threshold" type="range" min="0" max="1" step="0.1" class="form-range" />
          </div>
        </div>

        <!-- 阻断配置 -->
        <div class="config-section">
          <h3>{{ t('outputCompliance.config.blocking') }}</h3>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.blockMessage') }}</label>
            <input v-model="config.block_message" type="text" class="form-input" />
          </div>
        </div>

        <!-- 采样和保留配置 -->
        <div class="config-section">
          <h3>{{ t('outputCompliance.config.dataManagement') }}</h3>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.samplingRate') }} ({{ config.sampling_rate }})</label>
            <input v-model.number="config.sampling_rate" type="range" min="0.1" max="1" step="0.1" class="form-range" />
          </div>
          <div class="form-row">
            <label>{{ t('outputCompliance.config.retentionDays') }}</label>
            <input v-model.number="config.retention_days" type="number" min="1" max="365" class="form-input-small" />
          </div>
        </div>

        <div class="form-actions">
          <button class="btn-primary" :disabled="configSaving" @click="saveConfig">
            {{ configSaving ? t('outputCompliance.config.saving') : t('outputCompliance.config.save') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.output-compliance-view {
  padding: 1.5rem;
  max-width: 1600px;
  margin: 0 auto;
}

.view-header h2 {
  margin: 0 0 0.5rem;
  font-size: 1.75rem;
  font-weight: 600;
}

.view-subtitle {
  color: #666;
  margin: 0 0 1.5rem;
}

/* 标签页 */
.tab-bar {
  display: flex;
  gap: 0;
  margin-bottom: 1.5rem;
  border-bottom: 1px solid #e5e7eb;
}

.tab-btn {
  padding: 0.75rem 1.5rem;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  cursor: pointer;
  font-size: 0.9375rem;
  color: #666;
  transition: all 0.2s;
}

.tab-btn:hover { color: #3b82f6; }
.tab-btn.active {
  color: #3b82f6;
  border-bottom-color: #3b82f6;
  font-weight: 500;
}

/* 统计卡片 */
.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 1rem;
  margin-bottom: 1.5rem;
}

.stat-card {
  background: white;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 1rem;
}

.stat-label {
  font-size: 0.875rem;
  color: #666;
  margin-bottom: 0.5rem;
}

.stat-value {
  font-size: 1.75rem;
  font-weight: 600;
}

.stat-danger { color: #ef4444; }
.stat-warn { color: #f59e0b; }

/* 筛选栏 */
.filter-bar {
  display: flex;
  gap: 0.75rem;
  margin-bottom: 1.5rem;
  flex-wrap: wrap;
}

.filter-input,
.filter-select {
  padding: 0.5rem 0.75rem;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 0.875rem;
  min-width: 150px;
}

.filter-input:focus,
.filter-select:focus {
  outline: none;
  border-color: #3b82f6;
}

/* 表格 */
.table-container {
  background: white;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  overflow-x: auto;
  margin-bottom: 1rem;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.875rem;
}

.data-table th {
  background: #f9fafb;
  padding: 0.75rem 1rem;
  text-align: left;
  font-weight: 600;
  border-bottom: 1px solid #e5e7eb;
  white-space: nowrap;
}

.data-table td {
  padding: 0.75rem 1rem;
  border-bottom: 1px solid #f3f4f6;
}

.loading-cell,
.empty-cell {
  text-align: center;
  padding: 2rem;
  color: #999;
}

.loading-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
}

.session-id {
  font-family: monospace;
  font-size: 0.8rem;
  max-width: 150px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.content-preview {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.date-cell {
  white-space: nowrap;
  font-size: 0.8rem;
  color: #666;
}

/* 分页 */
.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 1rem;
}

.page-info {
  font-size: 0.875rem;
  color: #666;
}

/* 按钮 */
.btn-primary,
.btn-secondary {
  padding: 0.5rem 1rem;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.875rem;
  font-weight: 500;
  transition: all 0.2s;
}

.btn-primary {
  background: #3b82f6;
  color: white;
}

.btn-primary:hover { background: #2563eb; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

.btn-secondary {
  background: white;
  color: #374151;
  border: 1px solid #d1d5db;
}

.btn-secondary:hover { background: #f9fafb; }
.btn-secondary:disabled { opacity: 0.5; cursor: not-allowed; }

/* Badge */
.badge-blue {
  background: #dbeafe;
  color: #1e40af;
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
  font-size: 0.75rem;
}

.badge-yellow {
  background: #fef3c7;
  color: #92400e;
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
  font-size: 0.75rem;
}

.badge-green {
  background: #d1fae5;
  color: #065f46;
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
  font-size: 0.75rem;
}

.badge-gray {
  background: #f3f4f6;
  color: #374151;
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
  font-size: 0.75rem;
}

/* 加载和错误 */
.loading-state {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 2rem;
  color: #666;
}

.spinner {
  display: inline-block;
  width: 14px;
  height: 14px;
  border: 2px solid #e5e7eb;
  border-top-color: #3b82f6;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin { to { transform: rotate(360deg); } }

.error-banner {
  background: #fef2f2;
  border: 1px solid #fecaca;
  color: #dc2626;
  padding: 0.75rem 1rem;
  border-radius: 6px;
  margin-bottom: 1rem;
}

.success-banner {
  background: #d1fae5;
  border: 1px solid #6ee7b7;
  color: #065f46;
  padding: 0.75rem 1rem;
  border-radius: 6px;
  margin-bottom: 1rem;
}

/* 配置面板 */
.config-panel {
  background: white;
  border-radius: 8px;
  padding: 1.5rem;
}

.config-form {
  max-width: 800px;
}

.config-section {
  margin-bottom: 2rem;
  padding-bottom: 1.5rem;
  border-bottom: 1px solid #e5e7eb;
}

.config-section:last-of-type {
  border-bottom: none;
}

.config-section h3 {
  margin: 0 0 1rem;
  font-size: 1rem;
  font-weight: 600;
  color: #374151;
}

.form-row {
  display: flex;
  align-items: center;
  gap: 1rem;
  margin-bottom: 1rem;
  flex-wrap: wrap;
}

.form-row label {
  min-width: 180px;
  font-weight: 500;
  color: #374151;
}

.form-input {
  flex: 1;
  min-width: 200px;
  padding: 0.5rem 0.75rem;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 0.875rem;
}

.form-input:focus {
  outline: none;
  border-color: #3b82f6;
}

.form-input-small {
  width: 100px;
  padding: 0.5rem 0.75rem;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 0.875rem;
}

.form-select {
  padding: 0.5rem 0.75rem;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 0.875rem;
  min-width: 150px;
}

.form-select:focus {
  outline: none;
  border-color: #3b82f6;
}

.form-checkbox {
  width: 18px;
  height: 18px;
  cursor: pointer;
}

.form-range {
  flex: 1;
  min-width: 200px;
  cursor: pointer;
}

.form-actions {
  margin-top: 1.5rem;
  display: flex;
  justify-content: flex-end;
}
</style>
