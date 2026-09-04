<script setup lang="ts">
/**
 * 节点详情「设置与维护」内的并发 + 指纹 slot 段。
 * 数据由父组件分段注入；本组件只负责展示与写回。
 */
import { computed, ref, watch } from 'vue'
import {
  setConcurrencyAuto,
  type CredentialMonitorSummary,
} from '../api/credential-monitor'
import {
  getCredentialFpSlotStats,
  updateCredential,
  type FpSlotStats,
} from '../api/providers'
import FpSlotVisualizer from './FpSlotVisualizer.vue'

const props = defineProps<{
  credentialId: number
  providerId: number | null
  monitor: CredentialMonitorSummary | null
  monitorLoading: boolean
  canEdit: boolean
}>()

const emit = defineEmits<{
  saved: []
  error: [message: string]
}>()

const fpStats = ref<FpSlotStats | null>(null)
const fpLoading = ref(false)
const fpError = ref('')
const saving = ref(false)

// ── Unified concurrency + fp-slot editor ──────────────────────────────
// Replaces the previous two separate dialogs ("调整自动并发" and
// "修改槽位上限") with a single editor that updates 手动并发
// (concurrency_limit), 自动并发 (concurrency_limit_auto) and 指纹槽位
// (fp_slot_limit) in one save. The three values were previously spread
// across this panel (auto concurrency + fp slot) and the credential
// drawer (manual concurrency), so operators could not sync them together.
const unifiedDialogOpen = ref(false)
const manualDraft = ref<number | null>(null) // 手动并发 = concurrency_limit (null = 未设置)
const autoDraft = ref<number | null>(null) // 自动并发 = concurrency_limit_auto (null = 未设置)
const fpDraft = ref<number | null>(null) // 指纹槽位 = fp_slot_limit
const reasonDraft = ref('')
const origManual = ref<number | null>(null)
const origAuto = ref<number | null>(null)
const origFp = ref<number | null>(null)

const effectiveProviderId = computed(() => props.providerId ?? props.monitor?.provider_id ?? null)

watch(
  () => [props.credentialId, effectiveProviderId.value] as const,
  ([credentialId, providerId]) => {
    fpStats.value = null
    fpError.value = ''
    if (credentialId && providerId) void loadFpStats()
  },
  { immediate: true },
)

function normalizeInt(v: number | null | undefined | ''): number | null {
  if (v === '' || v == null) return null
  // Truncate toward zero so fractional input (e.g. 1.5) becomes a valid
  // integer; the backend only accepts whole concurrency counts.
  const n = Math.trunc(Number(v))
  return Number.isFinite(n) ? n : null
}

async function loadFpStats() {
  const providerId = effectiveProviderId.value
  if (!providerId) {
    fpError.value = '缺少 provider_id，无法加载指纹槽位。'
    return
  }
  fpLoading.value = true
  fpError.value = ''
  try {
    const stats = await getCredentialFpSlotStats(providerId, props.credentialId)
    fpStats.value = stats
  } catch (error) {
    fpStats.value = null
    fpError.value = error instanceof Error ? error.message : '指纹槽位加载失败'
  } finally {
    fpLoading.value = false
  }
}

function openUnifiedDialog() {
  origManual.value = props.monitor?.concurrency_limit ?? null
  origAuto.value = props.monitor?.concurrency_limit_auto ?? null
  origFp.value = fpStats.value?.slot_limit ?? null
  manualDraft.value = origManual.value
  autoDraft.value = origAuto.value
  fpDraft.value = origFp.value
  reasonDraft.value = ''
  unifiedDialogOpen.value = true
}

async function saveUnified() {
  if (!props.canEdit || saving.value) return
  const manual = normalizeInt(manualDraft.value)
  const auto = normalizeInt(autoDraft.value)
  const fp = normalizeInt(fpDraft.value)
  // 自动并发后端要求 ≥ 1，且不支持清空（原值非空时清空应明确拒绝）
  if (auto == null) {
    if (origAuto.value != null) {
      emit('error', '自动并发不支持清空，请输入 ≥ 1 的值。')
    }
    return
  }
  if (auto < 1) {
    emit('error', '自动并发必须 ≥ 1。')
    return
  }
  // 指纹槽位 NOT NULL，必须 ≥ 0
  if (fp == null || fp < 0) {
    emit('error', '指纹槽位上限必须 ≥ 0。')
    return
  }
  // 手动并发若设置，必须 ≥ 0
  if (manual != null && manual < 0) {
    emit('error', '手动并发必须 ≥ 0。')
    return
  }
  // 指纹槽位不能超过手动并发（手动未设置时不约束）
  if (manual != null && fp > manual) {
    emit('error', `指纹槽位（${fp}）不能超过手动并发（${manual}）。`)
    return
  }
  const changedManualOrFp = manual !== origManual.value || fp !== origFp.value
  const changedAuto = auto !== origAuto.value
  if (!changedManualOrFp && !changedAuto) {
    // 无任何变更：填了原因也静默关闭，没填原因则提示需填写原因（防止空提交）
    if (!reasonDraft.value.trim()) {
      emit('error', '请填写调整原因，或无需调整请直接关闭。')
      return
    }
    unifiedDialogOpen.value = false
    return
  }
  // 自动并发变更（单独或组合）都需要审计原因；手动并发 + 指纹槽位走无 reason 字段的 PATCH。
  // 三者同时变更时，两个接口都要调用，不能互相覆盖。
  const reason = reasonDraft.value.trim()
  if (changedAuto && !reason) {
    emit('error', '调整自动并发需填写原因。')
    return
  }
  saving.value = true
  try {
    if (changedManualOrFp) {
      await updateCredential(effectiveProviderId.value!, props.credentialId, {
        concurrency_limit: manual,
        fp_slot_limit: fp,
      })
    }
    if (changedAuto && auto != null) {
      await setConcurrencyAuto(props.credentialId, auto, reason)
    }
    unifiedDialogOpen.value = false
    await loadFpStats()
    emit('saved')
  } catch (error) {
    emit('error', error instanceof Error ? error.message : '保存失败')
  } finally {
    saving.value = false
  }
}

defineExpose({ reload: loadFpStats })
</script>

<template>
  <section class="nd-section">
    <h3>并发与指纹槽位</h3>

    <div v-if="monitorLoading" class="nd-seg-loading" role="status">并发数据加载中…</div>
    <template v-else-if="monitor">
      <dl class="nd-grid">
        <div><dt>手动并发</dt><dd>{{ monitor.concurrency_limit ?? '未设置' }}</dd></div>
        <div><dt>自动并发</dt><dd>{{ monitor.concurrency_limit_auto ?? '未设置' }}</dd></div>
        <div><dt>生效并发</dt><dd>{{ monitor.effective_concurrency }}</dd></div>
      </dl>
      <div class="nd-actions">
        <button class="btn btn-sm" :disabled="!canEdit || saving" @click="openUnifiedDialog">调整并发与槽位</button>
      </div>
    </template>
    <p v-else class="nd-muted">暂无并发摘要。</p>

    <div class="nd-fp-block">
      <div class="nd-fp-head">
        <strong>指纹槽位</strong>
        <button class="btn btn-sm btn-ghost" :disabled="fpLoading" @click="loadFpStats">
          {{ fpLoading ? '加载中…' : '↻ 刷新' }}
        </button>
      </div>
      <div v-if="fpLoading && !fpStats" class="nd-seg-loading" role="status">指纹槽位加载中…</div>
      <p v-else-if="fpError" class="nd-notice nd-notice--warn">{{ fpError }}</p>
      <template v-else-if="fpStats">
        <dl class="nd-grid">
          <div><dt>上限</dt><dd>{{ fpStats.slot_limit ?? (fpStats.unlimited ? '无限' : '—') }}</dd></div>
          <div><dt>健康</dt><dd>{{ fpStats.healthy_slots }}</dd></div>
          <div><dt>占用 / 空闲</dt><dd>{{ fpStats.occupied_slots }} / {{ fpStats.free_slots ?? '—' }}</dd></div>
        </dl>
        <FpSlotVisualizer
          v-if="fpStats.slot_limit && fpStats.details?.length"
          :details="fpStats.details"
          :slot-limit="fpStats.slot_limit"
        />
        <p v-else-if="fpStats.unlimited" class="nd-muted">{{ fpStats.message || '指纹槽位未启用上限。' }}</p>
      </template>
    </div>

    <div v-if="unifiedDialogOpen" class="nd-dialog-mask" @click.self="unifiedDialogOpen = false">
      <div class="nd-dialog" role="dialog" aria-label="调整并发与槽位">
        <h4>调整并发与槽位</h4>
        <label>手动并发（留空 = 使用自动并发）
          <input v-model.number="manualDraft" type="number" min="0" placeholder="未设置" />
        </label>
        <label>自动并发（≥ 1）
          <input v-model.number="autoDraft" type="number" min="1" />
        </label>
        <label>指纹槽位上限（≥ 0）
          <input v-model.number="fpDraft" type="number" min="0" />
        </label>
        <label>调整原因
          <input v-model="reasonDraft" placeholder="请输入原因" />
        </label>
        <div class="nd-actions">
          <button class="btn btn-ghost btn-sm" @click="unifiedDialogOpen = false">取消</button>
          <button class="btn btn-primary btn-sm" :disabled="saving" @click="saveUnified">确认</button>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.nd-section { border: 1px solid var(--kx-border); border-radius: 8px; padding: 14px; margin-bottom: 12px; }
.nd-section h3 { margin: 0 0 12px; font-size: 14px; }
.nd-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin: 0 0 10px; }
.nd-grid dt { color: var(--kx-muted); font-size: 11px; margin-bottom: 3px; }
.nd-grid dd { margin: 0; font-size: 12px; }
.nd-actions { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.nd-muted { color: var(--kx-muted); font-size: 12px; }
.nd-seg-loading { padding: 12px; text-align: center; color: var(--kx-muted); font-size: 12px; }
.nd-fp-block { margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--kx-border); }
.nd-fp-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.nd-notice { padding: 8px 10px; border-radius: 6px; margin: 0 0 12px; font-size: 12px; }
.nd-notice--warn { background: color-mix(in srgb, var(--kx-warning) 12%, transparent); color: var(--kx-warning); }
.nd-dialog-mask { position: fixed; inset: 0; z-index: 3050; background: color-mix(in srgb, var(--kx-text) 38%, transparent); display: flex; align-items: flex-start; justify-content: center; padding-top: 80px; }
.nd-dialog { width: min(420px, 92vw); background: var(--kx-surface); color: var(--kx-text); border-radius: 8px; padding: 18px; box-shadow: 0 12px 32px var(--overlay-light); display: grid; gap: 10px; }
.nd-dialog h4 { margin: 0; font-size: 15px; }
.nd-dialog label { display: grid; gap: 5px; font-size: 12px; }
.nd-dialog input { box-sizing: border-box; width: 100%; padding: 7px; border: 1px solid var(--kx-border); border-radius: 5px; background: var(--kx-bg); color: inherit; }
@media (max-width: 700px) { .nd-grid { grid-template-columns: 1fr; } }
</style>
