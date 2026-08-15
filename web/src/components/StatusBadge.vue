<template>
  <span :class="['status-badge', `status-badge--${state}`]" :title="reason || defaultTooltip">
    <span class="status-badge-dot" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ModelEffectiveState } from '../api/credential-monitor'

const props = withDefaults(
  defineProps<{
    state: ModelEffectiveState | string
    reason?: string
  }>(),
  { reason: '' },
)

interface StateMeta {
  label: string
  defaultTooltip: string
}

const META: Record<ModelEffectiveState, StateMeta> = {
  available:       { label: '可用',       defaultTooltip: '模型可用,所有依赖项健康' },
  manual_disabled: { label: '已禁用',     defaultTooltip: '被管理员手动禁用' },
  probe_broken:    { label: '探测失败',   defaultTooltip: '探测系统标记为 broken_confirmed' },
  offer_missing:   { label: '未声明',     defaultTooltip: 'model_offers 缺失' },
  binding_missing: { label: '绑定缺失',   defaultTooltip: 'credential_model_bindings 不可用' },
}

const label = computed(() => META[props.state as ModelEffectiveState]?.label ?? props.state)
const defaultTooltip = computed(() => META[props.state as ModelEffectiveState]?.defaultTooltip ?? '')
</script>

<style scoped>
/* Status badge (2026-06-23) — 5 状态统一色阶 badge, 详情页模型可用性表用.
   颜色尽量和现有 .badge / .state-pill 风格保持一致 (style.css:79-85). */
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: 99px;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
  border: 1px solid transparent;
  white-space: nowrap;
}
.status-badge-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  flex-shrink: 0;
}

.status-badge--available {
  color: var(--success);
  background: var(--success-soft);
  border-color: color-mix(in srgb, var(--success) 30%, transparent);
}
.status-badge--manual_disabled {
  color: var(--danger);
  background: var(--danger-soft);
  border-color: color-mix(in srgb, var(--danger) 30%, transparent);
}
.status-badge--probe_broken {
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 18%, var(--danger-soft));
  border-color: color-mix(in srgb, var(--danger) 40%, transparent);
}
.status-badge--offer_missing {
  color: var(--muted);
  background: color-mix(in srgb, var(--muted) 12%, transparent);
  border-color: color-mix(in srgb, var(--muted) 30%, transparent);
}
.status-badge--binding_missing {
  color: var(--warning);
  background: var(--warning-soft);
  border-color: color-mix(in srgb, var(--warning) 30%, transparent);
}
</style>
