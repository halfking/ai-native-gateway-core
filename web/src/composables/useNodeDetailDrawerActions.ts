import type { Ref, ComputedRef } from 'vue'
import type { RoutingCandidate } from '../api/routing'
import { patchCandidateBinding } from '../api/routing'
import { updateCredentialLifecycle, type CredentialLifecycleStatus } from '../api/providers'
import {
  sessionPingCredential, setManualDisabled, toggleModelAvailability, type CredentialModelStatus,
} from '../api/credential-monitor'
import type { LiveNodeStatus } from '../composables/liveStreamStore'
import { useSessionSummaryJump } from '../composables/useSessionSummaryJump'
import { openRequestDetailPage } from '../utils/openRequestDetailPage'

type PingResult = { status: string; latency_ms: number; tested_at: string; error?: string }

export function useNodeDetailDrawerActions(opts: {
  emit: { (e: 'applied'): void }
  canEdit: ComputedRef<boolean>
  currentNode: ComputedRef<LiveNodeStatus | null>
  candidate: Ref<RoutingCandidate | null>
  selectedModel: Ref<string>
  selectedModelStatus: ComputedRef<CredentialModelStatus | null>
  saving: Ref<boolean>
  actionMessage: Ref<string>
  actionError: Ref<string>
  pingResult: Ref<PingResult | null>
  lifecycle: Ref<CredentialLifecycleStatus>
  manualPriority: Ref<number>
  priorityFlag: Ref<boolean>
  routingTier: Ref<number>
  weight: Ref<number>
  modelActionReason: Ref<string>
  detailRequestId: Ref<string | null>
  refreshCurrentTab: () => Promise<void>
}) {
  const {
    emit, canEdit, currentNode, candidate, selectedModel, selectedModelStatus,
    saving, actionMessage, actionError, pingResult, lifecycle, manualPriority,
    priorityFlag, routingTier, weight, modelActionReason, detailRequestId, refreshCurrentTab,
  } = opts

  const { jumpToSessionSummary } = useSessionSummaryJump({
    onBeforeJump: () => { detailRequestId.value = null },
  })

  function openRequestDetail(rid: string | undefined) {
    if (!rid) return
    openRequestDetailPage(rid)
    detailRequestId.value = null
  }
  function closeRequestDetail() { detailRequestId.value = null }

  async function testNow() {
    const node = currentNode.value
    if (!node || !selectedModel.value || !canEdit.value || saving.value) return
    saving.value = true; actionMessage.value = ''; actionError.value = ''; pingResult.value = null
    try {
      const result = await sessionPingCredential(node.credential_id, selectedModel.value)
      pingResult.value = result
      if (result.status === 'healthy') actionMessage.value = `会话 Ping 成功：${result.latency_ms}ms`
      else actionError.value = result.error || `会话 Ping 失败：${result.status}`
    } catch (error) {
      actionError.value = error instanceof Error ? error.message : '会话 Ping 失败'
    } finally { saving.value = false }
  }

  async function saveSettings() {
    const node = currentNode.value
    const row = candidate.value
    if (!node || !row || !canEdit.value || saving.value) return
    saving.value = true; actionMessage.value = ''; actionError.value = ''
    try {
      const patch: { manual_priority?: number; priority?: boolean; routing_tier?: number; weight?: number } = {}
      if (manualPriority.value !== (row.manual_priority ?? 99)) patch.manual_priority = manualPriority.value
      if (priorityFlag.value !== (row.priority ?? false)) patch.priority = priorityFlag.value
      if (routingTier.value !== row.tier) patch.routing_tier = routingTier.value
      if (weight.value !== row.weight) patch.weight = weight.value
      if (Object.keys(patch).length) await patchCandidateBinding(row.credential_id, row.model_name, patch)
      if (lifecycle.value !== (row.lifecycle_status ?? 'active')) {
        await updateCredentialLifecycle(node.provider_id!, node.credential_id, lifecycle.value)
      }
      actionMessage.value = '设置已保存，正在等待实时状态对账。'
      emit('applied')
      await refreshCurrentTab()
    } catch (error) {
      actionError.value = error instanceof Error ? error.message : '保存设置失败'
    } finally { saving.value = false }
  }

  async function onEmergencyApplied() { emit('applied'); await refreshCurrentTab() }

  async function setCredentialDisabled(disabled: boolean) {
    const node = currentNode.value
    if (!node || saving.value || !canEdit.value) return
    const reason = modelActionReason.value.trim()
    if (!reason) { actionError.value = '请输入维护原因。'; return }
    saving.value = true
    try {
      await setManualDisabled(node.credential_id, disabled, reason)
      actionMessage.value = disabled ? '凭据已手工禁用。' : '凭据已恢复。'
      modelActionReason.value = ''
      emit('applied')
      await refreshCurrentTab()
    } catch (error) {
      actionError.value = error instanceof Error ? error.message : '维护失败'
    } finally { saving.value = false }
  }

  async function toggleSelectedModel() {
    const node = currentNode.value
    const model = selectedModelStatus.value
    if (!node || !model || saving.value || !canEdit.value) return
    const reason = modelActionReason.value.trim()
    if (!reason) { actionError.value = '请输入维护原因。'; return }
    const action = model.binding_unavailable_reason === 'manual_offline' ? 'online' : 'offline'
    saving.value = true
    try {
      await toggleModelAvailability(node.credential_id, model.raw_model_name, action, reason)
      actionMessage.value = action === 'online' ? '模型已恢复上线。' : '模型已手工下线。'
      modelActionReason.value = ''
      emit('applied')
      await refreshCurrentTab()
    } catch (error) {
      actionError.value = error instanceof Error ? error.message : '模型维护失败'
    } finally { saving.value = false }
  }

  return {
    jumpToSessionSummary, openRequestDetail, closeRequestDetail,
    testNow, saveSettings, onEmergencyApplied, setCredentialDisabled, toggleSelectedModel,
  }
}
