<script setup lang="ts">
import type { RoutingCandidate } from '../api/routing'
import type { LiveNodeStatus } from '../composables/liveStreamStore'
import { statusClass } from '../utils/nodeDetailFormat'
import { useNodeDetailDrawerLoad, type NodeDetailTab } from '../composables/useNodeDetailDrawerLoad'
import { useNodeDetailDrawerActions } from '../composables/useNodeDetailDrawerActions'
import RequestLogDrawer from './RequestLogDrawer.vue'
import NodeDetailConcurrencyPanel from './NodeDetailConcurrencyPanel.vue'
import NodeDetailOtherModelsPanel from './NodeDetailOtherModelsPanel.vue'
import NodeDetailAvailabilityPanel from './NodeDetailAvailabilityPanel.vue'
import NodeDetailEmergencyPanel from './NodeDetailEmergencyPanel.vue'
import NodeDetailOverviewPanel from './NodeDetailOverviewPanel.vue'
import NodeDetailRequestsPanel from './NodeDetailRequestsPanel.vue'
import NodeDetailMaintainPanel from './NodeDetailMaintainPanel.vue'

const props = defineProps<{
  modelValue: boolean
  node: LiveNodeStatus | null
  model?: string
  initialTab?: NodeDetailTab
  seedCandidate?: RoutingCandidate | null
  requireSuperAdminEdit?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  applied: []
}>()

const load = useNodeDetailDrawerLoad(props, emit)
const {
  activeTab, settingsSubTab, loading, detailLoaded, detailLoading, requestsLoaded, requestsLoading,
  settingsLoaded, settingsLoading, loadError, candidate, candidateLoading, monitor, selectedModel,
  windowEntries, windowStats, windowSource, history, decisions, saving, actionMessage, actionError,
  pingResult, lifecycle, manualPriority, routingTier, weight, modelActionReason, coreLoaded, coreLoading,
  detailRequestId, visible, canEdit, editGateHint, currentNode, allModels, models, selectedModelStatus,
  resolvedProviderId, headlineState, errorKinds, failedWindowEntries, failedDecisions, otherModelsNeedRefresh,
  mergeMonitorModels, chooseModel, refreshCurrentTab,
} = load

const {
  jumpToSessionSummary, openRequestDetail, closeRequestDetail,
  testNow, saveSettings, onEmergencyApplied, setCredentialDisabled, toggleSelectedModel,
} = useNodeDetailDrawerActions({
  emit, canEdit, currentNode, candidate, selectedModel, selectedModelStatus,
  saving, actionMessage, actionError, pingResult, lifecycle, manualPriority, routingTier, weight,
  modelActionReason, detailRequestId, refreshCurrentTab,
})

const node = currentNode
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="nd-mask" @click.self="visible = false" />
    <aside v-if="visible && node" class="nd-drawer" role="dialog" aria-modal="true" :aria-label="`节点 ${node.credential_id} 详情`">
      <header class="nd-header">
        <div>
          <div class="nd-eyebrow">节点 #{{ node.credential_id }} · {{ node.provider_code || `Provider ${node.provider_id ?? '—'}` }}</div>
          <h2>{{ selectedModel || '未上报模型绑定' }}</h2>
          <div class="nd-state-row">
            <span class="nd-state" :class="statusClass(headlineState === '可用' ? 'ready' : headlineState)">{{ headlineState }}</span>
            <span v-if="node.in_flight" class="nd-muted">在途 {{ node.in_flight }}</span>
            <span v-if="node.last_latency_ms != null" class="nd-muted">最近 {{ node.last_latency_ms }}ms</span>
          </div>
        </div>
        <div class="nd-header-actions">
          <button class="btn btn-sm btn-ghost" :disabled="loading || saving" @click="refreshCurrentTab">↻ 刷新</button>
          <button class="btn btn-sm btn-ghost" @click="visible = false">关闭</button>
        </div>
      </header>

      <div class="nd-tabs" role="tablist">
        <button :class="{ active: activeTab === 'detail' }" role="tab" @click="activeTab = 'detail'">明细与近期情况<small v-if="detailLoading"> · 加载中</small><small v-else-if="!detailLoaded"> · 未加载</small></button>
        <button :class="{ active: activeTab === 'availability' }" role="tab" @click="activeTab = 'availability'">可用性<small v-if="candidateLoading"> · 加载中</small></button>
        <button :class="{ active: activeTab === 'requests' }" role="tab" @click="activeTab = 'requests'">最近路由请求<small v-if="requestsLoading"> · 加载中</small><small v-else-if="!requestsLoaded"> · 未加载</small></button>
        <button :class="{ active: activeTab === 'settings' }" role="tab" @click="activeTab = 'settings'">设置与维护<small v-if="settingsLoading || coreLoading || candidateLoading"> · 加载中</small><small v-else-if="!settingsLoaded"> · 未加载</small></button>
      </div>

      <div class="nd-body">
        <p v-if="loadError" class="nd-notice nd-notice--warn">{{ loadError }}</p>
        <p v-if="actionMessage" class="nd-notice nd-notice--ok">{{ actionMessage }}</p>
        <p v-if="actionError" class="nd-notice nd-notice--error">{{ actionError }}</p>

        <NodeDetailOverviewPanel
          v-if="activeTab === 'detail'"
          :node="node"
          :detail-loading="detailLoading"
          :detail-loaded="detailLoaded"
          :candidate="candidate"
          :candidate-loading="candidateLoading"
          :models="models"
          :selected-model="selectedModel"
          :window-entries="windowEntries"
          :window-stats="windowStats"
          :window-source="windowSource"
          :error-kinds="errorKinds"
          :history="history"
          :failed-window-entries="failedWindowEntries"
          :failed-decisions="failedDecisions"
          @choose-model="chooseModel"
          @open-request="openRequestDetail"
        />

        <NodeDetailAvailabilityPanel
          v-else-if="activeTab === 'availability'"
          :candidate="candidate"
          :loading="candidateLoading || detailLoading"
        />

        <NodeDetailRequestsPanel
          v-else-if="activeTab === 'requests'"
          :requests-loading="requestsLoading"
          :requests-loaded="requestsLoaded"
          :decisions="decisions"
          @open-request="openRequestDetail"
        />

        <template v-else-if="activeTab === 'settings'">
          <div v-if="settingsLoading && !coreLoaded && !candidate" class="nd-seg-loading" role="status">设置与维护加载中…</div>
          <template v-else>
            <div class="nd-subtabs" role="tablist" aria-label="设置与维护子页">
              <button type="button" role="tab" :class="{ active: settingsSubTab === 'maintain' }" @click="settingsSubTab = 'maintain'">当前维护</button>
              <button type="button" role="tab" :class="{ active: settingsSubTab === 'other-models' }" @click="settingsSubTab = 'other-models'">其它模型</button>
            </div>
            <p v-if="editGateHint" class="nd-notice nd-notice--warn">{{ editGateHint }}</p>
            <template v-if="settingsSubTab === 'maintain'">
              <NodeDetailMaintainPanel
                :can-edit="canEdit"
                :saving="saving"
                :core-loading="coreLoading"
                :selected-model="selectedModel"
                :ping-result="pingResult"
                :candidate="candidate"
                :candidate-loading="candidateLoading"
                v-model:lifecycle="lifecycle"
                v-model:manual-priority="manualPriority"
                v-model:routing-tier="routingTier"
                v-model:weight="weight"
                v-model:model-action-reason="modelActionReason"
                :selected-model-status="selectedModelStatus"
                @test-now="testNow"
                @save-settings="saveSettings"
                @set-credential-disabled="setCredentialDisabled"
                @toggle-selected-model="toggleSelectedModel"
              >
                <template #emergency>
                  <NodeDetailEmergencyPanel
                    :candidate="candidate"
                    :raw-model="selectedModel"
                    :can-edit="canEdit"
                    @applied="onEmergencyApplied"
                    @message="actionMessage = $event"
                    @error="actionError = $event"
                  />
                </template>
                <template #concurrency>
                  <NodeDetailConcurrencyPanel
                    v-if="node"
                    :credential-id="node.credential_id"
                    :provider-id="resolvedProviderId"
                    :monitor="monitor"
                    :monitor-loading="coreLoading && !monitor"
                    :can-edit="canEdit"
                    @saved="actionMessage = '并发/指纹槽位已保存。'; emit('applied')"
                    @error="actionError = $event"
                  />
                </template>
              </NodeDetailMaintainPanel>
            </template>
            <NodeDetailOtherModelsPanel
              v-else-if="node"
              :credential-id="node.credential_id"
              :current-model="selectedModel"
              :models="allModels"
              :can-edit="canEdit"
              :needs-refresh="otherModelsNeedRefresh"
              @refreshed="mergeMonitorModels"
              @message="actionMessage = $event"
              @error="actionError = $event"
            />
          </template>
        </template>
      </div>
    </aside>
    <RequestLogDrawer
      :request-id="detailRequestId"
      stack-level="nested"
      @close="closeRequestDetail"
      @generate-session-summary="jumpToSessionSummary"
    />
  </Teleport>
</template>

<style scoped>
.nd-mask { position: fixed; inset: 0; z-index: 3000; background: color-mix(in srgb, #000 38%, transparent); }
.nd-drawer { position: fixed; z-index: 3001; top: 0; right: 0; width: min(940px, 94vw); height: 100vh; display: flex; flex-direction: column; background: var(--kx-surface); box-shadow: -12px 0 32px rgba(0,0,0,.24); color: var(--kx-text); }
.nd-header { padding: 18px 22px 14px; border-bottom: 1px solid var(--kx-border); display:flex; justify-content:space-between; gap:16px; }
.nd-eyebrow,.nd-muted,small { color: var(--kx-muted); font-size:12px; }.nd-header h2 { margin:4px 0 7px; font-size:18px; overflow-wrap:anywhere; }.nd-header-actions,.nd-state-row,.nd-actions,.nd-error-kinds { display:flex; gap:8px; align-items:center; flex-wrap:wrap; }.nd-tabs { display:flex; gap:4px; padding:10px 22px 0; border-bottom:1px solid var(--kx-border); }.nd-tabs button { border:0; background:transparent; color:var(--kx-muted); padding:8px 12px; cursor:pointer; border-bottom:2px solid transparent; }.nd-tabs button.active { color:var(--kx-primary); border-bottom-color:var(--kx-primary); font-weight:600; }.nd-subtabs { display:flex; gap:6px; margin:0 0 12px; }.nd-subtabs button { border:1px solid var(--kx-border); background:transparent; color:var(--kx-muted); padding:6px 10px; border-radius:6px; cursor:pointer; font-size:12px; }.nd-subtabs button.active { color:var(--kx-primary); border-color:var(--kx-primary); background:color-mix(in srgb, var(--kx-primary) 8%, transparent); font-weight:600; }.nd-body { overflow:auto; padding:16px 22px 34px; }.nd-section { border:1px solid var(--kx-border); border-radius:8px; padding:14px; margin-bottom:12px; }.nd-section h3 { margin:0 0 12px; font-size:14px; }.nd-grid { display:grid; grid-template-columns:repeat(3, minmax(0,1fr)); gap:12px; margin:0; }.nd-grid div { min-width:0; }.nd-grid dt { color:var(--kx-muted); font-size:11px; margin-bottom:3px; }.nd-grid dd { margin:0; font-size:12px; overflow-wrap:anywhere; }.is-ok { color:var(--kx-success); }.is-warn { color:var(--kx-warning); }.is-bad { color:var(--kx-danger); }.nd-state { border-radius:999px; padding:2px 8px; border:1px solid currentColor; font-size:12px; }.nd-models { display:flex; gap:6px; flex-wrap:wrap; }.nd-model { display:grid; gap:3px; text-align:left; padding:8px; border:1px solid var(--kx-border); border-radius:6px; background:transparent; color:inherit; cursor:pointer; max-width:250px; }.nd-model.active { border-color:var(--kx-primary); background:color-mix(in srgb, var(--kx-primary) 8%, transparent); }.nd-model span { font-size:11px; }.nd-window { display:flex; align-items:stretch; height:26px; gap:2px; overflow:hidden; }.nd-window-cell { width:5px; min-width:3px; border:0; padding:0; border-radius:2px; background:var(--kx-danger); }.nd-window-cell.ok { background:var(--kx-success); }.nd-window-cell.is-clickable { cursor:pointer; }.nd-window-cell:disabled { cursor:default; opacity:.7; }.nd-link { border:0; background:transparent; color:var(--kx-primary); cursor:pointer; padding:0; font:inherit; text-decoration:underline; }.nd-seg-loading { padding:28px; text-align:center; color:var(--kx-muted); font-size:12px; }.nd-stat-line { font-size:12px; margin-top:8px; }.nd-error-kinds span { font-size:11px; border:1px solid var(--kx-border); border-radius:999px; padding:2px 7px; }.nd-history { padding:0; list-style:none; margin:0; }.nd-history li { display:grid; grid-template-columns:140px 100px 1fr; gap:8px; border-bottom:1px solid var(--kx-border); padding:7px 0; font-size:11px; }.nd-history time { color:var(--kx-muted); }.nd-table-wrap { overflow:auto; }.nd-table { border-collapse:collapse; width:100%; font-size:11px; }.nd-table th,.nd-table td { text-align:left; padding:6px; border-bottom:1px solid var(--kx-border); white-space:nowrap; }.nd-notice { padding:8px 10px; border-radius:6px; margin:0 0 12px; font-size:12px; }.nd-notice--warn { background:color-mix(in srgb, var(--kx-warning) 12%, transparent); color:var(--kx-warning); }.nd-notice--ok { background:color-mix(in srgb, var(--kx-success) 12%, transparent); color:var(--kx-success); }.nd-notice--error { background:color-mix(in srgb, var(--kx-danger) 12%, transparent); color:var(--kx-danger); }.nd-loading { padding:36px; text-align:center; color:var(--kx-muted); }.nd-form-grid { display:grid; grid-template-columns:repeat(2, minmax(0,1fr)); gap:12px; margin-bottom:12px; }.nd-form-grid label,.nd-reason { display:grid; gap:5px; font-size:12px; }.nd-form-grid input,.nd-form-grid select,.nd-reason input { box-sizing:border-box; width:100%; padding:7px; border:1px solid var(--kx-border); border-radius:5px; background:var(--kx-bg); color:inherit; }.nd-reason { margin-bottom:10px; max-width:560px; }
@media (max-width:700px) { .nd-drawer { width:100vw; }.nd-header { padding:14px; }.nd-body { padding:12px; }.nd-grid,.nd-form-grid { grid-template-columns:1fr; }.nd-history li { grid-template-columns:1fr; gap:2px; }.nd-header { flex-direction:column; }.nd-header-actions { justify-content:flex-end; } }
</style>
