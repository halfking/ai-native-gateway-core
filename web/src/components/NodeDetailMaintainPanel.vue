<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { RoutingCandidate } from '../api/routing'
import {
  CREDENTIAL_LIFECYCLE_STATUSES, type CredentialLifecycleStatus,
} from '../api/providers'
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
  // 2026-09-05: manual-disabled display for the settings tab (source:
  // monitor.manual_disabled / live node), independent from lifecycle_status.
  // Unban goes through setCredentialDisabled(false).
  manualDisabled: boolean
  manualStateDetail: string
  manualPriority: number
  priorityFlag: boolean
  routingTier: number
  weight: number
  modelActionReason: string
  selectedModelStatus: CredentialModelStatus | null
}>()

const emit = defineEmits<{
  'update:manualPriority': [value: number]
  'update:priorityFlag': [value: boolean]
  'update:routingTier': [value: number]
  'update:weight': [value: number]
  'update:modelActionReason': [value: string]
  // 2026-09-05: lifecycle is now a direct-manipulation state chip group
  // (click saves immediately), replacing the old select + save button.
  lifecycleChange: [value: CredentialLifecycleStatus]
  testNow: []
  saveSettings: []
  setCredentialDisabled: [disabled: boolean]
  toggleSelectedModel: []
}>()

const { t } = useI18n()

// Lifecycle state chips: dot color follows semantics
// (active=green, disabled=red, suspended=yellow, retired=grey). Labels and
// tooltips are i18n keys so the chip group stays translatable.
const lifecycleOptions = [
  { value: 'active', labelKey: 'providers.lifecycleLabelActive', hintKey: 'providers.lifecycleHintActive' },
  { value: 'disabled', labelKey: 'providers.lifecycleLabelDisabled', hintKey: 'providers.lifecycleHintDisabled' },
  { value: 'suspended', labelKey: 'providers.lifecycleLabelSuspended', hintKey: 'providers.lifecycleHintSuspended' },
  { value: 'retired', labelKey: 'providers.lifecycleLabelRetired', hintKey: 'providers.lifecycleHintRetired' },
] as const satisfies ReadonlyArray<{
  value: CredentialLifecycleStatus
  labelKey: string
  hintKey: string
}>
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

  <section class="nd-section">
    <h3>{{ t('providers.lifecycleTitle') }} <small>{{ t('providers.lifecycleHint') }}</small></h3>
    <div class="nd-lifecycle-row" role="radiogroup" :aria-label="t('providers.lifecycleTitle')">
      <button
        v-for="opt in lifecycleOptions"
        :key="opt.value"
        type="button"
        role="radio"
        class="nd-lc-chip"
        :class="[`nd-lc-chip--${opt.value}`, { 'is-current': lifecycle === opt.value }]"
        :aria-checked="lifecycle === opt.value"
        :disabled="!canEdit || saving"
        :title="t(opt.hintKey)"
        @click="emit('lifecycleChange', opt.value)"
      >
        <span class="nd-lc-dot" aria-hidden="true" />
        <span class="nd-lc-label">{{ t(opt.labelKey) }}</span>
        <code class="nd-lc-code">{{ opt.value }}</code>
        <span v-if="lifecycle === opt.value" class="nd-lc-current">{{ t('providers.lifecycleCurrent') }}</span>
      </button>
    </div>
  </section>

  <section v-if="candidateLoading && !candidate" class="nd-section">
    <h3>路由排序</h3>
    <div class="nd-seg-loading" role="status">候选排序加载中…</div>
  </section>
  <section v-else-if="candidate" class="nd-section">
    <h3>路由排序 <small>{{ selectedModel }}</small></h3>
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
    </div>
    <button class="btn btn-primary btn-sm" :disabled="saving || !canEdit" @click="emit('saveSettings')">保存设置</button>
  </section>

  <slot name="concurrency" />

  <section class="nd-section">
    <h3>{{ t('providers.manualDisableTitle') }}</h3>
    <div class="nd-manual-row">
      <span v-if="manualDisabled" class="pill pill--err" data-testid="manual-disabled-badge">{{ t('providers.manualDisabledBadge') }}</span>
      <span v-else class="pill pill--ok" data-testid="manual-disabled-badge">{{ t('providers.manualNormalBadge') }}</span>
      <span v-if="manualDisabled && manualStateDetail" class="nd-muted">{{ manualStateDetail }}</span>
      <button
        v-if="manualDisabled"
        class="btn btn-success btn-sm"
        :disabled="saving || !canEdit"
        :title="t('providers.manualUnbanActionTitle')"
        @click="emit('setCredentialDisabled', false)"
      >
        {{ t('providers.manualUnbanAction') }}
      </button>
      <button
        v-else
        class="btn btn-danger btn-sm"
        :disabled="saving || !canEdit"
        :title="t('providers.manualDisableActionTitle')"
        @click="emit('setCredentialDisabled', true)"
      >
        {{ t('providers.manualDisableAction') }}
      </button>
    </div>
    <p class="nd-muted nd-manual-hint">{{ t('providers.manualIndependenceHint') }}</p>
  </section>

  <section class="nd-section">
    <h3>模型维护</h3>
    <label class="nd-reason">维护原因（必填）
      <input
        :disabled="!canEdit"
        placeholder="说明本次状态修改原因"
        :value="modelActionReason"
        @input="emit('update:modelActionReason', ($event.target as HTMLInputElement).value)"
      />
    </label>
    <div class="nd-actions">
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
/* ── lifecycle state chips ──────────────────────────── */
.nd-lifecycle-row { display: flex; gap: 8px; flex-wrap: wrap; }
.nd-lc-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: 16px;
  border: 1px solid var(--kx-border, var(--border));
  background: var(--kx-surface-soft, transparent);
  color: var(--kx-muted, var(--muted));
  font-size: 12px;
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s, color 0.15s;
}
.nd-lc-chip:hover:not(:disabled) { border-color: var(--kx-primary, var(--accent)); color: var(--kx-text, var(--fg)); }
.nd-lc-chip:disabled { opacity: 0.55; cursor: not-allowed; }
.nd-lc-chip.is-current { font-weight: 600; cursor: default; }
.nd-lc-dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; }
code.nd-lc-code { font-size: 10.5px; opacity: 0.75; }
.nd-lc-current {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 8px;
  background: var(--kx-primary-soft, var(--accent-soft, rgba(0, 0, 0, 0.06)));
  color: var(--kx-primary, var(--accent));
}
/* Current-state coloring (only the chip marked is-current gets the tint). */
.nd-lc-chip--active.is-current { border-color: color-mix(in srgb, var(--kx-success, #22a06b) 55%, var(--kx-border, var(--border))); color: var(--kx-success, #22a06b); background: color-mix(in srgb, var(--kx-success, #22a06b) 10%, transparent); }
.nd-lc-chip--disabled.is-current { border-color: color-mix(in srgb, var(--kx-danger, #d64545) 55%, var(--kx-border, var(--border))); color: var(--kx-danger, #d64545); background: color-mix(in srgb, var(--kx-danger, #d64545) 10%, transparent); }
.nd-lc-chip--suspended.is-current { border-color: color-mix(in srgb, var(--kx-warning, #d9a441) 55%, var(--kx-border, var(--border))); color: var(--kx-warning, #d9a441); background: color-mix(in srgb, var(--kx-warning, #d9a441) 10%, transparent); }
.nd-lc-chip--retired.is-current { color: var(--kx-muted, var(--muted)); }

/* ── manual-disabled status row ─────────────────────── */
.nd-manual-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.nd-manual-hint { margin: 8px 0 0; font-size: 11px; }

.nd-priority-flag { display: flex; flex-direction: column; gap: 2px; }
.nd-priority-flag small { color: var(--kx-muted, var(--muted)); font-size: 11px; }
.nd-priority-flag input[type='checkbox'] { width: 16px; height: 16px; margin: 4px 0 0; }
</style>
