<script setup lang="ts">
// NodesView.vue — 节点（凭据健康）：卡片列表；点开 Sheet 看摘要；
// 每模型明细是宽表 → 「全页查看」进专注工作区（重要表格全页查看，07 §8）。
import { computed, onMounted, ref } from 'vue'
import {
  fetchMonitorSummary,
  normalizeMonitorPayload,
  type CredentialMonitorSummary,
  type CredentialModelStatus,
} from '../api/credentials'
import { useHyperPage } from '../composables/useHyperPage'
import { focusWorkspace, type FocusHandle } from '../runtime/focusWorkspace'
import { useTitleStore } from '../stores/titleStore'
import SkeletonList from '../components/ui/SkeletonList.vue'
import ErrorRetry from '../components/ui/ErrorRetry.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import Sheet from '../components/ui/Sheet.vue'
import Icon from '../components/Icon.vue'
import FocusWorkspace from '../components/focus/FocusWorkspace.vue'
import { t } from '../i18n'

const titleStore = useTitleStore()
useHyperPage({ routeTitle: t('nodes.title') })

const nodes = ref<CredentialMonitorSummary[]>([])
const loading = ref(true)
const loadError = ref('')
const openId = ref<number | null>(null)
const focusHandle = ref<FocusHandle | null>(null)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const payload = await fetchMonitorSummary()
    nodes.value = normalizeMonitorPayload(payload)
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

const openNode = computed(() => nodes.value.find((n) => n.id === openId.value) ?? null)

function toneOf(node: CredentialMonitorSummary): 'success' | 'warning' | 'danger' | 'info' {
  if (node.manual_disabled) return 'info'
  if (node.effective_state === 'healthy' || node.availability_state === 'available') return 'success'
  if (node.broken_model_count > 0 || node.consecutive_failures > 0) return 'warning'
  if (node.effective_state === 'down' || node.availability_state === 'unavailable') return 'danger'
  return 'info'
}

function labelOf(node: CredentialMonitorSummary): string {
  if (node.manual_disabled) return 'manual'
  if (node.effective_state) return node.effective_state
  return node.availability_state || node.status
}

function openFocus(node: CredentialMonitorSummary, e: Event): void {
  focusHandle.value = focusWorkspace().open({
    title: `${node.provider_name} · ${t('nodes.modelTable')}`,
    triggerEl: (e.currentTarget as HTMLElement) ?? null,
  })
}

function availTone(m: CredentialModelStatus): string {
  if (!m.offer_available || !m.binding_available) return 'danger'
  if (m.probe_state === 'healthy_confirmed') return 'success'
  if (m.probe_state === 'recovering' || m.probe_state === 'unknown') return 'warning'
  return 'success'
}

onMounted(() => {
  titleStore.setRegistered(t('nodes.title'))
  void load()
})
</script>

<template>
  <section class="m-page">
    <SkeletonList v-if="loading" :lines="5" />
    <ErrorRetry v-else-if="loadError" :message="loadError" @retry="load" />
    <EmptyState v-else-if="nodes.length === 0" :message="t('nodes.noNodes')" />

    <template v-else>
      <button
        v-for="node in nodes"
        :key="node.id"
        type="button"
        class="m-card node-card"
        :data-row-id="`node-${node.id}`"
        @click="openId = node.id"
      >
        <div class="m-card__row">
          <span class="node-name">{{ node.label || `#${node.id}` }}</span>
          <span class="m-chip" :class="`m-chip--${toneOf(node)}`">
            <span class="dot"></span>{{ labelOf(node) }}
          </span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ node.provider_name }}</span>
          <span class="m-kv__v num">{{ t('nodes.modelAvail') }} {{ node.model_available }}/{{ node.model_total }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('nodes.concurrency') }}</span>
          <span class="m-kv__v num">{{ node.effective_concurrency }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('nodes.requests') }}</span>
          <span class="m-kv__v num">{{ node.total_requests }}</span>
        </div>
      </button>
    </template>

    <!-- 节点详情 Sheet：摘要有序 KV + 宽表入口 -->
    <Sheet
      v-if="openNode"
      :title="openNode.label || `#${openNode.id}`"
      @close="openId = null"
    >
      <div class="m-card">
        <div class="m-kv">
          <span class="m-kv__k">{{ t('nodes.modelAvail') }}</span>
          <span class="m-kv__v num">{{ openNode.model_available }}/{{ openNode.model_total }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('nodes.successRate') }}</span>
          <span class="m-kv__v num">
            {{ openNode.aggregated_success_rate !== null && openNode.aggregated_success_rate !== undefined
              ? `${(openNode.aggregated_success_rate * 100).toFixed(1)}%` : '—' }}
          </span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('nodes.concurrency') }}</span>
          <span class="m-kv__v num">{{ openNode.effective_concurrency }}</span>
        </div>
        <div v-if="openNode.effective_reason" class="m-kv">
          <span class="m-kv__k">reason</span>
          <span class="m-kv__v">{{ openNode.effective_reason }}</span>
        </div>
      </div>

      <button
        v-if="openNode.models && openNode.models.length"
        type="button"
        class="m-btn m-btn--ghost focus-entry"
        @click="openFocus(openNode, $event)"
      >
        <Icon name="focus" />
        {{ t('nodes.detail') }}
      </button>
    </Sheet>

    <!-- 专注工作区：每模型明细宽表全页查看（停靠表头 + 横滚 + 状态色） -->
    <FocusWorkspace v-if="focusHandle && openNode" :handle="focusHandle" @exit="focusHandle = null">
      <div class="m-table-wrap">
        <table class="m-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Offer</th>
              <th>Probe</th>
              <th>SR</th>
              <th class="num">Samples</th>
              <th class="num">P95</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in openNode.models ?? []" :key="m.raw_model_name">
              <td>{{ m.canonical_name ?? m.raw_model_name }}</td>
              <td :class="`cell-${m.offer_available ? 'ok' : 'bad'}`">
                {{ m.offer_available ? '✓' : '✗' }}{{ m.offer_unavailable_reason ? ` ${m.offer_unavailable_reason}` : '' }}
              </td>
              <td :class="`cell-${availTone(m) === 'success' ? 'ok' : availTone(m) === 'warning' ? 'warn' : 'bad'}`">
                {{ m.probe_state }}{{ m.probe_last_status ? `/${m.probe_last_status}` : '' }}
              </td>
              <td class="num">
                {{ m.recent_success_rate !== null && m.recent_success_rate !== undefined
                  ? `${(m.recent_success_rate * 100).toFixed(0)}%` : '—' }}
              </td>
              <td class="num">{{ m.recent_samples }}</td>
              <td class="num">{{ m.p95_latency_ms != null ? `${Math.round(m.p95_latency_ms)}ms` : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </FocusWorkspace>
  </section>
</template>

<style scoped>
.node-card {
  display: block;
  width: 100%;
  text-align: left;
  color: var(--app-text);
}

.node-name {
  font-weight: 600;
  font-size: 0.9375rem;
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
}

.focus-entry {
  width: 100%;
  gap: var(--app-space-2);
}

.cell-ok {
  color: var(--app-success);
}

.cell-warn {
  color: var(--app-warning);
}

.cell-bad {
  color: var(--app-danger);
}
</style>
