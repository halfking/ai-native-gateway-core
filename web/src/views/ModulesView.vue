<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  listModules,
  getModule,
  toggleModule,
  type ModuleDefinition,
  type ModuleWithStatus,
} from '../api/modules'
import { listSettings, getSetting, updateSetting, type SettingItem } from '../api'

const { t } = useI18n()
const router = useRouter()
const modules = ref<ModuleWithStatus[]>([])
const loading = ref(false)
const toggling = ref<string | null>(null)
const error = ref<string | null>(null)
const selectedKey = ref<string | null>(null)
const selectedModule = ref<ModuleDefinition | null>(null)
const selectedEnabled = ref(false)
const moduleSettings = ref<SettingItem[]>([])
const activeTab = ref<'overview' | 'config' | 'integration'>('overview')

const categoryOrder = ['compression', 'session', 'security', 'rate_limit', 'general', 'integration']
const categoryLabels: Record<string, string> = {
  compression: t('modulesView.category.compression'),
  session: t('modulesView.category.session'),
  security: t('modulesView.category.security'),
  rate_limit: t('modulesView.category.rate_limit'),
  general: t('modulesView.category.general'),
  integration: t('modulesView.category.integration'),
}

const groupedModules = computed(() => {
  const groups: { category: string; label: string; modules: ModuleWithStatus[] }[] = []
  const catMap = new Map<string, ModuleWithStatus[]>()
  for (const m of modules.value) {
    const list = catMap.get(m.category) || []
    list.push(m)
    catMap.set(m.category, list)
  }
  const seen = new Set<string>()
  for (const cat of categoryOrder) {
    if (catMap.has(cat)) {
      groups.push({ category: cat, label: categoryLabels[cat] || cat, modules: catMap.get(cat)! })
      seen.add(cat)
    }
  }
  for (const [cat, mods] of catMap) {
    if (!seen.has(cat)) {
      groups.push({ category: cat, label: categoryLabels[cat] || cat, modules: mods })
    }
  }
  return groups
})

const enabledCount = computed(() => modules.value.filter(m => m.enabled).length)
const totalCount = computed(() => modules.value.length)

async function loadModules() {
  loading.value = true
  error.value = null
  try {
    const r = await listModules()
    modules.value = r.items
    // If a module was selected, refresh its detail
    if (selectedKey.value) {
      await selectModule(selectedKey.value)
    }
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.loadFailed')
  } finally {
    loading.value = false
  }
}

async function selectModule(key: string) {
  selectedKey.value = key
  // Find the basic info from the list
  const found = modules.value.find(m => m.key === key)
  if (found) {
    selectedModule.value = found
    selectedEnabled.value = found.enabled
  }
  // Load config settings
  try {
    // Load from module detail API
    const detail = await getModule(key)
    selectedModule.value = detail.module
    selectedEnabled.value = detail.module.enabled
    // Load settings related to this module
    const allSettings = await listSettings()
    if (selectedModule.value) {
      const configKeys = selectedModule.value.config_keys || []
      const relatedKeys = [selectedModule.value.setting_key, ...configKeys].filter(Boolean)
      moduleSettings.value = allSettings.items.filter(s => relatedKeys.includes(s.key))
    }
  } catch (e: any) {
    // If the detail API fails, keep using list data
    moduleSettings.value = []
  }
  activeTab.value = 'overview'
}

async function doToggle(key: string) {
  toggling.value = key
  error.value = null
  try {
    const mod = modules.value.find(m => m.key === key)
    if (!mod) return
    const newState = !mod.enabled
    const r = await toggleModule(key, newState)
    mod.enabled = r.enabled
    if (selectedKey.value === key) {
      selectedEnabled.value = r.enabled
    }
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.operationFailed')
  } finally {
    toggling.value = null
  }
}

async function saveSetting(settingKey: string, value: any) {
  try {
    await updateSetting(settingKey, { value })
    await selectModule(selectedKey.value!)
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.saveFailed')
  }
}

function dangerLevelLabel(level: number): { label: string; cls: string } {
  switch (level) {
    case 0: return { label: t('modulesView.dangerLevel.safe'), cls: 'level-safe' }
    case 1: return { label: t('modulesView.dangerLevel.warn'), cls: 'level-warn' }
    case 2: return { label: t('modulesView.dangerLevel.danger'), cls: 'level-danger' }
    case 3: return { label: t('modulesView.dangerLevel.breaking'), cls: 'level-breaking' }
    default: return { label: t('modulesView.dangerLevel.unknown'), cls: '' }
  }
}

function goToSettings(key: string) {
  router.push('/admin/settings')
}

onMounted(() => {
  loadModules()
})
</script>

<template>
  <div class="modules-view">
    <!-- Header -->
    <div class="page-header">
      <div class="page-header-left">
        <h1 class="page-title">{{ t('modulesView.pageTitle') }}</h1>
        <p class="page-subtitle">{{ t('modulesView.pageSubtitle') }}</p>
      </div>
      <div class="page-header-right">
        <div class="summary-badge">
          <span class="summary-count">{{ enabledCount }}</span>
          <span class="summary-sep">/</span>
          <span class="summary-total">{{ totalCount }}</span>
          <span class="summary-label">{{ t('modulesView.modulesEnabled') }}</span>
        </div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div class="layout">
      <!-- Left: Module list grouped by category -->
      <section class="list-pane">
        <div v-if="loading && !modules.length" class="loading">{{ t('modulesView.loading') }}</div>
        <template v-else>
          <div
            v-for="group in groupedModules"
            :key="group.category"
            class="module-group"
          >
            <div class="group-header">
              <span class="group-label">{{ group.label }}</span>
              <span class="group-count">{{ group.modules.length }}</span>
            </div>
            <div
              v-for="mod in group.modules"
              :key="mod.key"
              class="module-card"
              :class="{
                active: selectedKey === mod.key,
                disabled: !mod.enabled,
              }"
              @click="selectModule(mod.key)"
            >
              <div class="card-icon">{{ mod.icon }}</div>
              <div class="card-body">
                <div class="card-title-row">
                  <span class="card-title">{{ mod.name }}</span>
                  <span
                    class="status-dot"
                    :class="mod.enabled ? 'dot-on' : 'dot-off'"
                    :title="mod.enabled ? t('modulesView.status.enabled') : t('modulesView.status.disabled')"
                  />
                </div>
                <div class="card-desc">{{ mod.description }}</div>
              </div>
              <label class="toggle-wrap" @click.stop>
                <input
                  type="checkbox"
                  class="toggle-input"
                  :checked="mod.enabled"
                  :disabled="toggling === mod.key"
                  @change="doToggle(mod.key)"
                />
                <span class="toggle-track">
                  <span class="toggle-knob" />
                </span>
              </label>
            </div>
          </div>
        </template>
      </section>

      <!-- Right: Module detail -->
      <aside class="detail-pane" v-if="selectedModule">
        <div class="detail-header">
          <span class="detail-icon">{{ selectedModule.icon }}</span>
          <div class="detail-title-area">
            <h2 class="detail-title">{{ selectedModule.name }}</h2>
            <span
              class="status-badge"
              :class="selectedEnabled ? 'badge-on' : 'badge-off'"
            >
              {{ selectedEnabled ? t('modulesView.status.enabled') : t('modulesView.status.disabled') }}
            </span>
          </div>
        </div>

        <!-- Tabs -->
        <div class="tab-bar">
          <button
            class="tab-btn"
            :class="{ active: activeTab === 'overview' }"
            @click="activeTab = 'overview'"
          >{{ t('modulesView.tabs.overview') }}</button>
          <button
            class="tab-btn"
            :class="{ active: activeTab === 'config' }"
            @click="activeTab = 'config'"
          >{{ t('modulesView.tabs.config') }}</button>
          <button
            v-if="selectedModule.integration"
            class="tab-btn"
            :class="{ active: activeTab === 'integration' }"
            @click="activeTab = 'integration'"
          >{{ t('modulesView.tabs.integration') }}</button>
        </div>

        <!-- Overview tab -->
        <div v-if="activeTab === 'overview'" class="tab-content">
          <div class="info-section">
            <h3 class="section-title">{{ t('modulesView.overview.sectionDescription') }}</h3>
            <p class="section-text">{{ selectedModule.description }}</p>
          </div>

          <div class="info-section">
            <h3 class="section-title">{{ t('modulesView.overview.sectionCapabilities') }}</h3>
            <ul class="cap-list">
              <li v-for="(cap, i) in selectedModule.capabilities" :key="i" class="cap-item">
                <span class="cap-check">✓</span>
                <span>{{ cap }}</span>
              </li>
            </ul>
          </div>

          <div class="meta-grid">
            <div class="meta-item">
              <span class="meta-label">{{ t('modulesView.overview.labelKey') }}</span>
              <code class="meta-value">{{ selectedModule.key }}</code>
            </div>
            <div class="meta-item">
              <span class="meta-label">{{ t('modulesView.overview.labelDanger') }}</span>
              <span
                class="level-badge"
                :class="dangerLevelLabel(selectedModule.danger_level).cls"
              >{{ dangerLevelLabel(selectedModule.danger_level).label }}</span>
            </div>
            <div class="meta-item">
              <span class="meta-label">{{ t('modulesView.overview.labelConfigCount') }}</span>
              <span class="meta-value">{{ selectedModule.config_keys?.length || 0 }}</span>
            </div>
            <div class="meta-item">
              <span class="meta-label">{{ t('modulesView.overview.labelStatus') }}</span>
              <span class="meta-value" :class="selectedEnabled ? 'text-green' : 'text-muted'">
                {{ selectedEnabled ? t('modulesView.status.enabled') : t('modulesView.status.disabled') }}
              </span>
            </div>
          </div>

          <div class="info-section action-section">
            <button
              class="btn-action"
              :class="selectedEnabled ? 'btn-danger' : 'btn-primary'"
              :disabled="toggling === selectedModule.key"
              @click="doToggle(selectedModule.key)"
            >
              {{ toggling === selectedModule.key ? '处理中…' : selectedEnabled ? '禁用此模块' : '启用此模块' }}
            </button>
            <button class="btn-ghost" @click="goToSettings(selectedModule.key)">
              查看所有系统设置
            </button>
          </div>
        </div>

        <!-- Config tab -->
        <div v-if="activeTab === 'config'" class="tab-content">
          <div class="info-section" v-if="moduleSettings.length === 0">
            <p class="text-muted">此模块没有可配置的设置项。</p>
          </div>
          <div
            v-for="setting in moduleSettings"
            :key="setting.key"
            class="config-card"
          >
            <div class="config-header">
              <code class="config-key">{{ setting.key }}</code>
              <span class="src-badge" :class="'src-' + setting.source">
                {{ setting.source || '默认' }}
              </span>
            </div>
            <p class="config-desc">{{ setting.description }}</p>
            <div class="config-editor">
              <!-- Boolean -->
              <div v-if="setting.type === 'bool'" class="config-bool">
                <label class="switch-label-sm">
                  <input
                    type="checkbox"
                    class="toggle-input"
                    :checked="setting.value === true"
                    @change="saveSetting(setting.key, ($event.target as HTMLInputElement).checked)"
                  />
                  <span class="toggle-track-sm">
                    <span class="toggle-knob-sm" />
                  </span>
                  <span class="switch-text-sm">{{ setting.value === true ? '启用' : '禁用' }}</span>
                </label>
              </div>
              <!-- Number -->
              <div v-else-if="setting.type === 'int' || setting.type === 'float'" class="config-number">
                <input
                  type="number"
                  class="number-input"
                  :value="setting.value ?? setting.default"
                  :step="setting.type === 'float' ? '0.01' : '1'"
                  @change="saveSetting(setting.key, parseFloat(($event.target as HTMLInputElement).value))"
                />
              </div>
              <!-- String -->
              <div v-else-if="setting.type === 'string' || setting.type === 'url'" class="config-string">
                <input
                  type="text"
                  class="text-input"
                  :value="setting.value ?? setting.default"
                  @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
                  :placeholder="'输入' + setting.description"
                />
              </div>
              <!-- Select (enum) -->
              <div v-else-if="setting.type === 'enum' && setting.options" class="config-select">
                <select
                  class="select-input"
                  :value="setting.value ?? setting.default"
                  @change="saveSetting(setting.key, ($event.target as HTMLSelectElement).value)"
                >
                  <option
                    v-for="opt in setting.options"
                    :key="opt"
                    :value="opt"
                  >{{ opt }}</option>
                </select>
              </div>
            </div>
          </div>
        </div>

        <!-- Integration tab -->
        <div v-if="activeTab === 'integration'" class="tab-content">
          <div class="integration-card" v-if="selectedModule.integration">
            <div class="integ-header">
              <span class="integ-icon">
                {{ selectedModule.key === 'feishu_bot' ? '📱' : '🔗' }}
              </span>
              <div class="integ-info">
                <h3 class="integ-title">{{ selectedModule.integration.label }} 集成</h3>
                <p class="integ-desc">{{ selectedModule.integration.description }}</p>
              </div>
            </div>
            <div class="integ-body">
              <div class="integ-docs" v-if="selectedModule.integration.doc_url">
                <span class="integ-docs-label">对接文档：</span>
                <a
                  :href="selectedModule.integration.doc_url"
                  target="_blank"
                  rel="noopener"
                  class="integ-link"
                >{{ selectedModule.integration.doc_url }}</a>
              </div>
              <div class="integ-steps">
                <h4 class="steps-title">配置步骤</h4>
                <ol class="steps-list">
                  <li>在飞书开放平台创建自定义机器人</li>
                  <li>复制 Webhook URL 并粘贴到下方配置中</li>
                  <li>（可选）配置签名验证令牌</li>
                  <li>开启"飞书机器人集成"开关</li>
                </ol>
              </div>
              <div class="integ-status">
                <span class="status-indicator" :class="selectedEnabled ? 'connected' : 'disconnected'" />
                <span>{{ selectedEnabled ? '集成已启用' : '集成未启用 — 请先开启此模块' }}</span>
              </div>
            </div>
          </div>
        </div>
      </aside>

      <!-- Empty state -->
      <aside v-else class="detail-pane detail-empty">
        <div class="empty-state">
          <span class="empty-icon">⚙️</span>
          <p>选择一个模块查看详情与配置</p>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.modules-view {
  padding: 0;
  max-width: 1400px;
  margin: 0 auto;
  color: var(--text-primary, #e6edf3);
  font-size: 13px;
}

/* ── Page Header ── */
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--border, #30363d);
}
.page-header-left {
  flex: 1;
}
.page-title {
  font-size: 20px;
  font-weight: 700;
  margin: 0 0 4px;
  color: var(--text-primary, #e6edf3);
}
.page-subtitle {
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
  margin: 0;
}
.page-header-right {
  flex-shrink: 0;
}
.summary-badge {
  display: flex;
  align-items: baseline;
  gap: 2px;
  padding: 8px 16px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  font-size: 13px;
}
.summary-count {
  font-size: 24px;
  font-weight: 700;
  color: #34d399;
}
.summary-sep {
  color: var(--text-secondary, #8b949e);
  margin: 0 2px;
}
.summary-total {
  font-size: 18px;
  color: var(--text-secondary, #8b949e);
}
.summary-label {
  margin-left: 8px;
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
}

.error-banner {
  padding: 10px 14px;
  background: rgba(248, 113, 113, 0.1);
  border: 1px solid rgba(248, 113, 113, 0.3);
  color: #f87171;
  border-radius: 6px;
  margin-bottom: 12px;
}

.layout {
  display: grid;
  grid-template-columns: 420px 1fr;
  gap: 16px;
  min-height: 70vh;
}

/* ── Module List ── */
.list-pane {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 10px;
  overflow-y: auto;
  max-height: calc(100vh - 200px);
  padding: 8px;
}
.loading {
  text-align: center;
  padding: 48px;
  color: var(--text-secondary, #8b949e);
}

.module-group {
  margin-bottom: 8px;
}
.group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px 4px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.03em;
  color: var(--text-secondary, #8b949e);
  text-transform: uppercase;
}
.group-count {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--bg, #0f1117);
  border-radius: 8px;
  color: var(--text-muted, #6e7681);
}

.module-card {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 10px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s;
  border: 1px solid transparent;
  margin-bottom: 2px;
}
.module-card:hover {
  background: var(--bg-hover, #21262d);
}
.module-card.active {
  background: rgba(99, 102, 241, 0.08);
  border-color: var(--accent, #6366f1);
}
.module-card.disabled {
  opacity: 0.65;
}
.card-icon {
  font-size: 22px;
  flex-shrink: 0;
  width: 36px;
  height: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg, #0f1117);
  border-radius: 8px;
}
.card-body {
  flex: 1;
  min-width: 0;
}
.card-title-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.card-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}
.card-desc {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── Status Dot ── */
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.dot-on {
  background: #34d399;
  box-shadow: 0 0 4px rgba(52, 211, 153, 0.4);
}
.dot-off {
  background: #6e7681;
}

/* ── Toggle Switch ── */
.toggle-wrap {
  flex-shrink: 0;
  cursor: pointer;
}
.toggle-input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}
.toggle-track {
  display: block;
  width: 36px;
  height: 20px;
  background: var(--border, #30363d);
  border-radius: 10px;
  position: relative;
  transition: background 0.2s;
}
.toggle-knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  background: #fff;
  border-radius: 50%;
  transition: transform 0.2s;
}
.toggle-input:checked + .toggle-track {
  background: var(--accent, #6366f1);
}
.toggle-input:checked + .toggle-track .toggle-knob {
  transform: translateX(16px);
}
.toggle-input:disabled + .toggle-track {
  opacity: 0.5;
  cursor: not-allowed;
}

/* ── Detail Pane ── */
.detail-pane {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 10px;
  padding: 20px;
  overflow-y: auto;
  max-height: calc(100vh - 200px);
}
.detail-empty {
  display: flex;
  align-items: center;
  justify-content: center;
}
.empty-state {
  text-align: center;
  color: var(--text-secondary, #8b949e);
}
.empty-icon {
  font-size: 40px;
  display: block;
  margin-bottom: 12px;
}

.detail-header {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 16px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--border, #30363d);
}
.detail-icon {
  font-size: 32px;
}
.detail-title-area {
  flex: 1;
}
.detail-title {
  margin: 0 0 4px;
  font-size: 18px;
  font-weight: 700;
}
.status-badge {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
}
.badge-on {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}
.badge-off {
  background: rgba(139, 148, 158, 0.15);
  color: #8b949e;
}

/* ── Tabs ── */
.tab-bar {
  display: flex;
  gap: 4px;
  margin-bottom: 16px;
  border-bottom: 1px solid var(--border, #30363d);
  padding-bottom: 0;
}
.tab-btn {
  padding: 8px 16px;
  background: transparent;
  border: none;
  color: var(--text-secondary, #8b949e);
  font-size: 13px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  transition: color 0.15s, border-color 0.15s;
}
.tab-btn:hover {
  color: var(--text-primary, #e6edf3);
}
.tab-btn.active {
  color: var(--accent-h, #818cf8);
  border-bottom-color: var(--accent, #6366f1);
}

/* ── Tab Content ── */
.tab-content {
  font-size: 13px;
}
.info-section {
  margin-bottom: 20px;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
  margin: 0 0 8px;
}
.section-text {
  margin: 0;
  line-height: 1.6;
  color: var(--text-secondary, #8b949e);
}

.cap-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.cap-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 5px 0;
  color: var(--text-secondary, #8b949e);
  font-size: 13px;
  line-height: 1.5;
}
.cap-check {
  color: #34d399;
  font-weight: 700;
  flex-shrink: 0;
}

/* ── Meta Grid ── */
.meta-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
  padding: 14px;
  background: var(--bg, #0f1117);
  border-radius: 8px;
  margin-bottom: 20px;
}
.meta-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.meta-label {
  font-size: 11px;
  color: var(--text-muted, #6e7681);
}
.meta-value {
  font-size: 13px;
  color: var(--text-primary, #e6edf3);
}
.meta-value code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  color: var(--accent-h, #818cf8);
}

/* ── Danger Level ── */
.level-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.level-safe {
  background: rgba(52, 211, 153, 0.12);
  color: #34d399;
}
.level-warn {
  background: rgba(251, 191, 36, 0.12);
  color: #fbbf24;
}
.level-danger {
  background: rgba(248, 113, 113, 0.12);
  color: #f87171;
}
.level-breaking {
  background: rgba(239, 68, 68, 0.15);
  color: #ef4444;
}

/* ── Action Buttons ── */
.action-section {
  display: flex;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--border, #30363d);
}
.btn-action {
  padding: 8px 20px;
  border: none;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: opacity 0.15s;
}
.btn-action:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-primary {
  background: var(--accent, #6366f1);
  color: #fff;
}
.btn-danger {
  background: rgba(239, 68, 68, 0.85);
  color: #fff;
}
.btn-ghost {
  padding: 8px 16px;
  background: transparent;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 13px;
  cursor: pointer;
}
.btn-ghost:hover {
  background: var(--bg-hover, #21262d);
}

/* ── Config Cards ── */
.config-card {
  padding: 12px;
  margin-bottom: 12px;
  background: var(--bg, #0f1117);
  border-radius: 8px;
  border: 1px solid var(--border, #30363d);
}
.config-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
}
.config-key {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  color: var(--accent-h, #818cf8);
}
.config-desc {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin: 0 0 10px;
}

.config-editor .number-input,
.config-editor .text-input,
.config-editor .select-input {
  width: 100%;
  padding: 6px 10px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 13px;
  font-family: inherit;
}
.config-editor .number-input:focus,
.config-editor .text-input:focus,
.config-editor .select-input:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}

/* ── Config Boolean (small toggle) ── */
.config-bool {
  padding: 4px 0;
}
.switch-label-sm {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.toggle-track-sm {
  display: block;
  width: 32px;
  height: 18px;
  background: var(--border, #30363d);
  border-radius: 9px;
  position: relative;
  transition: background 0.2s;
}
.toggle-knob-sm {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  background: #fff;
  border-radius: 50%;
  transition: transform 0.2s;
}
.switch-label-sm .toggle-input:checked + .toggle-track-sm {
  background: var(--accent, #6366f1);
}
.switch-label-sm .toggle-input:checked + .toggle-track-sm .toggle-knob-sm {
  transform: translateX(14px);
}
.switch-text-sm {
  font-size: 12px;
  color: var(--text-primary, #e6edf3);
}

/* ── Source Badges ── */
.src-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 500;
}
.src-badge.src-db {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}
.src-badge.src-env {
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
}
.src-badge.src-default {
  background: rgba(139, 148, 158, 0.15);
  color: #8b949e;
}

/* ── Integration Card ── */
.integration-card {
  background: var(--bg, #0f1117);
  border-radius: 8px;
  border: 1px solid var(--border, #30363d);
  overflow: hidden;
}
.integ-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px;
  background: rgba(99, 102, 241, 0.05);
  border-bottom: 1px solid var(--border, #30363d);
}
.integ-icon {
  font-size: 28px;
}
.integ-info {
  flex: 1;
}
.integ-title {
  margin: 0 0 2px;
  font-size: 15px;
  font-weight: 600;
}
.integ-desc {
  margin: 0;
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
}
.integ-body {
  padding: 16px;
}
.integ-docs {
  margin-bottom: 14px;
  font-size: 12px;
}
.integ-docs-label {
  color: var(--text-secondary, #8b949e);
}
.integ-link {
  color: var(--accent-h, #818cf8);
  text-decoration: none;
  word-break: break-all;
}
.integ-link:hover {
  text-decoration: underline;
}
.steps-title {
  font-size: 13px;
  font-weight: 600;
  margin: 0 0 8px;
}
.steps-list {
  margin: 0;
  padding-left: 20px;
  font-size: 13px;
  line-height: 1.8;
  color: var(--text-secondary, #8b949e);
}
.integ-status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 14px;
  padding: 10px 12px;
  background: var(--bg-card, #161b22);
  border-radius: 6px;
  font-size: 12px;
}
.status-indicator {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.status-indicator.connected {
  background: #34d399;
  box-shadow: 0 0 4px rgba(52, 211, 153, 0.4);
}
.status-indicator.disconnected {
  background: #6e7681;
}

.text-green { color: #34d399; }
.text-muted { color: #6e7681; }

/* ── Responsive ── */
@media (max-width: 960px) {
  .layout {
    grid-template-columns: 1fr;
  }
  .list-pane,
  .detail-pane {
    max-height: none;
  }
}
</style>
