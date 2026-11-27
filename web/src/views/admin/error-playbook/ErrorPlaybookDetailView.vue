<template>
  <div class="error-playbook-view">
    <PageHeader :title="entry?.title || t('errorPlaybook.title')" :subtitle="t('errorPlaybook.nav.detail')">
      <template #actions>
        <button class="ep-btn" type="button" @click="goBack">
          {{ t('errorPlaybook.action.back') }}
        </button>
        <button class="ep-btn" type="button" :disabled="detailLoading" @click="load">
          {{ t('errorPlaybook.action.refresh') }}
        </button>
      </template>
    </PageHeader>

    <SaStateBanner
      v-if="detailError"
      kind="error"
      :title="t('errorPlaybook.error.detailFailed')"
      :reason="detailError"
      :action-label="t('errorPlaybook.action.retry')"
      @action="load"
    />

    <template v-if="entry">
      <!-- 条目头：身份 + 徽标 -->
      <section class="ep-section">
        <div class="ep-badges">
          <span class="ep-chip ep-chip--kind">{{ t(`errorPlaybook.kind.${entry.plan_kind}`) }}</span>
          <span class="ep-chip">{{ t(`errorPlaybook.source.${entry.source}`) }}</span>
          <span class="ep-chip" :class="entry.enabled ? 'ep-chip--succeeded' : ''">
            {{ entry.enabled ? t('errorPlaybook.filter.enabledOnly') : t('errorPlaybook.filter.disabledOnly') }}
          </span>
          <span class="ep-chip" :class="entry.auto_execute ? 'ep-chip--awaiting_approval' : ''">
            {{ t('errorPlaybook.col.autoExecute') }}: {{ entry.auto_execute ? '✓' : '—' }}
          </span>
          <span class="ep-chip">{{ t('errorPlaybook.col.confidence') }}: {{ entry.confidence.toFixed(2) }}</span>
        </div>
        <dl class="ep-meta">
          <div class="ep-meta__row">
            <dt>{{ t('errorPlaybook.col.errorType') }}</dt>
            <dd class="ep-mono">{{ entry.error_type }}</dd>
          </div>
          <div class="ep-meta__row" v-if="entry.match_pattern">
            <dt>{{ t('errorPlaybook.col.matchPattern') }}</dt>
            <dd class="ep-mono">{{ entry.match_pattern }}</dd>
          </div>
          <div class="ep-meta__row" v-if="entry.description">
            <dt>{{ t('errorPlaybook.form.description') }}</dt>
            <dd class="ep-pre">{{ entry.description }}</dd>
          </div>
          <div class="ep-meta__row" v-if="entry.root_cause">
            <dt>{{ t('errorPlaybook.form.rootCause') }}</dt>
            <dd class="ep-pre">{{ entry.root_cause }}</dd>
          </div>
          <div class="ep-meta__row">
            <dt>{{ t('errorPlaybook.col.hits') }}</dt>
            <dd>{{ fmt(entry.hit_count) }} · {{ t('errorPlaybook.col.successRate') }}: {{ fmt(entry.success_count) }} / {{ fmt(entry.failure_count) }}</dd>
          </div>
          <div class="ep-meta__row">
            <dt>{{ t('errorPlaybook.col.lastExecutedAt') }}</dt>
            <dd>
              {{ formatTime(entry.last_executed_at) }}
              <span v-if="entry.last_status" class="ep-chip" :class="statusClass(entry.last_status)">
                {{ statusLabel(entry.last_status) }}
              </span>
            </dd>
          </div>
        </dl>

        <div class="ep-actions">
          <button v-if="superAdmin" class="ep-btn ep-btn--primary" type="button" @click="openExecute">
            {{ t('errorPlaybook.action.execute') }}
          </button>
          <label class="ep-radio">
            <input
              type="checkbox"
              :checked="entry.enabled"
              @change="saveFlag('enabled', ($event.target as HTMLInputElement).checked)"
            />
            <span>{{ t('errorPlaybook.form.entryEnabled') }}</span>
          </label>
          <label class="ep-radio">
            <input
              type="checkbox"
              :checked="entry.auto_execute"
              @change="saveFlag('auto_execute', ($event.target as HTMLInputElement).checked)"
            />
            <span>{{ t('errorPlaybook.form.entryAutoExecute') }}</span>
          </label>
          <button v-if="superAdmin" class="ep-btn ep-btn--danger" type="button" @click="deleteOpen = true">
            {{ t('errorPlaybook.action.delete') }}
          </button>
        </div>
      </section>

      <!-- 方案展示 + 编辑（三形态编辑器；保存时文本字段随方案成对提交） -->
      <section class="ep-section">
        <h3 class="ep-section__title">{{ t('errorPlaybook.form.plan') }}</h3>
        <SaStateBanner
          v-if="saveError"
          kind="error"
          :title="t('errorPlaybook.banner.validationTitle')"
          :reason="saveError"
        />
        <SaStateBanner
          v-if="saveOk"
          kind="info"
          :title="t('errorPlaybook.banner.saveOkTitle')"
        />
        <PlanEditor :key="planEditorKey" :initial-kind="entry.plan_kind" :initial-plan="entry.plan" />
        <div class="ep-actions">
          <button v-if="superAdmin" class="ep-btn ep-btn--primary" type="button" :disabled="saving" @click="savePlan">
            {{ t('errorPlaybook.action.savePlan') }}
          </button>
        </div>
        <div class="ep-field ep-plan-texts">
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.title') }}</span>
            <input v-model="draft.title" class="ep-input" type="text" />
          </label>
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.description') }}</span>
            <textarea v-model="draft.description" class="ep-input ep-input--area" rows="2" />
          </label>
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.rootCause') }}</span>
            <textarea v-model="draft.rootCause" class="ep-input ep-input--area" rows="2" />
          </label>
        </div>
      </section>

      <!-- 执行留痕时间线 -->
      <section class="ep-section">
        <h3 class="ep-section__title">{{ t('errorPlaybook.exec.timeline') }}</h3>
        <SaStateBanner
          v-if="execError"
          kind="error"
          :title="t('errorPlaybook.error.execListFailed')"
          :reason="execError"
          :action-label="t('errorPlaybook.action.retry')"
          @action="loadExecutions"
        />
        <SaStateBanner
          v-if="revertError"
          kind="error"
          :title="t('errorPlaybook.error.revertFailed')"
          :reason="revertError"
        />
        <SaStateBanner
          v-if="lastExecuteError"
          kind="error"
          :title="t('errorPlaybook.error.executeFailed')"
          :reason="lastExecuteError"
        />
        <div v-if="!execLoading && executions.length === 0" class="ep-empty">
          {{ t('errorPlaybook.empty.noResultExecutions') }}
        </div>
        <ol class="ep-timeline">
          <li v-for="x in executions" :key="x.id" class="ep-timeline__item">
            <div class="ep-timeline__head">
              <span class="ep-chip" :class="statusClass(x.status)">{{ statusLabel(x.status) }}</span>
              <span class="ep-chip">{{ t(`errorPlaybook.trigger.${x.trigger_source}`) }}</span>
              <span class="ep-timeline__time">{{ formatTime(x.created_at) }}</span>
              <span class="ep-timeline__duration">{{ t('errorPlaybook.col.duration') }}: {{ x.duration_ms }}ms</span>
              <button v-if="superAdmin && (x.status === 'succeeded' || x.status === 'failed')"
                class="ep-btn ep-btn--ghost"
                type="button"
                :disabled="revertingId === x.id"
                @click="revert(x)"
              >
                {{ t('errorPlaybook.action.revert') }}
              </button>
            </div>
            <p v-if="x.error_message" class="ep-timeline__error">{{ x.error_message }}</p>
            <ul v-if="x.steps && x.steps.length" class="ep-steps-log">
              <li v-for="s in x.steps" :key="s.step" class="ep-steps-log__item">
                <span class="ep-chip" :class="stepClass(s.status)">{{ stepLabel(s.status) }}</span>
                <code class="ep-mono">{{ s.action }}</code>
                <span v-if="s.result" class="ep-steps-log__result">{{ s.result }}</span>
                <span
                  v-if="s.snapshot"
                  class="ep-chip ep-chip--snapshot"
                  :title="JSON.stringify(s.snapshot)"
                >
                  {{ t('errorPlaybook.exec.hasSnapshot') }}
                </span>
                <span v-if="s.error" class="ep-field-error">{{ s.error }}</span>
              </li>
            </ul>
          </li>
        </ol>
      </section>
    </template>

    <!-- 手动执行对话框 -->
    <div v-if="executeOpen" class="ep-modal-backdrop" @click.self="executeOpen = false">
      <div class="ep-modal" role="dialog" :aria-label="t('errorPlaybook.action.execute')">
        <h3 class="ep-modal__title">{{ t('errorPlaybook.action.execute') }}</h3>
        <SaStateBanner
          v-if="executeError"
          kind="error"
          :title="t('errorPlaybook.banner.validationTitle')"
          :reason="executeError"
        />
        <div class="ep-modal__body">
          <label class="ep-field">
            <span class="ep-field__label">{{ t('errorPlaybook.form.message') }}</span>
            <textarea v-model="executeMessage" class="ep-input ep-input--area" rows="3" />
          </label>
        </div>
        <div class="ep-modal__actions">
          <button class="ep-btn" type="button" @click="executeOpen = false">
            {{ t('errorPlaybook.action.cancel') }}
          </button>
          <button
            class="ep-btn ep-btn--primary"
            type="button"
            :disabled="executing"
            @click="submitExecute"
          >
            {{ t('errorPlaybook.action.execute') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 删除确认 -->
    <div v-if="deleteOpen" class="ep-modal-backdrop" @click.self="deleteOpen = false">
      <div class="ep-modal" role="dialog" :aria-label="t('errorPlaybook.action.delete')">
        <h3 class="ep-modal__title">{{ t('errorPlaybook.action.confirmDelete') }}</h3>
        <p class="ep-modal__body">{{ t('errorPlaybook.form.confirmDeleteText', { type: entry?.error_type }) }}</p>
        <SaStateBanner v-if="deleteError" kind="error" :title="t('errorPlaybook.error.deleteFailed')" :reason="deleteError" />
        <div class="ep-modal__actions">
          <button class="ep-btn" type="button" @click="deleteOpen = false">
            {{ t('errorPlaybook.action.cancel') }}
          </button>
          <button class="ep-btn ep-btn--danger" type="button" :disabled="deleting" @click="submitDelete">
            {{ t('errorPlaybook.action.confirmDelete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// ErrorPlaybookDetailView.vue — 条目详情页（2026-11-21）
//
// 三件事：
//   ① 方案编辑（PlanEditor 三形态）+ 文本字段同表单保存 —— 后端契约：
//      PUT 带 plan 时 title/description/root_cause 缺省会**清空**，所以三者
//      必须与方案成对提交；只拨开关的保存不带 plan（走 SetEntryFlags 分支）。
//   ② 执行留痕时间线：每步带旧值快照徽标；succeeded/failed 的执行可
//      「按快照回滚」（revert 也可能业务性 422 —— 回滚失败原文透出）。
//   ③ 手动执行（管理员显式触发 ⇒ 不看条目 auto_execute，但快照/留痕全量生效）。

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isSuperAdmin } from '../../../store'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import SaStateBanner from '../session-analytics/components/SaStateBanner.vue'
import PlanEditor from './components/PlanEditor.vue'
import { useErrorPlaybookLoader } from './useErrorPlaybookLoader'
import {
  getEntry,
  listExecutions,
  updateEntry,
  deleteEntry,
  executeEntry,
  revertExecution,
  type PlaybookEntry,
  type PlaybookExecution,
  type EntryWriteBody,
} from '../../../api/errorPlaybook'

const { t } = useI18n()
// ★ 2026-11-27 非管理员菜单轮：读面（entries/executions/stats GET）对所有
// 登录用户开放；写动作（PUT/DELETE/execute/analyze/revert）后端已过
// RequireSuperAdminForWrite ⇒ 页面写按钮对非 super 隐藏。
const superAdmin = isSuperAdmin()
const route = useRoute()
const router = useRouter()
const { loading: detailLoading, error: detailError, run } = useErrorPlaybookLoader()

const entry = ref<PlaybookEntry | null>(null)
const executions = ref<PlaybookExecution[]>([])
const execLoading = ref(false)
const execError = ref('')

const entryId = computed(() => Number(route.params.id))

const planEditorKey = ref(0)
const draft = ref({ title: '', description: '', rootCause: '' })
const saving = ref(false)
const saveError = ref('')
const saveOk = ref(false)

const executeOpen = ref(false)
const executing = ref(false)
const executeError = ref('')
const executeMessage = ref('')

const deleteOpen = ref(false)
const deleting = ref(false)
const deleteError = ref('')

const revertingId = ref<number | null>(null)

const revertError = ref('')

/** 最近一次手动执行的业务性失败（步骤失败 ⇒ 留痕已落库，如实带状态回）。 */
const lastExecuteError = ref('')

const planEditorRef = ref<InstanceType<typeof PlanEditor> | null>(null)

function fmt(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return Number(v).toLocaleString()
}

function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

function statusLabel(status: string): string {
  const known = ['succeeded', 'failed', 'skipped', 'awaiting_approval', 'no_action']
  return known.includes(status) ? t(`errorPlaybook.status.${status}`) : status
}

function statusClass(status: string): string {
  return `ep-chip--${status}`
}

function stepLabel(status: string): string {
  if (status === 'ok') return 'OK' // 技术状态词：内联不占 i18n 键（tok 同款纪律）
  if (status === 'skipped') return t('errorPlaybook.status.skipped')
  return t('errorPlaybook.status.failed')
}

function stepClass(status: string): string {
  if (status === 'ok') return 'ep-chip--succeeded'
  if (status === 'skipped') return ''
  return 'ep-chip--failed'
}

async function load(): Promise<void> {
  const e = await run(
    () => getEntry(entryId.value),
    'errorPlaybook.error.detailFailed',
  )
  if (!e) return
  entry.value = e
  draft.value = {
    title: e.title,
    description: e.description,
    rootCause: e.root_cause,
  }
  await loadExecutions()
}

async function loadExecutions(): Promise<void> {
  execLoading.value = true
  execError.value = ''
  try {
    const res = await listExecutions({ entry_id: entryId.value, limit: 50 })
    executions.value = res.executions ?? []
  } catch (err) {
    execError.value = err instanceof Error ? err.message : String(err)
  } finally {
    execLoading.value = false
  }
}

/** 只拨开关：不带 plan（后端走 SetEntryFlags 分支，不会碰方案与文本字段）。 */
async function saveFlag(field: 'enabled' | 'auto_execute', value: boolean): Promise<void> {
  if (!entry.value) return
  saveError.value = ''
  saveOk.value = false
  try {
    const body: EntryWriteBody = {}
    body[field] = value
    entry.value = await updateEntry(entry.value.id, body)
    saveOk.value = true
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
    await load()
  }
}

/** 方案 + 文本字段成对保存（见文件头契约注释）。 */
async function savePlan(): Promise<void> {
  if (!entry.value) return
  saveError.value = ''
  saveOk.value = false
  const plan = planEditorRef.value?.getPayload()
  if (!plan) return
  if (!plan.ok) {
    saveError.value = plan.error
    return
  }
  saving.value = true
  try {
    entry.value = await updateEntry(entry.value.id, {
      plan_kind: plan.payload.plan_kind,
      plan: plan.payload.plan,
      title: draft.value.title.trim(),
      description: draft.value.description.trim(),
      root_cause: draft.value.rootCause.trim(),
    })
    saveOk.value = true
    planEditorKey.value++ // 重挂编辑器，吃服务端归一化后的形状
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

function openExecute(): void {
  executeError.value = ''
  executeMessage.value = ''
  executeOpen.value = true
}

async function submitExecute(): Promise<void> {
  if (!entry.value) return
  executing.value = true
  executeError.value = ''
  lastExecuteError.value = ''
  try {
    const res = await executeEntry(entry.value.id, executeMessage.value.trim() || undefined)
    executeOpen.value = false
    if (res.error) {
      // 执行留痕已落库但步骤失败：如实告知（422/500 + execution 已返回）
      lastExecuteError.value = res.error
    }
    await load()
  } catch (e) {
    executeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    executing.value = false
  }
}

async function revert(x: PlaybookExecution): Promise<void> {
  revertingId.value = x.id
  revertError.value = ''
  try {
    await revertExecution(x.id)
    await loadExecutions()
  } catch (e) {
    revertError.value = e instanceof Error ? e.message : String(e)
  } finally {
    revertingId.value = null
  }
}

async function submitDelete(): Promise<void> {
  if (!entry.value) return
  deleting.value = true
  deleteError.value = ''
  try {
    await deleteEntry(entry.value.id)
    deleteOpen.value = false
    goBack()
  } catch (e) {
    deleteError.value = e instanceof Error ? e.message : String(e)
  } finally {
    deleting.value = false
  }
}

function goBack(): void {
  void router.push({ name: 'error-playbook-list' })
}

onMounted(load)
</script>

<style scoped>
.error-playbook-view {
  padding: 20px;
}

.ep-section {
  border: 1px solid var(--border);
  border-radius: var(--radius-card, 10px);
  padding: 14px;
  margin-bottom: 14px;
  background: var(--surface);
}

.ep-section__title {
  margin: 0 0 10px;
  font-size: 14px;
}

.ep-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 10px;
}

.ep-meta {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.ep-meta__row {
  display: flex;
  gap: 12px;
}

.ep-meta__row dt {
  min-width: 120px;
  color: var(--muted);
  font-size: 13px;
}

.ep-meta__row dd {
  margin: 0;
  color: var(--text);
  font-size: 13px;
  white-space: pre-wrap;
  word-break: break-all;
}

.ep-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 12px;
}

.ep-btn {
  padding: 6px 14px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}

.ep-btn--primary {
  border-color: var(--accent);
  background: var(--accent);
  color: var(--surface);
}

.ep-btn--danger {
  border-color: var(--danger, #c0392b);
  color: var(--danger, #c0392b);
}

.ep-btn--ghost {
  border-color: transparent;
  background: none;
  color: var(--accent);
}

.ep-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.ep-radio {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}

.ep-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ep-field__label {
  color: var(--muted);
  font-size: 12px;
}

.ep-field-error {
  margin: 0;
  color: var(--danger, #c0392b);
  font-size: 12px;
}

.ep-input {
  padding: 6px 10px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
}

.ep-input--area {
  resize: vertical;
}

.ep-plan-texts {
  margin-top: 12px;
  gap: 10px;
}

.ep-table-empty {
  color: var(--muted);
}

.ep-mono {
  font-family: ui-monospace, monospace;
  font-size: 12px;
}

.ep-pre {
  white-space: pre-wrap;
}

.ep-chip {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  border: 1px solid var(--border);
  color: var(--muted);
}

.ep-chip--kind {
  border-color: var(--accent);
  color: var(--accent);
}

.ep-chip--succeeded {
  border-color: var(--success, #2e7d32);
  color: var(--success, #2e7d32);
}

.ep-chip--failed {
  border-color: var(--danger, #c0392b);
  color: var(--danger, #c0392b);
}

.ep-chip--awaiting_approval {
  border-color: var(--warning, #b8860b);
  color: var(--warning, #b8860b);
}

.ep-chip--snapshot {
  border-style: dashed;
}

.ep-timeline {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ep-timeline__item {
  border: 1px solid var(--border);
  border-radius: var(--radius-card, 8px);
  padding: 10px;
}

.ep-timeline__head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.ep-timeline__time {
  color: var(--muted);
  font-size: 12px;
}

.ep-timeline__duration {
  color: var(--muted);
  font-size: 12px;
}

.ep-timeline__error {
  margin: 8px 0 0;
  color: var(--danger, #c0392b);
  font-size: 12px;
  white-space: pre-wrap;
}

.ep-steps-log {
  list-style: none;
  margin: 8px 0 0;
  padding: 8px 0 0;
  border-top: 1px dashed var(--border);
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.ep-steps-log__item {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 12px;
}

.ep-steps-log__result {
  color: var(--muted);
}

.ep-empty {
  color: var(--muted);
  font-size: 13px;
  padding: 8px 0;
}

.ep-modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 40px 16px;
  overflow-y: auto;
  z-index: 100;
}

.ep-modal {
  width: 640px;
  max-width: 100%;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-card, 10px);
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ep-modal__title {
  margin: 0;
  font-size: 15px;
}

.ep-modal__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ep-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 768px) {
  .error-playbook-view {
    padding: 12px;
  }
}
</style>
