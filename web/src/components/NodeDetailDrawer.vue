<script setup lang="ts">
import type { RoutingCandidate } from '../api/routing'
import type { LiveNodeStatus } from '../composables/liveStreamStore'
import { statusClass } from '../utils/nodeDetailFormat'
import { credentialDisplayName } from '../composables/useCredentialLabels'
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
  activeTab, otherModelsLoaded, otherModelsLoading, loading, detailLoaded, detailLoading, requestsLoaded, requestsLoading,
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
    <aside v-if="visible && node" class="nd-drawer" role="dialog" aria-modal="true" :aria-label="`${credentialDisplayName(node.credential_id)} 详情`">
      <header class="nd-header">
        <div>
          <div class="nd-eyebrow">{{ credentialDisplayName(node.credential_id) }} · {{ node.provider_code || `Provider ${node.provider_id ?? '—'}` }}</div>
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
        <button :class="{ active: activeTab === 'other-models' }" role="tab" @click="activeTab = 'other-models'">其它模型<small v-if="otherModelsLoading || (otherModelsNeedRefresh && detailLoading)"> · 加载中</small><small v-else-if="!otherModelsLoaded"> · 未加载</small></button>
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

        <template v-else-if="activeTab === 'other-models'">
          <div v-if="otherModelsLoading && !otherModelsLoaded" class="nd-seg-loading" role="status">其它模型加载中…</div>
          <template v-else-if="node">
            <p v-if="editGateHint" class="nd-notice nd-notice--warn">{{ editGateHint }}</p>
            <NodeDetailOtherModelsPanel
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

        <template v-else-if="activeTab === 'settings'">
          <div v-if="settingsLoading && !coreLoaded && !candidate" class="nd-seg-loading" role="status">设置与维护加载中…</div>
          <template v-else>
            <p v-if="editGateHint" class="nd-notice nd-notice--warn">{{ editGateHint }}</p>
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
.nd-eyebrow,.nd-muted,small { color: var(--kx-muted); font-size:12px; }
.nd-header h2 { margin:4px 0 7px; font-size:18px; overflow-wrap:anywhere; }
.nd-header-actions,.nd-state-row { display:flex; gap:8px; align-items:center; flex-wrap:wrap; }
.nd-tabs { display:flex; gap:4px; padding:10px 22px 0; border-bottom:1px solid var(--kx-border); flex-wrap:wrap; }
.nd-tabs button { border:0; background:transparent; color:var(--kx-muted); padding:8px 12px; cursor:pointer; border-bottom:2px solid transparent; }
.nd-tabs button.active { color:var(--kx-primary); border-bottom-color:var(--kx-primary); font-weight:600; }
.nd-body { overflow:auto; padding:16px 22px 34px; }
.nd-seg-loading { padding:28px; text-align:center; color:var(--kx-muted); font-size:12px; }
.nd-notice { padding:8px 10px; border-radius:6px; margin:0 0 12px; font-size:12px; }
.nd-notice--warn { background:color-mix(in srgb, var(--kx-warning) 12%, transparent); color:var(--kx-warning); }
.nd-notice--ok { background:color-mix(in srgb, var(--kx-success) 12%, transparent); color:var(--kx-success); }
.nd-notice--error { background:color-mix(in srgb, var(--kx-danger) 12%, transparent); color:var(--kx-danger); }
@media (max-width:700px) { .nd-drawer { width:100vw; }.nd-header { padding:14px; flex-direction:column; }.nd-body { padding:12px; }.nd-header-actions { justify-content:flex-end; } }
</style>
<style src="../styles/node-detail-drawer.css"></style>
