<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  listModules,
  getModule,
  toggleModule,
  testModule,
  getModuleConfig,
  type ModuleDefinition,
  type ModuleWithStatus,
} from '../api/modules'
import { listSettings, type SettingItem } from '../api'
import { useRouter } from 'vue-router'

const { t } = useI18n()
const router = useRouter()
const modules = ref<ModuleWithStatus[]>([])
const loading = ref(false)
const toggling = ref<string | null>(null)
const cascading = ref<string | null>(null)
const error = ref<string | null>(null)
const cascadeLog = ref<string[]>([])
const selectedKey = ref<string | null>(null)
const selectedModule = ref<ModuleWithStatus | null>(null)
const moduleSettings = ref<SettingItem[]>([])
const moduleConfigSummary = ref<Record<string, any> | null>(null)
const activeTab = ref<'overview' | 'config' | 'integration' | 'status'>('overview')

// 测试连接状态
const testing = ref(false)
const testResult = ref<{ ok: boolean; message: string; detail?: string } | null>(null)

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

// 当前选中模块的依赖区是否展示
const dependencySection = computed(() => {
  const m = selectedModule.value
  if (!m) return false
  return Array.isArray(m.dependencies) && m.dependencies.length > 0
})

// 未启用的必需依赖集合，用于显示“一键启用”按钮 + 计数
const missingRequiredDeps = computed(() => {
  const m = selectedModule.value
  if (!m || !Array.isArray(m.dependencies)) return []
  return m.dependencies.filter(d => d.required && !d.enabled)
})

// 配置项分组（按 settings key 前缀）
const groupedSettings = computed(() => {
  const groups: Record<string, SettingItem[]> = {}
  for (const s of moduleSettings.value) {
    const section = classifySetting(s.key)
    if (!groups[section]) groups[section] = []
    groups[section].push(s)
  }
  return groups
})

function classifySetting(key: string): string {
  // handoff.* → 按子组归到 master/trigger/summary/safety
  if (key.startsWith('handoff.')) {
    if (['handoff.enabled', 'handoff.trigger_mode', 'handoff.client_mode', 'handoff.skill_name'].includes(key)) return 'master'
    if (['handoff.absolute_threshold', 'handoff.percentage_threshold',
         'handoff.message_threshold', 'handoff.idle_minutes',
         'handoff.min_messages'].includes(key)) return 'trigger'
    if (['handoff.summary_engine', 'handoff.summary_model',
         'handoff.summary_keep_recent_n', 'handoff.summary_max_tokens',
         'handoff.summary_prompt_tpl', 'handoff.summary_extract_facts',
         'handoff.continue_hint_tpl'].includes(key)) return 'summary'
    if (['handoff.cooldown_seconds', 'handoff.max_per_session',
         'handoff.retry_on_failure', 'handoff.notify_level',
         'handoff.notify_webhook'].includes(key)) return 'safety'
  }
  // feishu_bot.alert.* → alerts
  if (key.startsWith('feishu_bot.alert')) return 'alerts'
  if (key.startsWith('feishu_bot.approval')) return 'approvals'
  if (key.startsWith('feishu_bot.commands')) return 'commands'
  if (key.startsWith('feishu_bot.signature') || key.startsWith('feishu_bot.timestamp')) return 'security'
  // webhook_url / verify_token / encrypt_key / connection_mode → connection
  if (key.includes('webhook') || key.includes('token') || key.includes('encrypt') || key.includes('connection_mode')) return 'connection'
  return 'general'
}

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

// Handoff 模块按功能分组的配置项
const handoffConfigGroups = computed(() => {
  if (selectedKey.value !== 'handoff') return null
  const settings = moduleSettings.value
  return {
    master: settings.filter(s => classifySetting(s.key) === 'master'),
    trigger: settings.filter(s => classifySetting(s.key) === 'trigger'),
    summary: settings.filter(s => classifySetting(s.key) === 'summary'),
    safety: settings.filter(s => classifySetting(s.key) === 'safety'),
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

const sectionOrder = ['connection', 'alerts', 'approvals', 'commands', 'security', 'general']

// handoff 摘要引擎依赖提示：没有可用压缩模块时提醒管理员配置系统摘要凭据。
function getHandoffDependencyWarning(key: string): string | null {
  if ((key === 'handoff.summary_engine' || key === 'handoff.summary_model') && !modules.value.find(m => m.key === 'compression')?.enabled) {
    return '摘要会安全回退到系统配置的摘要模型；启用会话压缩可复用会话上下文与候选回退。'
  }
  if (key === 'handoff.client_mode') {
    return 'transparent 会在网关内创建新会话并继续当前请求；explicit 返回 resume_packet 供客户端续跑。'
  }
  return null
}

async function loadModules() {
  loading.value = true
  error.value = null
  try {
    const r = await listModules()
    modules.value = r.items
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
  const found = modules.value.find(m => m.key === key)
  if (found) {
    selectedModule.value = found
  }
  try {
    const detail = await getModule(key)
    selectedModule.value = detail.module
    const allSettings = await listSettings()
    if (selectedModule.value) {
      const configKeys = selectedModule.value.config_keys || []
      const relatedKeys = [selectedModule.value.setting_key, ...configKeys].filter(Boolean)
      moduleSettings.value = allSettings.items.filter(s => relatedKeys.includes(s.key))
    }
    // 加载配置摘要（轻量聚合字段，仅部分模块实现）
    try {
      moduleConfigSummary.value = await getModuleConfig(key)
    } catch {
      moduleConfigSummary.value = null
    }
  } catch (e: any) {
    moduleSettings.value = []
    moduleConfigSummary.value = null
  }
  activeTab.value = 'overview'
  testResult.value = null
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
    if (selectedKey.value === key && selectedModule.value) {
      selectedModule.value.enabled = r.enabled
    }
    if (r.cascaded && r.cascaded.length > 0) {
      ElMessage.success(t('modulesView.overview.cascadeSuccess', {
        count: r.cascaded.length,
        names: r.cascaded.join('、'),
      }))
    }
    await loadModules()
    if (selectedKey.value === key) await selectModule(key)
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.operationFailed')
  } finally {
    toggling.value = null
  }
}

// 显式“一键启用所有必需依赖”：通过 cascade=true 让后端级联开启必需依赖，
// 然后再调用 toggle 真正启用主模块。
async function doEnableAllDependencies(key: string) {
  cascading.value = key
  error.value = null
  cascadeLog.value = []
  try {
    const mod = modules.value.find(m => m.key === key)
    if (!mod) return
    if (mod.enabled) {
      ElMessage.info(t('modulesView.overview.cascadeAlreadyEnabled'))
      return
    }
    const r = await toggleModule(key, true, { cascade: true })
    mod.enabled = r.enabled
    if (selectedKey.value === key && selectedModule.value) {
      selectedModule.value.enabled = r.enabled
    }
    cascadeLog.value = r.cascaded || []
    if (cascadeLog.value.length > 0) {
      ElMessage.success(t('modulesView.overview.cascadeSuccess', {
        count: cascadeLog.value.length,
        names: cascadeLog.value.join('、'),
      }))
    } else {
      ElMessage.success(t('modulesView.overview.cascadeEmpty'))
    }
    await loadModules()
    if (selectedKey.value === key) await selectModule(key)
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.operationFailed')
    // 级联失败时回滚本地乐观状态（如果之前有更新）
    await loadModules()
  } finally {
    cascading.value = null
  }
}

async function doTestConnection(key: string) {
  testing.value = true
  testResult.value = null
  try {
    const r = await testModule(key)
    if (r.reachable && !r.error) {
      testResult.value = {
        ok: true,
        message: t('modulesView.overview.testSuccess'),
        detail: r.response_ms ? `${r.response_ms} ms` : undefined,
      }
    } else {
      testResult.value = {
        ok: false,
        message: t('modulesView.overview.testFailed'),
        detail: r.error || r.lark_msg || 'unknown',
      }
    }
  } catch (e: any) {
    testResult.value = {
      ok: false,
      message: t('modulesView.overview.testFailed'),
      detail: e.message || t('modulesView.error.testFailed'),
    }
  } finally {
    testing.value = false
  }
}

async function saveSetting(settingKey: string, value: any) {
  try {
    const { updateSetting } = await import('../api')
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

function jumpToModule(key: string) {
  selectModule(key)
}

function goToAllSettings() {
  router.push('/admin/settings')
}

function missingModuleName(key: string): string {
  const m = modules.value.find(x => x.key === key)
  return m?.name || key
}

function isFeishuSelected(): boolean {
  return selectedModule.value?.key === 'feishu_bot'
}

function moduleDisplayName(key: string): string {
  const mod = modules.value.find(m => m.key === key)
  return mod?.name || key
}

function integrationTitle(moduleKey: string): string {
  if (moduleKey === 'feishu_bot') return t('modulesView.integration.feishuBotIntegration')
  if (moduleKey === 'wechat_bot') return t('modulesView.integration.wechatBotIntegration')
  if (moduleKey === 'dingtalk_bot') return t('modulesView.integration.dingtalkBotIntegration')
  return selectedModule.value?.integration?.label || moduleKey
}

function integrationSteps(moduleKey: string): string[] {
  if (moduleKey === 'feishu_bot') return t('modulesView.integration.feishuSteps') as unknown as string[]
  if (moduleKey === 'wechat_bot') return t('modulesView.integration.wechatSteps') as unknown as string[]
  if (moduleKey === 'dingtalk_bot') return t('modulesView.integration.dingtalkSteps') as unknown as string[]
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
                'has-missing': mod.dependencies?.some(dep => dep.required && !dep.enabled),
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
                  <span
                    v-if="mod.dependencies?.some(dep => dep.required && !dep.enabled)"
                    class="missing-badge"
                    :title="mod.blocked_reason || t('modulesView.overview.dependencyDisabled')"
                  >!</span>
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
              :class="selectedModule.enabled ? 'badge-on' : 'badge-off'"
            >
              {{ selectedModule.enabled ? t('modulesView.status.enabled') : t('modulesView.status.disabled') }}
            </span>
          </div>
        </div>

        <!-- Legacy dependency banner removed: backend now supplies dependencies + blocked_reason. -->

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
          <button
            v-if="moduleConfigSummary && isFeishuSelected()"
            class="tab-btn"
            :class="{ active: activeTab === 'status' }"
            @click="activeTab = 'status'"
          >{{ t('modulesView.tabs.status') }}</button>
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

          <div class="info-section" v-if="selectedModule.dependencies && selectedModule.dependencies.length > 0">
            <h3 class="section-title">{{ t('modulesView.overview.sectionDependencies') }}</h3>
            <div class="dependency-list">
              <div
                v-for="dep in selectedModule.dependencies"
                :key="dep.key"
                class="dep-item"
                :class="{ 'dep-disabled': !dep.enabled }"
              >
                <span class="dep-icon">{{ dep.icon }}</span>
                <div class="dep-info">
                  <span class="dep-name">{{ dep.name }}</span>
                  <span class="dep-key">{{ dep.key }}</span>
                </div>
                <span v-if="dep.required" class="dep-badge required">必需</span>
                <span v-else class="dep-badge optional">推荐</span>
                <span v-if="!dep.enabled" class="dep-status warning" :title="t('modulesView.overview.dependencyDisabled')">
                  ⚠️ {{ t('modulesView.overview.notEnabled') }}
                </span>
                <span v-else class="dep-status ok">✓</span>
              </div>
            </div>
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
              <span class="meta-value" :class="selectedModule.enabled ? 'text-green' : 'text-muted'">
                {{ selectedModule.enabled ? t('modulesView.status.enabled') : t('modulesView.status.disabled') }}
              </span>
            </div>
          </div>

          <!-- 依赖区块：必需依赖未满足时，给出警告 + 一键启用按钮 -->
          <div v-if="dependencySection" class="info-section dependency-section">
            <h3 class="section-title">
              <span class="dep-title-icon">🔗</span>
              {{ t('modulesView.overview.dependenciesTitle') }}
            </h3>
            <p v-if="selectedModule.blocked_reason" class="warning-banner">
              {{ selectedModule.blocked_reason }}
            </p>
            <div class="dependency-list">
              <div
                v-for="dep in selectedModule.dependencies || []"
                :key="dep.key"
                class="dependency-item"
                :class="{
                  'dep-enabled': dep.enabled,
                  'dep-disabled': !dep.enabled,
                  'dep-required': dep.required,
                }"
              >
                <span class="dep-status-icon">{{ dep.enabled ? '✅' : (dep.required ? '❌' : '⚠️') }}</span>
                <div class="dep-info">
                  <span class="dep-name">{{ dep.name }}</span>
                  <span class="dep-desc">{{ dep.description }}</span>
                </div>
                <span class="dep-badge" :class="dep.required ? 'badge-required' : 'badge-optional'">
                  {{ dep.required ? t('modulesView.overview.required') : t('modulesView.overview.optional') }}
                </span>
              </div>
            </div>
            <div v-if="missingRequiredDeps.length > 0" class="dependency-actions">
              <el-button
                type="primary"
                :loading="cascading === selectedModule.key"
                :disabled="selectedModule.enabled"
                @click="doEnableAllDependencies(selectedModule.key)"
              >
                {{ cascading === selectedModule.key
                   ? t('modulesView.overview.cascading')
                   : t('modulesView.overview.enableAllDeps', { count: missingRequiredDeps.length }) }}
              </el-button>
              <span class="dependency-actions-hint">
                {{ t('modulesView.overview.enableAllDepsHint') }}
              </span>
            </div>
            <div v-if="cascadeLog.length > 0" class="cascade-log">
              <strong>{{ t('modulesView.overview.cascadeLogTitle') }}</strong>
              <ul>
                <li v-for="k in cascadeLog" :key="k">{{ k }}</li>
              </ul>
            </div>
          </div>

          <div class="info-section action-section">
            <button
              class="btn-action"
              :class="selectedModule.enabled ? 'btn-danger' : 'btn-primary'"
              :disabled="toggling === selectedModule.key"
              @click="doToggle(selectedModule.key)"
            >
              {{ toggling === selectedModule.key ? t('modulesView.status.processing') : selectedModule.enabled ? t('modulesView.status.enabledAction') : t('modulesView.status.disabledAction') }}
            </button>
            <!-- 仅对支持 test endpoint 的模块显示测试按钮 -->
            <button
              v-if="selectedModule.key === 'feishu_bot'"
              class="btn-ghost"
              :disabled="testing || !selectedModule.enabled"
              @click="doTestConnection(selectedModule.key)"
            >
              {{ testing ? t('modulesView.overview.testInProgress') : t('modulesView.overview.testConnection') }}
            </button>
            <button class="btn-ghost" @click="goToAllSettings">
              {{ t('modulesView.overview.viewAllSettings') }}
            </button>
          </div>

          <!-- 测试结果 -->
          <div v-if="testResult" class="test-result" :class="testResult.ok ? 'test-ok' : 'test-fail'">
            <strong>{{ testResult.ok ? '✅' : '❌' }} {{ testResult.message }}</strong>
            <span v-if="testResult.detail" class="test-detail">{{ testResult.detail }}</span>
          </div>
        </div>

        <!-- Config tab -->
        <div v-if="activeTab === 'config'" class="tab-content">
          <div v-if="isFeishuSelected()" class="config-hint">
            {{ t('modulesView.feishu.connectionHint') }}
          </div>

          <template v-if="moduleSettings.length === 0">
            <div class="info-section">
              <p class="text-muted">{{ t('modulesView.config.noSettings') }}</p>
            </div>
          </template>

          <!-- 分组渲染（仅 feishu_bot 分组；其他模块沿用扁平列表） -->
          <template v-else-if="isFeishuSelected()">
            <div
              v-for="section in sectionOrder"
              :key="section"
              v-show="groupedSettings[section] && groupedSettings[section].length > 0"
              class="config-section"
            >
              <h3 class="config-section-title">
                {{ t(`modulesView.config.sections.${section}`) }}
              </h3>
              <div
                v-for="setting in groupedSettings[section]"
                :key="setting.key"
                class="config-card"
              >
                <div class="config-header">
                  <code class="config-key">{{ setting.key }}</code>
                  <span class="src-badge" :class="'src-' + (setting.source || 'default')">
                    {{ setting.source === 'db' ? t('modulesView.config.sourceDb') :
                       setting.source === 'env' ? t('modulesView.config.sourceEnv') :
                       t('modulesView.config.sourceDefault') }}
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
                      :min="setting.min"
                      :max="setting.max"
                      @change="saveSetting(setting.key, parseFloat(($event.target as HTMLInputElement).value))"
                    />
                    <span v-if="setting.min !== undefined || setting.max !== undefined" class="range-hint">
                      <span v-if="setting.min !== undefined">≥ {{ setting.min }}</span>
                      <span v-if="setting.max !== undefined"> ≤ {{ setting.max }}</span>
                    </span>
                  </div>
                  <!-- String / URL -->
                  <div v-else-if="setting.type === 'string' || setting.type === 'url'" class="config-string">
                    <input
                      type="text"
                      class="text-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
                      :placeholder="t('modulesView.config.inputPlaceholder', { description: setting.description })"
                    />
                  </div>
                  <!-- Enum -->
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
          </template>

          <!-- handoff模块的分组配置表单 -->
          <template v-if="selectedKey === 'handoff' && handoffConfigGroups">
            <!-- 主开关 + 触发模式 + skill -->
            <div v-if="handoffConfigGroups.master.length > 0" class="config-group">
              <h3 class="config-group-title">{{ t('modulesView.handoff.groupMaster') }}</h3>
              <p class="config-group-hint">{{ t('modulesView.handoff.groupMasterHint') }}</p>
              <div
                v-for="setting in handoffConfigGroups.master"
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
                  <div v-else-if="setting.type === 'enum' && setting.options" class="config-select">
                    <select
                      class="select-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLSelectElement).value)"
                    >
                      <option v-for="opt in setting.options" :key="opt" :value="opt">{{ opt }}</option>
                    </select>
                  </div>
                  <div v-else-if="setting.type === 'string' || setting.type === 'url'" class="config-string">
                    <input
                      type="text"
                      class="text-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- 触发阈值 -->
            <div v-if="handoffConfigGroups.trigger.length > 0" class="config-group">
              <h3 class="config-group-title">{{ t('modulesView.handoff.groupTrigger') }}</h3>
              <p class="config-group-hint">{{ t('modulesView.handoff.groupTriggerHint') }}</p>
              <div
                v-for="setting in handoffConfigGroups.trigger"
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
                  <div v-if="setting.type === 'int'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="1"
                      @change="saveSetting(setting.key, parseInt(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                  <div v-else-if="setting.type === 'float'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="0.01"
                      min="0"
                      max="1"
                      @change="saveSetting(setting.key, parseFloat(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- 摘要生成 -->
            <div v-if="handoffConfigGroups.summary.length > 0" class="config-group">
              <h3 class="config-group-title">{{ t('modulesView.handoff.groupSummary') }}</h3>
              <p class="config-group-hint">{{ t('modulesView.handoff.groupSummaryHint') }}</p>
              <div
                v-for="setting in handoffConfigGroups.summary"
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
                <div v-if="getHandoffDependencyWarning(setting.key)" class="dependency-warning">
                  {{ getHandoffDependencyWarning(setting.key) }}
                </div>
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
                  <div v-else-if="setting.type === 'enum' && setting.options" class="config-select">
                    <select
                      class="select-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLSelectElement).value)"
                    >
                      <option v-for="opt in setting.options" :key="opt" :value="opt">
                        {{ opt === 'llm' ? 'LLM 调用 (llm)' : opt === 'rule' ? '规则抽取 (rule)' : opt === 'hybrid' ? '混合模式 (hybrid)' : opt }}
                      </option>
                    </select>
                  </div>
                  <div v-else-if="setting.type === 'int'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="1"
                      @change="saveSetting(setting.key, parseInt(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                  <div v-else class="config-string">
                    <input
                      type="text"
                      class="text-input"
                      :value="setting.value ?? setting.default"
                      :placeholder="setting.description"
                      @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
                    />
                  </div>
                </div>
              </div>
            </div>

            <!-- 安全限制 + 通知 -->
            <div v-if="handoffConfigGroups.safety.length > 0" class="config-group">
              <h3 class="config-group-title">{{ t('modulesView.handoff.groupSafety') }}</h3>
              <p class="config-group-hint">{{ t('modulesView.handoff.groupSafetyHint') }}</p>
              <div
                v-for="setting in handoffConfigGroups.safety"
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
                  <div v-if="setting.type === 'enum' && setting.options" class="config-select">
                    <select
                      class="select-input"
                      :value="setting.value ?? setting.default"
                      @change="saveSetting(setting.key, ($event.target as HTMLSelectElement).value)"
                    >
                      <option v-for="opt in setting.options" :key="opt" :value="opt">
                        {{ opt === 'none' ? '静默 (none)' : opt === 'info' ? '信息 (info)' : opt === 'warn' ? '警告 (warn)' : opt }}
                      </option>
                    </select>
                  </div>
                  <div v-else-if="setting.type === 'int'" class="config-number">
                    <input
                      type="number"
                      class="number-input"
                      :value="setting.value ?? setting.default"
                      step="1"
                      @change="saveSetting(setting.key, parseInt(($event.target as HTMLInputElement).value))"
                    />
                  </div>
                  <div v-else-if="setting.type === 'string' || setting.type === 'url'" class="config-string">
                    <input
                      type="text"
                      class="text-input"
                      :value="setting.value ?? setting.default"
                      :placeholder="setting.type === 'url' ? 'https://...' : ''"
                      @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
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
                <span class="src-badge" :class="'src-' + (setting.source || 'default')">
                  {{ setting.source === 'db' ? t('modulesView.config.sourceDb') :
                     setting.source === 'env' ? t('modulesView.config.sourceEnv') :
                     t('modulesView.config.sourceDefault') }}
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
                <div v-else-if="setting.type === 'int' || setting.type === 'float'" class="config-number">
                  <input
                    type="number"
                    class="number-input"
                    :value="setting.value ?? setting.default"
                    :step="setting.type === 'float' ? '0.01' : '1'"
                    :min="setting.min"
                    :max="setting.max"
                    @change="saveSetting(setting.key, parseFloat(($event.target as HTMLInputElement).value))"
                  />
                </div>
                <div v-else-if="setting.type === 'string' || setting.type === 'url'" class="config-string">
                  <input
                    type="text"
                    class="text-input"
                    :value="setting.value ?? setting.default"
                    @change="saveSetting(setting.key, ($event.target as HTMLInputElement).value)"
                    :placeholder="t('modulesView.config.inputPlaceholder', { description: setting.description })"
                  />
                </div>
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
                {{ selectedModule.key === 'feishu_bot' ? '📱' : selectedModule.key === 'wechat_bot' ? '💬' : selectedModule.key === 'dingtalk_bot' ? '🤖' : '🔗' }}
              </span>
              <div class="integ-info">
                <h3 class="integ-title">
                  {{ selectedModule.key === 'feishu_bot' ? t('modulesView.integration.feishuBotIntegration') : selectedModule.integration.label }}
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
              <div class="integ-steps">
                <h4 class="steps-title">{{ t('modulesView.integration.stepsTitle') }}</h4>
                <ol class="steps-list">
                  <li v-for="(step, i) in selectedModule.key === 'feishu_bot' ? t('modulesView.integration.feishuSteps') : []" :key="i">{{ step }}</li>
                </ol>
              </div>
              <div class="integ-status">
                <span class="status-indicator" :class="selectedModule.enabled ? 'connected' : 'disconnected'" />
                <span>{{ selectedModule.enabled ? t('modulesView.integration.enabledStatus') : t('modulesView.integration.disabledHint') }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- Status tab (runtime summary) -->
        <div v-if="activeTab === 'status' && moduleConfigSummary" class="tab-content">
          <div class="status-grid">
            <div
              v-for="(value, key) in moduleConfigSummary"
              :key="key"
              class="status-item"
            >
              <span class="status-key">{{ key }}</span>
              <span
                class="status-value"
                :class="typeof value === 'boolean' ? (value ? 'text-green' : 'text-muted') : ''"
              >{{ typeof value === 'boolean' ? (value ? '✓' : '✗') : (Array.isArray(value) ? value.join(', ') : value) }}</span>
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
.page-header-left { flex: 1; }
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
.page-header-right { flex-shrink: 0; }
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
.summary-sep { color: var(--text-secondary, #8b949e); margin: 0 2px; }
.summary-total { font-size: 18px; color: var(--text-secondary, #8b949e); }
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

.module-group { margin-bottom: 8px; }
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
  position: relative;
}
.module-card:hover { background: var(--bg-hover, #21262d); }
.module-card.active {
  background: rgba(99, 102, 241, 0.08);
  border-color: var(--accent, #6366f1);
}
.module-card.disabled { opacity: 0.65; }
.module-card.has-missing { border-left: 3px solid #fbbf24; }
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
.card-body { flex: 1; min-width: 0; }
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
.missing-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 10px;
  font-weight: 700;
  color: #92400e;
  background: #fbbf24;
  border-radius: 50%;
  margin-left: 2px;
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
.dot-off { background: #6e7681; }

/* ── Toggle Switch ── */
.toggle-wrap { flex-shrink: 0; cursor: pointer; }
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
.toggle-input:checked + .toggle-track { background: var(--accent, #6366f1); }
.toggle-input:checked + .toggle-track .toggle-knob { transform: translateX(16px); }
.toggle-input:disabled + .toggle-track { opacity: 0.5; cursor: not-allowed; }

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
.empty-state { text-align: center; color: var(--text-secondary, #8b949e); }
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
.detail-icon { font-size: 32px; }
.detail-title-area { flex: 1; }
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

/* ── Dependency Banner (soft hint) ── */
.dep-banner {
  margin-bottom: 14px;
  padding: 10px 12px;
  border-radius: 8px;
  font-size: 12px;
}
.dep-banner.dep-ok {
  background: rgba(52, 211, 153, 0.08);
  border: 1px solid rgba(52, 211, 153, 0.3);
  color: #34d399;
}
.dep-banner.dep-missing {
  background: rgba(251, 191, 36, 0.08);
  border: 1px solid rgba(251, 191, 36, 0.3);
  color: #fbbf24;
}
.dep-msg-ok { font-weight: 500; }
.dep-msg-warn { margin-bottom: 6px; font-weight: 500; }
.dep-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 4px;
}
.dep-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  background: rgba(251, 191, 36, 0.18);
  border: 1px solid rgba(251, 191, 36, 0.4);
  border-radius: 4px;
  cursor: pointer;
  font-weight: 500;
  transition: background 0.15s;
}
.dep-chip:hover { background: rgba(251, 191, 36, 0.3); }
.dep-jump {
  font-size: 10px;
  opacity: 0.7;
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
.tab-btn:hover { color: var(--text-primary, #e6edf3); }
.tab-btn.active {
  color: var(--accent-h, #818cf8);
  border-bottom-color: var(--accent, #6366f1);
}

/* ── Tab Content ── */
.tab-content { font-size: 13px; }
.info-section { margin-bottom: 20px; }
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

/* ── Dependencies ── */
.dependency-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.dep-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  transition: background 0.15s;
}
.dep-item:hover {
  background: var(--bg-hover, #21262d);
}
.dep-item.dep-disabled {
  opacity: 0.7;
  border-color: rgba(251, 191, 36, 0.3);
}
.dep-icon {
  font-size: 20px;
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
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}
.dep-key {
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, monospace;
  color: var(--text-secondary, #8b949e);
}
.dep-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 500;
  flex-shrink: 0;
}
.dep-badge.required {
  background: rgba(239, 68, 68, 0.12);
  color: #f87171;
}
.dep-badge.optional {
  background: rgba(139, 148, 158, 0.12);
  color: #8b949e;
}
.dep-status {
  font-size: 12px;
  flex-shrink: 0;
}
.dep-status.ok {
  color: #34d399;
}
.dep-status.warning {
  color: #fbbf24;
  display: flex;
  align-items: center;
  gap: 4px;
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
.btn-action:disabled { opacity: 0.5; cursor: not-allowed; }
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
.btn-ghost:hover { background: var(--bg-hover, #21262d); }
.btn-ghost:disabled { opacity: 0.5; cursor: not-allowed; }

/* ── Test Result Banner ── */
.test-result {
  margin-top: 12px;
  padding: 10px 14px;
  border-radius: 6px;
  font-size: 12px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.test-ok {
  background: rgba(52, 211, 153, 0.1);
  border: 1px solid rgba(52, 211, 153, 0.3);
  color: #34d399;
}
.test-fail {
  background: rgba(248, 113, 113, 0.1);
  border: 1px solid rgba(248, 113, 113, 0.3);
  color: #f87171;
}
.test-detail {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 11px;
  opacity: 0.8;
}

/* ── Config Hint ── */
.config-hint {
  padding: 10px 12px;
  background: rgba(99, 102, 241, 0.06);
  border-left: 3px solid var(--accent, #6366f1);
  border-radius: 4px;
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin-bottom: 14px;
  line-height: 1.5;
}

/* ── Config Section (grouped) ── */
.config-section {
  margin-bottom: 18px;
}
.config-section-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--accent-h, #818cf8);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin: 0 0 10px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border, #30363d);
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
.range-hint {
  display: inline-block;
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-muted, #6e7681);
  font-family: ui-monospace, SFMono-Regular, monospace;
}

/* ── Config Boolean (small toggle) ── */
.config-bool { padding: 4px 0; }
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
.switch-label-sm .toggle-input:checked + .toggle-track-sm { background: var(--accent, #6366f1); }
.switch-label-sm .toggle-input:checked + .toggle-track-sm .toggle-knob-sm { transform: translateX(14px); }
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
.integ-icon { font-size: 28px; }
.integ-info { flex: 1; }
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
.integ-body { padding: 16px; }
.integ-docs {
  margin-bottom: 14px;
  font-size: 12px;
}
.integ-docs-label { color: var(--text-secondary, #8b949e); }
.integ-link {
  color: var(--accent-h, #818cf8);
  text-decoration: none;
  word-break: break-all;
}
.integ-link:hover { text-decoration: underline; }
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
.status-indicator.disconnected { background: #6e7681; }

/* ── Status Tab (Runtime Summary) ── */
.status-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  padding: 12px;
  background: var(--bg, #0f1117);
  border-radius: 8px;
}
.status-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 10px;
  font-size: 12px;
}
.status-key {
  color: var(--text-muted, #6e7681);
  font-family: ui-monospace, SFMono-Regular, monospace;
}
.status-value {
  color: var(--text-primary, #e6edf3);
  font-weight: 500;
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

.dep-title-icon {
  margin-right: 6px;
}
.dependency-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
  flex-wrap: wrap;
}
.dependency-actions-hint {
  font-size: 11px;
  color: var(--text-muted, #6e7681);
}
.cascade-log {
  margin-top: 12px;
  padding: 10px 12px;
  background: rgba(52, 211, 153, 0.08);
  border: 1px solid rgba(52, 211, 153, 0.3);
  border-radius: 6px;
  font-size: 12px;
  color: #34d399;
}
.cascade-log ul {
  margin: 6px 0 0 16px;
  padding: 0;
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
.config-group-hint {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  margin: -8px 0 12px;
  padding: 0;
  line-height: 1.5;
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
  .layout { grid-template-columns: 1fr; }
  .list-pane,
  .detail-pane { max-height: none; }
  .meta-grid { grid-template-columns: 1fr; }
  .status-grid { grid-template-columns: 1fr; }
}
</style>