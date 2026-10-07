<script setup lang="ts">
// FreeDiscoveryView — 免费资源自动发现读面（第四十八批）。
//
//   GET /api/free-discovery/templates                 (admin)
//   GET /api/free-discovery/templates/presets         (admin)
//   GET /api/free-discovery/templates/{id}            (admin)
//   GET /api/free-discovery/tasks                     (admin)
//   GET /api/free-discovery/tasks/{id}                (admin)
//   GET /api/free-discovery/tasks/{id}/results       (admin)
//   GET /api/free-discovery/scan-scheduler/status     (**superAdmin**)
//
// ★★★★★★★★★★ 列表端点的 `updated_at` 恒为 Go 零值：`ListTasks` 的 SELECT **漏了这一列**
//   （`discovery_engine.go:460-464`），而 `GetTask`（`:408-412`）**有**。
//   ⇒ 页面**不许**把列表里的「最后更新」当真值显示，要显式标注它读不出来。
//
// ★★★★★★★★ `templates` 有 nil-guard（永不为 null），`presets` **没有**（可能为 null）。
//   ⇒ 「空」在本族有两种表示，页面分别渲染，不把 null 降级成「没有预设」。
//
// ★★★★★★★★ 同一个 handler、同一条路径：GET 是 admin 档、POST/PUT/PATCH/DELETE 是
//   superAdmin 档 ⇒ 抽屉席不设 requiresRole，但同路径写操作 tenant_admin 会 403。
//
// ★★★★★★★ `fdTenant` = `EffectiveTenantID` ⇒ **super_admin 看到的也只是 `default`
//   一个租户**，不是全部租户。
//
// ★★★ `?status=` 未知取值**不被拒** ⇒ 空结果既可能是「真没有」也可能是「过滤写错了」。
// ★★★ `limit` 超上界是**回落 50 而不是 clamp 到 200**（注释写的是 capped at 200）。
// ★★ 不碰的写端点：templates 的 PUT/PATCH/DELETE、POST templates、POST scan、
//   POST import、POST import-orbi，以及 scan-scheduler 那条以外的写面。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchTemplates,
  fetchPresets,
  fetchTasks,
  fetchTaskById,
  fetchTaskResults,
  fetchScanSchedulerStatus,
  fdTaskLimitEffective,
  taskLimitWasRewritten,
  resultsStatusParam,
  resultsStatusIsKnown,
  resultsStatusIsUnfiltered,
  resultsEmptyIsIndeterminate,
  templateCredentialIsIndeterminate,
  templateAutoDisabled,
  templateManuallyDisabled,
  templateHasScanFailures,
  templateHasLastScanFailureAt,
  createdAtIsUnreadable,
  taskTemplateDeleted,
  taskUpdatedAtIsUnreadable,
  taskIsInFlight,
  taskHasErrorMessage,
  resultFreeTypeUninferable,
  resultZeroFields,
  resultNotImported,
  resultSharesNoPool,
  schedulerProviderWasTypedNil,
  schedulerNeverSwept,
  schedulerLastErrorMissing,
  schedulerEnabledButNotStarted,
  schedulerIntervalUnset,
  fdDepsMissingMessage,
  scanSchedulerMissingMessage,
  tenantAdminWriteForbiddenMessage,
  invalidTaskIdMessage,
  fdTemplatesInternalErrorMessage,
  fdRequestFailedMessage,
  FD_TASK_LIMIT_DEFAULT,
  type FdTemplate,
  type FdPreset,
  type FdTask,
  type FdResult,
  type FdScanSchedulerStatus,
} from '@/api/freeDiscovery'

useHyperPage({ title: () => t('fd.title') })

// ── 模板 ───────────────────────────────────────────────────────────────
const enabledOnly = ref(false)
const templates = ref<FdTemplate[] | null>(null)
const templatesError = ref<string | null>(null)
const templatesLoading = ref(false)

// ── 预设 ───────────────────────────────────────────────────────────────
const presets = ref<FdPreset[] | null | undefined>(undefined)
const presetsError = ref<string | null>(null)
const presetsLoading = ref(false)

// ── 任务列表 ───────────────────────────────────────────────────────────
const rawLimit = ref('')
const tasks = ref<FdTask[] | null>(null)
const tasksError = ref<string | null>(null)
const tasksLoading = ref(false)

/** ★★ 后端实际生效的 limit（回落 50 / 改写 201 都在这里显出来）。 */
const effectiveLimit = computed(() => fdTaskLimitEffective(rawLimit.value.trim()))
const limitRewritten = computed(() => taskLimitWasRewritten(rawLimit.value.trim()))

// ── 任务详情 + 结果 ────────────────────────────────────────────────────
const taskId = ref('')
const taskDetail = ref<FdTask | null>(null)
const taskDetailError = ref<string | null>(null)
const taskDetailLoading = ref(false)

const rawStatus = ref('pending')
const results = ref<FdResult[] | null>(null)
const resultsError = ref<string | null>(null)
const resultsLoading = ref(false)

/** ★ 实际下发的过滤值（空 ⇒ 后端回落成 pending）。 */
const sentStatus = computed(() => resultsStatusParam(rawStatus.value.trim()))
const statusUnknown = computed(() => !resultsStatusIsKnown(sentStatus.value))
const statusUnfiltered = computed(() => resultsStatusIsUnfiltered(sentStatus.value))

// ── scheduler（superAdmin 档）──────────────────────────────────────────
const scheduler = ref<FdScanSchedulerStatus | null>(null)
const schedulerError = ref<string | null>(null)
const schedulerLoading = ref(false)

async function loadTemplates(): Promise<void> {
  templatesLoading.value = true
  templatesError.value = null
  templates.value = null
  try {
    const r = await fetchTemplates({ enabledOnly: enabledOnly.value })
    templates.value = r.templates
  } catch (e) {
    // ★★ 抛错不许退化成「没有模板」—— 那和服务没起来长得一样
    templates.value = null
    templatesError.value = (e as Error)?.message || t('common.error')
  } finally {
    templatesLoading.value = false
  }
}

async function loadPresets(): Promise<void> {
  presetsLoading.value = true
  presetsError.value = null
  presets.value = undefined
  try {
    const r = await fetchPresets()
    presets.value = r.presets
  } catch (e) {
    presets.value = undefined
    presetsError.value = (e as Error)?.message || t('common.error')
  } finally {
    presetsLoading.value = false
  }
}

async function loadTasks(): Promise<void> {
  tasksLoading.value = true
  tasksError.value = null
  tasks.value = null
  try {
    const raw = rawLimit.value.trim()
    const r = await fetchTasks(raw === '' ? undefined : { limit: raw })
    tasks.value = r.tasks
  } catch (e) {
    tasks.value = null
    tasksError.value = (e as Error)?.message || t('common.error')
  } finally {
    tasksLoading.value = false
  }
}

async function loadTaskDetail(): Promise<void> {
  const id = taskId.value.trim()
  if (!id) return
  taskDetailLoading.value = true
  taskDetailError.value = null
  taskDetail.value = null
  try {
    taskDetail.value = await fetchTaskById(id)
  } catch (e) {
    taskDetail.value = null
    taskDetailError.value = (e as Error)?.message || t('common.error')
  } finally {
    taskDetailLoading.value = false
  }
}

async function loadResults(): Promise<void> {
  const id = taskId.value.trim()
  if (!id) return
  resultsLoading.value = true
  resultsError.value = null
  results.value = null
  try {
    const r = await fetchTaskResults(id, { status: rawStatus.value.trim() })
    results.value = r.results
  } catch (e) {
    results.value = null
    resultsError.value = (e as Error)?.message || t('common.error')
  } finally {
    resultsLoading.value = false
  }
}

async function loadScheduler(): Promise<void> {
  schedulerLoading.value = true
  schedulerError.value = null
  scheduler.value = null
  try {
    scheduler.value = await fetchScanSchedulerStatus()
  } catch (e) {
    scheduler.value = null
    schedulerError.value = (e as Error)?.message || t('common.error')
  } finally {
    schedulerLoading.value = false
  }
}

onBeforeUnmount(() => {
  templates.value = null
  templatesError.value = null
  presets.value = undefined
  presetsError.value = null
  tasks.value = null
  tasksError.value = null
  taskDetail.value = null
  taskDetailError.value = null
  results.value = null
  resultsError.value = null
  scheduler.value = null
  schedulerError.value = null
})
</script>

<template>
  <div class="fd">
    <!-- ══ 面板 1：模板列表 ══ -->
    <section class="fd__panel">
      <span class="fd__panel-title">{{ t('fd.templatesTitle') }}</span>

      <p class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.enabledLiteralNote') }}</span>
      </p>
      <p class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.ciphertextHiddenNote') }}</span>
      </p>
      <p class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.tenantScopeNote') }}</span>
      </p>

      <label class="fd__check" for="fd-enabled">
        <input id="fd-enabled" v-model="enabledOnly" type="checkbox" />
        <span class="fd__check-t">{{ t('fd.enabledOnly') }}</span>
      </label>

      <button type="button" class="fd__btn" @click="loadTemplates">{{ t('fd.templatesRun') }}</button>
      <p v-if="templatesLoading" class="fd__msg">{{ t('common.loading') }}</p>

      <p v-if="templatesError" class="fd__msg fd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ templatesError }}</span>
      </p>
      <p v-if="templatesError && fdDepsMissingMessage(templatesError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.depsMissingNote') }}</span>
      </p>
      <p v-if="templatesError && fdTemplatesInternalErrorMessage(templatesError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.templatesInternalErrorNote') }}</span>
      </p>

      <template v-if="templates">
        <div class="fd__head">
          <span class="fd__sub">{{ t('fd.templatesCount', { n: templates.length }) }}</span>
        </div>
        <!-- ★ nil-guard 保证 templates 恒是数组，所以「空」就是真的空 -->
        <p v-if="templates.length === 0" class="fd__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('fd.templatesEmptyNote') }}</span>
        </p>

        <ul v-if="templates.length" class="fd__list">
          <li v-for="tl in templates" :key="tl.id" class="fd__item">
            <div class="fd__head">
              <span class="fd__title">{{ tl.provider_code }}</span>
              <span class="fd__badge">
                <StatusDot :tone="tl.enabled ? 'success' : 'warning'" />
                <span class="fd__badge-t">{{ tl.enabled ? t('common.yes') : t('common.no') }}</span>
              </span>
            </div>
            <p class="fd__meta">{{ tl.display_name }}</p>
            <div class="fd__grid">
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.apiType') }}</span>
                <span class="fd__cell-v">{{ tl.api_type }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.tosVerdict') }}</span>
                <span class="fd__cell-v">{{ tl.tos_verdict }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.apiKeyEnv') }}</span>
                <span class="fd__cell-v">{{ tl.api_key_env || '—' }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.scanFailures') }}</span>
                <span class="fd__cell-v">{{ tl.consecutive_scan_failures }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.baseUrl') }}</span>
                <span class="fd__cell-v">{{ tl.base_url }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.tosUrl') }}</span>
                <span class="fd__cell-v">{{ tl.tos_url || '—' }}</span>
              </span>
            </div>

            <!-- ★★ 两个 omitempty 键：缺失 ≠ null，分开说 -->
            <p v-if="templateCredentialIsIndeterminate(tl)" class="fd__note fd__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('fd.credentialIndeterminateNote') }}</span>
            </p>
            <p v-if="templateAutoDisabled(tl)" class="fd__note fd__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('fd.autoDisabledNote', { at: tl.auto_disabled_at || '—' }) }}</span>
            </p>
            <p v-if="templateManuallyDisabled(tl)" class="fd__note">
              <AppIcon name="key" :size="13" />
              <span>{{ t('fd.manuallyDisabledNote') }}</span>
            </p>
            <p v-if="templateHasScanFailures(tl) && !templateHasLastScanFailureAt(tl)" class="fd__note fd__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('fd.failureCountNoTimestampNote') }}</span>
            </p>
            <p v-if="createdAtIsUnreadable(tl.created_at)" class="fd__note fd__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('fd.createdAtZeroNote') }}</span>
            </p>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 面板 2：预设 ══ -->
    <section class="fd__panel">
      <span class="fd__panel-title">{{ t('fd.presetsTitle') }}</span>

      <p class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.presetsNullNote') }}</span>
      </p>
      <p class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.presetsNoTosUrlNote') }}</span>
      </p>

      <button type="button" class="fd__btn" @click="loadPresets">{{ t('fd.presetsRun') }}</button>
      <p v-if="presetsLoading" class="fd__msg">{{ t('common.loading') }}</p>

      <p v-if="presetsError" class="fd__msg fd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ presetsError }}</span>
      </p>
      <p v-if="presetsError && fdDepsMissingMessage(presetsError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.depsMissingNote') }}</span>
      </p>

      <template v-if="presets !== undefined">
        <!-- ★★ null 与 [] 是两种不同的「空」，分别渲染 -->
        <p v-if="presets === null" class="fd__note fd__note--warn" data-fd="presets-null">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.presetsNullResult') }}</span>
        </p>
        <template v-else>
          <div class="fd__head">
            <span class="fd__sub">{{ t('fd.presetsCount', { n: presets.length }) }}</span>
          </div>
          <p v-if="presets.length === 0" class="fd__note" data-fd="presets-empty">
            <AppIcon name="key" :size="13" />
            <span>{{ t('fd.presetsEmptyNote') }}</span>
          </p>
          <ul v-if="presets.length" class="fd__list">
            <li v-for="p in presets" :key="p.provider_code" class="fd__item">
              <div class="fd__head">
                <span class="fd__title">{{ p.provider_code }}</span>
                <span class="fd__badge">
                  <span class="fd__badge-t">{{ p.tos_verdict }}</span>
                </span>
              </div>
              <p class="fd__meta">{{ p.display_name }}</p>
              <div class="fd__grid">
                <span class="fd__cell">
                  <span class="fd__cell-l">{{ t('fd.baseUrl') }}</span>
                  <span class="fd__cell-v">{{ p.base_url }}</span>
                </span>
                <span class="fd__cell">
                  <span class="fd__cell-l">{{ t('fd.apiType') }}</span>
                  <span class="fd__cell-v">{{ p.api_type }}</span>
                </span>
              </div>
              <p class="fd__meta">{{ t('fd.tosNotes') }}：{{ p.tos_notes || '—' }}</p>
            </li>
          </ul>
        </template>
      </template>
    </section>

    <!-- ══ 面板 3：任务列表 ══ -->
    <section class="fd__panel">
      <span class="fd__panel-title">{{ t('fd.tasksTitle') }}</span>

      <!-- ★★★ 本批头号缺陷：列表里的 updated_at 是假值 -->
      <p class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.tasksUpdatedAtNote') }}</span>
      </p>

      <label class="fd__label" for="fd-limit">{{ t('fd.limitLabel') }}</label>
      <input
        id="fd-limit"
        v-model="rawLimit"
        class="fd__input"
        type="text"
        inputmode="numeric"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('fd.limitPlaceholder')"
      />
      <p class="fd__meta">{{ t('fd.limitEffectiveNote', { n: effectiveLimit }) }}</p>
      <p v-if="limitRewritten" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.limitRewrittenNote', { n: effectiveLimit, default: FD_TASK_LIMIT_DEFAULT }) }}</span>
      </p>

      <button type="button" class="fd__btn" @click="loadTasks">{{ t('fd.tasksRun') }}</button>
      <p v-if="tasksLoading" class="fd__msg">{{ t('common.loading') }}</p>

      <p v-if="tasksError" class="fd__msg fd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ tasksError }}</span>
      </p>
      <p v-if="tasksError && fdDepsMissingMessage(tasksError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.depsMissingNote') }}</span>
      </p>
      <p v-if="tasksError && fdRequestFailedMessage(tasksError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.requestFailedNote') }}</span>
      </p>

      <template v-if="tasks">
        <div class="fd__head">
          <span class="fd__sub">{{ t('fd.tasksCount', { n: tasks.length }) }}</span>
        </div>
        <p v-if="tasks.length === 0" class="fd__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('fd.tasksEmptyNote') }}</span>
        </p>

        <ul v-if="tasks.length" class="fd__list">
          <li v-for="tk in tasks" :key="tk.id" class="fd__item">
            <div class="fd__head">
              <span class="fd__title">#{{ tk.id }} · {{ tk.provider_code }}</span>
              <span class="fd__badge">
                <StatusDot :tone="tk.status === 'failed' ? 'danger' : taskIsInFlight(tk) ? 'warning' : 'success'" />
                <span class="fd__badge-t">{{ tk.status }}</span>
              </span>
            </div>
            <div class="fd__grid">
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.triggerType') }}</span>
                <span class="fd__cell-v">{{ tk.trigger_type }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.modelsFound') }}</span>
                <span class="fd__cell-v">{{ tk.models_found }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.modelsImported') }}</span>
                <span class="fd__cell-v">{{ tk.models_imported }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.templateRef') }}</span>
                <span class="fd__cell-v">
                  <template v-if="taskTemplateDeleted(tk)">{{ t('fd.templateDeleted') }}</template>
                  <template v-else>{{ tk.template_id }}</template>
                </span>
              </span>
            </div>
            <!-- ★★ 列表里的 updated_at 恒为 Go 零值 ⇒ 显式说「读不出来」而不是显示它 -->
            <p v-if="taskUpdatedAtIsUnreadable(tk)" class="fd__meta" data-fd="updated-at-unreadable">
              {{ t('fd.updatedAtUnreadable') }}
            </p>
            <p v-if="taskHasErrorMessage(tk)" class="fd__note fd__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('fd.taskErrorLine', { msg: tk.error_message }) }}</span>
            </p>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 面板 4：任务详情 + 结果 ══ -->
    <section class="fd__panel">
      <span class="fd__panel-title">{{ t('fd.taskDetailTitle') }}</span>

      <label class="fd__label" for="fd-task">{{ t('fd.taskIdLabel') }}</label>
      <input
        id="fd-task"
        v-model="taskId"
        class="fd__input"
        type="text"
        inputmode="numeric"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('fd.taskIdPlaceholder')"
      />

      <div class="fd__actions">
        <button type="button" class="fd__btn fd__btn--half" :disabled="!taskId.trim()" @click="loadTaskDetail">
          {{ t('fd.taskDetailRun') }}
        </button>
        <button type="button" class="fd__btn fd__btn--half" :disabled="!taskId.trim()" @click="loadResults">
          {{ t('fd.resultsRun') }}
        </button>
      </div>
      <p v-if="taskDetailLoading || resultsLoading" class="fd__msg">{{ t('common.loading') }}</p>

      <!-- ★ 结果过滤：未知取值后端不拒 ⇒ 请求前就提示 -->
      <label class="fd__label" for="fd-status">{{ t('fd.resultsStatusLabel') }}</label>
      <input
        id="fd-status"
        v-model="rawStatus"
        class="fd__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('fd.resultsStatusPlaceholder')"
      />

      <!-- 详情 -->
      <p v-if="taskDetailError" class="fd__msg fd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ taskDetailError }}</span>
      </p>
      <!-- ★ id 非数字是 400 不是 404 -->
      <p v-if="taskDetailError && invalidTaskIdMessage(taskDetailError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.invalidTaskIdNote') }}</span>
      </p>

      <template v-if="taskDetail">
        <div class="fd__head">
          <span class="fd__sub">{{ t('fd.taskDetailSub', { id: taskDetail.id }) }}</span>
          <span class="fd__badge">
            <StatusDot :tone="taskDetail.status === 'failed' ? 'danger' : 'success'" />
            <span class="fd__badge-t">{{ taskDetail.status }}</span>
          </span>
        </div>
        <div class="fd__grid">
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.templateRef') }}</span>
            <span class="fd__cell-v">
              <template v-if="taskTemplateDeleted(taskDetail)">{{ t('fd.templateDeleted') }}</template>
              <template v-else>{{ taskDetail.template_id }}</template>
            </span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.triggeredBy') }}</span>
            <span class="fd__cell-v">{{ taskDetail.triggered_by || '—' }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.startedAt') }}</span>
            <span class="fd__cell-v">{{ taskDetail.started_at || '—' }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.completedAt') }}</span>
            <span class="fd__cell-v">{{ taskDetail.completed_at || '—' }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.updatedAt') }}</span>
            <span class="fd__cell-v">
              <!-- ★ 详情端点是真值，与列表不同 -->
              <template v-if="taskUpdatedAtIsUnreadable(taskDetail)">{{ t('fd.updatedAtZero') }}</template>
              <template v-else>{{ taskDetail.updated_at }}</template>
            </span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.createdAt') }}</span>
            <span class="fd__cell-v">{{ taskDetail.created_at || '—' }}</span>
          </span>
        </div>
        <p v-if="taskHasErrorMessage(taskDetail)" class="fd__note fd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.taskErrorLine', { msg: taskDetail.error_message }) }}</span>
        </p>
      </template>

      <!-- 结果 -->
      <div class="fd__head">
        <span class="fd__sub">{{ t('fd.resultsTitle') }}</span>
      </div>
      <p class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.resultsStatusNote', { sent: sentStatus }) }}</span>
      </p>
      <!-- ★ 未知 status 不会被拒 ⇒ 空结果可能是过滤写错了 -->
      <p v-if="statusUnknown" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.statusUnknownNote', { sent: sentStatus }) }}</span>
      </p>
      <p v-if="statusUnfiltered" class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.statusUnfilteredNote') }}</span>
      </p>

      <p v-if="resultsError" class="fd__msg fd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ resultsError }}</span>
      </p>
      <p v-if="resultsError && invalidTaskIdMessage(resultsError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.invalidTaskIdNote') }}</span>
      </p>

      <template v-if="results">
        <div class="fd__head">
          <span class="fd__sub">{{ t('fd.resultsCount', { n: results.length }) }}</span>
        </div>
        <p v-if="resultsEmptyIsIndeterminate({ results })" class="fd__note fd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.resultsEmptyNote') }}</span>
        </p>

        <ul v-if="results.length" class="fd__list">
          <li v-for="r in results" :key="r.id" class="fd__item">
            <div class="fd__head">
              <span class="fd__title">{{ r.model_id }}</span>
              <span class="fd__badge">
                <StatusDot :tone="r.import_status === 'imported' ? 'success' : r.import_status === 'conflict' ? 'danger' : 'warning'" />
                <span class="fd__badge-t">{{ r.import_status }}</span>
              </span>
            </div>
            <p class="fd__meta">{{ r.display_name }}</p>
            <div class="fd__grid">
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.contextWindow') }}</span>
                <span class="fd__cell-v">{{ r.context_window }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.maxTokens') }}</span>
                <span class="fd__cell-v">{{ r.max_tokens }}</span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.freeType') }}</span>
                <span class="fd__cell-v">
                  <!-- ★ '' 唯一对应 SQL NULL ⇒ 这里可以确定地说「推断不出」 -->
                  <template v-if="resultFreeTypeUninferable(r)">{{ t('fd.freeTypeUnknown') }}</template>
                  <template v-else>{{ r.free_type }}</template>
                </span>
              </span>
              <span class="fd__cell">
                <span class="fd__cell-l">{{ t('fd.poolKey') }}</span>
                <span class="fd__cell-v">{{ r.pool_key || '—' }}</span>
              </span>
            </div>
            <p v-if="resultZeroFields(r).length" class="fd__note fd__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('fd.zeroAmbiguousNote', { fields: resultZeroFields(r).join(' / ') }) }}</span>
            </p>
            <p v-if="resultNotImported(r)" class="fd__note">
              <AppIcon name="key" :size="13" />
              <span>{{ t('fd.notImportedNote') }}</span>
            </p>
            <p v-if="resultSharesNoPool(r)" class="fd__meta">{{ t('fd.noPoolNote') }}</p>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 面板 5：scan-scheduler（superAdmin 档）══════════════════════ -->
    <section class="fd__panel">
      <span class="fd__panel-title">{{ t('fd.schedulerTitle') }}</span>

      <p class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.schedulerTierNote') }}</span>
      </p>
      <p class="fd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('fd.schedulerSkipsDepsNote') }}</span>
      </p>

      <button type="button" class="fd__btn" @click="loadScheduler">{{ t('fd.schedulerRun') }}</button>
      <p v-if="schedulerLoading" class="fd__msg">{{ t('common.loading') }}</p>

      <p v-if="schedulerError" class="fd__msg fd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ schedulerError }}</span>
      </p>
      <!-- ★ 403：这条是 superAdmin 档，抽屉席本身却是 admin 档 -->
      <p v-if="schedulerError && tenantAdminWriteForbiddenMessage(schedulerError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.schedulerForbiddenNote') }}</span>
      </p>
      <p v-if="schedulerError && scanSchedulerMissingMessage(schedulerError)" class="fd__note fd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('fd.schedulerMissingNote') }}</span>
      </p>

      <template v-if="scheduler">
        <div class="fd__grid">
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedEnabled') }}</span>
            <span class="fd__cell-v">
              <template v-if="scheduler.enabled">{{ t('common.yes') }}</template>
              <template v-else>{{ t('common.no') }}</template>
            </span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedStarted') }}</span>
            <span class="fd__cell-v">
              <template v-if="scheduler.started">{{ t('common.yes') }}</template>
              <template v-else>{{ t('common.no') }}</template>
            </span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedInterval') }}</span>
            <span class="fd__cell-v">{{ scheduler.interval || '—' }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedLastSweep') }}</span>
            <span class="fd__cell-v">
              <template v-if="schedulerNeverSwept(scheduler)">{{ t('fd.neverSwept') }}</template>
              <template v-else>{{ scheduler.last_sweep_at }}</template>
            </span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedSweeps') }}</span>
            <span class="fd__cell-v">{{ scheduler.sweeps_total }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedScans') }}</span>
            <span class="fd__cell-v">{{ scheduler.scans_total }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedScansFailed') }}</span>
            <span class="fd__cell-v">{{ scheduler.scans_failed }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedScansSkipped') }}</span>
            <span class="fd__cell-v">{{ scheduler.scans_skipped }}</span>
          </span>
          <span class="fd__cell">
            <span class="fd__cell-l">{{ t('fd.schedCyclesFailed') }}</span>
            <span class="fd__cell-v">{{ scheduler.cycles_failed }}</span>
          </span>
        </div>
        <!-- ★★ typed-nil 分支：200 + 全零 + interval 空串，与 503 是两种表示 -->
        <p v-if="schedulerProviderWasTypedNil(scheduler)" class="fd__note fd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.schedulerTypedNilNote') }}</span>
        </p>
        <p v-if="schedulerIntervalUnset(scheduler) && !schedulerProviderWasTypedNil(scheduler)" class="fd__note fd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.schedulerIntervalUnsetNote') }}</span>
        </p>
        <p v-if="schedulerEnabledButNotStarted(scheduler)" class="fd__note fd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.schedulerEnabledNotStartedNote') }}</span>
        </p>
        <p v-if="schedulerNeverSwept(scheduler) && !schedulerProviderWasTypedNil(scheduler)" class="fd__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('fd.schedulerNeverSweptNote') }}</span>
        </p>
        <p v-if="!schedulerLastErrorMissing(scheduler)" class="fd__note fd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('fd.schedulerLastErrorLine', { msg: scheduler.last_error || '—' }) }}</span>
        </p>
      </template>
    </section>

    <p class="fd__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('fd.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.fd__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.fd__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.fd__sub { display: block; font-size: 13px; font-weight: 600; margin: 10px 0 4px; }
.fd__head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.fd__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }
.fd__msg { font-size: 13px; color: var(--text-2, #666); padding: 8px 0; }
.fd__msg--err { color: var(--danger, #c0392b); }
.fd__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--text-2, #666); margin: 6px 0; }
.fd__note--warn { color: var(--app-warning); }
.fd__meta { font-size: 12px; color: var(--text-2, #666); margin: 4px 0 0; word-break: break-all; }
.fd__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.fd__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; flex-shrink: 0; }
.fd__badge-t { color: var(--text-2, #666); word-break: break-all; }
.fd__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.fd__cell { display: flex; flex-direction: column; }
.fd__cell-l { font-size: 11px; color: var(--app-text-muted); }
.fd__cell-v { font-size: 14px; font-weight: 600; word-break: break-all; }
.fd__list { list-style: none; margin: 0; padding: 0; }
.fd__item { padding: 10px 0; border-top: 1px solid var(--border, #eee); }
.fd__actions { display: flex; gap: 8px; }
.fd__btn--half { flex: 1; margin-top: 8px; }
.fd__check { display: flex; align-items: center; gap: 8px; min-height: 48px; font-size: 14px; }
.fd__check input { width: 22px; height: 22px; }
.fd__check-t { font-size: 14px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.fd__input { width: 100%; min-height: 48px; padding: 8px 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }
.fd__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--primary, #1976d2); background: transparent; color: var(--primary, #1976d2); }
.fd__btn[disabled] { opacity: .5; }
</style>
