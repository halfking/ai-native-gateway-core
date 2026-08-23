<script setup lang="ts">
import type { RoutingCandidate } from '../api/routing'
import type { CredentialLifecycleStatus } from '../api/providers'
import type { CredentialModelStatus } from '../api/credential-monitor'
import { fmtTime } from '../utils/nodeDetailFormat'

defineProps<{
  canEdit: boolean
  saving: boolean
  coreLoading: boolean
  selectedModel: string
  pingResult: { status: string; latency_ms: number; tested_at: string; error?: string } | null
  candidate: RoutingCandidate | null
  candidateLoading: boolean
  lifecycle: CredentialLifecycleStatus
  manualPriority: number
  priorityFlag: boolean
  routingTier: number
  weight: number
  modelActionReason: string
  selectedModelStatus: CredentialModelStatus | null
}>()

const emit = defineEmits<{
  'update:lifecycle': [value: CredentialLifecycleStatus]
  'update:manualPriority': [value: number]
  'update:priorityFlag': [value: boolean]
  'update:routingTier': [value: number]
  'update:weight': [value: number]
  'update:modelActionReason': [value: string]
  testNow: []
  saveSettings: []
  setCredentialDisabled: [disabled: boolean]
  toggleSelectedModel: []
}>()
</script>

<template>
  <section class="nd-section">
    <h3>连通性 <small v-if="coreLoading">核心状态加载中…</small></h3>
    <div class="nd-actions">
      <button class="btn btn-primary btn-sm" :disabled="saving || !canEdit || !selectedModel" @click="emit('testNow')">
        {{ saving ? '处理中…' : '会话 Ping' }}
      </button>
      <span v-if="pingResult" class="nd-muted">{{ pingResult.status }} · {{ pingResult.latency_ms }}ms · {{ fmtTime(pingResult.tested_at) }}</span>
    </div>
  </section>

  <slot name="emergency" />

  <section v-if="candidateLoading && !candidate" class="nd-section">
    <h3>路由排序与生命周期</h3>
    <div class="nd-seg-loading" role="status">候选排序加载中…</div>
  </section>
  <section v-else-if="candidate" class="nd-section">
    <h3>路由排序与生命周期 <small>{{ selectedModel }}</small></h3>
    <div class="nd-form-grid">
      <label title="排序序号：数字越小越靠前，只决定同组候选的先后顺序，不保证独占流量。">
        排序序号
        <input
          type="number" min="0" max="99" :disabled="!canEdit"
          :value="manualPriority"
          @input="emit('update:manualPriority', Number(($event.target as HTMLInputElement).value))"
        />
      </label>
      <label
        class="nd-priority-flag"
        title="优先凭据：额度（quota）充足时优先承接全部流量，额度耗尽自动让位给其它凭据。是布尔标志，与上方「排序序号」无关。"
      >
        优先凭据
        <input
          type="checkbox" :disabled="!canEdit"
          :checked="priorityFlag"
          @change="emit('update:priorityFlag', ($event.target as HTMLInputElement).checked)"
        />
        <small>额度充足时优先承接 · 与排序序号无关</small>
      </label>
      <label>Routing Tier
        <input
          type="number" min="0" max="9" :disabled="!canEdit"
          :value="routingTier"
          @input="emit('update:routingTier', Number(($event.target as HTMLInputElement).value))"
        />
      </label>
      <label>权重
        <input
          type="number" min="0" max="10000" :disabled="!canEdit"
          :value="weight"
          @input="emit('update:weight', Number(($event.target as HTMLInputElement).value))"
        />
      </label>
      <label>生命周期
        <select
          :disabled="!canEdit"
          :value="lifecycle"
          @change="emit('update:lifecycle', ($event.target as HTMLSelectElement).value as CredentialLifecycleStatus)"
        >
          <option value="active">active（在用）</option>
          <option value="disabled">disabled（停用）</option>
          <option value="suspended">suspended（暂停）</option>
          <option value="retired">retired（退役）</option>
        </select>
      </label>
    </div>
    <button class="btn btn-primary btn-sm" :disabled="saving || !canEdit" @click="emit('saveSettings')">保存设置</button>
  </section>

  <slot name="concurrency" />

  <section class="nd-section">
    <h3>凭据与模型维护</h3>
    <label class="nd-reason">维护原因（必填）
      <input
        :disabled="!canEdit"
        placeholder="说明本次状态修改原因"
        :value="modelActionReason"
        @input="emit('update:modelActionReason', ($event.target as HTMLInputElement).value)"
      />
    </label>
    <div class="nd-actions">
      <button class="btn btn-danger btn-sm" :disabled="saving || !canEdit" @click="emit('setCredentialDisabled', true)">禁用凭据</button>
      <button class="btn btn-success btn-sm" :disabled="saving || !canEdit" @click="emit('setCredentialDisabled', false)">恢复凭据</button>
      <button
        v-if="selectedModelStatus"
        class="btn btn-sm"
        :disabled="saving || !canEdit"
        @click="emit('toggleSelectedModel')"
      >
        {{ selectedModelStatus.binding_unavailable_reason === 'manual_offline' ? '恢复当前模型' : '下线当前模型' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.nd-priority-flag { display: flex; flex-direction: column; gap: 2px; }
.nd-priority-flag small { color: var(--kx-muted, #888); font-size: 11px; }
.nd-priority-flag input[type='checkbox'] { width: 16px; height: 16px; margin: 4px 0 0; }
</style>
