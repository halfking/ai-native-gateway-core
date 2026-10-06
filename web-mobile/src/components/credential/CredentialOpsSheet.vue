<script setup lang="ts">
// CredentialOpsSheet — 凭据的写操作面板（模型绑定开关 + 凭据调参）。
//
// 从凭据热力图 / 节点页进入。抽成独立组件而不是塞进某个视图，是因为
// 三个入口（heatmap / nodes / node-health）都可能要用它。
//
// ───────────────────────────────────────────────────────────────────────────
// ★ 本组件的三个硬约束，全部来自后端契约，不是产品偏好：
//
// (1) **reason 一律必填**，哪怕后端不校验。
//     promote / demote / set-concurrency-auto 三个端点都**不校验** reason，
//     而空 reason 会写进 auditLog；promote 还会落一个悬空的
//     `state_reason_detail = "manual_promote: "`。
//     ⇒ validateReason 在**提交前**拦下，空手提交不了。
//
// (2) **「上线」只对 manual_offline 提供**。
//     后端 409：only manual_offline can be toggled back to online
//     （model_probe_broken 等自动判定由探测共识持有，操作员点了必然 409）。
//     ⇒ 与其让用户点了吃 409，不如按 reason 决定是否渲染该按钮。
//
// (3) **`raw_model_name` 原样透传**，不规范化。
//     规范化后的名字会命中不了（404 binding not found），
//     而 404 与「确实没绑这个模型」在移动端无法区分。
//
// 另：每个写操作都**必须**有独立的错误位与成功位 —— 静默失败
//   （原 KeysView.doDisable 缺 catch）是本专题修过的真实缺陷。

import { computed, ref, watch } from 'vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppConfirm from '@/components/common/AppConfirm.vue'
import { t } from '@/i18n'
import {
  promoteCredential,
  demoteCredential,
  setConcurrencyAuto,
  toggleCredentialModel,
  validateReason,
  canToggleOnline,
  WRITE_REASON_MAX,
  type ModelToggleResult,
} from '@/api/credentialWriteOps'

export interface OpsModel {
  /** ★ 原样透传给后端，不做规范化 */
  raw_model_name: string
  available?: boolean
  unavailable_reason?: string | null
}

const props = defineProps<{
  open: boolean
  credentialId: number
  credentialLabel: string
  models: OpsModel[]
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'done'): void
}>()

const openProxy = computed({
  get: () => props.open,
  set: (v: boolean) => emit('update:open', v),
})

type OpKind = 'toggle-offline' | 'toggle-online' | 'promote' | 'demote' | 'concurrency'

const pending = ref<OpKind | null>(null)
const pendingModel = ref<OpsModel | null>(null)
const confirmOpen = ref(false)
const reasonText = ref('')
const recoverHours = ref(2)
const concurrencyValue = ref(10)
const opError = ref<string | null>(null)
const opOk = ref<string | null>(null)
/** reason 的就地校验错误（不用 opError 混同 —— 那个是后端错误位）。 */
const reasonError = ref<string | null>(null)

function resetTransient(): void {
  reasonText.value = ''
  reasonError.value = null
  opError.value = null
  opOk.value = null
  pending.value = null
  pendingModel.value = null
}
watch(() => props.open, (v) => { if (!v) resetTransient() })

/** 空 reason 不让提交：给一个带凭据 id 的默认理由，而不是发空串。 */
function effectiveReason(): string {
  const typed = reasonText.value.trim()
  if (typed !== '') return typed
  return t('nodes.reasonDefaultDisable')
}

function startOp(op: OpKind, model: OpsModel | null = null): void {
  reasonText.value = ''
  reasonError.value = null
  opError.value = null
  opOk.value = null
  pending.value = op
  pendingModel.value = model
  confirmOpen.value = true
}

const confirmMeta = computed(() => {
  const op = pending.value
  const name = props.credentialLabel
  if (op === 'toggle-offline') {
    return {
      title: t('credOps.confirmToggleOffline', { model: pendingModel.value?.raw_model_name ?? '' }),
      body: t('credOps.toggleHint'),
      label: t('credOps.takeOffline'),
      danger: true,
    }
  }
  if (op === 'toggle-online') {
    return {
      title: t('credOps.confirmToggleOnline', { model: pendingModel.value?.raw_model_name ?? '' }),
      body: '',
      label: t('credOps.putOnline'),
      danger: false,
    }
  }
  if (op === 'promote') return { title: t('credOps.confirmPromote'), body: name, label: t('credOps.promote'), danger: false }
  if (op === 'demote') {
    return {
      title: t('credOps.confirmDemote', { hours: recoverHours.value }),
      body: name,
      label: t('credOps.demote'),
      danger: true,
    }
  }
  return {
    title: t('credOps.confirmConcurrency', { n: concurrencyValue.value }),
    body: name,
    label: t('credOps.concurrency'),
    danger: false,
  }
})

/** 403 单独说人话：这是权限档位问题，不是网络故障。 */
function describeError(err: unknown): string {
  const status = (err as { status?: number })?.status
  if (status === 403) return t('credOps.errForbidden')
  if (status === 404) return t('credOps.errNotFound')
  if (status === 409) return t('credOps.errConflict')
  const msg = err instanceof Error ? err.message : String(err)
  return msg || t('common.error')
}

function what(op: OpKind): string {
  if (op === 'toggle-offline') return t('credOps.takeOffline')
  if (op === 'toggle-online') return t('credOps.putOnline')
  if (op === 'promote') return t('credOps.promote')
  if (op === 'demote') return t('credOps.demote')
  return t('credOps.concurrency')
}

async function runOp(): Promise<void> {
  const op = pending.value
  if (op === null) return
  // ★ reason 必填：后端有三个端点不校验，但空 reason 会毁掉审计日志
  const rerr = validateReason(reasonText.value)
  if (rerr !== null) {
    reasonError.value = t(rerr === 'reasonTooLong' ? 'credOps.reasonTooLong' : 'credOps.reasonRequired')
    confirmOpen.value = false
    return
  }
  confirmOpen.value = false
  opError.value = null
  opOk.value = null
  try {
    if (op === 'toggle-offline' || op === 'toggle-online') {
      const m = pendingModel.value
      if (!m) return
      const res: ModelToggleResult = await toggleCredentialModel(
        props.credentialId,
        m.raw_model_name,
        op === 'toggle-online' ? 'online' : 'offline',
        effectiveReason(),
      )
      opOk.value = t('credOps.opOk', { what: `${what(op)} → ${res.available ? 'online' : 'offline'}` })
    } else if (op === 'promote') {
      await promoteCredential(props.credentialId, effectiveReason())
      opOk.value = t('credOps.opOk', { what: what(op) })
    } else if (op === 'demote') {
      await demoteCredential(props.credentialId, effectiveReason(), recoverHours.value)
      opOk.value = t('credOps.opOk', { what: what(op) })
    } else {
      await setConcurrencyAuto(props.credentialId, concurrencyValue.value, effectiveReason())
      opOk.value = t('credOps.opOk', { what: `${what(op)}=${concurrencyValue.value}` })
    }
    emit('done')
  } catch (err) {
    opError.value = describeError(err)
  } finally {
    pending.value = null
    pendingModel.value = null
    reasonText.value = ''
    reasonError.value = null
  }
}

function isOnline(m: OpsModel): boolean {
  return m.available === true
}
</script>

<template>
  <AppSheet v-model="openProxy" presentation="sheet" :title="t('credOps.title')">
    <p v-if="opError" class="ops__error">{{ opError }}</p>
    <p v-else-if="opOk" class="ops__ok">{{ opOk }}</p>

    <h3 class="ops__section">{{ t('credOps.modelsSection') }}</h3>
    <p class="ops__hint">{{ t('credOps.toggleHint') }}</p>
    <ul class="ops__list">
      <li v-for="m in models" :key="m.raw_model_name" class="ops__row">
        <span class="ops__row-name">{{ m.raw_model_name }}</span>
        <button
          v-if="isOnline(m)"
          type="button"
          class="ops__btn ops__btn--danger"
          @click="startOp('toggle-offline', m)"
        >
          {{ t('credOps.takeOffline') }}
        </button>
        <button
          v-else-if="canToggleOnline(m.unavailable_reason)"
          type="button"
          class="ops__btn"
          @click="startOp('toggle-online', m)"
        >
          {{ t('credOps.putOnline') }}
        </button>
        <!-- ★ 非 manual_offline 就不给「上线」按钮，并说明为什么 -->
        <span v-else class="ops__blocked">
          {{ t('credOps.onlineBlocked', { reason: m.unavailable_reason || '—' }) }}
        </span>
      </li>
    </ul>

    <h3 class="ops__section">{{ t('credOps.credSection') }}</h3>
    <div class="ops__row">
      <button type="button" class="ops__btn" @click="startOp('promote')">{{ t('credOps.promote') }}</button>
      <label class="ops__field">
        <span>{{ t('credOps.recoverHours', { n: recoverHours }) }}</span>
        <input v-model.number="recoverHours" type="number" min="1" step="1" class="ops__input" />
      </label>
      <button type="button" class="ops__btn ops__btn--danger" @click="startOp('demote')">
        {{ t('credOps.demote') }}
      </button>
    </div>
    <div class="ops__row">
      <label class="ops__field">
        <span>{{ t('credOps.concurrencyValue') }}</span>
        <input v-model.number="concurrencyValue" type="number" min="1" step="1" class="ops__input" />
      </label>
      <button type="button" class="ops__btn" @click="startOp('concurrency')">
        {{ t('credOps.concurrency') }}
      </button>
    </div>

    <AppConfirm v-model="confirmOpen" :title="confirmMeta.title" :body="confirmMeta.body" :confirm-label="confirmMeta.label" :danger="confirmMeta.danger" @confirm="runOp">
      <label class="ops__reason">
        <span class="ops__reason-label">{{ t('credOps.reasonLabel') }}</span>
        <textarea v-model="reasonText" class="ops__reason-input" rows="2" :maxlength="WRITE_REASON_MAX" :placeholder="t('credOps.reasonPlaceholder')" />
      </label>
    </AppConfirm>
    <p v-if="reasonError" class="ops__error ops__error--inline">{{ reasonError }}</p>
  </AppSheet>
</template>

<style scoped>
.ops__section {
  margin: var(--app-space-3) 0 var(--app-space-2);
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text-secondary);
}
.ops__hint {
  margin: 0 0 var(--app-space-2);
  font-size: 12px;
  color: var(--app-text-muted);
  line-height: 1.5;
}
.ops__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.ops__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  padding: var(--app-space-2) 0;
  border-bottom: 1px solid var(--app-border);
  min-height: 48px;
}
.ops__row-name {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  color: var(--app-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ops__btn {
  min-height: 48px;
  padding: 0 14px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-primary);
  font-size: 13px;
  white-space: nowrap;
}
.ops__btn--danger {
  color: var(--app-danger);
  border-color: var(--app-danger);
}
.ops__blocked {
  font-size: 12px;
  color: var(--app-text-muted);
  line-height: 1.4;
}
.ops__field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ops__input {
  width: 72px;
  min-height: 48px;
  padding: 0 8px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ops__reason {
  display: block;
  width: 100%;
}
.ops__reason-label {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ops__reason-input {
  width: 100%;
  min-height: 48px;
  padding: var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
  resize: vertical;
}
.ops__error {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 12px;
}
.ops__error--inline {
  margin-top: var(--app-space-1);
}
.ops__ok {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  background: var(--app-success-soft);
  color: var(--app-success);
  font-size: 12px;
}
</style>
