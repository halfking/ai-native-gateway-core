<script setup lang="ts">
// RoutingOverridesView — 路由覆盖规则（/overrides，**superAdmin 档**）。
//
// 数据源：GET/POST/DELETE/PATCH /api/admin/routing/overrides
//
// 它和已上移的 /routing-audit 是一对：
//   本页  = **现在生效的规则是什么**
//   audit = **这些规则是谁在什么时候改的**
// 只看审计不知道当前状态；只看规则不知道是不是刚被人动过。
//
// ⚠️★ 三个后端语义决定了 UI 的做法：
//
// (1) **DELETE 是软删** —— SQL 是 `SET expires_at = NOW() - INTERVAL '1 second'`，
//     行仍留在表里。⇒ 停用后必须用 `active=true` 刷新，
//     否则用户会看到刚删的规则还在列表里，以为删除失败而重复操作。
//
// (2) **创建后约 1 分钟才生效**（后端 201 的 message 明说 OverrideStore
//     下一个 1-min reload 生效）。⇒ 成功文案必须带这个延迟，
//     否则用户会立刻去查路由解析、看不到新规则，于是重复提交 ——
//     而重复提交必然撞 409（同一四元组已存在），越急越错。
//
// (3) **`profile` / `mode` 后端无枚举校验**（create.go 只校验 task_type 与
//     reason），也没有「合法值列表」端点 ⇒ 候选值只能从**现有规则**里取。
//     自由输入仍允许，但 UI 优先给候选。
//
// 整条线是 superAdmin（handler.go:1381），导航已按 requiresRole 挡住。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppSheet from '@/components/common/AppSheet.vue'
import AppConfirm from '@/components/common/AppConfirm.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchRoutingOverrides,
  createRoutingOverride,
  deleteRoutingOverride,
  extendRoutingOverride,
  isExpired,
  knownProfiles,
  knownModes,
  knownTaskTypes,
  overrideSummary,
  type RoutingOverride,
} from '@/api/routingOverrides'

useHyperPage({ title: () => t('overrides.title') })

const rows = ref<RoutingOverride[]>([])
const echo = ref<{ task_type: string; profile: string; active: string } | null>(null)
const activeOnly = ref(true)
const loading = ref(false)
const opError = ref<string | null>(null)
const opOk = ref<string | null>(null)
const pending = ref<'delete' | 'extend' | 'create' | null>(null)
const target = ref<RoutingOverride | null>(null)
const confirmOpen = ref(false)
const extendHours = ref(24)
const formError = ref<string | null>(null)

/** 新建表单。profile / mode 的候选来自现有规则。 */
const form = ref({ task_type: '', profile: '', mode: '', model_chosen: '', reason: '' })
const createOpen = ref(false)

const profileCandidates = computed(() => knownProfiles(rows.value))
const modeCandidates = computed(() => knownModes(rows.value))
const taskTypeCandidates = computed(() => knownTaskTypes(rows.value))

/** 停用后强制切到 active=true 视图 —— 见文件头 (1)。 */
async function load(): Promise<void> {
  loading.value = true
  try {
    const resp = await fetchRoutingOverrides(activeOnly.value ? { active: true } : {})
    rows.value = resp.overrides ?? []
    echo.value = resp.filter ?? null
  } catch (err) {
    rows.value = []
    echo.value = null
    opError.value = describeError(err)
  } finally {
    loading.value = false
  }
}
void load()

onBeforeUnmount(() => {
  rows.value = []
})

function describeError(err: unknown): string {
  const status = (err as { status?: number })?.status
  if (status === 403) return t('overrides.errForbidden')
  // 409 = 同一 (task_type, profile, model_chosen, mode) 已存在
  if (status === 409) return t('overrides.errDuplicate')
  const msg = err instanceof Error ? err.message : String(err)
  return msg || t('common.error')
}

function onActiveOnlyChange(): void {
  activeOnly.value = !activeOnly.value
  void load()
}

function startOp(op: 'delete' | 'extend', o: RoutingOverride): void {
  pending.value = op
  target.value = o
  confirmOpen.value = true
  opError.value = null
  opOk.value = null
}

function openCreate(): void {
  pending.value = 'create'
  form.value = { task_type: '', profile: '', mode: '', model_chosen: '', reason: '' }
  formError.value = null
  opError.value = null
  opOk.value = null
  createOpen.value = true
}

const confirmMeta = computed(() => {
  if (pending.value === 'delete') {
    return { title: t('overrides.confirmDelete'), body: target.value ? overrideSummary(target.value) : '', label: t('overrides.delete'), danger: true }
  }
  if (pending.value === 'extend') {
    return { title: t('overrides.confirmExtend', { n: extendHours.value }), body: target.value ? overrideSummary(target.value) : '', label: t('overrides.extend'), danger: false }
  }
  return { title: t('overrides.confirmCreate'), body: '', label: t('overrides.create'), danger: false }
})

async function runOp(): Promise<void> {
  const op = pending.value
  if (op === null) return

  if (op === 'create') {
    // ★ 后端只校验 task_type 与 reason（control/routing/create.go:167-185）
    const f = form.value
    if (f.task_type.trim() === '') {
      formError.value = t('overrides.reasonRequired')
      return
    }
    if (f.reason.trim() === '') {
      formError.value = t('overrides.reasonRequired')
      return
    }
    try {
      await createRoutingOverride({
        task_type: f.task_type.trim(),
        profile: f.profile.trim(),
        mode: f.mode.trim(),
        // ★ 空串是「指定了一个空模型」，所以这里必须发 null 而不是 ''
        model_chosen: f.model_chosen.trim() === '' ? null : f.model_chosen.trim(),
        reason: f.reason.trim(),
      })
      opOk.value = t('overrides.createOk')
      createOpen.value = false
      activeOnly.value = true
      await load()
    } catch (err) {
      formError.value = describeError(err)
    } finally {
      pending.value = null
    }
    return
  }

  confirmOpen.value = false
  const o = target.value
  if (!o) return
  try {
    if (op === 'delete') {
      await deleteRoutingOverride(o.id)
      opOk.value = t('overrides.okDeleted')
      // ★ 软删 ⇒ 切到 active=true 视图，否则刚删的规则还在列表里
      activeOnly.value = true
    } else {
      const base = o.expires_at ? Date.parse(o.expires_at) : Date.now()
      const to = new Date(base + extendHours.value * 3600_000).toISOString()
      await extendRoutingOverride(o.id, to)
      opOk.value = t('overrides.okExtended')
    }
    await load()
  } catch (err) {
    opError.value = describeError(err)
  } finally {
    pending.value = null
    target.value = null
  }
}

function expiredOf(o: RoutingOverride): boolean {
  return isExpired(o)
}
</script>

<template>
  <div class="view-root ov">
    <div class="ov__bar">
      <button
        type="button"
        class="ov__chip"
        :class="{ 'ov__chip--on': activeOnly }"
        :aria-pressed="activeOnly"
        @click="onActiveOnlyChange"
      >
        {{ t('overrides.activeOnly') }}
      </button>
      <button type="button" class="ov__create" @click="openCreate">{{ t('overrides.create') }}</button>
    </div>

    <p v-if="opError" class="ov__msg ov__msg--err">{{ opError }}</p>
    <p v-else-if="opOk" class="ov__msg ov__msg--ok">{{ opOk }}</p>
    <p class="ov__hint">
      <AppIcon name="alert" :size="14" />
      <span>{{ t('overrides.softDeleteHint') }}</span>
    </p>
    <p v-if="!loading" class="ov__count">{{ t('overrides.count', { n: rows.length }) }}</p>

    <p v-if="loading" class="ov__msg">{{ t('common.loading') }}</p>
    <p v-else-if="rows.length === 0" class="ov__msg">
      {{ activeOnly ? t('overrides.empty') : t('overrides.emptyFiltered') }}
    </p>

    <ul v-else class="ov__list">
      <li v-for="o in rows" :key="o.id" class="ov__item" :class="{ 'ov__item--expired': expiredOf(o) }">
        <div class="ov__item-head">
          <span class="ov__item-summary">
            <StatusDot :tone="expiredOf(o) ? 'muted' : 'success'" />
            {{ overrideSummary(o) }}
          </span>
          <span v-if="expiredOf(o)" class="badge badge--muted">{{ t('overrides.expired') }}</span>
        </div>
        <p v-if="o.reason" class="ov__item-reason">{{ o.reason }}</p>
        <div class="ov__item-meta">
          <span v-if="o.expires_at" class="ov__item-field">
            {{ t('overrides.expiresAt', { t: relativeTime(o.expires_at) }) }}
          </span>
          <span v-else class="ov__item-field">{{ t('overrides.forever') }}</span>
          <span v-if="o.created_by" class="ov__item-field">
            {{ t('overrides.createdBy', { who: o.created_by }) }}
          </span>
        </div>
        <div class="ov__item-actions">
          <label class="ov__extend-field">
            <span>{{ t('overrides.extendHours', { n: extendHours }) }}</span>
            <input v-model.number="extendHours" type="number" min="1" step="1" class="ov__extend-input" />
          </label>
          <button type="button" class="ov__btn" @click="startOp('extend', o)">{{ t('overrides.extend') }}</button>
          <button type="button" class="ov__btn ov__btn--danger" @click="startOp('delete', o)">
            {{ t('overrides.delete') }}
          </button>
        </div>
      </li>
    </ul>

    <AppConfirm
      v-model="confirmOpen"
      :title="confirmMeta.title"
      :body="confirmMeta.body"
      :confirm-label="confirmMeta.label"
      :danger="confirmMeta.danger"
      @confirm="runOp"
    />

    <AppSheet v-model="createOpen" presentation="sheet" :title="t('overrides.create')">
      <div class="ov__form">
        <label class="ov__field">
          <span>{{ t('overrides.taskType') }} *</span>
          <input v-model="form.task_type" class="ov__input" list="ov-task-types" />
        </label>
        <datalist id="ov-task-types">
          <option v-for="v in taskTypeCandidates" :key="v" :value="v" />
        </datalist>

        <label class="ov__field">
          <span>{{ t('overrides.profile') }}</span>
          <input v-model="form.profile" class="ov__input" list="ov-profiles" />
        </label>
        <datalist id="ov-profiles">
          <option v-for="v in profileCandidates" :key="v" :value="v" />
        </datalist>

        <label class="ov__field">
          <span>{{ t('overrides.mode') }}</span>
          <input v-model="form.mode" class="ov__input" list="ov-modes" />
        </label>
        <datalist id="ov-modes">
          <option v-for="v in modeCandidates" :key="v" :value="v" />
        </datalist>

        <label class="ov__field">
          <span>{{ t('overrides.modelChosen') }}</span>
          <input v-model="form.model_chosen" class="ov__input" />
        </label>

        <label class="ov__field">
          <span>{{ t('overrides.reason') }} *</span>
          <textarea v-model="form.reason" class="ov__input ov__input--area" rows="2" />
        </label>
        <p v-if="formError" class="ov__msg ov__msg--err">{{ formError }}</p>
        <button type="button" class="ov__submit" @click="runOp">{{ t('overrides.create') }}</button>
      </div>
    </AppSheet>
  </div>
</template>

<style scoped>
.ov {
  padding: var(--app-space-3);
}
.ov__bar {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.ov__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.ov__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.ov__create {
  margin-left: auto;
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-primary);
  border-radius: var(--app-radius-pill);
  background: var(--app-surface);
  color: var(--app-primary);
  font-size: 13px;
}
.ov__hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.ov__count {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
}
.ov__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.ov__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ov__msg--ok {
  background: var(--app-success-soft);
  color: var(--app-success);
}
.ov__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.ov__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.ov__item--expired {
  opacity: 0.65;
}
.ov__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ov__item-summary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  word-break: break-word;
}
.ov__item-reason {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ov__item-meta {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 4px;
}
.ov__item-field {
  font-size: 12px;
  color: var(--app-text-muted);
}
.ov__item-actions {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.ov__extend-field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ov__extend-input {
  width: 68px;
  min-height: 48px;
  padding: 0 8px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ov__btn {
  min-height: 48px;
  padding: 0 14px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-primary);
  font-size: 13px;
}
.ov__btn--danger {
  color: var(--app-danger);
  border-color: var(--app-danger);
}
.ov__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}
.ov__field {
  display: block;
}
.ov__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ov__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ov__input--area {
  padding: var(--app-space-2);
  resize: vertical;
}
.ov__submit {
  min-height: 48px;
  border: 1px solid var(--app-primary);
  border-radius: var(--app-radius-sm);
  background: var(--app-primary);
  color: var(--app-on-primary);
  font-size: 14px;
}
</style>
