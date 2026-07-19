<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CenterInstance, RegionStats } from '../../../api/ops'
import { buildRegionBuckets, formatRelativeTime, type RegionBucket } from '../regionTopology'

const props = defineProps<{
  regionStats: RegionStats[]
  nodes: CenterInstance[]
}>()

const emit = defineEmits<{
  (e: 'select-node', node: CenterInstance): void
}>()

const { t } = useI18n()

const buckets = computed<RegionBucket[]>(() =>
  buildRegionBuckets(props.regionStats, props.nodes),
)

function rel(iso?: string) {
  return formatRelativeTime(iso, {
    justNow: t('ops.overview.justNow'),
    minutesAgo: (n) => t('ops.overview.minutesAgo', { n }),
    hoursAgo: (n) => t('ops.overview.hoursAgo', { n }),
    daysAgo: (n) => t('ops.overview.daysAgo', { n }),
  }) || t('ops.overview.noHeartbeat')
}

function regionTone(row: RegionStats) {
  if (row.missing) return 'missing'
  if (row.online_instances > 0) return 'online'
  if (row.degraded_instances > 0) return 'degraded'
  return 'offline'
}

function regionLabel(row: RegionStats) {
  if (row.missing) return t('ops.overview.regionMissing')
  if (row.online_instances > 0) return t('ops.overview.regionOnline')
  if (row.degraded_instances > 0) return t('ops.overview.regionDegraded')
  return t('ops.overview.regionOffline')
}

function nodeTone(status: string) {
  if (status === 'online') return 'online'
  if (status === 'degraded') return 'degraded'
  return 'offline'
}
</script>

<template>
  <section class="topology">
    <div class="topology-header">
      <div>
        <h2>{{ t('ops.overview.topologyTitle') }}</h2>
        <p class="topology-hint">{{ t('ops.overview.topologyHint') }}</p>
      </div>
      <slot name="actions" />
    </div>

    <div class="region-grid">
      <article
        v-for="bucket in buckets"
        :key="bucket.region"
        class="region-card"
        :class="`tone-${regionTone(bucket.stats)}`"
      >
        <header class="region-head">
          <div class="region-title">
            <span class="region-name">{{ bucket.region }}</span>
            <span class="region-badge">{{ regionLabel(bucket.stats) }}</span>
          </div>
          <div class="region-meta">
            {{ t('ops.overview.nodesOnlineOf', {
              online: bucket.stats.online_instances,
              total: bucket.stats.total_instances || bucket.nodes.length,
            }) }}
            <span v-if="bucket.stats.last_heartbeat"> · {{ rel(bucket.stats.last_heartbeat) }}</span>
          </div>
        </header>

        <div v-if="bucket.nodes.length === 0" class="region-empty">
          {{ t('ops.overview.regionEmpty') }}
        </div>

        <div v-else class="node-grid" :class="{ sparse: bucket.nodes.length <= 2 }">
          <button
            v-for="node in bucket.nodes"
            :key="node.instance_id"
            type="button"
            class="node-card"
            :class="`node-${nodeTone(node.status)}`"
            @click="emit('select-node', node)"
          >
            <div class="node-top">
              <span class="node-host" :title="node.hostname">{{ node.hostname }}</span>
              <span class="node-status">{{ t(`ops.center.status.${node.status}`) }}</span>
            </div>
            <div class="node-mid">
              <span>{{ node.version || '—' }}</span>
              <span v-if="node.build_seq">#{{ node.build_seq }}</span>
            </div>
            <div class="node-foot">
              <span>{{ rel(node.last_heartbeat) }}</span>
              <span class="node-cta">{{ t('ops.overview.viewNode') }} →</span>
            </div>
          </button>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.topology {
  margin-bottom: 20px;
}

.topology-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.topology-header h2 {
  margin: 0;
  font-size: 16px;
  font-weight: 650;
}

.topology-hint {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.region-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 14px;
}

.region-card {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 14px;
  background: var(--el-bg-color-overlay);
  padding: 14px;
  border-left: 3px solid var(--el-border-color);
  min-height: 160px;
  display: flex;
  flex-direction: column;
}

.tone-online { border-left-color: var(--el-color-success); }
.tone-degraded { border-left-color: var(--el-color-warning); }
.tone-offline { border-left-color: var(--el-color-danger); }
.tone-missing { border-left-color: var(--el-border-color); border-style: dashed; }

.region-head {
  margin-bottom: 12px;
}

.region-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.region-name {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.02em;
}

.region-badge {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--el-fill-color);
  color: var(--el-text-color-regular);
}

.tone-online .region-badge {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}

.tone-degraded .region-badge {
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}

.tone-offline .region-badge {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.region-meta {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.region-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px dashed var(--el-border-color);
  border-radius: 10px;
  color: var(--el-text-color-secondary);
  font-size: 13px;
  min-height: 88px;
  background: color-mix(in srgb, var(--el-fill-color) 55%, transparent);
}

.node-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 10px;
}

.node-grid.sparse {
  grid-template-columns: repeat(auto-fit, minmax(220px, 280px));
}

.node-card {
  appearance: none;
  text-align: left;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: transparent;
  padding: 12px;
  cursor: pointer;
  transition: border-color 0.15s ease, background 0.15s ease;
}

.node-card:hover {
  border-color: var(--el-color-primary-light-5);
  background: color-mix(in srgb, var(--el-color-primary) 6%, transparent);
}

.node-top {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  align-items: center;
}

.node-host {
  font-weight: 650;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-status {
  font-size: 11px;
  flex-shrink: 0;
}

.node-online .node-status { color: var(--el-color-success); }
.node-degraded .node-status { color: var(--el-color-warning); }
.node-offline .node-status { color: var(--el-color-danger); }

.node-mid {
  margin-top: 8px;
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}

.node-foot {
  margin-top: 10px;
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.node-cta {
  color: var(--el-color-primary);
  font-weight: 600;
}
</style>
