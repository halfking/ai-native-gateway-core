<script setup lang="ts">
// IntegrityView — 模型完整性异常（superAdmin 档）。
//
// 这个页面与其他列表页有一处**根本差异**，别照抄它们：
//   节点/模型/供应商页是「一次拉全量 + 客户端切片」（数据量级 ~百，单端点返回全网）；
//   本页是**真·服务端分页**（COUNT(*) 给总数，limit/offset 取一页，默认 100 上限 500）。
// ⇒ 用 HyperList 的分页模式，fetchPage 直接把 page 映射成 offset，
//    缓存只用来做 rowId 去重与「已加载 X / 总计 Y」，**不能**当全量数据源。
import { computed, ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import {
  fetchModelIntegrityEvents,
  resolveModelIntegrityEvent,
  severityTone,
  type ModelIntegrityRecord,
} from '@/api/modelIntegrity'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { relativeTime } from '@/utils/format'

useHyperPage({ title: () => t('integrity.title') })
const auth = useAuthStore()

/** 后端整段 superAdmin（handler.go:924-925）⇒ 非 super_admin 不该进来。 */
const isSuperAdmin = computed(() => auth.role === 'super_admin')

const PAGE_SIZE = 20
/** 默认只看未处置的：运维默认关心「还没处理的」。 */
const unresolvedOnly = ref(true)
const total = ref(0)
const loadedIds = new Set<number>()

const controller = new ContinuousListController<ModelIntegrityRecord>({
  fetchPage: async (page) => {
    const resp = await fetchModelIntegrityEvents({
      limit: PAGE_SIZE,
      offset: (page - 1) * PAGE_SIZE,
      unresolved_only: unresolvedOnly.value,
    })
    // ★ resp.count 是**命中总数**不是本页条数（后端 COUNT(*)，:140/:202）。
    //   写成 resp.events.length 会让「已加载 X / 总计 Y」永远显示同一数。
    total.value = resp.count
    for (const e of resp.events) loadedIds.add(e.id)
    return { items: resp.events, total: resp.count }
  },
  stableKey: (e) => `integrity-${e.id}`,
  scopeKey: 'integrity',
})

async function toggleUnresolved(): Promise<void> {
  unresolvedOnly.value = !unresolvedOnly.value
  loadedIds.clear()
  controller.loadFirst('requery')
}

const detail = ref<ModelIntegrityRecord | null>(null)
const detailOpenProxy = computed({
  get: () => detail.value != null,
  set: (v: boolean) => {
    if (!v) detail.value = null
  },
})

// ---- 处置 ----
const notes = ref('')
const resolving = ref(false)
const resolveError = ref<string | null>(null)
const resolveOk = ref(false)

async function doResolve(): Promise<void> {
  const rec = detail.value
  if (!rec || resolving.value) return
  resolving.value = true
  resolveError.value = null
  resolveOk.value = false
  try {
    await resolveModelIntegrityEvent(rec.id, notes.value.trim())
    resolveOk.value = true
    notes.value = ''
    // 就地标记已处置，避免用户以为没生效而重复点
    rec.resolved = true
    loadedIds.delete(rec.id)
    // 从当前列表移除：默认视角就是「未处置」，留着会自相矛盾
    if (unresolvedOnly.value) detail.value = null
    controller.loadFirst('requery')
  } catch (err) {
    const status = (err as { status?: number })?.status
    resolveError.value = status === 403 ? t('integrity.errForbidden') : (err instanceof Error ? err.message : String(err))
  } finally {
    resolving.value = false
  }
}

function severityLabel(s: string): string {
  switch (s) {
    case 'critical':
      return t('integrity.sevCritical')
    case 'high':
      return t('integrity.sevHigh')
    case 'medium':
      return t('integrity.sevMedium')
    default:
      return t('integrity.sevLow')
  }
}
</script>

<template>
  <div class="view-root integrity">
    <p v-if="!isSuperAdmin" class="integrity__denied">{{ t('integrity.superAdminOnly') }}</p>

    <HyperList
      v-else
      :controller="controller"
      :item-key="(e: ModelIntegrityRecord) => `integrity-${e.id}`"
      :on-refresh="async () => { controller.loadFirst('requery') }"
      :empty-hint="unresolvedOnly ? t('integrity.emptyUnresolved') : t('integrity.emptyAll')"
    >
      <template #header>
        <div class="integrity__bar">
          <span class="integrity__total">{{ t('integrity.total', { n: total }) }}</span>
          <button type="button" class="btn btn--sm" @click="toggleUnresolved">
            <AppIcon name="check" :size="16" />
            {{ unresolvedOnly ? t('integrity.showAll') : t('integrity.showUnresolved') }}
          </button>
        </div>
      </template>

      <template #item="{ item: e }">
        <button type="button" class="data-card integrity__card" @click="detail = e">
          <div class="card-row">
            <span class="integrity__type">{{ e.anomaly_type }}</span>
            <span class="badge" :class="`badge--${severityTone(e.severity)}`">{{ severityLabel(e.severity) }}</span>
          </div>
          <div class="integrity__fields">
            <span v-if="e.provider_code" class="integrity__field">{{ e.provider_code }}</span>
            <span v-if="e.client_model" class="integrity__field">{{ e.client_model }}</span>
            <span class="integrity__field">{{ relativeTime(e.detected_at) }}</span>
          </div>
          <div v-if="e.expected_value !== undefined || e.actual_value !== undefined" class="integrity__diff">
            <span v-if="e.expected_value !== undefined" class="integrity__exp">{{ e.expected_value }}</span>
            <span v-if="e.actual_value !== undefined" class="integrity__act">{{ e.actual_value }}</span>
          </div>
          <span v-if="e.resolved" class="integrity__resolved">{{ t('integrity.resolvedBadge') }}</span>
        </button>
      </template>
    </HyperList>

    <AppSheet v-model="detailOpenProxy" presentation="sheet" :title="detail ? detail.anomaly_type : ''">
      <template v-if="detail">
        <div class="data-card">
          <div class="card-field">
            <span>{{ t('integrity.severity') }}</span>
            <span class="card-field__value">{{ severityLabel(detail.severity) }}</span>
          </div>
          <div v-if="detail.provider_code" class="card-field">
            <span>{{ t('integrity.provider') }}</span>
            <span class="card-field__value">{{ detail.provider_code }}</span>
          </div>
          <div v-if="detail.client_model" class="card-field">
            <span>{{ t('integrity.clientModel') }}</span>
            <span class="card-field__value">{{ detail.client_model }}</span>
          </div>
          <div v-if="detail.outbound_model" class="card-field">
            <span>{{ t('integrity.outboundModel') }}</span>
            <span class="card-field__value">{{ detail.outbound_model }}</span>
          </div>
          <div v-if="detail.expected_value !== undefined" class="card-field">
            <span>{{ t('integrity.expected') }}</span>
            <span class="card-field__value">{{ detail.expected_value }}</span>
          </div>
          <div v-if="detail.actual_value !== undefined" class="card-field">
            <span>{{ t('integrity.actual') }}</span>
            <span class="card-field__value">{{ detail.actual_value }}</span>
          </div>
          <div v-if="detail.sample" class="card-field">
            <span>{{ t('integrity.sample') }}</span>
            <span class="card-field__value">{{ detail.sample }}</span>
          </div>
          <div class="card-field">
            <span>{{ t('integrity.detectedAt') }}</span>
            <span class="card-field__value">{{ relativeTime(detail.detected_at) }}</span>
          </div>
        </div>

        <div v-if="detail.resolved" class="integrity__resolved-block">
          <p class="integrity__resolved">{{ t('integrity.resolvedBadge') }}</p>
          <p v-if="detail.resolution_notes" class="integrity__notes-read">{{ detail.resolution_notes }}</p>
        </div>

        <div v-else-if="isSuperAdmin" class="integrity__resolve">
          <p v-if="resolveOk" class="integrity__msg integrity__msg--ok">{{ t('integrity.resolveDone') }}</p>
          <p v-if="resolveError" class="integrity__msg integrity__msg--err">{{ resolveError }}</p>
          <label class="integrity__notes">
            <span class="integrity__notes-label">{{ t('integrity.notesLabel') }}</span>
            <textarea v-model="notes" class="integrity__notes-input" rows="3" :placeholder="t('integrity.notesPlaceholder')" />
          </label>
          <button type="button" class="btn btn--primary btn--block" :disabled="resolving" @click="doResolve">
            {{ resolving ? t('common.loading') : t('integrity.resolve') }}
          </button>
          <p class="integrity__hint">{{ t('integrity.notesOptional') }}</p>
        </div>
      </template>
    </AppSheet>
  </div>
</template>

<style scoped>
.integrity__denied {
  color: var(--app-text-secondary);
  font-size: 0.875rem;
  padding: var(--app-space-4) 0;
}

.integrity__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}

.integrity__total {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.integrity__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  font: inherit;
  color: inherit;
  cursor: pointer;
}

.integrity__card:active {
  background: var(--app-primary-softer);
}

.integrity__type {
  font-weight: 600;
  font-size: 0.875rem;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.integrity__fields {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
}

.integrity__field {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.integrity__diff {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  font-size: 0.75rem;
  font-family: var(--app-font-mono, monospace);
}

.integrity__exp {
  color: var(--app-success);
}

.integrity__act {
  color: var(--app-danger);
}

.integrity__resolved {
  color: var(--app-success);
  font-size: 0.75rem;
}

.integrity__resolved-block {
  margin-top: var(--app-space-3);
}

.integrity__notes-read {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  margin-top: var(--app-space-1);
}

.integrity__resolve {
  margin-top: var(--app-space-3);
}

.integrity__msg {
  font-size: 0.8125rem;
  margin-bottom: var(--app-space-2);
}

.integrity__msg--ok {
  color: var(--app-success);
}

.integrity__msg--err {
  color: var(--app-danger);
}

.integrity__notes {
  display: block;
  margin-bottom: var(--app-space-3);
}

.integrity__notes-label {
  display: block;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
  margin-bottom: var(--app-space-1);
}

.integrity__notes-input {
  width: 100%;
  min-height: 48px;
  padding: var(--app-space-2) var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text);
  font: inherit;
  font-size: 16px;
  resize: vertical;
}

.integrity__hint {
  margin-top: var(--app-space-2);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}
</style>
