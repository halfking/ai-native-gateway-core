<script setup lang="ts">
// AlertsView — 告警时间线（candidate-failures 告警环）。
import { ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { fetchAlerts, type CandidateFailureAlert } from '@/api/alerts'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import { fmtTime } from '@/utils/format'

useHyperPage({ title: () => t('alerts.title') })

let cache: CandidateFailureAlert[] = []
const controller = new ContinuousListController<CandidateFailureAlert>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: cache.length }
    const resp = await fetchAlerts()
    cache = resp.data ?? []
    return { items: cache, total: cache.length }
  },
  stableKey: (a) => `${a.credential_id}-${a.raw_model_name}-${a.ts}`,
  scopeKey: 'alerts',
})

const expanded = ref<string | null>(null)

function kindTone(kind: string): string {
  if (kind.includes('auth')) return 'badge--danger'
  if (kind.includes('rate') || kind.includes('429')) return 'badge--warning'
  return 'badge--info'
}

function alertKey(a: CandidateFailureAlert): string {
  return `${a.credential_id}-${a.raw_model_name}-${a.ts}`
}

/**
 * 这条告警**有没有可展开的内容**。
 *
 * ⚠️ 为什么必须显式判：`expanded` 唯一驱动的渲染是
 *   `<pre v-if="expanded === key && a.last_response_preview">`
 * 原来卡片**无条件**渲染成 `<button>` + `cursor:pointer`，于是没有
 * `last_response_preview` 的告警点下去什么都不发生 —— 实测属性、类名、高度、
 * 文字、子节点**全部零变化**（`changed:false`），是一张骗人的可点卡片。
 *
 * ⇒ 修法是**撤掉假可供性**（渲染成 div、不给 pointer），而不是补一个箭头：
 *   箭头会承诺「点开有东西」，而实际上仍然没有内容。
 */
function expandable(a: CandidateFailureAlert): boolean {
  return !!a.last_response_preview
}

function toggle(a: CandidateFailureAlert): void {
  if (!expandable(a)) return
  expanded.value = expanded.value === alertKey(a) ? null : alertKey(a)
}
</script>

<template>
  <HyperList
    :controller="controller"
    :item-key="alertKey"
    :empty-hint="t('alerts.emptyHint')"
  >
    <template #item="{ item: a }">
      <!-- 有内容才可点；否则是 div，不带 pointer、不带 active 反馈（见 expandable 注释） -->
      <component
        :is="expandable(a) ? 'button' : 'div'"
        class="data-card alert-card"
        :class="{ 'alert-card--expandable': expandable(a) }"
        :type="expandable(a) ? 'button' : undefined"
        :aria-expanded="expandable(a) ? expanded === alertKey(a) : undefined"
        @click="toggle(a)"
      >
        <div class="card-row">
          <span class="badge" :class="kindTone(a.error_kind)">{{ a.error_kind }}</span>
          <span class="alert-card__count num">×{{ a.count }}</span>
        </div>
        <div class="alert-card__model">{{ a.raw_model_name }}</div>
        <div class="alert-card__meta">
          <span>{{ t('alerts.credential') }} #{{ a.credential_id }}</span>
          <span>{{ fmtTime(a.ts) }}</span>
        </div>
        <pre v-if="expanded === alertKey(a) && a.last_response_preview" class="alert-card__body">{{ a.last_response_preview }}</pre>
      </component>
    </template>
  </HyperList>
</template>

<style scoped>
.alert-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  font: inherit;
  color: inherit;
}

/* 只有真有内容可展开时才给可点的视觉暗示 —— 空白卡片挂着 pointer 就是骗人 */
.alert-card--expandable {
  cursor: pointer;
}

.alert-card--expandable:active {
  background: var(--app-primary-softer);
}

.alert-card__count {
  font-size: 0.8125rem;
  color: var(--app-danger);
  font-weight: 600;
}

.alert-card__model {
  font-size: 0.875rem;
  font-weight: 500;
  word-break: break-all;
}

.alert-card__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.alert-card__body {
  margin: 0;
  padding: var(--app-space-2);
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-sm);
  font-size: 0.6875rem;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 160px;
  overflow-y: auto;
}
</style>
