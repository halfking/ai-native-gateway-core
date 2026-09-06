<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { isSuperAdmin } from '../store'
import CredentialMonitorView from './CredentialMonitorView.vue'
import CredentialHeatmapView from './CredentialHeatmapView.vue'
import RoutingLogView from './RoutingLogView.vue'
import ProbeHealthPanel from './probe/ProbeHealthPanel.vue'
import SegTabs, { type SegTab } from '../components/SegTabs.vue'

// CredentialMonitorWithTabs — host of the /routing-v2/credentials page tabs
// (docs/FEATURE-REQ-credential-heatmap-routing-log.md §3).
// The 探测健康 tab is super_admin only: it embeds the former /probe-health
// page, which was super-gated end to end.

type ViewTab = 'list' | 'heatmap' | 'routing-log' | 'probe-health'
const activeTab = ref<ViewTab>('list')
const route = useRoute()
const router = useRouter()

const tabs = ref<SegTab[]>([
  { value: 'list', label: '列表视图' },
  { value: 'heatmap', label: '热力图' },
  { value: 'routing-log', label: '路由记录' },
])

const isSuper = isSuperAdmin()
if (isSuper) {
  tabs.value.push({ value: 'probe-health', label: '探测健康' })
}

function normalizeTab(v: unknown): ViewTab | null {
  const s = String(v)
  if (s === 'list' || s === 'heatmap' || s === 'routing-log') return s
  if (s === 'probe-health' || s === 'probe') return isSuper ? 'probe-health' : null
  return null
}

// Deep-link support: /routing-v2/credentials?tab=heatmap (used by the
// /probe-health redirect and heatmap/log entry points).
onMounted(() => {
  const tab = normalizeTab(route.query.tab)
  if (tab) activeTab.value = tab
})

watch(() => route.query.tab, (v) => {
  const tab = normalizeTab(v)
  if (tab) activeTab.value = tab
})

watch(activeTab, (v) => {
  const current = route.query.tab
  if (normalizeTab(current) !== v) {
    router.replace({ query: { ...route.query, tab: v } })
  }
})
</script>

<template>
  <div class="credential-monitor-wrapper">
    <!-- Tab switcher -->
    <div class="view-tabs-container">
      <SegTabs v-model="activeTab" :tabs="tabs" />
    </div>

    <!-- Tab content -->
    <CredentialMonitorView v-if="activeTab === 'list'" />
    <CredentialHeatmapView v-else-if="activeTab === 'heatmap'" />
    <RoutingLogView v-else-if="activeTab === 'routing-log'" />
    <ProbeHealthPanel v-else-if="activeTab === 'probe-health'" embedded />
  </div>
</template>

<style scoped>
.credential-monitor-wrapper {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  min-height: 100vh;
}

.view-tabs-container {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 8px 12px;
}
</style>
