<template>
  <div class="data-init">
    <!-- 顶部：说明 + 刷新 -->
    <div class="card intro">
      <div class="intro-text">
        <h3>{{ t('dataLifecycle.dataInit.title') }}</h3>
        <p>{{ t('dataLifecycle.dataInit.intro') }}</p>
      </div>
      <div class="intro-meta">
        <span v-if="generatedAt" class="stamp">{{ t('dataLifecycle.dataInit.generatedAt') }} {{ formatTime(generatedAt) }}</span>
        <button class="btn btn-ghost btn-sm" :disabled="busy" @click="load">
          {{ busy ? t('dataLifecycle.loading') : t('dataLifecycle.refresh') }}
        </button>
      </div>
    </div>

    <div v-if="loadError" class="card error-card">{{ loadError }}</div>

    <!-- 类别卡片 -->
    <div class="grid">
      <div
        v-for="cat in categories"
        :key="cat.key"
        class="cat-card"
        :class="{ selected: selected.includes(cat.key) }"
      >
        <label class="cat-head">
          <input
            type="checkbox"
            :checked="selected.includes(cat.key)"
            :disabled="busy"
            @change="toggle(cat.key)"
          />
          <span class="cat-name">{{ labelFor(cat) }}</span>
          <span class="level" :class="cat.level">
            {{ cat.level === 'operational'
              ? t('dataLifecycle.dataInit.level.operational')
              : t('dataLifecycle.dataInit.level.configuration') }}
          </span>
        </label>

        <div class="metrics">
          <div class="metric">
            <span class="metric-label">{{ t('dataLifecycle.dataInit.rows') }}</span>
            <span class="metric-value">{{ formatNumber(cat.rows) }}</span>
          </div>
          <div class="metric">
            <span class="metric-label">{{ t('dataLifecycle.dataInit.size') }}</span>
            <span class="metric-value">{{ cat.bytes_human }}</span>
          </div>
        </div>

        <dl class="detail">
          <div>
            <dt>{{ t('dataLifecycle.dataInit.keepProtected') }}</dt>
            <dd>{{ formatNumber(cat.protected_rows) }}</dd>
          </div>
          <div>
            <dt>{{ t('dataLifecycle.dataInit.willDelete') }}</dt>
            <dd>{{ formatNumber(cat.target_rows) }}</dd>
          </div>
          <div v-if="cat.oldest_at">
            <dt>{{ t('dataLifecycle.dataInit.oldest') }}</dt>
            <dd>{{ formatTime(cat.oldest_at) }}</dd>
          </div>
          <div v-if="cat.newest_at">
            <dt>{{ t('dataLifecycle.dataInit.newest') }}</dt>
            <dd>{{ formatTime(cat.newest_at) }}</dd>
          </div>
        </dl>

        <p v-if="keepNoteFor(cat)" class="keep-note">{{ keepNoteFor(cat) }}</p>
        <p v-if="cat.missing_tables?.length" class="missing-note">
          {{ t('dataLifecycle.dataInit.missingTables') }}: {{ cat.missing_tables.join(', ') }}
        </p>

        <button class="btn btn-ghost btn-sm tables-toggle" @click="toggleTables(cat.key)">
          {{ expandedTables.includes(cat.key)
            ? t('dataLifecycle.dataInit.hideTables')
            : t('dataLifecycle.dataInit.showTables') }}
        </button>

        <table v-if="expandedTables.includes(cat.key)" class="table-list">
          <thead>
            <tr>
              <th scope="col">{{ t('dataLifecycle.dataInit.tableName') }}</th>
              <th scope="col">{{ t('dataLifecycle.dataInit.rows') }}</th>
              <th scope="col">{{ t('dataLifecycle.dataInit.size') }}</th>
              <th scope="col">{{ t('dataLifecycle.dataInit.willDelete') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tbl in cat.tables" :key="tbl.name">
              <td><code>{{ tbl.name }}</code></td>
              <td>{{ tbl.exists ? formatNumber(tbl.rows) : t('dataLifecycle.dataInit.tableMissing') }}</td>
              <td>{{ tbl.exists ? tbl.bytes_human : '—' }}</td>
              <td>{{ tbl.exists ? formatNumber(tbl.target_rows) : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 操作区 -->
    <div class="card actions">
      <p class="default-hint">{{ t('dataLifecycle.dataInit.defaultHint') }}</p>
      <div class="selection">
        <span>
          {{ t('dataLifecycle.dataInit.selectedCount', { count: selected.length }) }}
          <template v-if="selected.length">
            · {{ t('dataLifecycle.dataInit.selectedRows', { rows: formatNumber(selectedTargetRows) }) }}
          </template>
        </span>
        <div class="selection-actions">
          <button class="btn btn-ghost btn-sm" :disabled="busy" @click="selectAll">
            {{ t('dataLifecycle.dataInit.selectAll') }}
          </button>
          <button class="btn btn-ghost btn-sm" :disabled="busy" @click="selectNone">
            {{ t('dataLifecycle.dataInit.selectNone') }}
          </button>
          <button
            class="btn btn-primary btn-sm"
            :disabled="busy || !selected.length"
            @click="preview"
          >
            {{ busy ? t('dataLifecycle.loading') : t('dataLifecycle.dataInit.preview') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 预览结果 -->
    <div v-if="previewResult" class="card preview">
      <h3>{{ t('dataLifecycle.dataInit.previewTitle') }}</h3>

      <div class="preview-summary">
        <div>
          <span class="metric-label">{{ t('dataLifecycle.dataInit.willDelete') }}</span>
          <span class="metric-value danger">{{ formatNumber(previewResult.total_target_rows) }}</span>
        </div>
        <div>
          <span class="metric-label">{{ t('dataLifecycle.dataInit.keepProtected') }}</span>
          <span class="metric-value">{{ formatNumber(previewResult.total_protected_rows) }}</span>
        </div>
      </div>

      <ul v-if="previewResult.warnings?.length" class="warnings">
        <li v-for="w in previewResult.warnings" :key="w">{{ warningText(w) }}</li>
      </ul>

      <table class="table-list">
        <thead>
          <tr>
            <th scope="col">{{ t('dataLifecycle.dataInit.category') }}</th>
            <th scope="col">{{ t('dataLifecycle.dataInit.willDelete') }}</th>
            <th scope="col">{{ t('dataLifecycle.dataInit.keepProtected') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="pc in previewResult.categories" :key="pc.key">
            <td>{{ previewLabel(pc) }}</td>
            <td>{{ formatNumber(pc.target_rows) }}</td>
            <td>{{ formatNumber(pc.protected_rows) }}</td>
          </tr>
        </tbody>
      </table>

      <p class="hint">{{ t('dataLifecycle.dataInit.tokenHint') }}</p>

      <!-- 手打确认短语：破坏性动作不允许「点一下就删」 -->
      <div class="confirm-row">
        <label class="confirm-label" for="data-init-confirm">
          {{ t('dataLifecycle.dataInit.confirmLabel') }}
        </label>
        <input
          id="data-init-confirm"
          v-model="confirmPhrase"
          class="confirm-input"
          type="text"
          :placeholder="confirmPhrase"
          autocomplete="off"
          :disabled="busy"
        />
        <button
          class="btn btn-danger btn-sm"
          :disabled="busy || !confirmPhraseMatches"
          @click="execute"
        >
          {{ busy ? t('dataLifecycle.loading') : t('dataLifecycle.dataInit.execute') }}
        </button>
      </div>
    </div>

    <!-- 执行结果 -->
    <div v-if="lastResult" class="card result-card">
      <h3>{{ t('dataLifecycle.dataInit.resultTitle') }}</h3>
      <p class="result-line">
        {{ t('dataLifecycle.dataInit.resultSummary', {
          rows: formatNumber(lastResult.total_rows_deleted),
          actor: lastResult.actor,
        }) }}
      </p>
      <table class="table-list">
        <thead>
          <tr>
            <th scope="col">{{ t('dataLifecycle.dataInit.category') }}</th>
            <th scope="col">{{ t('dataLifecycle.dataInit.deleted') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in lastResult.results" :key="r.key">
            <td>{{ categoryLabel(r.key) }}</td>
            <td>{{ formatNumber(r.rows_deleted) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../../i18n'
import { formatNumberLocale } from '../../utils/format'
import {
  dataInitCatalog,
  dataInitPreview,
  dataInitExecute,
  type DataInitCatalogResponse,
  type DataInitCategoryStat,
  type DataInitExecuteResponse,
  type DataInitPreviewCategory,
  type DataInitPreviewResponse,
} from '../../api/dataInit'
import { surfaceError } from '../../i18n/errorSurface'

const { t } = useI18n()

// ★ 后端（admin/data_lifecycle_data_init.go）下发的 label_key / keep_note_key /
//   warning 键分别落在这三个前缀下。这里把前缀写成**字面量**：① 死键门据此
//   认得它们是活键（动态前缀登记，不是「8 语种共有 + 0 引用」的死键）；
//   ② 「类别 → i18n 键」的对应关系在源码里可对拍，不用翻后端才知道。
const DATA_INIT_LABEL_PREFIX = 'dataLifecycle.dataInit.categories.'
const DATA_INIT_KEEP_PREFIX = 'dataLifecycle.dataInit.keep.'
const DATA_INIT_WARNING_PREFIX = 'dataLifecycle.dataInit.warning.'

const categories = ref<DataInitCategoryStat[]>([])
const generatedAt = ref('')
const loadError = ref('')
const busy = ref(false)

const selected = ref<string[]>([])
const expandedTables = ref<string[]>([])
const previewResult = ref<DataInitPreviewResponse | null>(null)
const lastResult = ref<DataInitExecuteResponse | null>(null)
const confirmPhrase = ref('')

// ★ 「默认要重置数据」的落地方式：**首次激活时默认全选**。
//   ⇒ 面板一打开就是「准备重置」的姿态：点「预览重置」即可看到将被删除的行数，
//     不必先手动勾一遍类别。
//   ★ 只应用一次（defaultSelectionApplied）：之后用户手动改过的选择不会被
//     「刷新」悄悄覆盖 ⇒ 刷新 ≠ 重置用户的显式意图。
//   ★ 执行成功后把标志复位 ⇒ 面板回到默认姿态，可以接着做下一轮归零。
const defaultSelectionApplied = ref(false)

// ★ 确认短语固定为 RESET：短、大写、不会与任何界面文案撞形。
const CONFIRM_WORD = 'RESET'
const confirmPhraseMatches = computed(() => confirmPhrase.value.trim() === CONFIRM_WORD)

const selectedTargetRows = computed(() => {
  const set = new Set(selected.value)
  return categories.value
    .filter(c => set.has(c.key))
    .reduce((sum, c) => sum + (c.target_rows || 0), 0)
})

function formatNumber(n: number): string {
  return formatNumberLocale(n || 0, localeRef.value)
}

function formatTime(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  const pad = (x: number) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function categoryLabel(key: string): string {
  const cat = categories.value.find(c => c.key === key)
  return cat ? labelFor(cat) : key
}

// labelFor 取类别的显示名。
//
// ★ 后端下发的 label_key 必须落在约定前缀下 ⇒ 不符合时**回落到原始类别 key**
//   （界面上出现一个可辨识的英文 key，比渲染出一个空标签或乱码更有用）。
function labelFor(cat: DataInitCategoryStat): string {
  const key = cat.label_key && cat.label_key.startsWith(DATA_INIT_LABEL_PREFIX) ? cat.label_key : ''
  return key ? t(key) : cat.key
}

// keepNoteFor 取「保留了什么」的说明。
function keepNoteFor(cat: DataInitCategoryStat): string {
  const key = cat.keep_note_key && cat.keep_note_key.startsWith(DATA_INIT_KEEP_PREFIX) ? cat.keep_note_key : ''
  return key ? t(key) : ''
}

// previewLabel 取预览里的类别显示名（预览响应用的是同一套 label_key 契约）。
function previewLabel(pc: DataInitPreviewCategory): string {
  const key = pc.label_key && pc.label_key.startsWith(DATA_INIT_LABEL_PREFIX) ? pc.label_key : ''
  return key ? t(key) : pc.key
}

// warningText 取告警文案。
function warningText(w: string): string {
  return w.startsWith(DATA_INIT_WARNING_PREFIX) ? t(w) : w
}

async function load() {
  busy.value = true
  loadError.value = ''
  try {
    const res: DataInitCatalogResponse = await dataInitCatalog()
    categories.value = res.categories || []
    generatedAt.value = res.generated_at || ''
    // ★ 首次加载 ⇒ 默认全选（默认要重置数据）；已应用过则保留用户的显式选择。
    applyDefaultSelection()
    // 预览结果与统计是两次独立读数 ⇒ 统计刷新后旧的预览不再可信 ⇒ 作废。
    previewResult.value = null
    confirmPhrase.value = ''
  } catch (e: unknown) {
    categories.value = []
    // ★ 错误文案走 surfaceError（唯一出口）：主文案本地化 + 原始 detail 只作次要原因。
    loadError.value = surfaceError(e, { key: 'dataLifecycle.dataInit.errors.load' })
  } finally {
    busy.value = false
  }
}

// applyDefaultSelection 首次激活时把全部类别选上（默认重置姿态）。
// 只生效一次；执行成功后由 execute() 复位 ⇒ 下一轮归零重新回到默认姿态。
function applyDefaultSelection() {
  if (defaultSelectionApplied.value) return
  defaultSelectionApplied.value = true
  selected.value = categories.value.map(c => c.key)
}

function selectAll() {
  selected.value = categories.value.map(c => c.key)
  // 选中集合变了 ⇒ 之前的预览令牌绑定的是旧集合 ⇒ 作废。
  previewResult.value = null
  confirmPhrase.value = ''
}

function selectNone() {
  selected.value = []
  previewResult.value = null
  confirmPhrase.value = ''
}

function toggle(key: string) {
  const idx = selected.value.indexOf(key)
  if (idx >= 0) {
    selected.value = selected.value.filter(k => k !== key)
  } else {
    selected.value = [...selected.value, key]
  }
  // 选中集合变了 ⇒ 之前的预览令牌绑定的是旧集合 ⇒ 作废。
  previewResult.value = null
  confirmPhrase.value = ''
}

function toggleTables(key: string) {
  const idx = expandedTables.value.indexOf(key)
  if (idx >= 0) {
    expandedTables.value = expandedTables.value.filter(k => k !== key)
  } else {
    expandedTables.value = [...expandedTables.value, key]
  }
}

async function preview() {
  if (!selected.value.length) return
  busy.value = true
  loadError.value = ''
  try {
    previewResult.value = await dataInitPreview(selected.value)
    confirmPhrase.value = ''
  } catch (e: unknown) {
    previewResult.value = null
    loadError.value = surfaceError(e, { key: 'dataLifecycle.dataInit.errors.preview' })
  } finally {
    busy.value = false
  }
}

async function execute() {
  if (!previewResult.value || !confirmPhraseMatches.value) return
  busy.value = true
  loadError.value = ''
  try {
    lastResult.value = await dataInitExecute(selected.value, previewResult.value.confirm_token)
    previewResult.value = null
    confirmPhrase.value = ''
    selected.value = []
    // ★ 执行后复位默认标志 ⇒ load() 会重新全选 ⇒ 面板回到「准备重置」的默认姿态。
    defaultSelectionApplied.value = false
    await load()
  } catch (e: unknown) {
    loadError.value = surfaceError(e, { key: 'dataLifecycle.dataInit.errors.execute' })
  } finally {
    busy.value = false
  }
}

onMounted(load)
defineExpose({ load })
</script>

<style scoped>
.data-init { padding: 16px; }

.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 16px;
  color: var(--text);
}

h3 { margin: 0 0 8px; font-size: 16px; }

.intro { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
.intro-text p { margin: 0; color: var(--muted); font-size: 13px; line-height: 1.6; }
.intro-meta { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.stamp { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }

.error-card { border-color: var(--danger); color: var(--danger); font-size: 13px; }

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.cat-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: var(--kx-space-3);
  color: var(--text);
}
.cat-card.selected { border-color: var(--accent); }

.cat-head { display: flex; align-items: center; gap: 8px; cursor: pointer; margin-bottom: 12px; }
.cat-name { font-size: 14px; font-weight: 600; flex: 1; }

.level {
  font-size: 11px;
  padding: 2px var(--kx-space-1);
  border-radius: 4px;
  white-space: nowrap;
}
.level.operational { color: var(--accent-h); background: color-mix(in srgb, var(--accent) 15%, transparent); }
.level.configuration { color: var(--warning); background: var(--warning-soft); }

.metrics { display: grid; grid-template-columns: 1fr 1fr; gap: var(--kx-space-2); margin-bottom: var(--kx-space-2); }
.metric { display: flex; flex-direction: column; gap: 2px; }
.metric-label { color: var(--muted); font-size: 11px; }
.metric-value { font-size: var(--kx-text-2xl); font-weight: 700; font-variant-numeric: tabular-nums; }
.metric-value.danger { color: var(--danger); }

.detail { margin: 0 0 var(--kx-space-2); display: flex; flex-direction: column; gap: var(--kx-space-1); }
.detail div { display: flex; justify-content: space-between; gap: 8px; font-size: 12px; }
.detail dt { color: var(--muted); margin: 0; }
.detail dd { margin: 0; font-variant-numeric: tabular-nums; }

.keep-note { margin: 0 0 8px; font-size: 12px; color: var(--muted); line-height: 1.5; }
.missing-note { margin: 0 0 8px; font-size: 12px; color: var(--warning); line-height: 1.5; }

.tables-toggle { margin-bottom: 8px; }

.table-list { width: 100%; border-collapse: collapse; font-size: 12px; margin-top: 8px; }
.table-list th {
  text-align: left;
  padding: var(--kx-space-1);
  color: var(--muted);
  font-weight: 500;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.table-list td {
  padding: var(--kx-space-1);
  border-bottom: 1px solid var(--border);
  font-variant-numeric: tabular-nums;
}
.table-list tr:last-child td { border-bottom: none; }
.table-list code { color: var(--accent-h); }

.actions .selection { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.actions span { color: var(--muted); font-size: 13px; }
.selection-actions { display: flex; align-items: center; gap: var(--kx-space-2); flex-wrap: wrap; }
.default-hint { margin: 0 0 var(--kx-space-3); color: var(--muted); font-size: 12px; line-height: 1.6; }

.preview-summary { display: grid; grid-template-columns: repeat(2, minmax(120px, 1fr)); gap: 12px; margin-bottom: 12px; }
.preview-summary > div { display: flex; flex-direction: column; gap: 2px; }

.warnings { margin: 0 0 var(--kx-space-3); padding-left: var(--kx-space-4); color: var(--warning); font-size: 12px; line-height: 1.6; }

.hint { color: var(--muted); font-size: 12px; margin: 12px 0 8px; line-height: 1.6; }

.confirm-row { display: flex; align-items: center; gap: var(--kx-space-2); flex-wrap: wrap; }
.confirm-label { font-size: 13px; color: var(--text); }
.confirm-input {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: var(--kx-space-1) var(--kx-space-2);
  background: var(--bg-subtle);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  min-width: 140px;
}

.result-card { border-color: var(--success); }
.result-line { margin: 0 0 8px; font-size: 13px; }

.btn {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 12px;
  color: var(--text);
  background: var(--bg-subtle);
  cursor: pointer;
  font-size: 13px;
}
.btn-primary { background: var(--accent); border-color: var(--accent); color: var(--on-primary); }
.btn-danger { background: var(--danger); border-color: var(--danger); color: var(--on-primary); }
.btn-ghost { background: transparent; }
.btn-sm { padding: var(--kx-space-1) var(--kx-space-2); font-size: 12px; }
.btn:disabled { opacity: .45; cursor: not-allowed; }

@media (max-width: 768px) {
  .intro { flex-direction: column; }
  .metrics { grid-template-columns: 1fr; }
}
</style>
