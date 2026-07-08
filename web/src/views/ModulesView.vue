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

// 检查依赖模块的状态
const dependencyStatus = computed(() => {
  if (!selectedModule.value?.dependencies) return []
  return selectedModule.value.dependencies.map(dep => {
    const mod = modules.value.find(m => m.key === dep.key)
    return {
      ...dep,
      enabled: mod?.enabled ?? false,
      moduleName: mod?.name ?? dep.name
    }
  })
})

// 检查是否有未满足的必需依赖
const hasUnmetDependencies = computed(() => {
  return dependencyStatus.value.some(d => d.required && !d.enabled)
})

// 按类别分组的配置项
const securityConfigGroups = computed(() => {
  if (selectedKey.value !== 'security') return null
  const settings = moduleSettings.value
  return {
    mode: settings.filter(s => s.key.startsWith('security.mode')),
    llm: settings.filter(s => s.key.startsWith('security.llm.')),
    intent: settings.filter(s => s.key.startsWith('security.intent.')),
    threat: settings.filter(s => s.key.startsWith('security.threat.')),
    response: settings.filter(s => s.key.startsWith('security.response.')),
    audit: settings.filter(s => s.key.startsWith('security.audit.')),
  }
})

// 检查配置项是否因依赖未满足而应被禁用
function isCheckDisabled(key: string): boolean {
  if (key === 'security.threat.checks.prompt_inject') {
    return !modules.value.find(m => m.key === 'prompt_injection')?.enabled
  }
  if (key === 'security.threat.checks.data_leak' || key === 'security.threat.checks.pii') {
    return !modules.value.find(m => m.key === 'output_compliance')?.enabled
  }
  if (key === 'security.response.high_risk') {
    const val = moduleSettings.value.find(s => s.key === key)?.value
    if (val === 'approval') {
      return !modules.value.find(m => m.key === 'session_audit')?.enabled
    }
  }
  return false
}

// 获取依赖警告信息
function getDependencyWarning(key: string): string | null {
  if (key === 'security.threat.checks.prompt_inject' && isCheckDisabled(key)) {
    return '⚠️ 依赖模块 prompt_injection 未启用，此检测项无法正常工作'
  }
  if ((key === 'security.threat.checks.data_leak' || key === 'security.threat.checks.pii') && isCheckDisabled(key)) {
    return '⚠️ 依赖模块 output_compliance 未启用，此检测项无法正常工作'
  }
  if (key === 'security.response.high_risk' && isCheckDisabled(key)) {
    return '⚠️ 审批动作依赖 session_audit 模块，请先启用'
  }
  return null
}

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
  // Find the basic info from the list (used immediately so the detail pane
  // populates before the network round-trip completes).
  const found = modules.value.find(m => m.key === key)
  if (found) {
    selectedModule.value = found
    selectedEnabled.value = found.enabled
  }
  // Load config settings. The detail API is the single source of truth for
  // `enabled` — if it disagrees with the list (e.g. changed by another admin),
  // we reconcile both so the list and detail pane stay in sync.
  try {
    const detail = await getModule(key)
    selectedModule.value = detail.module
    selectedEnabled.value = detail.module.enabled
    // Reconcile the list entry so the left toggle matches the right pane.
    const listMod = modules.value.find(m => m.key === key)
    if (listMod && listMod.enabled !== detail.module.enabled) {
      listMod.enabled = detail.module.enabled
    }
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
  const mod = modules.value.find(m => m.key === key)
  if (!mod) return
  toggling.value = key
  error.value = null
  const prevEnabled = mod.enabled      // snapshot for rollback
  const prevSelected = selectedEnabled.value
  // Optimistic update so the UI feels instant.
  mod.enabled = !prevEnabled
  if (selectedKey.value === key) {
    selectedEnabled.value = !prevSelected
  }
  try {
    const r = await toggleModule(key, !prevEnabled)
    // Authoritative state from the server.
    mod.enabled = r.enabled
    if (selectedKey.value === key) {
      selectedEnabled.value = r.enabled
    }
  } catch (e: any) {
    // Rollback on failure so the two panes never disagree.
    mod.enabled = prevEnabled
    if (selectedKey.value === key) {
      selectedEnabled.value = prevSelected
    }
    error.value = e.message || t('modulesView.error.operationFailed')
  } finally {
    toggling.value = null
  }
}

async function saveSetting(settingKey: string, value: any) {
  try {
    await updateSetting(settingKey, { value })
    await loadModules()  // 刷新模块列表（更新依赖状态）
    await selectModule(selectedKey.value!)  // 刷新当前详情
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

function isModuleEnabled(key: string): boolean {
  const mod = modules.value.find(m => m.key === key)
  return mod?.enabled ?? false
}

function moduleDisplayName(key: string): string {
  const mod = modules.value.find(m => m.key === key)
  return mod?.name || key
}

function integrationTitle(moduleKey: string): string {
  if (moduleKey === 'feishu_bot') return t('modulesView.integration.feishuBotIntegration')
  if (moduleKey === 'wechat_bot') return t('modulesView.integration.wechatBotIntegration')
  return selectedModule.value?.integration?.label || moduleKey
}

function integrationSteps(moduleKey: string): string[] {
  if (moduleKey === 'feishu_bot') return t('modulesView.integration.feishuSteps') as unknown as string[]
  if (moduleKey === 'wechat_bot') return t('modulesView.integration.wechatSteps') as unknown as string[]
  return []
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
            <button class="btn-ghost" @click="goToSettings(selectedModule.key)">
              {{ t('modulesView.overview.viewAllSettings') }}
            </button>
          </div>
        </div>

        <!-- Config tab -->
        <div v-if="activeTab === 'config'" class="tab-content">
          <div class="info-section" v-if="moduleSettings.length === 0">
            <p class="text-muted">{{ t('modulesView.config.noSettings') }}</p>
          </div>

          <!-- 模块依赖状态（仅security模块显示） -->
          <div v-if="selectedKey === 'security' && dependencyStatus.length > 0" class="info-section dependency-section">
            <h3 class="section-title">模块依赖状态</h3>
            <div v-if="hasUnmetDependencies" class="warning-banner">
              ⚠️ 部分必需依赖模块未启用，安全检测引擎可能无法正常工作
            </div>
            <div class="dependency-list">
              <div
                v-for="dep in dependencyStatus"
                :key="dep.key"
                class="dependency-item"
                :class="{ 'dep-enabled': dep.enabled, 'dep-disabled': !dep.enabled, 'dep-required': dep.required }"
              >
                <span class="dep-status-icon">{{ dep.enabled ? '✅' : (dep.required ? '❌' : '⚠️') }}</span>
                <div class="dep-info">
                  <span class="dep-name">{{ dep.moduleName }}</span>
                  <span class="dep-desc">{{ dep.description }}</span>
                </div>
                <span class="dep-badge" :class="dep.required ? 'badge-required' : 'badge-optional'">
                  {{ dep.required ? '必需' : '可选' }}
                </span>
              </div>
            </div>
          </div>

          <!-- security模块的分组配置表单 -->
          <template v-if="selectedKey === 'security' && securityConfigGroups">
            <!-- 基础配置 -->
            <div v-if="securityConfigGroups.mode.length > 0" class="config-group">
              <h3 class="config-group-title">基础配置</h3>
              <div
                v-for="setting in securityConfigGroups.mode"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + setting.source">
                    {{ setting.source || t('modulesView.config.sourceDefault') }}
                  </span>
                </div>
                <p class="config-desc">{{ setting.description }}</p>
                <div class="config-editor">
                  <div v-if="setting.type === 'string' && setting.options" class="config-select">
                    <select
                      class="select-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLSelectElement).value)"
                    >
                      <option
                        v-for="opt in setting.options"
                        :key="opt"
                        :value="opt"
                      >{{ opt === 'observe' ? '观察模式 (observe)' : opt === 'enforce' ? '强制模式 (enforce)' : opt }}</option>
                    </select>
                  </div>
                  <div v-else-if="setting.type === 'bool'" class="config-bool">
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
                      <span class="switch-text-sm">{{ setting.value === true ? t('modulesView.config.switchOn') : t('modulesView.config.switchOff') }}</span>
                    </label>
                  </div>
                </div>
              </div>
            </div>

            <!-- LLM模型配置 -->
            <div v-if="securityConfigGroups.llm.length > 0" class="config-group">
              <h3 class="config-group-title">LLM模型配置</h3>
              <div
                v-for="setting in securityConfigGroups.llm"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + setting.source">
                    {{ setting.source || t('modulesView.config.sourceDefault') }}
                  </span>
                </div>
                <p class="config-desc">{{ setting.description }}</p>
                <div class="config-editor">
                  <div class="config-string">
                    <input
                      type="text"
                      class="text-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
                      :placeholder="t('modulesView.config.inputPlaceholder', { description: setting.description })"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- 意图分析配置 -->
            <div v-if="securityConfigGroups.intent.length > 0" class="config-group">
              <h3 class="config-group-title">意图分析 (Intent Classification)</h3>
              <div
                v-for="setting in securityConfigGroups.intent"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + setting.source">
                    {{ setting.source || t('modulesView.config.sourceDefault') }}
                  </span>
                </div>
                <p class="config-desc">{{ setting.description }}</p>
                <div class="config-editor">
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
                      <span class="switch-text-sm">{{ setting.value === true ? t('modulesView.config.switchOn') : t('modulesView.config.switchOff') }}</span>
                    </label>
                  </div>
                  <div v-else-if="setting.type === 'float'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="0.1"
                      min="0"
                      max="1"
                      @change="saveSetting(setting.key, parseFloat(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- 威胁检测配置 -->
            <div v-if="securityConfigGroups.threat.length > 0" class="config-group">
              <h3 class="config-group-title">威胁检测 (Threat Detection)</h3>
              <div
                v-for="setting in securityConfigGroups.threat"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + setting.source">
                    {{ setting.source || t('modulesView.config.sourceDefault') }}
                  </span>
                  <span v-if="setting.key.includes('prompt_inject')" class="module-ref-badge">← prompt_injection</span>
                  <span v-else-if="setting.key.includes('data_leak') || setting.key.includes('pii')" class="module-ref-badge">← output_compliance</span>
                </div>
                <p class="config-desc">{{ setting.description }}</p>
                <div class="config-editor">
                  <div v-if="setting.type === 'bool'" class="config-bool">
                    <label class="switch-label-sm" :class="{ 'disabled': isCheckDisabled(setting.key) }">
                      <input
                        type="checkbox"
                        class="toggle-input"
                        :checked="setting.value === true"
                        :disabled="isCheckDisabled(setting.key)"
                        @change="saveSetting(setting.key, ($event.target as HTMLInputElement).checked)"
                      />
                      <span class="toggle-track-sm">
                        <span class="toggle-knob-sm" />
                      </span>
                      <span class="switch-text-sm">{{ setting.value === true ? t('modulesView.config.switchOn') : t('modulesView.config.switchOff') }}</span>
                    </label>
                    <div v-if="getDependencyWarning(setting.key)" class="dependency-warning">
                      {{ getDependencyWarning(setting.key) }}
                    </div>
                  </div>
                  <div v-else-if="setting.type === 'int'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="1"
                      min="0"
                      max="10"
                      @change="saveSetting(setting.key, parseInt(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- 响应策略配置 -->
            <div v-if="securityConfigGroups.response.length > 0" class="config-group">
              <h3 class="config-group-title">预设响应策略</h3>
              <div
                v-for="setting in securityConfigGroups.response"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + setting.source">
                    {{ setting.source || t('modulesView.config.sourceDefault') }}
                  </span>
                  <span v-if="setting.key.includes('high_risk')" class="module-ref-badge">← session_audit</span>
                </div>
                <p class="config-desc">{{ setting.description }}</p>
                <div class="config-editor">
                  <div v-if="setting.options" class="config-select">
                    <select
                      class="select-input"
                      :value="setting.value ?? setting.default"
                      :disabled="isCheckDisabled(setting.key)"
                      @change="saveSetting(setting.key, ($event.target as HTMLSelectElement).value)"
                    >
                      <option
                        v-for="opt in setting.options"
                        :key="opt"
                        :value="opt"
                      >{{ opt === 'log' ? '仅记录 (log)' : opt === 'warn' ? '警告 (warn)' : opt === 'sanitize' ? '清洗 (sanitize)' : opt === 'block' ? '阻断 (block)' : opt === 'approval' ? '人工审批 (approval)' : opt }}</option>
                    </select>
                    <div v-if="getDependencyWarning(setting.key)" class="dependency-warning">
                      {{ getDependencyWarning(setting.key) }}
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <!-- 审计联动配置 -->
            <div v-if="securityConfigGroups.audit.length > 0" class="config-group">
              <h3 class="config-group-title">审计联动</h3>
              <div
                v-for="setting in securityConfigGroups.audit"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + setting.source">
                    {{ setting.source || t('modulesView.config.sourceDefault') }}
                  </span>
                  <span v-if="setting.key.includes('audit.enabled')" class="module-ref-badge">← session_audit</span>
                  <span v-else-if="setting.key.includes('audit.log_all')" class="module-ref-badge">← audit</span>
                </div>
                <p class="config-desc">{{ setting.description }}</p>
                <div class="config-editor">
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
                      <span class="switch-text-sm">{{ setting.value === true ? t('modulesView.config.switchOn') : t('modulesView.config.switchOff') }}</span>
                    </label>
                  </div>
                  <div v-else-if="setting.type === 'float'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="0.1"
                      min="0"
                      max="1"
                      @change="saveSetting(setting.key, parseFloat(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                </div>
              </div>
            </div>
          </template>

          <!-- 其他模块的通用配置表单 -->
          <template v-else>
            <div
              v-for="setting in moduleSettings"
              :key="setting.key"
              class="config-card"
            >
              <div class="config-header">
                <code class="config-key">{{ setting.key }}</code>
                <span class="src-badge" :class="'src-' + setting.source">
                  {{ setting.source || t('modulesView.config.sourceDefault') }}
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
                    <span class="switch-text-sm">{{ setting.value === true ? t('modulesView.config.switchOn') : t('modulesView.config.switchOff') }}</span>
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
                    :placeholder="t('modulesView.config.inputPlaceholder', { description: setting.description })"
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
          </template>
        </div>

        <!-- Integration tab -->
        <div v-if="activeTab === 'integration'" class="tab-content">
          <div class="integration-card" v-if="selectedModule.integration">
            <div class="integ-header">
              <span class="integ-icon">
                {{ selectedModule.key === 'feishu_bot' ? '📱' : selectedModule.key === 'wechat_bot' ? '💬' : '🔗' }}
              </span>
              <div class="integ-info">
                <h3 class="integ-title">
                  {{ integrationTitle(selectedModule.key) }}
                  {{ t('modulesView.tabs.integration') }}
                </h3>
                <p class="integ-desc">{{ selectedModule.integration.description }}</p>
              </div>
            </div>
            <div class="integ-body">
              <div class="integ-docs" v-if="selectedModule.integration.doc_url">
                <span class="integ-docs-label">{{ t('modulesView.integration.docsLabel') }}</span>
                <a
                  :href="selectedModule.integration.doc_url"
                  target="_blank"
                  rel="noopener"
                  class="integ-link"
                >{{ selectedModule.integration.doc_url }}</a>
              </div>

              <!-- Prerequisite modules -->
              <div class="integ-prereq" v-if="selectedModule.requires?.length">
                <h4 class="steps-title">{{ t('modulesView.integration.prerequisitesTitle') }}</h4>
                <div class="prereq-list">
                  <span
                    v-for="req in selectedModule.requires"
                    :key="req"
                    class="prereq-badge"
                    :class="isModuleEnabled(req) ? 'prereq-met' : 'prereq-unmet'"
                  >
                    {{ moduleDisplayName(req) }}
                  </span>
                </div>
                <p class="prereq-hint" v-if="selectedModule.requires.some(r => !isModuleEnabled(r))">
                  {{ t('modulesView.integration.prerequisitesHint') }}
                </p>
              </div>

              <div class="integ-steps">
                <h4 class="steps-title">{{ t('modulesView.integration.stepsTitle') }}</h4>
                <ol class="steps-list">
                  <li v-for="(step, i) in integrationSteps(selectedModule.key)" :key="i">{{ step }}</li>
                </ol>
              </div>
              <div class="integ-status">
                <span class="status-indicator" :class="selectedEnabled ? 'connected' : 'disconnected'" />
                <span>{{ selectedEnabled ? t('modulesView.integration.enabledStatus') : t('modulesView.integration.disabledHint') }}</span>
              </div>
            </div>
          </div>
        </div>
      </aside>

      <!-- Empty state -->
      <aside v-else class="detail-pane detail-empty">
        <div class="empty-state">
          <span class="empty-icon">⚙️</span>
          <p>{{ t('modulesView.empty.selectModule') }}</p>
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
.integ-prereq {
  margin-top: 12px;
}
.prereq-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.prereq-badge {
  display: inline-flex;
  align-items: center;
  padding: 3px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
}
.prereq-badge.prereq-met {
  background: rgba(52, 211, 153, 0.12);
  color: #34d399;
  border: 1px solid rgba(52, 211, 153, 0.3);
}
.prereq-badge.prereq-unmet {
  background: rgba(248, 113, 113, 0.12);
  color: #f87171;
  border: 1px solid rgba(248, 113, 113, 0.3);
}
.prereq-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: #f87171;
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

/* ── Dependency Section ── */
.dependency-section {
  padding: 16px;
  background: var(--bg-card, #161b22);
  border-radius: 8px;
  border: 1px solid var(--border, #30363d);
  margin-bottom: 20px;
}
.warning-banner {
  padding: 10px 14px;
  background: rgba(251, 191, 36, 0.1);
  border: 1px solid rgba(251, 191, 36, 0.3);
  color: #fbbf24;
  border-radius: 6px;
  margin-bottom: 12px;
  font-size: 12px;
}
.dependency-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.dependency-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: var(--bg, #0f1117);
  border-radius: 6px;
  border: 1px solid var(--border, #30363d);
}
.dependency-item.dep-enabled {
  border-color: rgba(52, 211, 153, 0.3);
}
.dependency-item.dep-disabled.dep-required {
  border-color: rgba(248, 113, 113, 0.3);
}
.dep-status-icon {
  font-size: 16px;
  flex-shrink: 0;
}
.dep-info {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.dep-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary, #e6edf3);
}
.dep-desc {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
}
.dep-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 500;
}
.badge-required {
  background: rgba(248, 113, 113, 0.15);
  color: #f87171;
}
.badge-optional {
  background: rgba(107, 114, 128, 0.15);
  color: #9ca3af;
}

/* ── Config Group ── */
.config-group {
  margin-bottom: 24px;
}
.config-group-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
  margin: 0 0 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border, #30363d);
}

/* ── Module Reference Badge ── */
.module-ref-badge {
  display: inline-block;
  padding: 2px 8px;
  background: rgba(99, 102, 241, 0.12);
  color: #818cf8;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 500;
  margin-left: 8px;
}

/* ── Dependency Warning ── */
.dependency-warning {
  margin-top: 8px;
  padding: 8px 10px;
  background: rgba(251, 191, 36, 0.1);
  border: 1px solid rgba(251, 191, 36, 0.3);
  border-radius: 4px;
  color: #fbbf24;
  font-size: 11px;
  line-height: 1.4;
}

.switch-label-sm.disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.switch-label-sm.disabled .toggle-input:disabled {
  cursor: not-allowed;
}

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
