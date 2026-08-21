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
const fpLimitDraft = ref<number | null>(null)
const concurrencyDraft = ref(5)
const concurrencyReason = ref('')
const concurrencyDialogOpen = ref(false)
const fpDialogOpen = ref(false)
const fpReason = ref('')
const saving = ref(false)

const effectiveProviderId = computed(() => props.providerId ?? props.monitor?.provider_id ?? null)

watch(
  () => [props.credentialId, effectiveProviderId.value] as const,
  ([credentialId, providerId]) => {
    fpStats.value = null
    fpError.value = ''
    fpLimitDraft.value = null
    if (credentialId && providerId) void loadFpStats()
  },
  { immediate: true },
)

watch(
  () => props.monitor,
  monitor => {
    if (!monitor) return
    concurrencyDraft.value = monitor.concurrency_limit_auto || monitor.effective_concurrency || 5
    if (fpLimitDraft.value == null && fpStats.value?.slot_limit != null) {
      fpLimitDraft.value = fpStats.value.slot_limit
    }
  },
)

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
    fpLimitDraft.value = stats.slot_limit ?? null
  } catch (error) {
    fpStats.value = null
    fpError.value = error instanceof Error ? error.message : '指纹槽位加载失败'
  } finally {
    fpLoading.value = false
  }
}

function openConcurrencyDialog() {
  concurrencyDraft.value = props.monitor?.concurrency_limit_auto || props.monitor?.effective_concurrency || 5
  concurrencyReason.value = ''
  concurrencyDialogOpen.value = true
}

function openFpDialog() {
  fpLimitDraft.value = fpStats.value?.slot_limit ?? fpLimitDraft.value
  fpReason.value = ''
  fpDialogOpen.value = true
}

async function saveConcurrency() {
  if (!props.canEdit || saving.value) return
  const reason = concurrencyReason.value.trim()
  if (!reason) {
    emit('error', '请输入并发调整原因。')
    return
  }
  if (!Number.isFinite(concurrencyDraft.value) || concurrencyDraft.value < 1) {
    emit('error', '并发上限必须 ≥ 1。')
    return
  }
  saving.value = true
  try {
    await setConcurrencyAuto(props.credentialId, concurrencyDraft.value, reason)
    concurrencyDialogOpen.value = false
    emit('saved')
  } catch (error) {
    emit('error', error instanceof Error ? error.message : '保存并发失败')
  } finally {
    saving.value = false
  }
}

async function saveFpLimit() {
  if (!props.canEdit || saving.value) return
  const providerId = effectiveProviderId.value
  if (!providerId) {
    emit('error', '缺少 provider_id，无法保存指纹槽位上限。')
    return
  }
  const reason = fpReason.value.trim()
  if (!reason) {
    emit('error', '请输入指纹槽位调整原因。')
    return
  }
  if (fpLimitDraft.value == null || !Number.isFinite(fpLimitDraft.value) || fpLimitDraft.value < 0) {
    emit('error', '指纹槽位上限必须 ≥ 0。')
    return
  }
  saving.value = true
  try {
    await updateCredential(providerId, props.credentialId, { fp_slot_limit: fpLimitDraft.value })
    fpDialogOpen.value = false
    await loadFpStats()
    emit('saved')
  } catch (error) {
    emit('error', error instanceof Error ? error.message : '保存指纹槽位失败')
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
        <button class="btn btn-sm" :disabled="!canEdit || saving" @click="openConcurrencyDialog">调整自动并发</button>
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
        <div class="nd-actions">
          <button class="btn btn-sm" :disabled="!canEdit || saving || !effectiveProviderId" @click="openFpDialog">
            修改槽位上限
          </button>
        </div>
      </template>
    </div>

    <div v-if="concurrencyDialogOpen" class="nd-dialog-mask" @click.self="concurrencyDialogOpen = false">
      <div class="nd-dialog" role="dialog" aria-label="调整自动并发">
        <h4>手动调整并发自动值</h4>
        <label>并发上限<input v-model.number="concurrencyDraft" type="number" min="1" /></label>
        <label>调整原因<input v-model="concurrencyReason" placeholder="请输入原因" /></label>
        <div class="nd-actions">
          <button class="btn btn-ghost btn-sm" @click="concurrencyDialogOpen = false">取消</button>
          <button class="btn btn-primary btn-sm" :disabled="saving" @click="saveConcurrency">确认</button>
        </div>
      </div>
    </div>

    <div v-if="fpDialogOpen" class="nd-dialog-mask" @click.self="fpDialogOpen = false">
      <div class="nd-dialog" role="dialog" aria-label="修改指纹槽位上限">
        <h4>修改指纹槽位上限</h4>
        <label>槽位上限<input v-model.number="fpLimitDraft" type="number" min="0" /></label>
        <label>调整原因<input v-model="fpReason" placeholder="请输入原因" /></label>
        <div class="nd-actions">
          <button class="btn btn-ghost btn-sm" @click="fpDialogOpen = false">取消</button>
          <button class="btn btn-primary btn-sm" :disabled="saving" @click="saveFpLimit">确认</button>
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
.nd-dialog-mask { position: fixed; inset: 0; z-index: 3100; background: color-mix(in srgb, #000 38%, transparent); display: flex; align-items: flex-start; justify-content: center; padding-top: 80px; }
.nd-dialog { width: min(420px, 92vw); background: var(--kx-surface); color: var(--kx-text); border-radius: 8px; padding: 18px; box-shadow: 0 12px 32px rgba(0, 0, 0, .24); display: grid; gap: 10px; }
.nd-dialog h4 { margin: 0; font-size: 15px; }
.nd-dialog label { display: grid; gap: 5px; font-size: 12px; }
.nd-dialog input { box-sizing: border-box; width: 100%; padding: 7px; border: 1px solid var(--kx-border); border-radius: 5px; background: var(--kx-bg); color: inherit; }
@media (max-width: 700px) { .nd-grid { grid-template-columns: 1fr; } }
</style>
