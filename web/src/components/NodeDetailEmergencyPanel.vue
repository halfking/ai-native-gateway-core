<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { emergencyRepair, type EmergencyRepairAction, type RoutingCandidate } from '../api/routing'
import { useCredentialLabels } from '../composables/useCredentialLabels'

const props = defineProps<{
  candidate: RoutingCandidate | null
  rawModel: string
  canEdit: boolean
}>()

const emit = defineEmits<{
  applied: []
  message: [text: string]
  error: [text: string]
}>()
// credentialDisplayName resolves credential id → human label; the composable
// keeps a Map<id,label> refreshed via loadCredentialLabels(), and falls back
// to "凭据 #ID" if the label is missing.
const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()

const repairing = ref<EmergencyRepairAction | null>(null)
const pending = ref<{ action: EmergencyRepairAction; label: string } | null>(null)

const canForceEnable = computed(() => {
  const c = props.candidate
  if (!c) return false
  const inEffect = c.effective_at == null || new Date(c.effective_at).getTime() <= Date.now()
  const notExpired = c.expires_at == null || new Date(c.expires_at).getTime() > Date.now()
  return c.credential_status === 'active' && c.lifecycle_status === 'active' && inEffect && notExpired
})

const hasRuntimeBlock = computed(() => {
  const c = props.candidate
  if (!c) return false
  if (!c.routable) return true
  if (c.circuit_state && c.circuit_state !== 'closed') return true
  if (c.availability_state && c.availability_state !== 'ready') return true
  if (c.quota_state && c.quota_state !== 'ok') return true
  return false
})

const canForceDisable = computed(() => {
  const c = props.candidate
  return !!c && c.credential_status === 'active' && c.lifecycle_status === 'active'
})

const canClearCircuit = computed(() => {
  const c = props.candidate
  return !!c && (c.circuit_state === 'open' || c.circuit_state === 'half_open')
})

const canResetErrors = computed(() =>
  (props.candidate?.credential_consecutive_failures ?? props.candidate?.consecutive_failures ?? 0) > 0,
)

// Refresh the label cache whenever the candidate changes so the confirmation
// modal always shows the current label (best-effort, no-op when the cache is
// already fresh within the 60s TTL).
watch(() => props.candidate?.credential_id, (id) => {
  if (id) void loadCredentialLabels()
})

function request(action: EmergencyRepairAction, label: string) {
  if (!props.canEdit || repairing.value) return
  pending.value = { action, label }
}

function cancel() {
  if (repairing.value) return
  pending.value = null
}

async function confirm() {
  const item = pending.value
  if (!item || repairing.value || !props.canEdit || !props.rawModel) return
  if (item.action === 'force_enable' && !hasRuntimeBlock.value) {
    emit('message', '节点当前已健康（无运行态阻塞），无需强制启用')
    pending.value = null
    return
  }
  repairing.value = item.action
  try {
    const result = await emergencyRepair({
      credential_id: props.candidate!.credential_id,
      raw_model: props.rawModel,
      action: item.action,
      reason: `admin via node detail drawer: ${item.label}`,
    })
    const warnings: string[] = []
    if (item.action === 'force_enable' && result.cmb_rows_updated === 0) {
      warnings.push('模型绑定未更新，请检查 raw model 映射')
    }
    if (item.action === 'force_enable' && result.ursm_v2_cleared === false) {
      warnings.push('URSM 状态未清理，运行态可能仍需重试')
    }
    emit('message', warnings.length
      ? `「${item.label}」已执行；${warnings.join('；')}`
      : `「${item.label}」执行成功`)
    pending.value = null
    emit('applied')
  } catch (e: unknown) {
    emit('error', e instanceof Error ? e.message : '紧急维护失败')
  } finally {
    repairing.value = null
  }
}
</script>

<template>
  <section class="nd-em">
    <h3>紧急维护</h3>
    <p v-if="!candidate" class="nd-em-muted">候选未加载，紧急维护暂不可用。</p>
    <template v-else>
      <p class="nd-em-intro">以下操作会立即改写运行态；执行前请确认影响范围。</p>
      <div v-if="pending" class="nd-em-confirm" role="alertdialog">
        <p class="nd-em-confirm-title">确认执行「{{ pending.label }}」？</p>
        <p class="nd-em-confirm-body">将对 {{ credentialDisplayName(candidate.credential_id) }} / {{ rawModel }} 生效。</p>
        <div class="nd-em-confirm-actions">
          <button type="button" class="btn btn-ghost btn-sm" :disabled="repairing !== null" @click="cancel">取消</button>
          <button type="button" class="btn btn-danger btn-sm" :disabled="repairing !== null" @click="confirm">
            {{ repairing ? '执行中…' : '确认执行' }}
          </button>
        </div>
      </div>
      <div class="nd-em-grid" :class="{ dimmed: !!pending }">
        <div class="nd-em-card danger" :class="{ disabled: !canEdit || !canForceDisable || repairing !== null || !!pending }">
          <strong>强制禁用</strong>
          <p>写入 manual_disabled=true，立即不可路由。</p>
          <button class="btn btn-danger btn-sm" :disabled="!canEdit || !canForceDisable || repairing !== null || !!pending" @click="request('force_disable', '强制禁用')">强制禁用</button>
        </div>
        <div class="nd-em-card success" :class="{ disabled: !canEdit || !canForceEnable || repairing !== null || !!pending }">
          <strong>强制启用</strong>
          <p>清除运行态阻塞并恢复模型 binding。</p>
          <button class="btn btn-success btn-sm" :disabled="!canEdit || !canForceEnable || repairing !== null || !!pending" @click="request('force_enable', '强制启用')">强制启用</button>
        </div>
        <div class="nd-em-card warning" :class="{ disabled: !canEdit || !canClearCircuit || repairing !== null || !!pending }">
          <strong>清除熔断</strong>
          <p>当前：{{ candidate.circuit_state || 'closed' }}</p>
          <button class="btn btn-warning btn-sm" :disabled="!canEdit || !canClearCircuit || repairing !== null || !!pending" @click="request('clear_circuit', '清除熔断状态')">清除熔断</button>
        </div>
        <div class="nd-em-card info" :class="{ disabled: !canEdit || !canResetErrors || repairing !== null || !!pending }">
          <strong>重置错误计数</strong>
          <p>连续失败：{{ candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0 }}</p>
          <button class="btn btn-sm" :disabled="!canEdit || !canResetErrors || repairing !== null || !!pending" @click="request('reset_errors', '重置错误计数')">重置计数</button>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.nd-em { border: 1px solid var(--kx-border); border-radius: 8px; padding: 14px; margin-bottom: 12px; }
.nd-em h3 { margin: 0 0 8px; font-size: 14px; }
.nd-em-intro, .nd-em-muted { margin: 0 0 10px; font-size: 12px; color: var(--kx-muted); }
.nd-em-confirm { margin-bottom: 12px; padding: 10px 12px; border-radius: 6px; border-left: 3px solid var(--kx-danger); background: color-mix(in srgb, var(--kx-danger) 10%, transparent); }
.nd-em-confirm-title { margin: 0 0 4px; font-size: 13px; font-weight: 600; }
.nd-em-confirm-body { margin: 0 0 8px; font-size: 12px; color: var(--kx-muted); }
.nd-em-confirm-actions { display: flex; gap: 8px; }
.nd-em-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.nd-em-grid.dimmed { opacity: 0.55; }
.nd-em-card { border: 1px solid var(--kx-border); border-radius: 8px; padding: 10px; background: var(--kx-surface-soft, transparent); }
.nd-em-card.disabled { opacity: 0.5; }
.nd-em-card.danger { border-left: 3px solid var(--kx-danger); }
.nd-em-card.success { border-left: 3px solid var(--kx-success); }
.nd-em-card.warning { border-left: 3px solid var(--kx-warning); }
.nd-em-card.info { border-left: 3px solid var(--accent); }
.nd-em-card strong { display: block; font-size: 13px; margin-bottom: 4px; }
.nd-em-card p { margin: 0 0 8px; font-size: 11px; color: var(--kx-muted); }
@media (max-width: 700px) { .nd-em-grid { grid-template-columns: 1fr; } }
</style>
