<script setup lang="ts">
// NodeHealthView — 单凭据的健康事件时间线（/node-health/:id）。
//
// 数据源：GET /api/admin/node-health/{credential_id}/timeline（AdminMiddleware）
// 真源是 node_probe_runs（探测完成后落库的审计行），不返回 body/headers。
//
// ⚠️ 与热力图相邻但语义相反的一条约束：这里 `since` 超 7d 是**静默 clamp**
//   （node_health.go:64-66），不像热力图那样 400。
//   ⇒ 若无其事地显示「30 天」而实际只取到 7 天，用户会以为看全了。
//   所以 buildSince 在前端夹住，界面也只提供 ≤7 天的选项。
//
// ⚠️ 响应的 credential_id 是**字符串**（后端 strconv.FormatInt 后再序列化）。

import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchNodeHealthTimeline,
  buildSince,
  credentialIdOf,
  eventTone,
  NODE_HEALTH_EVENT_CAP,
  NODE_HEALTH_MAX_SINCE_HOURS,
  type NodeRecoveryEvent,
} from '@/api/nodeHealth'

useHyperPage({ title: () => t('nodeHealth.title') })
const route = useRoute()
const router = useRouter()

interface Range {
  key: string
  hours: number
}
/** 只给 ≤7 天的选项——超出会被静默截断，给出来就是骗人。 */
const RANGES: Range[] = [
  { key: 'nodeHealth.range6h', hours: 6 },
  { key: 'nodeHealth.range24h', hours: 24 },
  { key: 'nodeHealth.range3d', hours: 72 },
  { key: 'nodeHealth.range7d', hours: NODE_HEALTH_MAX_SINCE_HOURS },
]

const rangeKey = ref('nodeHealth.range24h')
const events = ref<NodeRecoveryEvent[]>([])
const resolvedId = ref<number | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const credId = computed(() => {
  const raw = route.params.id
  const s = Array.isArray(raw) ? raw[0] : raw
  const n = Number(s)
  return Number.isFinite(n) && n > 0 ? n : null
})

const hours = computed(() => RANGES.find((r) => r.key === rangeKey.value)?.hours ?? 24)

async function load(): Promise<void> {
  const id = credId.value
  if (id == null) {
    error.value = t('nodeHealth.noEvents')
    return
  }
  loading.value = true
  error.value = null
  try {
    const resp = await fetchNodeHealthTimeline(id, { since: buildSince(hours.value) })
    events.value = resp.events ?? []
    // ★ 后端 credential_id 是字符串；用 id 做 key 会让路由 id 与响应 id 对不上时静默错位
    resolvedId.value = credentialIdOf(resp) ?? id
  } catch (err) {
    events.value = []
    resolvedId.value = null
    error.value = (err as Error)?.message ?? null
  } finally {
    loading.value = false
  }
}

watch([credId, rangeKey], () => { void load() }, { immediate: true })

function onRangeChange(key: string): void {
  if (rangeKey.value === key) return
  rangeKey.value = key
}

/** 撞到端点硬上限时如实说明，而不是假装这就是全部。 */
const truncated = computed(() => events.value.length >= NODE_HEALTH_EVENT_CAP)

function goBack(): void {
  if (window.history.length > 1) router.back()
  else void router.push('/heatmap')
}
</script>

<template>
  <div class="view-root nh">
    <div class="nh__row" role="tablist">
      <button
        v-for="r in RANGES"
        :key="r.key"
        type="button"
        class="nh__chip"
        :class="{ 'nh__chip--on': rangeKey === r.key }"
        :aria-selected="rangeKey === r.key"
        @click="onRangeChange(r.key)"
      >
        {{ t(r.key) }}
      </button>
    </div>

    <p class="nh__hint">{{ t('nodeHealth.windowMaxed') }}</p>
    <p v-if="resolvedId" class="nh__meta">
      <span>#{{ resolvedId }}</span>
      <span>{{ t('nodeHealth.eventCount', { n: events.length }) }}</span>
    </p>

    <p v-if="loading" class="nh__msg">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="nh__msg nh__msg--err">{{ error }}</p>
    <p v-else-if="events.length === 0" class="nh__msg">{{ t('nodeHealth.empty') }}</p>
    <template v-else>
      <p v-if="truncated" class="nh__truncated">{{ t('nodeHealth.truncated', { n: NODE_HEALTH_EVENT_CAP }) }}</p>
      <ol class="nh__timeline">
        <li v-for="(e, i) in events" :key="`${e.occurred_at}-${i}`" class="nh__event">
          <div class="nh__event-head">
            <span class="badge" :class="`badge--${eventTone(e)}`">{{ e.event_type }}</span>
            <span class="nh__event-time">{{ relativeTime(e.occurred_at) }}</span>
          </div>
          <p v-if="e.duration_ms != null" class="nh__event-line">
            {{ t('nodeHealth.duration', { ms: e.duration_ms }) }}
          </p>
          <p v-if="e.reason_code" class="nh__event-line nh__event-reason">
            {{ t('nodeHealth.reason', { c: e.reason_code }) }}
          </p>
          <p v-if="e.note" class="nh__event-note">{{ e.note }}</p>
        </li>
      </ol>
    </template>

    <button type="button" class="nh__back" @click="goBack">{{ t('nodeHealth.back') }}</button>
  </div>
</template>

<style scoped>
.nh {
  padding: var(--app-space-3);
}
.nh__row {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-bottom: var(--app-space-2);
}
.nh__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.nh__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.nh__hint {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.nh__meta {
  display: flex;
  gap: var(--app-space-2);
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.nh__msg {
  padding: var(--app-space-4) 0;
  color: var(--app-text-muted);
  font-size: 14px;
  text-align: center;
}
.nh__msg--err {
  color: var(--app-danger);
}
.nh__truncated {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-size: 12px;
}
.nh__timeline {
  list-style: none;
  margin: 0;
  padding: 0;
}
.nh__event {
  position: relative;
  padding: var(--app-space-2) 0 var(--app-space-2) var(--app-space-4);
  border-left: 2px solid var(--app-border);
  margin-left: 6px;
}
.nh__event-head {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
}
.nh__event-time {
  margin-left: auto;
  font-size: 12px;
  color: var(--app-text-muted);
}
.nh__event-line {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--app-text-secondary);
}
.nh__event-reason {
  color: var(--app-danger);
}
.nh__event-note {
  margin: 3px 0 0;
  font-size: 12px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.nh__back {
  width: 100%;
  min-height: 48px;
  margin-top: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-primary);
  font-size: 14px;
}
</style>
