<script setup lang="ts">
// FreeDiscoveryView.vue — 免费资源自动发现管理页 (2026-09-09)。
//
// 三 Tab 结构 (规划文档 .agents/orbi-free-resource-integration-plan.md §2.5):
//   1. 模板管理  — 内置预设一键创建 / 自定义模板 / Orbi JSON 导入 / CRUD
//   2. 任务与结果审查 — 触发扫描 / 任务列表 / 发现结果勾选 + 批量导入
//   3. 导入历史  — 从任务列表派生 (models_imported > 0) + 导入明细回看
//
// 后端契约: docs/freediscovery-configuration.md §4; 请求体全部 snake_case。
// 扫描失败时后端仍返回 200 + status=failed 任务体, UI 需展示 error_message。

import { useI18n } from 'vue-i18n'
import { formatDateTime } from '../utils/datetime'
import { ref, computed, onMounted } from 'vue'
import {
  listFreeDiscoveryTemplates,
  createFreeDiscoveryTemplate,
  updateFreeDiscoveryTemplate,
  deleteFreeDiscoveryTemplate,
  getFreeDiscoveryPresets,
  importFreeDiscoveryOrbiTemplate,
  scanFreeDiscovery,
  listFreeDiscoveryTasks,
  listFreeDiscoveryTaskResults,
  importFreeDiscoveryResults,
  type FreeDiscoveryTemplate,
  type FreeDiscoveryPreset,
  type FreeDiscoveryTask,
  type FreeDiscoveryResult,
  type FreeDiscoveryImportSummary,
  type FreeDiscoveryConflictPolicy,
} from '../api'

const { t } = useI18n()

type TabId = 'templates' | 'tasks' | 'history'
const activeTab = ref<TabId>('templates')

const loading = ref(false)
const error = ref('')
const message = ref('')

function flash(msg: string): void {
  message.value = msg
  setTimeout(() => {
    if (message.value === msg) message.value = ''
  }, 6000)
}
function fail(err: unknown): void {
  error.value = err instanceof Error ? err.message : String(err)
  setTimeout(() => {
    if (error.value && error.value === (err instanceof Error ? err.message : String(err))) error.value = ''
  }, 8000)
}

// ── 模板管理 ────────────────────────────────────────────────────────────

const templates = ref<FreeDiscoveryTemplate[]>([])
const presets = ref<FreeDiscoveryPreset[]>([])
const presetCreating = ref('')

async function loadTemplates(): Promise<void> {
  loading.value = true
  try {
    const [tplResp, presetResp] = await Promise.all([
      listFreeDiscoveryTemplates(),
      getFreeDiscoveryPresets(),
    ])
    templates.value = tplResp.templates || []
    presets.value = presetResp.presets || []
    error.value = ''
  } catch (e) {
    fail(e)
  } finally {
    loading.value = false
  }
}

const existingProviderCodes = computed(() => new Set(templates.value.map((x) => x.provider_code)))

async function createFromPreset(p: FreeDiscoveryPreset): Promise<void> {
  presetCreating.value = p.provider_code
  try {
    await createFreeDiscoveryTemplate({
      provider_code: p.provider_code,
      display_name: p.display_name,
      base_url: p.base_url,
      api_type: p.api_type,
      api_key_env: p.api_key_env,
      tos_verdict: p.tos_verdict === 'unknown' ? 'ambiguous' : p.tos_verdict,
      tos_notes: p.tos_notes,
    })
    flash(t('freeDiscovery.presets.createDone', { name: p.display_name }))
    await loadTemplates()
  } catch (e) {
    fail(e)
  } finally {
    presetCreating.value = ''
  }
}

const showCustomForm = ref(false)
const formSubmitting = ref(false)
const customForm = ref({
  provider_code: '',
  display_name: '',
  base_url: '',
  api_type: 'openai-completions',
  api_key_env: '',
  models_endpoint: '/models',
  tos_verdict: 'ambiguous',
  tos_notes: '',
})

async function submitCustomForm(): Promise<void> {
  formSubmitting.value = true
  try {
    await createFreeDiscoveryTemplate({
      provider_code: customForm.value.provider_code.trim(),
      display_name: customForm.value.display_name.trim() || customForm.value.provider_code.trim(),
      base_url: customForm.value.base_url.trim(),
      api_type: customForm.value.api_type,
      api_key_env: customForm.value.api_key_env.trim(),
      models_endpoint: customForm.value.models_endpoint.trim() || '/models',
      tos_verdict: customForm.value.tos_verdict,
      tos_notes: customForm.value.tos_notes.trim(),
    })
    flash(t('freeDiscovery.form.created'))
    showCustomForm.value = false
    customForm.value = {
      provider_code: '',
      display_name: '',
      base_url: '',
      api_type: 'openai-completions',
      api_key_env: '',
      models_endpoint: '/models',
      tos_verdict: 'ambiguous',
      tos_notes: '',
    }
    await loadTemplates()
  } catch (e) {
    fail(e)
  } finally {
    formSubmitting.value = false
  }
}

const showOrbiImport = ref(false)
const orbiJson = ref('')
const orbiImporting = ref(false)
const orbiResult = ref<{ created: number; failed: number; errors: string[] } | null>(null)

async function submitOrbiImport(): Promise<void> {
  let parsed: unknown
  try {
    parsed = JSON.parse(orbiJson.value)
  } catch {
    fail(new Error(t('freeDiscovery.orbi.invalidJson')))
    return
  }
  orbiImporting.value = true
  try {
    const r = await importFreeDiscoveryOrbiTemplate(parsed as Parameters<typeof importFreeDiscoveryOrbiTemplate>[0])
    orbiResult.value = r
    await loadTemplates()
  } catch (e) {
    fail(e)
  } finally {
    orbiImporting.value = false
  }
}

async function toggleTemplate(tpl: FreeDiscoveryTemplate): Promise<void> {
  try {
    await updateFreeDiscoveryTemplate(tpl.id, { enabled: !tpl.enabled })
    tpl.enabled = !tpl.enabled
  } catch (e) {
    fail(e)
  }
}

async function removeTemplate(tpl: FreeDiscoveryTemplate): Promise<void> {
  if (!window.confirm(t('freeDiscovery.tpl.deleteConfirm', { name: tpl.display_name }))) return
  try {
    await deleteFreeDiscoveryTemplate(tpl.id)
    flash(t('freeDiscovery.tpl.deleted', { name: tpl.display_name }))
    await loadTemplates()
  } catch (e) {
    fail(e)
  }
}

// ── 任务与结果审查 ──────────────────────────────────────────────────────

const tasks = ref<FreeDiscoveryTask[]>([])
const tasksLoading = ref(false)

async function loadTasks(): Promise<void> {
  tasksLoading.value = true
  try {
    const resp = await listFreeDiscoveryTasks(50)
    tasks.value = resp.tasks || []
  } catch (e) {
    fail(e)
  } finally {
    tasksLoading.value = false
  }
}

const scanTemplateId = ref<number | null>(null)
const scanning = ref(false)

async function startScan(): Promise<void> {
  if (!scanTemplateId.value) return
  scanning.value = true
  try {
    // 上游失败也返回 200 + failed 任务体, 此处统一按任务处理
    const task = await scanFreeDiscovery(scanTemplateId.value)
    if (task.status === 'failed') {
      fail(new Error(task.error_message || t('freeDiscovery.scan.failed')))
    } else {
      flash(t('freeDiscovery.scan.done', { n: task.models_found }))
    }
    activeTab.value = 'tasks'
    await loadTasks()
    await selectTask(task)
  } catch (e) {
    fail(e)
  } finally {
    scanning.value = false
  }
}

const selectedTask = ref<FreeDiscoveryTask | null>(null)
const results = ref<FreeDiscoveryResult[]>([])
const resultsFilter = ref<'pending' | 'all'>('pending')
const resultsLoading = ref(false)
const selectedResultIds = ref<Set<number>>(new Set())
const conflictPolicy = ref<FreeDiscoveryConflictPolicy>('skip')
const importing = ref(false)
const lastImportSummary = ref<FreeDiscoveryImportSummary | null>(null)

async function selectTask(task: FreeDiscoveryTask): Promise<void> {
  selectedTask.value = task
  lastImportSummary.value = null
  selectedResultIds.value = new Set()
  await loadResults()
}

async function loadResults(): Promise<void> {
  if (!selectedTask.value) return
  resultsLoading.value = true
  try {
    const resp = await listFreeDiscoveryTaskResults(selectedTask.value.id, resultsFilter.value)
    results.value = resp.results || []
    selectedResultIds.value = new Set()
  } catch (e) {
    fail(e)
  } finally {
    resultsLoading.value = false
  }
}

const selectableResults = computed(() => results.value.filter((r) => r.import_status === 'pending'))
const allSelectableChecked = computed(
  () => selectableResults.value.length > 0 && selectableResults.value.every((r) => selectedResultIds.value.has(r.id)),
)

function toggleResult(id: number): void {
  const next = new Set(selectedResultIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedResultIds.value = next
}

function toggleAllResults(): void {
  if (allSelectableChecked.value) selectedResultIds.value = new Set()
  else selectedResultIds.value = new Set(selectableResults.value.map((r) => r.id))
}

async function runImport(onlySelected: boolean): Promise<void> {
  if (!selectedTask.value) return
  const body: { task_id: number; result_ids?: number[]; conflict_policy: FreeDiscoveryConflictPolicy } = {
    task_id: selectedTask.value.id,
    conflict_policy: conflictPolicy.value,
  }
  if (onlySelected) body.result_ids = [...selectedResultIds.value]
  importing.value = true
  try {
    const summary = await importFreeDiscoveryResults(body)
    lastImportSummary.value = summary
    flash(t('freeDiscovery.res.importedToast', { ...summary }))
    await Promise.all([loadResults(), loadTasks()])
  } catch (e) {
    fail(e)
  } finally {
    importing.value = false
  }
}

// ── 导入历史 ────────────────────────────────────────────────────────────

const historyTasks = computed(() => tasks.value.filter((x) => x.models_imported > 0))
const historyDetail = ref<FreeDiscoveryResult[] | null>(null)
const historyDetailTask = ref<FreeDiscoveryTask | null>(null)
const historyLoading = ref(false)

async function showHistoryDetail(task: FreeDiscoveryTask): Promise<void> {
  historyDetailTask.value = task
  historyLoading.value = true
  try {
    const resp = await listFreeDiscoveryTaskResults(task.id, 'all')
    historyDetail.value = resp.results || []
  } catch (e) {
    fail(e)
  } finally {
    historyLoading.value = false
  }
}

const historyDetailCounts = computed(() => {
  const out = { imported: 0, conflict: 0, skipped: 0, pending: 0 }
  for (const r of historyDetail.value || []) {
    if (r.import_status === 'imported') out.imported++
    else if (r.import_status === 'conflict') out.conflict++
    else if (r.import_status === 'skipped') out.skipped++
    else out.pending++
  }
  return out
})

// ── 展示辅助 ────────────────────────────────────────────────────────────

function taskStatusKey(s: string): string {
  const map: Record<string, string> = {
    pending: 'freeDiscovery.status.taskPending',
    running: 'freeDiscovery.status.running',
    success: 'freeDiscovery.status.success',
    failed: 'freeDiscovery.status.failed',
  }
  return map[s] || 'freeDiscovery.status.taskPending'
}

function triggerKey(s: string): string {
  const map: Record<string, string> = {
    manual: 'freeDiscovery.trigger.manual',
    scheduled: 'freeDiscovery.trigger.scheduled',
    webhook: 'freeDiscovery.trigger.webhook',
  }
  return map[s] || 'freeDiscovery.trigger.manual'
}

function importStatusKey(s: string): string {
  const map: Record<string, string> = {
    pending: 'freeDiscovery.status.review',
    imported: 'freeDiscovery.status.imported',
    skipped: 'freeDiscovery.status.skipped',
    conflict: 'freeDiscovery.status.conflict',
  }
  return map[s] || 'freeDiscovery.status.review'
}

function tosClass(v: string): string {
  if (v === 'ok') return 'tos-ok'
  if (v === 'caution') return 'tos-caution'
  if (v === 'avoid') return 'tos-avoid'
  return 'tos-ambiguous'
}

function taskStatusClass(s: string): string {
  if (s === 'success') return 'st-success'
  if (s === 'failed') return 'st-failed'
  if (s === 'running') return 'st-running'
  return 'st-pending'
}

function importStatusClass(s: string): string {
  if (s === 'imported') return 'st-success'
  if (s === 'conflict') return 'st-warning'
  if (s === 'skipped') return 'st-muted'
  return 'st-pending'
}

function fmtTime(iso: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return formatDateTime(d)
}

function fmtNum(n: number): string {
  // 0 是合法值 (无配额/无限额), 仅 null/undefined 显示破折号
  if (n == null) return '—'
  return String(n)
}

async function loadAll(): Promise<void> {
  await Promise.all([loadTemplates(), loadTasks()])
}

onMounted(loadAll)
</script>

<template>
  <div class="freediscovery-view">
    <div class="page-header">
      <div>
        <h2>{{ t('freeDiscovery.page.title') }}</h2>
        <p class="page-desc">{{ t('freeDiscovery.page.desc') }}</p>
      </div>
      <button class="btn btn-ghost btn-sm" :disabled="loading || tasksLoading" @click="loadAll">
        {{ loading || tasksLoading ? t('freeDiscovery.common.loading') : t('freeDiscovery.common.refresh') }}
      </button>
    </div>

    <div v-if="error" class="banner banner-error">{{ error }}</div>
    <div v-if="message" class="banner banner-ok">{{ message }}</div>

    <div class="tab-bar">
      <button
        v-for="tab in ([
          { id: 'templates' as TabId, label: t('freeDiscovery.tabs.templates') },
          { id: 'tasks' as TabId, label: t('freeDiscovery.tabs.tasks') },
          { id: 'history' as TabId, label: t('freeDiscovery.tabs.history') },
        ])"
        :key="tab.id"
        class="tab-btn"
        :class="{ active: activeTab === tab.id }"
        @click="activeTab = tab.id"
      >
        {{ tab.label }}
      </button>
    </div>

    <!-- ── Tab 1: templates ───────────────────────────────────────── -->
    <div v-if="activeTab === 'templates'" class="tab-panel">
      <div class="card">
        <div class="card-title">{{ t('freeDiscovery.presets.title') }}</div>
        <div class="preset-grid">
          <div v-for="p in presets" :key="p.provider_code" class="preset-card">
            <div class="preset-head">
              <span class="preset-name">{{ p.display_name }}</span>
              <span class="badge" :class="tosClass(p.tos_verdict)">{{ p.tos_verdict }}</span>
            </div>
            <code class="preset-url">{{ p.base_url }}</code>
            <div v-if="p.api_type && p.api_type !== 'openai-completions'" class="preset-warn">
              ⚠ {{ t('freeDiscovery.presets.scannerPending') }}
            </div>
            <div class="preset-key">{{ p.api_key_env || t('freeDiscovery.presets.keyless') }}</div>
            <button
              class="btn btn-sm btn-primary"
              :disabled="presetCreating === p.provider_code || existingProviderCodes.has(p.provider_code)"
              @click="createFromPreset(p)"
            >
              {{
                existingProviderCodes.has(p.provider_code)
                  ? t('freeDiscovery.presets.exists')
                  : presetCreating === p.provider_code
                    ? t('freeDiscovery.common.loading')
                    : t('freeDiscovery.presets.create')
              }}
            </button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head-row">
          <div class="card-title">{{ t('freeDiscovery.tpl.listTitle', { n: templates.length }) }}</div>
          <div class="head-actions">
            <button class="btn btn-ghost btn-sm" @click="showCustomForm = !showCustomForm">
              {{ showCustomForm ? t('freeDiscovery.common.hideForm') : t('freeDiscovery.form.show') }}
            </button>
            <button class="btn btn-ghost btn-sm" @click="showOrbiImport = !showOrbiImport">
              {{ showOrbiImport ? t('freeDiscovery.common.hideForm') : t('freeDiscovery.orbi.show') }}
            </button>
          </div>
        </div>

        <form v-if="showCustomForm" class="inline-form" @submit.prevent="submitCustomForm">
          <label>
            <span>{{ t('freeDiscovery.form.providerCode') }} *</span>
            <input v-model="customForm.provider_code" required placeholder="groq" />
          </label>
          <label>
            <span>{{ t('freeDiscovery.form.displayName') }}</span>
            <input v-model="customForm.display_name" placeholder="Groq Cloud" />
          </label>
          <label class="grow">
            <span>{{ t('freeDiscovery.form.baseUrl') }} *</span>
            <input v-model="customForm.base_url" required placeholder="https://api.groq.com/openai/v1" />
          </label>
          <label>
            <span>{{ t('freeDiscovery.form.apiType') }}</span>
            <select v-model="customForm.api_type">
              <option value="openai-completions">openai-completions</option>
              <option value="google-generative-ai">google-generative-ai</option>
              <option value="anthropic">anthropic</option>
            </select>
          </label>
          <label>
            <span>{{ t('freeDiscovery.form.apiKeyEnv') }}</span>
            <input v-model="customForm.api_key_env" placeholder="$GROQ_API_KEY" />
          </label>
          <label>
            <span>{{ t('freeDiscovery.form.tosVerdict') }}</span>
            <select v-model="customForm.tos_verdict">
              <option value="ok">ok</option>
              <option value="caution">caution</option>
              <option value="ambiguous">ambiguous</option>
              <option value="avoid">avoid</option>
            </select>
          </label>
          <div class="form-hint">{{ t('freeDiscovery.form.apiKeyEnvHint') }}</div>
          <button class="btn btn-sm btn-primary" type="submit" :disabled="formSubmitting">
            {{ formSubmitting ? t('freeDiscovery.common.loading') : t('freeDiscovery.form.submit') }}
          </button>
        </form>

        <div v-if="showOrbiImport" class="orbi-box">
          <div class="form-hint">{{ t('freeDiscovery.orbi.hint') }}</div>
          <textarea
            v-model="orbiJson"
            rows="5"
            placeholder='{"providers": {"groq": {"baseUrl": "https://api.groq.com/openai/v1", "api": "openai-completions", "apiKey": "$GROQ_API_KEY"}}}'
          ></textarea>
          <div class="head-actions">
            <button class="btn btn-sm btn-primary" :disabled="orbiImporting || !orbiJson.trim()" @click="submitOrbiImport">
              {{ orbiImporting ? t('freeDiscovery.common.loading') : t('freeDiscovery.orbi.import') }}
            </button>
          </div>
          <div v-if="orbiResult" class="form-hint">
            {{ t('freeDiscovery.orbi.done', { created: orbiResult.created, failed: orbiResult.failed }) }}
            <code v-for="e in orbiResult.errors" :key="e" class="orbi-err">{{ e }}</code>
          </div>
        </div>

        <div class="table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('freeDiscovery.tpl.name') }}</th>
                <th>{{ t('freeDiscovery.tpl.baseUrl') }}</th>
                <th>{{ t('freeDiscovery.tpl.apiType') }}</th>
                <th>{{ t('freeDiscovery.tpl.keyEnv') }}</th>
                <th>{{ t('freeDiscovery.tpl.tos') }}</th>
                <th>{{ t('freeDiscovery.tpl.enabled') }}</th>
                <th>{{ t('freeDiscovery.tpl.createdAt') }}</th>
                <th>{{ t('freeDiscovery.common.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="templates.length === 0">
                <td colspan="8" class="empty-cell">{{ t('freeDiscovery.common.empty') }}</td>
              </tr>
              <tr v-for="tpl in templates" :key="tpl.id">
                <td>
                  <div class="tpl-name">{{ tpl.display_name }}</div>
                  <code class="tpl-code">{{ tpl.provider_code }}</code>
                </td>
                <td><code class="tpl-url">{{ tpl.base_url }}</code></td>
                <td><code>{{ tpl.api_type }}</code></td>
                <td><code>{{ tpl.api_key_env || '—' }}</code></td>
                <td><span class="badge" :class="tosClass(tpl.tos_verdict)">{{ tpl.tos_verdict }}</span></td>
                <td>
                  <button class="toggle" :class="{ on: tpl.enabled }" @click="toggleTemplate(tpl)">
                    {{ tpl.enabled ? t('freeDiscovery.common.enabled') : t('freeDiscovery.common.disabled') }}
                  </button>
                </td>
                <td class="muted">{{ fmtTime(tpl.created_at) }}</td>
                <td class="actions-cell">
                  <button
                    class="btn btn-sm btn-ghost"
                    :disabled="!tpl.enabled || scanning"
                    :title="!tpl.enabled ? t('freeDiscovery.tpl.disabledScanHint') : ''"
                    @click="scanTemplateId = tpl.id; startScan()"
                  >
                    {{ t('freeDiscovery.tpl.scan') }}
                  </button>
                  <button class="btn btn-sm btn-ghost danger" @click="removeTemplate(tpl)">
                    {{ t('freeDiscovery.tpl.delete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- ── Tab 2: tasks & review ──────────────────────────────────── -->
    <div v-if="activeTab === 'tasks'" class="tab-panel">
      <div class="card">
        <div class="card-title">{{ t('freeDiscovery.scan.title') }}</div>
        <div class="scan-bar">
          <select v-model.number="scanTemplateId" class="scan-select">
            <option :value="null" disabled>{{ t('freeDiscovery.scan.pickTemplate') }}</option>
            <option v-for="tpl in templates.filter((x) => x.enabled)" :key="tpl.id" :value="tpl.id">
              {{ tpl.display_name }} ({{ tpl.provider_code }})
            </option>
          </select>
          <button
            class="btn btn-sm btn-primary"
            :disabled="scanning || !scanTemplateId"
            @click="startScan"
          >
            {{ scanning ? t('freeDiscovery.scan.running') : t('freeDiscovery.scan.start') }}
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-title">{{ t('freeDiscovery.task.listTitle', { n: tasks.length }) }}</div>
        <div class="table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>#</th>
                <th>{{ t('freeDiscovery.task.provider') }}</th>
                <th>{{ t('freeDiscovery.task.status') }}</th>
                <th>{{ t('freeDiscovery.task.trigger') }}</th>
                <th>{{ t('freeDiscovery.task.found') }}</th>
                <th>{{ t('freeDiscovery.task.imported') }}</th>
                <th>{{ t('freeDiscovery.task.by') }}</th>
                <th>{{ t('freeDiscovery.task.time') }}</th>
                <th>{{ t('freeDiscovery.task.error') }}</th>
                <th>{{ t('freeDiscovery.common.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="tasks.length === 0">
                <td colspan="10" class="empty-cell">{{ t('freeDiscovery.common.empty') }}</td>
              </tr>
              <tr
                v-for="task in tasks"
                :key="task.id"
                :class="{ selected: selectedTask?.id === task.id }"
              >
                <td>{{ task.id }}</td>
                <td><code>{{ task.provider_code }}</code></td>
                <td>
                  <span class="badge" :class="taskStatusClass(task.status)">
                    {{ t(taskStatusKey(task.status)) }}
                  </span>
                </td>
                <td class="muted">{{ t(triggerKey(task.trigger_type)) }}</td>
                <td class="num">{{ task.models_found }}</td>
                <td class="num">{{ task.models_imported }}</td>
                <td class="muted">{{ task.triggered_by }}</td>
                <td class="muted">{{ fmtTime(task.created_at) }}</td>
                <td class="err-cell" :title="task.error_message">{{ task.error_message || '—' }}</td>
                <td class="actions-cell">
                  <button class="btn btn-sm btn-ghost" @click="selectTask(task)">
                    {{ t('freeDiscovery.task.review') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="selectedTask" class="card">
        <div class="card-head-row">
          <div class="card-title">
            {{ t('freeDiscovery.res.title', { id: selectedTask.id, provider: selectedTask.provider_code }) }}
            <span class="badge" :class="taskStatusClass(selectedTask.status)">
              {{ t(taskStatusKey(selectedTask.status)) }}
            </span>
          </div>
          <div class="head-actions">
            <button
              class="tab-btn small"
              :class="{ active: resultsFilter === 'pending' }"
              @click="resultsFilter = 'pending'; loadResults()"
            >
              {{ t('freeDiscovery.res.pending') }}
            </button>
            <button
              class="tab-btn small"
              :class="{ active: resultsFilter === 'all' }"
              @click="resultsFilter = 'all'; loadResults()"
            >
              {{ t('freeDiscovery.res.all') }}
            </button>
          </div>
        </div>

        <div class="table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>
                  <input
                    type="checkbox"
                    :checked="allSelectableChecked"
                    :disabled="selectableResults.length === 0"
                    @change="toggleAllResults"
                  />
                </th>
                <th>{{ t('freeDiscovery.res.model') }}</th>
                <th>{{ t('freeDiscovery.res.displayName') }}</th>
                <th>{{ t('freeDiscovery.res.freeType') }}</th>
                <th>{{ t('freeDiscovery.res.monthly') }}</th>
                <th>{{ t('freeDiscovery.res.daily') }}</th>
                <th>{{ t('freeDiscovery.res.tos') }}</th>
                <th>{{ t('freeDiscovery.res.importStatus') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="results.length === 0">
                <td colspan="8" class="empty-cell">{{ t('freeDiscovery.res.none') }}</td>
              </tr>
              <tr v-for="r in results" :key="r.id">
                <td>
                  <input
                    type="checkbox"
                    :checked="selectedResultIds.has(r.id)"
                    :disabled="r.import_status !== 'pending'"
                    @change="toggleResult(r.id)"
                  />
                </td>
                <td><code class="model-id">{{ r.model_id }}</code></td>
                <td>{{ r.display_name || '—' }}</td>
                <td><code>{{ r.free_type || '—' }}</code></td>
                <td class="num">{{ fmtNum(r.monthly_tokens) }}</td>
                <td class="num">{{ fmtNum(r.daily_tokens) }}</td>
                <td>
                  <span class="badge" :class="tosClass(r.tos_verdict)" :title="r.tos_notes">{{ r.tos_verdict }}</span>
                </td>
                <td>
                  <span class="badge" :class="importStatusClass(r.import_status)">
                    {{ t(importStatusKey(r.import_status)) }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="import-bar">
          <label class="policy-label">
            <span>{{ t('freeDiscovery.res.policy') }}</span>
            <select v-model="conflictPolicy">
              <option value="skip">{{ t('freeDiscovery.res.policySkip') }}</option>
              <option value="overwrite">{{ t('freeDiscovery.res.policyOverwrite') }}</option>
              <option value="merge">{{ t('freeDiscovery.res.policyMerge') }}</option>
            </select>
          </label>
          <button
            class="btn btn-sm btn-primary"
            :disabled="importing || selectedResultIds.size === 0"
            @click="runImport(true)"
          >
            {{ t('freeDiscovery.res.importSelected', { n: selectedResultIds.size }) }}
          </button>
          <button
            class="btn btn-sm btn-ghost"
            :disabled="importing || selectableResults.length === 0"
            @click="runImport(false)"
          >
            {{ t('freeDiscovery.res.importAllPending') }}
          </button>
          <span v-if="lastImportSummary" class="import-summary">
            {{
              t('freeDiscovery.res.importedToast', {
                imported: lastImportSummary.imported,
                skipped: lastImportSummary.skipped,
                conflicted: lastImportSummary.conflicted,
                failed: lastImportSummary.failed,
              })
            }}
          </span>
        </div>
      </div>
    </div>

    <!-- ── Tab 3: import history ──────────────────────────────────── -->
    <div v-if="activeTab === 'history'" class="tab-panel">
      <div class="card">
        <div class="card-title">{{ t('freeDiscovery.hist.title', { n: historyTasks.length }) }}</div>
        <p class="page-desc">{{ t('freeDiscovery.hist.desc') }}</p>
        <div class="table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>#</th>
                <th>{{ t('freeDiscovery.task.provider') }}</th>
                <th>{{ t('freeDiscovery.task.imported') }}</th>
                <th>{{ t('freeDiscovery.task.found') }}</th>
                <th>{{ t('freeDiscovery.task.trigger') }}</th>
                <th>{{ t('freeDiscovery.task.by') }}</th>
                <th>{{ t('freeDiscovery.hist.completed') }}</th>
                <th>{{ t('freeDiscovery.common.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="historyTasks.length === 0">
                <td colspan="8" class="empty-cell">{{ t('freeDiscovery.hist.none') }}</td>
              </tr>
              <tr v-for="task in historyTasks" :key="task.id">
                <td>{{ task.id }}</td>
                <td><code>{{ task.provider_code }}</code></td>
                <td class="num">{{ task.models_imported }}</td>
                <td class="num">{{ task.models_found }}</td>
                <td class="muted">{{ t(triggerKey(task.trigger_type)) }}</td>
                <td class="muted">{{ task.triggered_by }}</td>
                <td class="muted">{{ fmtTime(task.completed_at) }}</td>
                <td class="actions-cell">
                  <button class="btn btn-sm btn-ghost" @click="showHistoryDetail(task)">
                    {{ t('freeDiscovery.hist.detail') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="historyDetailTask" class="card">
        <div class="card-title">
          {{ t('freeDiscovery.hist.detailTitle', { id: historyDetailTask.id, provider: historyDetailTask.provider_code }) }}
        </div>
        <div v-if="historyLoading" class="muted">{{ t('freeDiscovery.common.loading') }}</div>
        <template v-else>
          <div class="hist-counts">
            <span class="badge st-success">{{ t('freeDiscovery.status.imported') }}: {{ historyDetailCounts.imported }}</span>
            <span class="badge st-warning">{{ t('freeDiscovery.status.conflict') }}: {{ historyDetailCounts.conflict }}</span>
            <span class="badge st-muted">{{ t('freeDiscovery.status.skipped') }}: {{ historyDetailCounts.skipped }}</span>
            <span class="badge st-pending">{{ t('freeDiscovery.status.review') }}: {{ historyDetailCounts.pending }}</span>
          </div>
          <div class="table-wrap">
            <table class="data-table">
              <thead>
                <tr>
                  <th>{{ t('freeDiscovery.res.model') }}</th>
                  <th>{{ t('freeDiscovery.res.tos') }}</th>
                  <th>{{ t('freeDiscovery.res.importStatus') }}</th>
                  <th>{{ t('freeDiscovery.hist.importedAt') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in historyDetail || []" :key="r.id">
                  <td><code class="model-id">{{ r.model_id }}</code></td>
                  <td><span class="badge" :class="tosClass(r.tos_verdict)">{{ r.tos_verdict }}</span></td>
                  <td>
                    <span class="badge" :class="importStatusClass(r.import_status)">
                      {{ t(importStatusKey(r.import_status)) }}
                    </span>
                  </td>
                  <td class="muted">{{ fmtTime(r.imported_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.freediscovery-view {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}
.page-header h2 {
  margin: 0 0 4px;
  font-size: 20px;
}
.page-desc {
  margin: 0;
  font-size: 13px;
  color: var(--text-secondary);
}
.banner {
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 13px;
  border: 1px solid;
}
.banner-error {
  background: var(--danger-soft, #fff1f1);
  border-color: var(--danger, #c2413b);
  color: var(--danger, #c2413b);
  word-break: break-all;
}
.banner-ok {
  background: var(--success-soft, #e6f7ef);
  border-color: var(--success, #16845b);
  color: var(--success, #16845b);
}
.tab-bar {
  display: flex;
  gap: 6px;
  border-bottom: 1px solid var(--border);
}
.tab-btn {
  padding: 7px 14px;
  font-size: 13px;
  border: none;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  border-bottom: 2px solid transparent;
}
.tab-btn.active {
  color: var(--primary, #1e4fd6);
  border-bottom-color: var(--primary, #1e4fd6);
  font-weight: 600;
}
.tab-btn.small {
  padding: 4px 10px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
}
.tab-btn.small.active {
  border-bottom: 1px solid var(--primary, #1e4fd6);
  background: var(--bg-subtle, #eaf0ff);
}
.tab-panel {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
}
.card-title {
  margin: 0 0 12px;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.card-head-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.head-actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

/* Presets */
.preset-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  gap: 12px;
}
.preset-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.preset-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}
.preset-name {
  font-weight: 600;
  font-size: 13px;
}
.preset-url {
  font-size: 11px;
  color: var(--text-secondary);
  word-break: break-all;
}
.preset-key {
  font-size: 11px;
  color: var(--text-secondary);
}
.preset-warn {
  font-size: 11px;
  color: var(--warning, #b7791f);
}

/* Badges */
.badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 10px;
  font-size: 11px;
  white-space: nowrap;
}
.tos-ok {
  background: var(--success-soft, #e6f7ef);
  color: var(--success, #16845b);
}
.tos-caution {
  background: var(--warning-soft, #fff7e8);
  color: var(--warning, #b7791f);
}
.tos-avoid {
  background: var(--danger-soft, #fff1f1);
  color: var(--danger, #c2413b);
}
.tos-ambiguous {
  background: var(--bg-hover);
  color: var(--text-secondary);
}
.st-success {
  background: var(--success-soft, #e6f7ef);
  color: var(--success, #16845b);
}
.st-failed {
  background: var(--danger-soft, #fff1f1);
  color: var(--danger, #c2413b);
}
.st-running {
  background: var(--bg-subtle, #eaf0ff);
  color: var(--primary, #1e4fd6);
}
.st-pending {
  background: var(--warning-soft, #fff7e8);
  color: var(--warning, #b7791f);
}
.st-warning {
  background: var(--warning-soft, #fff7e8);
  color: var(--warning, #b7791f);
}
.st-muted {
  background: var(--bg-hover);
  color: var(--text-secondary);
}

/* Form */
.inline-form {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: flex-end;
  margin-bottom: 14px;
  padding: 12px;
  border: 1px dashed var(--border);
  border-radius: 8px;
}
.inline-form label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--text-secondary);
}
.inline-form label.grow {
  flex: 1;
  min-width: 220px;
}
.inline-form input,
.inline-form select {
  padding: 6px 8px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 13px;
  background: var(--bg-card);
  color: var(--text);
}
.form-hint {
  width: 100%;
  font-size: 12px;
  color: var(--text-secondary);
}
.orbi-box {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 14px;
  padding: 12px;
  border: 1px dashed var(--border);
  border-radius: 8px;
}
.orbi-box textarea {
  font-family: monospace;
  font-size: 12px;
  padding: 8px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-card);
  color: var(--text);
}
.orbi-err {
  display: block;
  font-size: 11px;
  color: var(--danger, #c2413b);
}

/* Table */
.table-wrap {
  overflow-x: auto;
}
.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.data-table th {
  text-align: left;
  padding: 8px 10px;
  color: var(--text-secondary);
  font-weight: 500;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.data-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.data-table tr.selected td {
  background: var(--bg-subtle, #eaf0ff);
}
.empty-cell {
  text-align: center;
  color: var(--text-secondary);
  padding: 20px 0;
}
.tpl-name {
  font-weight: 500;
}
.tpl-code,
.tpl-url {
  font-size: 11px;
  color: var(--text-secondary);
}
.tpl-url {
  word-break: break-all;
}
.model-id {
  font-size: 12px;
  word-break: break-all;
}
.num {
  font-variant-numeric: tabular-nums;
}
.muted {
  color: var(--text-secondary);
  font-size: 12px;
}
.err-cell {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--danger, #c2413b);
  font-size: 12px;
}
.actions-cell {
  white-space: nowrap;
}

/* Toggle + buttons */
.toggle {
  border: 1px solid var(--border);
  background: var(--bg-hover);
  color: var(--text-secondary);
  border-radius: 10px;
  font-size: 11px;
  padding: 2px 10px;
  cursor: pointer;
}
.toggle.on {
  background: var(--success-soft, #e6f7ef);
  color: var(--success, #16845b);
  border-color: var(--success, #16845b);
}
.btn.danger {
  color: var(--danger, #c2413b);
}

/* Scan bar */
.scan-bar {
  display: flex;
  gap: 10px;
  align-items: center;
}
.scan-select {
  min-width: 260px;
  padding: 6px 8px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 13px;
  background: var(--bg-card);
  color: var(--text);
}

/* Import bar */
.import-bar {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  margin-top: 12px;
}
.policy-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-secondary);
}
.policy-label select {
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 12px;
  background: var(--bg-card);
  color: var(--text);
}
.import-summary {
  font-size: 12px;
  color: var(--success, #16845b);
}
.hist-counts {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
</style>
