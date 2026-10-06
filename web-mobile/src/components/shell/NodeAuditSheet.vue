<script setup lang="ts">
// NodeAuditSheet — 节点操作审计（GET /api/admin/audit/node-operations）。
// 挂在**供应商**维度而非凭据维度 —— 依据是后端覆盖面实测，见 api/nodeAudit.ts 头注。
import { computed, ref, watch } from 'vue'
import { fetchNodeAudit, operationLabel, type NodeAuditEntry } from '@/api/nodeAudit'
import { t } from '@/i18n'
import AppSheet from '@/components/common/AppSheet.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { relativeTime } from '@/utils/format'

const props = defineProps<{ providerId: number | null; providerName: string }>()
const openProxy = computed({
  get: () => props.providerId != null,
  set: (v: boolean) => {
    if (!v) emitClose()
  },
})
const emit = defineEmits<{ close: [] }>()
function emitClose(): void {
  emit('close')
}

const entries = ref<NodeAuditEntry[] | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(providerId: number): Promise<void> {
  loading.value = true
  error.value = null
  entries.value = null
  try {
    const resp = await fetchNodeAudit({ provider_id: providerId, limit: 30 })
    entries.value = resp.entries
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.providerId,
  (id) => {
    if (id != null) void load(id)
  },
  { immediate: true },
)

function tone(e: NodeAuditEntry): 'success' | 'warning' | 'danger' | 'muted' {
  if (e.status && e.status !== 'ok' && e.status !== 'success') return 'warning'
  if (e.operation === 'enable_toggle') {
    return e.enabled ? 'success' : 'muted'
  }
  return 'muted'
}

/** from→to 只在两端都有值时展示；单边有值不拼成半截箭头。 */
function transition(e: NodeAuditEntry): string | null {
  if (e.from_state && e.to_state) return `${e.from_state} → ${e.to_state}`
  return e.to_state || e.from_state || null
}
</script>

<template>
  <AppSheet v-model="openProxy" presentation="sheet" :title="t('audit.title')">
    <div class="audit__scope">
      {{ t('audit.scopeProvider', { name: providerName }) }}
    </div>
    <p class="audit__scope-hint">{{ t('audit.coverageHint') }}</p>

    <p v-if="loading" class="audit__state">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="audit__state audit__state--err">{{ error }}</p>
    <p v-else-if="entries && entries.length === 0" class="audit__state">{{ t('audit.empty') }}</p>

    <ul v-else-if="entries" class="audit__list">
      <li v-for="(e, i) in entries" :key="`${e.request_id}-${i}`" class="audit__item">
        <div class="card-row">
          <span class="audit__op">
            <StatusDot :tone="tone(e)" />
            {{ operationLabel(e.operation, t) }}
          </span>
          <span class="audit__time">{{ relativeTime(e.created_at) }}</span>
        </div>
        <div v-if="transition(e)" class="audit__line">{{ transition(e) }}</div>
        <div v-if="e.reason" class="audit__reason">{{ e.reason }}</div>
        <div class="audit__meta">
          <span v-if="e.operator_id">{{ t('audit.operator', { id: e.operator_id }) }}</span>
          <span v-if="e.source">{{ t('audit.source', { v: e.source }) }}</span>
          <span v-if="typeof e.latency_ms === 'number'">{{ e.latency_ms }}ms</span>
        </div>
      </li>
    </ul>
  </AppSheet>
</template>

<style scoped>
.audit__scope {
  font-weight: 600;
  font-size: 0.9375rem;
}

.audit__scope-hint {
  margin-top: var(--app-space-1);
  margin-bottom: var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.audit__state {
  color: var(--app-text-secondary);
  font-size: 0.875rem;
  padding: var(--app-space-3) 0;
}

.audit__state--err {
  color: var(--app-danger);
}

.audit__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.audit__item {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding-bottom: var(--app-space-3);
  border-bottom: 1px solid var(--app-border-subtle);
}

.audit__op {
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-2);
  font-weight: 600;
  font-size: 0.875rem;
}

.audit__time {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.audit__line {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  font-variant-numeric: tabular-nums;
}

.audit__reason {
  font-size: 0.8125rem;
  color: var(--app-text);
}

.audit__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}
</style>
