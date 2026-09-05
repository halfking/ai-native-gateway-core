<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { emergencyRepair, type EmergencyRepairAction, type RoutingCandidate } from '../api/routing'
import { useCredentialLabels } from '../composables/useCredentialLabels'
import { confirmDialog } from '../composables/useConfirmDialog'
import { useI18n } from 'vue-i18n'

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

const { t } = useI18n()
// credentialDisplayName resolves credential id → human label; the composable
// keeps a Map<id,label> refreshed via loadCredentialLabels(), and falls back
// to "凭据 #ID" if the label is missing.
const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()

const repairing = ref<EmergencyRepairAction | null>(null)

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

async function request(action: EmergencyRepairAction, label: string) {
  if (!props.canEdit || repairing.value || !props.rawModel || !props.candidate) return
  // 统一确认交互（useConfirmDialog）：语义与原内联 confirm 一致——
  // 用户确认后才执行紧急维护动作，取消则直接返回。
  const confirmed = await confirmDialog(
    t('providers.emergencyConfirmAction', {
      action: label,
      credential: credentialDisplayName(props.candidate.credential_id),
      model: props.rawModel,
    }),
  )
  if (!confirmed) return
  if (action === 'force_enable' && !hasRuntimeBlock.value) {
    emit('message', t('providers.noNeedForceEnable'))
    return
  }
  repairing.value = action
  try {
    const result = await emergencyRepair({
      credential_id: props.candidate.credential_id,
      raw_model: props.rawModel,
      action,
      reason: `admin via node detail drawer: ${label}`,
    })
    const warnings: string[] = []
    if (action === 'force_enable' && result.cmb_rows_updated === 0) {
      warnings.push(t('providers.warningCmbNotUpdated'))
    }
    if (action === 'force_enable' && result.ursm_v2_cleared === false) {
      warnings.push(t('providers.warningUrsmNotCleared'))
    }
    emit('message', warnings.length
      ? t('providers.actionDoneWithWarnings', { action: label, warnings: warnings.join('；') })
      : t('providers.actionDone', { action: label }))
    emit('applied')
  } catch (e: unknown) {
    emit('error', e instanceof Error ? e.message : t('providers.emergencyFailed'))
  } finally {
    repairing.value = null
  }
}
</script>

<template>
  <section class="nd-em">
    <h3>{{ t('providers.emergencyTitle') }}</h3>
    <p v-if="!candidate" class="nd-em-muted">{{ t('providers.emergencyUnavailable') }}</p>
    <template v-else>
      <p class="nd-em-intro">{{ t('providers.emergencyIntro') }}</p>
      <div class="nd-em-grid">
        <div class="nd-em-card danger" :class="{ disabled: !canEdit || !canForceDisable || repairing !== null }">
          <strong>{{ t('providers.forceDisable') }}</strong>
          <p>{{ t('providers.forceDisableDesc') }}</p>
          <button class="btn btn-danger btn-sm" :disabled="!canEdit || !canForceDisable || repairing !== null" @click="request('force_disable', t('providers.forceDisable'))">{{ t('providers.forceDisable') }}</button>
        </div>
        <div class="nd-em-card success" :class="{ disabled: !canEdit || !canForceEnable || repairing !== null }">
          <strong>{{ t('providers.forceEnable') }}</strong>
          <p>{{ t('providers.forceEnableDesc') }}</p>
          <button class="btn btn-success btn-sm" :disabled="!canEdit || !canForceEnable || repairing !== null" @click="request('force_enable', t('providers.forceEnable'))">{{ t('providers.forceEnable') }}</button>
        </div>
        <div class="nd-em-card warning" :class="{ disabled: !canEdit || !canClearCircuit || repairing !== null }">
          <strong>{{ t('providers.clearCircuit') }}</strong>
          <p>{{ t('providers.clearCircuitDesc') }}{{ candidate.circuit_state || 'closed' }}</p>
          <button class="btn btn-warning btn-sm" :disabled="!canEdit || !canClearCircuit || repairing !== null" @click="request('clear_circuit', t('providers.clearCircuitAction'))">{{ t('providers.clearCircuit') }}</button>
        </div>
        <div class="nd-em-card info" :class="{ disabled: !canEdit || !canResetErrors || repairing !== null }">
          <strong>{{ t('providers.resetErrors') }}</strong>
          <p>{{ t('providers.consecutiveFailures') }}{{ candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0 }}</p>
          <button class="btn btn-sm" :disabled="!canEdit || !canResetErrors || repairing !== null" @click="request('reset_errors', t('providers.resetErrors'))">{{ t('providers.resetCountButton') }}</button>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.nd-em { border: 1px solid var(--kx-border); border-radius: 8px; padding: 14px; margin-bottom: 12px; }
.nd-em h3 { margin: 0 0 8px; font-size: 14px; }
.nd-em-intro, .nd-em-muted { margin: 0 0 10px; font-size: 12px; color: var(--kx-muted); }
.nd-em-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
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
