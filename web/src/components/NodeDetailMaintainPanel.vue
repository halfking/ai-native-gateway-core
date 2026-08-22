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
  routingTier: number
  weight: number
  modelActionReason: string
  selectedModelStatus: CredentialModelStatus | null
}>()

const emit = defineEmits<{
  'update:lifecycle': [value: CredentialLifecycleStatus]
  'update:manualPriority': [value: number]
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
      <label>人工优先级
        <input
          type="number" min="0" max="99" :disabled="!canEdit"
          :value="manualPriority"
          @input="emit('update:manualPriority', Number(($event.target as HTMLInputElement).value))"
        />
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
