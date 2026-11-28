<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted, watch } from 'vue'
import {
  listSettings,
  getSetting,
  updateSetting,
  rollbackSetting,
  getBasicConfig,
  type SettingItem,
  type SettingSpec,
  type BasicConfigResp,
  type BasicSettingEntry,
  type BasicInfraFacts,
} from '../api'
import { confirmDialog } from '../composables/useConfirmDialog'
import { fetchSystemTimezone } from '../utils/systemTimezone'
// ★★ 图标一律用图标库的组件（DESIGN.md §6「图标是画出来的」——不许用 emoji /
//   Unicode 字形冒充图标系统）。第 29 增量：这一组原本是 11 个 emoji 字形
//   （📋 🗜 🚦 ⏱ 🔁 🔀 💬 🔐 🔒 ⚡ ⚙️）⇒ **emoji 藏在一个数据数组里** ⇒
//   源码侧的 V3 字形判据（只看元素字面量）**看不见它** ⇒ 只能靠人改 + 补判据。
import {
  AlarmClock,
  ChatDotRound,
  CircleCheck,
  Coin,
  Files,
  Lock,
  Monitor,
  RefreshRight,
  Search,
  Setting,
  Share,
  Timer,
  Warning,
} from '@element-plus/icons-vue'

const { t } = useI18n()


const items = ref<SettingItem[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const selectedKey = ref<string>('')
const selected = ref<SettingSpec | null>(null)
const currentValue = ref<any>(null)
const currentSource = ref<string>('')
const editBuffer = ref<string>('') // JSON text editor
const filterCategory = ref<string>('')
const tagInput = ref('')

// ── 基础设置轮（2026-11-20）───────────────────────────────────────────
// ① 跨分类搜索：键名 / 描述 / 类别名逐条包含匹配（前端本地过滤，
//    list 结果本身就是全量注册面，不需要后端再开搜索参数）。
const searchText = ref('')
const filteredItems = computed(() => {
  const q = searchText.value.trim().toLowerCase()
  if (!q) return items.value
  return items.value.filter((it) => {
    const catLabel = categoryLabel(it.category)
    return (
      it.key.toLowerCase().includes(q) ||
      (it.description || '').toLowerCase().includes(q) ||
      (catLabel && catLabel.toLowerCase().includes(q))
    )
  })
})
function categoryLabel(category: string): string {
  const c = categories.value.find((x) => x.key === category)
  return c ? c.label : ''
}

// ② 基础设置面板：GET /api/admin/basic-config 的分组聚合展示，
//    保存仍走既有 updateSetting 通道（校验+审计+热应用不复制）。
const showBasicConfig = ref(false)
const basicConfig = ref<BasicConfigResp | null>(null)
const basicLoading = ref(false)
const basicBuffers = ref<Record<string, string>>({})
const basicSavingKey = ref<string>('')

async function toggleBasicConfig() {
  showBasicConfig.value = !showBasicConfig.value
  if (showBasicConfig.value && !basicConfig.value) {
    await loadBasicConfig()
  }
}

async function loadBasicConfig() {
  basicLoading.value = true
  try {
    basicConfig.value = await getBasicConfig()
    // 为每个可编辑键初始化编辑缓冲（值 → 字符串形态）。
    const buffers: Record<string, string> = {}
    for (const g of basicConfig.value.groups) {
      for (const it of g.items) {
        buffers[it.key] = basicValueToString(it)
      }
    }
    basicBuffers.value = buffers
  } catch (e: any) {
    error.value = e.message || t('settings.detail.errors.loadListFailed')
  } finally {
    basicLoading.value = false
  }
}

function basicValueToString(it: BasicSettingEntry): string {
  const v = it.value ?? it.default
  if (v === null || v === undefined) return ''
  return String(v)
}

async function saveBasicSetting(entry: BasicSettingEntry) {
  const raw = basicBuffers.value[entry.key]
  if (raw === undefined) return
  // 按类型解析（与右侧详情编辑器 save() 同构的解析语义）。
  let parsed: any
  if (entry.type === 'int') {
    parsed = Number(raw)
    if (isNaN(parsed)) {
      error.value = t('settings.detail.errors.invalidNumber')
      return
    }
  } else if (entry.type === 'float') {
    parsed = Number(raw)
    if (isNaN(parsed)) {
      error.value = t('settings.detail.errors.invalidNumber')
      return
    }
  } else {
    parsed = raw
  }
  basicSavingKey.value = entry.key
  error.value = null
  try {
    await updateSetting(entry.key, { value: parsed })
    // 重拉面板拿到新 source/value；同时刷新左侧列表的现值列。
    await Promise.all([loadBasicConfig(), loadList()])
  } catch (e: any) {
    error.value = e.message || t('settings.detail.errors.saveFailed')
  } finally {
    basicSavingKey.value = ''
  }
}

// 只读基础设施实况的格式化值（redis 口令后端本就不回传；目录 env 空则示「未设置」）。
function infraValue(key: keyof BasicInfraFacts): string {
  const infra = basicConfig.value?.infra
  if (!infra) return '—'
  if (key === 'redis') {
    const r = infra.redis
    return r.configured ? `${r.addr}${r.db ? ' db=' + r.db : ''}` : t('settings.basicConfig.infraNotConfigured')
  }
  if (key === 'database') {
    const d = infra.database
    if (!d.configured) return t('settings.basicConfig.infraNotConfigured')
    return d.host ? `${d.host}/${d.database || ''}` : d.source
  }
  const raw = infra[key]
  return (typeof raw === 'string' && raw) ? raw : t('settings.basicConfig.infraNotSet')
}

// 时区轮（2026-11-20）：system.timezone 专用下拉的候选与当前偏移提示。
// 候选在选中该键时从 /api/admin/system-timezone 拉取（后端 choices 白名单面）。
const timezoneChoices = ref<Array<{ name: string; label: string }>>([])
const timezoneCurrentOffset = ref<string>('')
async function loadTimezoneChoices() {
  try {
    const res = await fetch('/api/admin/system-timezone', { credentials: 'include' })
    if (!res.ok) return
    const data = await res.json()
    if (Array.isArray(data.choices)) timezoneChoices.value = data.choices
    if (typeof data.utc_offset === 'string') timezoneCurrentOffset.value = data.utc_offset
  } catch {
    /* 拉不到候选时退回 string 输入框（v-else-if 的 choices 空数组仍渲染空下拉，
       补一个兜底：把当前值塞进候选保证可见） */
  }
}
watch(selectedKey, (key) => {
  if (key === 'system.timezone' && timezoneChoices.value.length === 0) {
    void loadTimezoneChoices()
  }
})

const categories = computed(() => [
  { key: '',                   label: t('settings.category.all'),  icon: Files },
  { key: 'compression',        label: t('settings.category.compression'),  icon: Coin },
  { key: 'rate_limit',         label: t('settings.category.rateLimit'),  icon: Timer },
  { key: 'timeout',            label: t('settings.category.timeout'),  icon: AlarmClock },
  { key: 'retry',              label: t('settings.category.retry'),  icon: RefreshRight },
  { key: 'routing',            label: t('settings.category.routing'),  icon: Share },
  { key: 'session',            label: t('settings.category.session'),  icon: ChatDotRound },
  { key: 'security',           label: t('settings.category.security'),  icon: Lock },
  { key: 'output_compliance',  label: t('settings.category.outputCompliance'),  icon: CircleCheck },
  { key: 'circuit_breaker',    label: t('settings.category.circuitBreaker'),  icon: Warning },
  // 2026-11-28: 会话无进展循环守卫（loop_guard.*，读方 session_loop_guard.go）。
  { key: 'loop_guard',         label: t('settings.category.loopGuard'),  icon: AlarmClock },
  { key: 'general',            label: t('settings.category.general'),  icon: Setting },
])

const basicGroups = computed(() => basicConfig.value?.groups ?? [])

// 基础设置分组卡片的展示标题（i18n）。
function basicGroupTitle(id: string): string {
  const map: Record<string, string> = {
    identity: t('settings.basicConfig.group.identity'),
    storage: t('settings.basicConfig.group.storage'),
    schedules: t('settings.basicConfig.group.schedules'),
  }
  return map[id] || id
}

// 基础设置条目的展示标签（i18n；兜底用键名本身）。
function basicEntryLabel(key: string): string {
  const map: Record<string, string> = {
    'gateway.display_name': t('settings.basicConfig.entry.displayName'),
    'system.skin': t('settings.basicConfig.entry.skin'),
    'system.timezone': t('settings.basicConfig.entry.timezone'),
    'gateway.mode': t('settings.basicConfig.entry.gatewayMode'),
    'storage.attachment_public_base_url': t('settings.basicConfig.entry.attachmentUrl'),
    'storage.attachment_dir_override': t('settings.basicConfig.entry.attachmentDir'),
    'reports.daily_rollup.hour': t('settings.basicConfig.entry.rollupHour'),
  }
  return map[key] || key
}

// 皮肤候选的展示标签。
function skinLabel(v: string): string {
  const map: Record<string, string> = {
    auto: t('settings.basicConfig.skin.auto'),
    light: t('settings.basicConfig.skin.light'),
    dark: t('settings.basicConfig.skin.dark'),
  }
  return map[v] || v
}

// 运行模式候选的展示标签。
function gatewayModeLabel(v: string): string {
  const map: Record<string, string> = {
    simple: t('settings.gateway.modeEnum.simple'),
    expert: t('settings.gateway.modeEnum.expert'),
  }
  return map[v] || v
}

function dangerLabel(level: number): string {
  return ['', t('settings.dangerLevel.note'), t('settings.dangerLevel.warn'), t('settings.dangerLevel.danger')][level] || ''
}

async function loadList() {
  loading.value = true
  error.value = null
  try {
    const r = await listSettings({ category: filterCategory.value || undefined })
    items.value = r.items
  } catch (e: any) {
    error.value = e.message || t('settings.detail.errors.loadListFailed')
  } finally {
    loading.value = false
  }
}

async function selectKey(key: string) {
  selectedKey.value = key
  try {
    const resp = await getSetting(key)
    selected.value = resp.spec
    currentValue.value = resp.value
    currentSource.value = resp.source

    // Smart initialization based on type
    if (resp.spec.type === 'bool') {
      editBuffer.value = String(resp.value ?? resp.spec.default)
    } else if (resp.spec.type === 'int' || resp.spec.type === 'float' || resp.spec.type === 'string') {
      editBuffer.value = String(resp.value ?? resp.spec.default)
    } else {
      editBuffer.value = JSON.stringify(resp.value ?? resp.spec.default, null, 2)
    }
    tagInput.value = ''
  } catch (e: any) {
    error.value = e.message || t('settings.detail.errors.loadDetailFailed')
  }
}

const isSessionAliasSetting = computed(() => selectedKey.value === 'session.id_body_keys')

const sessionAliasTags = computed<string[]>(() => {
  if (!isSessionAliasSetting.value) return []
  return editBuffer.value
    .split(',')
    .map(v => v.trim())
    .filter(Boolean)
})

function syncSessionAliasTags(tags: string[]) {
  editBuffer.value = tags.join(',')
}

function addSessionAliasTag() {
  if (!isSessionAliasSetting.value) return
  const next = tagInput.value.trim()
  if (!next) return
  const tags = [...sessionAliasTags.value]
  if (!tags.some(t => t.toLowerCase() === next.toLowerCase())) {
    tags.push(next)
    syncSessionAliasTags(tags)
  }
  tagInput.value = ''
}

function removeSessionAliasTag(tag: string) {
  if (!isSessionAliasSetting.value) return
  syncSessionAliasTags(sessionAliasTags.value.filter(t => t !== tag))
}

function onSessionAliasKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ',') {
    e.preventDefault()
    addSessionAliasTag()
  }
  if (e.key === 'Backspace' && !tagInput.value && sessionAliasTags.value.length > 0) {
    e.preventDefault()
    removeSessionAliasTag(sessionAliasTags.value[sessionAliasTags.value.length - 1])
  }
}

const sessionAliasPreviewExamples = computed(() => {
  if (!isSessionAliasSetting.value) return [] as Array<{ key: string; body: string; label: string; mapping: string }>
  return sessionAliasTags.value.slice(0, 4).map((key, index) => {
    const body = JSON.stringify({
      metadata: {
        [key]: `demo-session-${index + 1}`,
      },
      messages: [{ role: 'user', content: 'hello' }],
    }, null, 2)
    return {
      key,
 label: t('settings.detail.detectField', { key }),
      mapping: `metadata.${key} -> session_id`,
      body,
    }
  })
})

async function save() {
  if (!selectedKey.value || !selected.value) return
  saving.value = true
  error.value = null
  try {
    let parsed: any
    // Smart parsing based on type
    if (selected.value.type === 'bool') {
      parsed = editBuffer.value === 'true'
    } else if (selected.value.type === 'int' || selected.value.type === 'float') {
      parsed = Number(editBuffer.value)
      if (isNaN(parsed)) {
        throw new Error(t('settings.detail.errors.invalidNumber'))
      }
    } else if (selected.value.type === 'string') {
      parsed = String(editBuffer.value)
    } else {
      // Fallback to JSON parsing
      parsed = JSON.parse(editBuffer.value)
    }

    await updateSetting(selectedKey.value, { value: parsed })
    await loadList()
    await selectKey(selectedKey.value)
    // 时区轮（2026-11-20）：保存 system.timezone 后即时刷新全局时区状态，
    // 显示格式化与日切立刻切换（不等 App.vue 下次启动）。
    if (selectedKey.value === 'system.timezone') {
      await fetchSystemTimezone()
    }
  } catch (e: any) {
    error.value = e.message || t('settings.detail.errors.saveFailed')
  } finally {
    saving.value = false
  }
}

// Helper to get friendly label for enum values
function getEnumLabel(key: string, value: any): string {
  const labels: Record<string, Record<string, string>> = {
    'compression.mode': {
 '0': t('settings.compression.enumLabels.off'),
 '1': t('settings.compression.enumLabels.auto'),
 '2': t('settings.compression.enumLabels.on4xx'),
    },
    'compression.strategy': {
      'naive': t('settings.compression.strategyEnum.naive'),
      'smart': t('settings.compression.strategyEnum.smart'),
      'adaptive': t('settings.compression.strategyEnum.adaptive'),
    },
  }
  return labels[key]?.[String(value)] || String(value)
}

// Get description for enum options
function getEnumDescription(key: string, value: string): string {
  const descriptions: Record<string, Record<string, string>> = {
    'compression.mode': {
      '0': t('settings.compression.enumDescriptions.off'),
      '1': t('settings.compression.enumDescriptions.auto'),
      '2': t('settings.compression.enumDescriptions.on4xx'),
    },
  }
  return descriptions[key]?.[value] || ''
}

// Get detailed documentation for settings
function getSettingDocs(key: string): { title: string; content: string } | null {
  const docs: Record<string, { title: string; content: string }> = {
    'compression.mode': {
      title: t('settings.docs.compressionModeTitle'),
      content: t('settings.docs.compressionModeContent'),
    },
    'cache.enabled': {
      title: t('settings.docs.cacheEnabledTitle'),
      content: t('settings.docs.cacheEnabledContent'),
    },
    'format_conversion.enabled': {
      title: t('settings.docs.formatConversionTitle'),
      content: t('settings.docs.formatConversionContent'),
    },
    'rate_limit_rpm': {
      title: t('settings.docs.rateLimitRpmTitle'),
      content: t('settings.docs.rateLimitRpmContent'),
    },
    'rate_limit_concurrent': {
      title: t('settings.docs.rateLimitConcurrentTitle'),
      content: t('settings.docs.rateLimitConcurrentContent'),
    },
  }
  return docs[key] || null
}

async function rollback() {
  if (!selectedKey.value) return
  if (!(await confirmDialog(t('settings.editor.rollbackConfirm', { key: selectedKey.value })))) return
  try {
    await rollbackSetting(selectedKey.value)
    await loadList()
    await selectKey(selectedKey.value)
  } catch (e: any) {
    error.value = e.message || t('settings.detail.errors.rollbackFailed')
  }
}

function switchCategory(key: string) {
  filterCategory.value = key
  showBasicConfig.value = false
  loadList()
}

const filteredCount = computed(() => filteredItems.value.length)

onMounted(() => {
  loadList()
})
</script>

<template>
  <div class="settings-view">
    <div v-if="error" class="error-banner">{{ error }}</div>

    <!-- 顶部工具条：跨分类搜索 + 基础设置开关（2026-11-20 基础设置轮） -->
    <div class="settings-toolbar">
      <div class="search-box">
        <span class="search-icon"><Search /></span>
        <input
          v-model="searchText"
          type="text"
          class="search-input"
          :placeholder="t('settings.search.placeholder')"
        />
        <button v-if="searchText" type="button" class="search-clear" @click="searchText = ''">x</button>
      </div>
      <button
        class="btn basic-toggle"
        :class="{ active: showBasicConfig }"
        @click="toggleBasicConfig"
      >
        <span class="basic-toggle-icon"><Monitor /></span>
        <span>{{ t('settings.basicConfig.toggle') }}</span>
      </button>
    </div>

    <!-- 基础设置面板：系统级公共配置分组卡片 + 只读基础设施实况 -->
    <section v-if="showBasicConfig" class="basic-config-panel">
      <div v-if="basicLoading" class="loading">{{ t('common.loading') }}</div>
      <template v-else-if="basicConfig">
        <div class="basic-groups">
          <div v-for="g in basicGroups" :key="g.id" class="basic-group-card">
            <h4 class="basic-group-title">{{ basicGroupTitle(g.id) }}</h4>
            <div v-for="it in g.items" :key="it.key" class="basic-entry">
              <div class="basic-entry-main">
                <div class="basic-entry-label">
                  {{ basicEntryLabel(it.key) }}
                  <code class="basic-entry-key">{{ it.key }}</code>
                  <span class="src-badge" :class="'src-' + it.source">{{ it.source || '—' }}</span>
                  <span v-if="it.hot_reload" class="basic-hot-badge">{{ t('settings.basicConfig.hotReload') }}</span>
                </div>
                <div class="basic-entry-desc">{{ it.description }}</div>
              </div>
              <div class="basic-entry-editor">
                <!-- enum → 下拉 -->
                <select
                  v-if="it.options && it.options.length"
                  v-model="basicBuffers[it.key]"
                  class="select-input basic-editor-input"
                >
                  <option v-for="o in it.options" :key="o" :value="o">
                    {{ it.key === 'system.skin' ? skinLabel(o) : (it.key === 'gateway.mode' ? gatewayModeLabel(o) : o) }}
                  </option>
                </select>
                <!-- system.timezone → 复用既有候选下拉 -->
                <select
                  v-else-if="it.key === 'system.timezone'"
                  v-model="basicBuffers[it.key]"
                  class="select-input basic-editor-input"
                >
                  <option v-for="c in timezoneChoices" :key="c.name" :value="c.name">
                    {{ c.label }}（{{ c.name }}）
                  </option>
                </select>
                <!-- int/float → 数字输入 -->
                <input
                  v-else-if="it.type === 'int' || it.type === 'float'"
                  v-model="basicBuffers[it.key]"
                  type="number"
                  class="number-input basic-editor-input"
                  :step="it.type === 'float' ? '0.01' : '1'"
                />
                <!-- 其余 → 文本输入 -->
                <input
                  v-else
                  v-model="basicBuffers[it.key]"
                  type="text"
                  class="text-input basic-editor-input"
                />
                <button
                  class="btn btn-primary basic-save-btn"
                  :disabled="basicSavingKey === it.key || basicBuffers[it.key] === basicValueToString(it)"
                  @click="saveBasicSetting(it)"
                >
                  {{ basicSavingKey === it.key ? t('settings.editor.saving') : t('settings.editor.save') }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- 只读基础设施实况：redis / 数据库 / 实例 / 目录 env（修改需重启） -->
        <div class="basic-infra-card">
          <h4 class="basic-group-title">{{ t('settings.basicConfig.infraTitle') }}</h4>
          <p class="basic-infra-hint">{{ t('settings.basicConfig.infraHint') }}</p>
          <div class="basic-infra-grid">
            <div class="basic-infra-item">
              <span class="basic-infra-label">{{ t('settings.basicConfig.infra.instance') }}</span>
              <code>{{ infraValue('instance_id') }}</code>
            </div>
            <div class="basic-infra-item">
              <span class="basic-infra-label">{{ t('settings.basicConfig.infra.redis') }}</span>
              <code>{{ infraValue('redis') }}</code>
            </div>
            <div class="basic-infra-item">
              <span class="basic-infra-label">{{ t('settings.basicConfig.infra.database') }}</span>
              <code>{{ infraValue('database') }}</code>
            </div>
            <div class="basic-infra-item">
              <span class="basic-infra-label">{{ t('settings.basicConfig.infra.cacheDir') }}</span>
              <code>{{ infraValue('cache_dir_env') }}</code>
            </div>
            <div class="basic-infra-item">
              <span class="basic-infra-label">{{ t('settings.basicConfig.infra.attachmentDir') }}</span>
              <code>{{ infraValue('attachment_dir_env') }}</code>
            </div>
          </div>
        </div>
      </template>
    </section>

    <div class="layout">
      <aside class="category-bar">
        <button
          v-for="c in categories" :key="c.key"
          class="cat-btn" :class="{ active: filterCategory === c.key }"
          @click="switchCategory(c.key)"
        >
          <span class="cat-icon"><component :is="c.icon" /></span>
          <span class="cat-label">{{ c.label }}</span>
        </button>
      </aside>

      <section class="list-pane">
        <div class="list-header">
<span>{{ t('settings.list.count', { n: filteredCount }) }}</span>
        </div>
<div v-if="loading" class="loading">{{ t('common.loading') }}</div>
<div v-else-if="!filteredItems.length" class="empty">{{ t('settings.list.empty') }}</div>
        <table v-else class="settings-table">
          <thead>
            <tr>
 <th>{{ t('settings.list.colSetting') }}</th>
 <th>{{ t('settings.list.colCurrentValue') }}</th>
 <th>{{ t('settings.list.colSource') }}</th>
 <th>{{ t('settings.list.colDanger') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="it in filteredItems" :key="it.key"
              :class="{ active: selectedKey === it.key }"
              @click="selectKey(it.key)"
            >
              <td class="cell-key">
                <code>{{ it.key }}</code>
                <div class="cell-desc">{{ it.description }}</div>
              </td>
              <td class="cell-value">
                <code>{{ JSON.stringify(it.value) }}</code>
              </td>
              <td>
                <span class="src-badge" :class="'src-' + it.source">{{ it.source || '—' }}</span>
              </td>
              <td class="cell-danger">{{ dangerLabel(it.danger_level) }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <aside class="detail-pane" v-if="selected">
        <h3 class="detail-title">
          <code>{{ selectedKey }}</code>
        </h3>
        <p class="detail-desc">{{ selected.description }}</p>

        <!-- Tenant-scoped warning -->
        <div v-if="selected.scope === 'tenant'" class="tenant-warning">
          <div class="warning-icon"><Warning /></div>
          <div class="warning-content">
 <strong>{{ t('settings.detail.tenantScopedTitle') }}</strong>
 <p>{{ t('settings.detail.tenantScopedBody') }}</p>
          </div>
        </div>

        <!-- Detailed documentation -->
        <div v-if="getSettingDocs(selectedKey)" class="detail-docs">
          <div class="docs-title" v-html="getSettingDocs(selectedKey)!.title"></div>
          <div class="docs-content" v-html="getSettingDocs(selectedKey)!.content"></div>
        </div>

        <dl class="meta">
 <dt>{{ t('settings.detail.type') }}</dt><dd>{{ selected.type }}</dd>
 <dt>{{ t('settings.detail.currentValue') }}</dt>
          <dd>
            <code class="current-value">{{ JSON.stringify(currentValue) }}</code>
            <span class="src-badge" :class="'src-' + currentSource">{{ currentSource }}</span>
          </dd>
 <dt>{{ t('settings.detail.defaultValue') }}</dt><dd><code>{{ JSON.stringify(selected.default) }}</code></dd>
 <dt v-if="selected.options">{{ t('settings.detail.options') }}</dt>
          <dd v-if="selected.options">
            <code v-for="o in selected.options" :key="o" class="opt-chip">{{ o }}</code>
          </dd>
 <dt>{{ t('settings.detail.dangerLevel') }}</dt><dd>{{ dangerLabel(selected.danger_level) }}</dd>
 <dt>{{ t('settings.detail.hotReload') }}</dt>
          <dd>
 <span v-if="selected.hot_reload" class="src-badge src-db">{{ t('common.yes') }}</span>
 <span v-else class="src-badge src-default">{{ t('settings.detail.noRequiresRestart') }}</span>
          </dd>
 <dt v-if="selected.observability">{{ t('settings.detail.observability') }}</dt>
          <dd v-if="selected.observability">
            <a :href="selected.observability" target="_blank" rel="noopener">
              <code>{{ selected.observability }}</code>
            </a>
          </dd>
        </dl>

        <div v-if="selected.scope !== 'tenant'" class="editor">
 <label class="editor-label">{{ t('settings.editor.newValue') }}</label>

          <!-- Boolean type: Switch -->
          <div v-if="selected.type === 'bool'" class="editor-boolean">
            <label class="switch-label">
              <input
                type="checkbox"
                v-model="editBuffer"
                :true-value="'true'"
                :false-value="'false'"
                class="switch-input"
              />
              <span class="switch-track"></span>
              <span class="switch-text">{{ editBuffer === 'true' ? t('settings.editor.enabledText') : t('settings.editor.disabledText') }}</span>
            </label>
          </div>

          <!-- Enum type with known options: Select -->
          <div v-else-if="selectedKey === 'compression.mode'" class="editor-select">
            <select v-model="editBuffer" class="select-input">
 <option value="0">0 - {{ t('settings.compression.enumLabels.off') }}</option>
 <option value="1">1 - {{ t('settings.compression.enumLabels.auto') }}</option>
 <option value="2">2 - {{ t('settings.compression.enumLabels.on4xx') }} {{ t('settings.gateway.recommended') }}</option>
            </select>
            <div class="select-hint">
 <div v-if="editBuffer === '0'" class="hint-item">{{ t('settings.compression.enumDescriptions.off') }}</div>
 <div v-else-if="editBuffer === '1'" class="hint-item">{{ t('settings.compression.enumDescriptions.auto') }}</div>
 <div v-else-if="editBuffer === '2'" class="hint-item">{{ t('settings.compression.enumDescriptions.on4xx') }}</div>
            </div>
          </div>

          <!-- gateway.mode enum: Select -->
          <div v-else-if="selectedKey === 'gateway.mode'" class="editor-select">
            <select v-model="editBuffer" class="select-input">
              <option value="simple">{{ t('settings.gateway.modeEnum.simple') }}</option>
 <option value="expert">{{ t('settings.gateway.modeEnum.expert') }} {{ t('settings.gateway.recommended') }}</option>
            </select>
            <div class="select-hint">
              <div v-if="editBuffer === 'simple'" class="hint-item">{{ t('settings.gateway.modeDescriptions.simple') }}</div>
              <div v-else-if="editBuffer === 'expert'" class="hint-item">{{ t('settings.gateway.modeDescriptions.expert') }}</div>
            </div>
          </div>

          <!-- system.timezone: 系统时区下拉（2026-11-20 时区轮）。
               候选来自 /api/admin/system-timezone 的 choices（后端白名单面）；
               保存后即时刷新全局时区状态，显示/日切立刻切换。 -->
          <div v-else-if="selectedKey === 'system.timezone'" class="editor-select">
            <select v-model="editBuffer" class="select-input">
              <option v-for="c in timezoneChoices" :key="c.name" :value="c.name">
                {{ c.label }}（{{ c.name }}）
              </option>
            </select>
            <div class="select-hint">
              <div class="hint-item">{{ t('settings.timezone.hint') }}</div>
              <div v-if="timezoneCurrentOffset" class="hint-item">{{ t('settings.timezone.currentNow', { offset: timezoneCurrentOffset }) }}</div>
            </div>
          </div>

          <!-- Number type: Number input -->
          <div v-else-if="selected.type === 'int' || selected.type === 'float'" class="editor-number">
            <input
              type="number"
              v-model="editBuffer"
              class="number-input"
              :step="selected.type === 'float' ? '0.01' : '1'"
            />
          </div>

          <!-- Session alias string: tag editor -->
          <div v-else-if="isSessionAliasSetting" class="editor-tags">
            <div class="tag-editor-shell">
              <span v-for="tag in sessionAliasTags" :key="tag" class="tag-chip">
                <span>{{ tag }}</span>
                <button type="button" class="tag-chip-remove" @click="removeSessionAliasTag(tag)">x</button>
              </span>
              <input
                v-model="tagInput"
                type="text"
                class="tag-input"
 :placeholder="t('settings.editor.aliasPlaceholder')"
                @keydown="onSessionAliasKeydown"
                @blur="addSessionAliasTag"
              />
            </div>
 <div class="json-hint">{{ t('settings.editor.aliasHint') }}</div>
            <div class="alias-preview">
 <div class="alias-preview-title">{{ t('settings.editor.aliasPreviewTitle') }}</div>
              <div class="alias-preview-list">
                <div v-for="example in sessionAliasPreviewExamples" :key="example.key" class="alias-preview-card">
                  <div class="alias-preview-label">{{ example.label }}</div>
                  <div class="alias-preview-mapping">{{ example.mapping }}</div>
                  <pre class="alias-preview-code">{{ example.body }}</pre>
                </div>
              </div>
            </div>
          </div>

          <!-- String type: Text input -->
          <div v-else-if="selected.type === 'string'" class="editor-string">
            <input
              type="text"
              v-model="editBuffer"
              class="text-input"
 :placeholder="t('settings.editor.stringPlaceholder')"
            />
          </div>

          <!-- Fallback: JSON textarea -->
          <div v-else class="editor-json">
            <textarea
              v-model="editBuffer"
              rows="4"
              class="editor-textarea"
              spellcheck="false"
 :placeholder="t('settings.editor.jsonPlaceholder')"
            />
 <div class="json-hint">{{ t('settings.editor.jsonHint') }}</div>
          </div>

          <div class="editor-actions">
            <button class="btn btn-primary" :disabled="saving" @click="save">
              {{ saving ? t('settings.editor.saving') : t('settings.editor.save') }}
            </button>
 <button class="btn btn-ghost" @click="rollback">{{ t('settings.editor.rollback') }}</button>
          </div>
        </div>
      </aside>

      <div v-else class="detail-pane empty-detail">
 <p>{{ t('settings.detail.emptyHint') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* === Dark theme — consistent with rest of app === */
.settings-view {
  padding: 16px;
  color: var(--text-primary);
  font-size: 13px;
}
.error-banner {
  padding: 8px;
  background: color-mix(in srgb, var(--danger) 10%, transparent);
  border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
  color: var(--danger);
  border-radius: 6px;
  margin-bottom: 12px;
}

/* === 基础设置轮（2026-11-20）：工具条 + 基础设置面板 === */
.settings-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
.search-box {
  position: relative;
  flex: 1;
  max-width: 420px;
  display: flex;
  align-items: center;
}
.search-icon {
  position: absolute;
  left: 10px;
  display: flex;
  color: var(--text-secondary);
  pointer-events: none;
}
.search-input {
  width: 100%;
  padding: 8px 32px 8px 32px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg-card);
  color: var(--text-primary);
  font-size: 13px;
}
.search-input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 10%, transparent);
}
.search-clear {
  position: absolute;
  right: 8px;
  border: 0;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  font-size: 13px;
  padding: 2px 4px;
}
.search-clear:hover {
  color: var(--text-primary);
}
.basic-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
  background: var(--bg-card);
  color: var(--text-primary);
  border: 1px solid var(--border);
}
.basic-toggle:hover {
  background: var(--bg-hover);
}
.basic-toggle.active {
  border-color: var(--accent);
  color: var(--accent-h);
}
.basic-toggle-icon {
  display: flex;
}

.basic-config-panel {
  margin-bottom: 12px;
}
.basic-groups {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 12px;
}
.basic-group-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 14px;
}
.basic-group-title {
  margin: 0 0 10px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}
.basic-entry {
  padding: 10px 0;
  border-top: 1px solid var(--border);
}
.basic-entry:first-of-type {
  border-top: 0;
}
.basic-entry-label {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
}
.basic-entry-key {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 11px;
  color: var(--text-secondary);
  font-weight: 400;
}
.basic-entry-desc {
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-secondary);
  line-height: 1.5;
}
.basic-entry-editor {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  align-items: center;
}
.basic-editor-input {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  padding: 6px 10px;
}
.basic-save-btn {
  white-space: nowrap;
}
.basic-hot-badge {
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  background: color-mix(in srgb, var(--success) 14%, transparent);
  color: var(--success);
}

.basic-infra-card {
  margin-top: 12px;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 14px;
}
.basic-infra-hint {
  margin: 0 0 10px;
  font-size: 11px;
  color: var(--text-secondary);
}
.basic-infra-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 8px;
}
.basic-infra-item {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 6px 8px;
  background: var(--bg);
  border-radius: 6px;
  min-width: 0;
}
.basic-infra-label {
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
}
.basic-infra-item code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  color: var(--text-primary);
  word-break: break-all;
}

/* 搜索态下右侧详情面板仍可用：grid 三栏不变，只有列表行被过滤 */

.layout {
  display: grid;
  grid-template-columns: 160px 1fr 360px;
  gap: 12px;
  min-height: 70vh;
}

/* === Category bar === */
.category-bar {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.cat-btn {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  text-align: left;
}
.cat-btn:hover {
  background: var(--bg-hover);
}
.cat-btn.active {
  background: var(--bg-card);
  color: var(--text-primary);
  border-color: var(--accent);
}
.cat-icon { font-size: 16px; }

/* === List pane === */
.list-pane {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: auto;
}
.list-header {
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
  color: var(--text-secondary);
  font-size: 12px;
}
.loading, .empty {
  text-align: center;
  padding: 32px;
  color: var(--text-secondary);
}

.settings-table {
  width: 100%;
  border-collapse: collapse;
}
.settings-table th,
.settings-table td {
  padding: 8px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.settings-table th {
  color: var(--text-secondary);
  font-weight: 500;
  background: var(--bg);
  font-size: 12px;
}
.settings-table tr {
  cursor: pointer;
}
.settings-table tr.active td {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}
.settings-table tr:hover:not(.active) td {
  background: var(--bg-hover);
}
.cell-key code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  color: var(--accent-h);
}
.cell-desc {
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-secondary);
}
.cell-value code {
  padding: 1px 4px;
  background: var(--bg);
  border-radius: 3px;
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, monospace;
}
/* 危险等级是短标签。表格是 auto 布局、value 列的 JSON 很长 ⇒ 危险列会被
   挤到 ~41px，于是「🟡 注意」按字竖排（实测 41×124，一列一个字）。
   标签逐字断行没有含义，宁可让这列多占几十像素。 */
.cell-danger {
  white-space: nowrap;
}

/* === Source badges === */
.src-badge {
  display: inline-block;
  padding: 1px 4px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
  margin-left: 4px;
}
.src-badge.src-db {
  background: color-mix(in srgb, var(--success) 15%, transparent);
  color: var(--success);
}
.src-badge.src-env {
  background: color-mix(in srgb, var(--accent) 15%, transparent);
  color: var(--accent-h);
}
.src-badge.src-default {
  background: color-mix(in srgb, var(--text-secondary) 15%, transparent);
  color: var(--text-secondary);
}

/* === Detail pane === */
.detail-pane {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  overflow: auto;
}
.empty-detail {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-secondary);
  font-size: 14px;
}
.detail-title {
  margin: 0 0 4px;
  font-size: 14px;
}
.detail-title code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  color: var(--accent-h);
}
.detail-desc {
  font-size: 12px;
  color: var(--text-secondary);
  margin: 0 0 12px;
}
.meta {
  display: grid;
  grid-template-columns: 80px 1fr;
  gap: 4px 12px;
  font-size: 12px;
  margin: 0 0 16px;
}
.meta dt {
  color: var(--text-secondary);
}
.meta dd {
  margin: 0;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
}
.meta code {
  padding: 1px 4px;
  background: var(--bg);
  border-radius: 3px;
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, monospace;
}
.meta a {
  color: var(--accent-h);
  text-decoration: none;
}
.meta a:hover {
  text-decoration: underline;
}
.opt-chip {
  background: var(--bg);
  color: var(--text-secondary);
}
.current-value {
  font-weight: 600;
  color: var(--text-primary);
}

/* === Editor === */
.editor {
  border-top: 1px solid var(--border);
  padding-top: 12px;
}
.editor-label {
  display: block;
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 4px;
}
.editor-textarea {
  width: 100%;
  padding: 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text-primary);
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  resize: vertical;
  margin-bottom: 8px;
}
.editor-textarea:focus {
  outline: none;
  border-color: var(--accent);
}
.editor-actions {
  display: flex;
  gap: 8px;
}

/* === Buttons === */
.btn {
  padding: 4px 12px;
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  border: 1px solid transparent;
}
.btn-primary {
  background: var(--accent);
  color: var(--on-primary);
}
.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-ghost {
  background: transparent;
  color: var(--text-primary);
  border: 1px solid var(--border);
}
.btn-ghost:hover {
  background: var(--bg-hover);
}

/* === Smart Editor Styles === */
.editor-boolean {
  padding: 16px 0;
}

.switch-label {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  cursor: pointer;
  user-select: none;
}

.switch-input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}

.switch-track {
  position: relative;
  width: 44px;
  height: 24px;
  background: var(--border);
  border-radius: 12px;
  transition: background 0.2s var(--kx-motion-ease-out);
}

.switch-track::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 20px;
  height: 20px;
  background: white;
  border-radius: 50%;
  transition: transform 0.2s var(--kx-motion-ease-out);
}

.switch-input:checked + .switch-track {
  background: var(--accent);
}

.switch-input:checked + .switch-track::after {
  transform: translateX(20px);
}

.switch-text {
  font-size: 14px;
  font-weight: 500;
  color: var(--text);
}

.editor-select, .editor-number, .editor-string, .editor-json, .editor-tags {
  margin: 12px 0;
}

.select-input, .number-input, .text-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-secondary);
  color: var(--text);
  font-size: 14px;
  font-family: inherit;
}

.select-input:focus, .number-input:focus, .text-input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 10%, transparent);
}

.tag-editor-shell {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding: 8px;
  min-height: 44px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-secondary);
}

.tag-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--accent) 14%, transparent);
  color: var(--accent-h);
  font-size: 12px;
}

.tag-chip-remove {
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  padding: 0;
  font-size: 12px;
}

.tag-input {
  flex: 1 1 180px;
  min-width: 140px;
  border: 0;
  outline: 0;
  background: transparent;
  color: var(--text);
  font-size: 14px;
}

.select-hint {
  margin-top: 8px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--accent) 5%, transparent);
  border: 1px solid var(--accent);
  border-radius: 4px;
}

.hint-item {
  font-size: 13px;
  line-height: 1.5;
  color: var(--muted);
}

.json-hint {
  margin-top: 4px;
  font-size: 12px;
  color: var(--muted);
}

.alias-preview {
  margin-top: 12px;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: color-mix(in srgb, var(--accent) 5%, transparent);
}

.alias-preview-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}

.alias-preview-list {
  display: grid;
  gap: 8px;
}

.alias-preview-card {
  border: 1px solid color-mix(in srgb, var(--accent) 14%, transparent);
  border-radius: 8px;
  background: var(--bg);
  padding: 8px;
}

.alias-preview-label {
  font-size: 12px;
  color: var(--accent-h);
  margin-bottom: 4px;
}

.alias-preview-mapping {
  font-size: 12px;
  color: var(--text);
  margin-bottom: 8px;
}

.alias-preview-code {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-secondary);
  font-family: ui-monospace, SFMono-Regular, monospace;
}

/* === Documentation Section === */
.tenant-warning {
  display: flex;
  gap: 12px;
  margin: 16px 0;
  padding: 16px;
  background: color-mix(in srgb, var(--warning) 10%, transparent);
  border: 1px solid color-mix(in srgb, var(--warning) 30%, transparent);
  border: 1px solid var(--warning);
  border-radius: 8px;
}

.warning-icon {
  font-size: 24px;
  line-height: 1;
}

.warning-content {
  flex: 1;
}

.warning-content strong {
  display: block;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 4px;
}

.warning-content p {
  font-size: 13px;
  line-height: 1.5;
  color: var(--text);
  margin: 0;
}

.detail-docs {
  margin: 16px 0;
  padding: 16px;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: 8px;
}

.docs-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 12px;
}

.docs-content {
  font-size: 13px;
  line-height: 1.6;
  color: var(--text);
}

.docs-content p {
  margin: 8px 0;
}

.docs-content ul {
  margin: 8px 0;
  padding-left: 16px;
}

.docs-content li {
  margin: 4px 0;
}

.docs-content code {
  padding: 2px 4px;
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  border-radius: 3px;
  font-size: 12px;
  font-family: 'Menlo', 'Monaco', 'Courier New', monospace;
}

.docs-note {
  margin-top: 12px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  border: 1px solid var(--accent);
  border-radius: 4px;
  font-size: 13px;
}

/* === Responsive === */
@media (max-width: 1024px) {
  .layout {
    grid-template-columns: 1fr;
  }
  .category-bar {
    flex-direction: row;
    overflow-x: auto;
  }
  .settings-toolbar {
    flex-wrap: wrap;
  }
  .basic-groups,
  .basic-infra-grid {
    grid-template-columns: 1fr;
  }
}

/* 2026-10-27 第 26 增量：空间移动的 reduced-motion 路径（impeccable animate.md:77）。
   颜色 / 透明度反馈保留（那是「有意义的状态变化」），只把**空间移动**停下来。 */
@media (prefers-reduced-motion: reduce) {
  .switch-track::after { transition: none; }
}
</style>
