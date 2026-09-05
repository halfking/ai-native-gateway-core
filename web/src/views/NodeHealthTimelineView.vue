<script setup lang="ts">
// NodeHealthTimelineView.vue — 节点恢复时间线（node-health timeline API）
//
// 单凭据视角：列出过去一段时间的关键恢复事件。数据来自
// GET /api/admin/node-health/{credential_id}/timeline（node_probe_runs）。
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  fetchNodeRecoveryTimeline,
  type NodeRecoveryEvent,
  type NodeRecoveryTimelineResponse,
} from '../api/node-health'
import { credentialDisplayName, useCredentialLabels } from '../composables/useCredentialLabels'

const route = useRoute()
const { t, locale } = useI18n()

const credentialId = computed(() => String(route.params.credentialId || ''))

// 凭据显示：订阅标签缓存 revision，标签异步加载完成后自动刷新。
const { labelRevision } = useCredentialLabels()
const credentialName = computed(() => {
  void labelRevision.value
  const id = Number(credentialId.value)
  return Number.isFinite(id) && id > 0 ? credentialDisplayName(id) : credentialId.value
})
const events = ref<NodeRecoveryEvent[]>([])
const loading = ref(false)
const error = ref('')
const observationDegraded = ref(false)

const sortedEvents = computed(() =>
  [...events.value].sort((a, b) => a.occurred_at.localeCompare(b.occurred_at)),
)

function fmtTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(locale.value, { hour12: false })
}

function fmtMs(ms?: number): string {
  if (ms === undefined || ms === null) return ''
  if (ms >= 1000) return (ms / 1000).toFixed(1) + 's'
  return ms + 'ms'
}

function eventClass(ev: NodeRecoveryEvent): string {
  switch (ev.event_type) {
    case 'reconnected':
    case 'recovered':
      return 'is-success'
    case 'probing':
      return 'is-primary'
    case 'failed':
    case 'quarantined':
      return 'is-danger'
    case 'degraded':
      return 'is-warning'
    default:
      return 'is-muted'
  }
}

function eventLabel(ev: NodeRecoveryEvent): string {
  return t(`nodeHealthTimeline.events.${ev.event_type}`)
}

async function load(id: string) {
  if (!id) return
  loading.value = true
  error.value = ''
  try {
    const payload: NodeRecoveryTimelineResponse = await fetchNodeRecoveryTimeline(id)
    events.value = payload.events
    observationDegraded.value = payload.observation_status === 'observation_degraded'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    events.value = []
  } finally {
    loading.value = false
  }
}

watch(credentialId, (id) => { void load(id) }, { immediate: true })

onMounted(() => {
  // watch(immediate) 已处理首次加载；这里留作扩展 SSE 接入点。
})
</script>

<template>
  <section class="node-health-timeline" data-testid="node-health-timeline">
    <header class="nht-head">
      <h2>{{ t('nodeHealthTimeline.title') }}</h2>
      <p class="nht-sub">
        <span>{{ t('nodeHealthTimeline.subtitle') }}</span>
        <span class="nht-cred" :title="`#${credentialId}`">{{ credentialName }}</span>
        <span v-if="observationDegraded" class="nht-chip is-warning">
          {{ t('nodeHealthTimeline.observationDegraded') }}
        </span>
      </p>
    </header>

    <div v-if="loading" class="nht-state">{{ t('nodeHealthTimeline.loading') }}</div>
    <div v-else-if="error" class="nht-state nht-state--error" data-testid="nht-error" role="alert">
      {{ t('nodeHealthTimeline.error') }}: {{ error }}
    </div>
    <div v-else-if="!sortedEvents.length" class="nht-state" data-testid="nht-empty">
      {{ t('nodeHealthTimeline.empty') }}
    </div>
    <ol v-else class="nht-list" data-testid="nht-list">
      <li v-for="(ev, idx) in sortedEvents" :key="`${ev.occurred_at}-${idx}`" class="nht-row" :class="eventClass(ev)" data-testid="nht-row">
        <span class="nht-marker" aria-hidden="true"></span>
        <div class="nht-content">
          <div class="nht-row-head">
            <strong>{{ eventLabel(ev) }}</strong>
            <time :datetime="ev.occurred_at">{{ fmtTime(ev.occurred_at) }}</time>
          </div>
          <div class="nht-row-meta">
            <span v-if="ev.reason_code" class="nht-meta-item">{{ t('nodeHealthTimeline.reason', { code: ev.reason_code }) }}</span>
            <span v-if="ev.duration_ms !== undefined" class="nht-meta-item">{{ t('nodeHealthTimeline.duration', { ms: fmtMs(ev.duration_ms) }) }}</span>
            <span v-if="ev.note" class="nht-meta-item">{{ ev.note }}</span>
          </div>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.node-health-timeline {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.nht-head h2 {
  margin: 0;
  font-size: 16px;
  color: var(--kx-text);
}

.nht-sub {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 4px 0 0;
  color: var(--kx-muted);
  font-size: 12px;
}

.nht-cred {
  font-family: var(--kx-mono, ui-monospace, monospace);
  color: var(--kx-text);
}

.nht-chip {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
}

.nht-chip.is-warning { color: var(--kx-warning); background: var(--kx-warning-soft); }

.nht-state {
  padding: 18px 8px;
  color: var(--kx-muted);
  font-size: 12px;
  text-align: center;
}

.nht-state--error { color: var(--kx-danger); }

.nht-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 0;
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-surface);
}

.nht-row {
  position: relative;
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr);
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--kx-border);
}

.nht-row:last-child { border-bottom: 0; }

.nht-row:not(:last-child)::after {
  content: '';
  position: absolute;
  left: 23px;
  top: 32px;
  bottom: -1px;
  width: 1px;
  background: var(--kx-border);
}

.nht-marker {
  width: 12px;
  height: 12px;
  margin-top: 6px;
  border-radius: 50%;
  background: var(--kx-muted);
  border: 2px solid var(--kx-surface);
  box-shadow: 0 0 0 1px var(--kx-border);
  z-index: 1;
}

.nht-row.is-success .nht-marker { background: var(--kx-success); box-shadow: 0 0 0 1px var(--kx-success); }
.nht-row.is-primary .nht-marker { background: var(--kx-primary); box-shadow: 0 0 0 1px var(--kx-primary); }
.nht-row.is-warning .nht-marker { background: var(--kx-warning); box-shadow: 0 0 0 1px var(--kx-warning); }
.nht-row.is-danger .nht-marker { background: var(--kx-danger); box-shadow: 0 0 0 1px var(--kx-danger); }
.nht-row.is-muted .nht-marker { background: var(--kx-muted); }

.nht-content { min-width: 0; }

.nht-row-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  color: var(--kx-text);
  font-size: 12px;
}

.nht-row-head time {
  flex: 0 0 auto;
  color: var(--kx-text-secondary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.nht-row-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin-top: 4px;
  color: var(--kx-text-secondary);
  font-size: 11px;
}

.nht-row.is-success .nht-row-head strong { color: var(--kx-success); }
.nht-row.is-warning .nht-row-head strong { color: var(--kx-warning); }
.nht-row.is-danger .nht-row-head strong { color: var(--kx-danger); }
.nht-row.is-primary .nht-row-head strong { color: var(--kx-primary); }
</style>