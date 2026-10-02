<script setup lang="ts">
// AutoRoutingOpsView.vue — AUTO 路由运营统一工作台（2026-10-02 整合轮）。
//
// 把原四个独立页面整合为 auto 路由闭环（v2 规划 §4.4）的单一入口：
//   ✍️ 标注工作台（AnnotationView，全员）→ 🧮 标注统计（AnnotationStatsView）
//   → 🗂️ 任务档案（TaskProfileView，super）→ 🎛️ 调参审批（AutoTuningView，super）
// 闭环语义：自动的是评测/提案生成；生效永远是门禁 + 人工批准。
//
// 设计口径（简明·紧凑·可观测·可操作·可控制）：
// - 可观测：页头 KPI 行一屏读出闭环状态（今日待标注 / 累计标注 / 分类准确率 /
//   待审提案 / 建议升档类型）；SegTabs 徽标数 = 待办入口提示。
// - 可操作/可控：每个面板保留自己的工具栏（过滤/生成/批准/应用），super 页签
//   仅 super_admin 可见（normalizeTab 双保险，深链 ?tab= 同步）。
// 旧四路由 redirect 到本页 ?tab= 深链（router.ts）；菜单收敛为一项。
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { isSuperAdmin } from '../store'
import { formatDateTime } from '../utils/datetime'
import { localeRef } from '../i18n'
import SegTabs, { type SegTab } from '../components/SegTabs.vue'
import PageHeader from '../components/ui/PageHeader.vue'
import StatsRow from '../components/ui/StatsRow.vue'
import StatCard from '../components/ui/StatCard.vue'
import AnnotationView from './AnnotationView.vue'
import AnnotationStatsView from './AnnotationStatsView.vue'
import TaskProfileView from './TaskProfileView.vue'
import AutoTuningView from './AutoTuningView.vue'
import { getAnnotationStats, getFirstTurnSamples } from '../api/annotations'
import { getTuningProposals } from '../api/tuning'
import { getTaskProfile, getTaskTypeCorrectionStats } from '../api/taskProfile'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

type OpsTab = 'annotate' | 'stats' | 'profiles' | 'tuning'
const activeTab = ref<OpsTab>('annotate')

const isSuper = isSuperAdmin()

function normalizeTab(v: unknown): OpsTab | null {
  const s = String(v)
  if (s === 'annotate' || s === 'stats') return s
  if (s === 'profiles' || s === 'tuning') return isSuper ? s : null
  return null
}

// ── KPI 概览（可观测）───────────────────────────────────────────────
const kpiLoading = ref(false)
const kpiUpdatedAt = ref('')
const pendingToday = ref<number | null>(null)
const totalAnnotations = ref<number | null>(null)
const accuracyPct = ref<number | null>(null)
const numAnnotators = ref<number | null>(null)
const pendingProposals = ref<number | null>(null)
const suggestedTiers = ref<number | null>(null)

async function loadKpi() {
  kpiLoading.value = true
  const today = new Date().toISOString().slice(0, 10)
  const jobs: Promise<unknown>[] = [
    // 今日待标注：first-turn 样本今日未标注计数（size:1 只要 total）。
    getFirstTurnSamples({ page: 1, size: 1, start_date: today, end_date: today, annotated: false })
      .then((r) => { pendingToday.value = r.total ?? 0 }),
    getAnnotationStats().then((s) => {
      totalAnnotations.value = s.overall?.total_annotations ?? 0
      accuracyPct.value = s.overall?.accuracy_percent ?? 0
      numAnnotators.value = s.overall?.num_annotators ?? 0
    }),
  ]
  if (isSuper) {
    jobs.push(
      getTuningProposals({ status: 'pending', limit: 200 })
        .then((r) => { pendingProposals.value = (r.proposals ?? []).length }),
      // 建议升档类型数：建议层 ≠ 当前层的任务类型（与任务档案面板同口径）。
      Promise.all([getTaskProfile(), getTaskTypeCorrectionStats(30)]).then(([p, s]) => {
        let n = 0
        for (const prof of p.profiles ?? []) {
          const sug = s.suggestions?.[prof.task_type]
          if (sug && sug.tier !== prof.preferred_tier) n++
        }
        suggestedTiers.value = n
      }),
    )
  }
  // 各指标独立失败：allSettled 不让单点 403/网络错拖垮整行，失败项显示 —。
  await Promise.allSettled(jobs)
  kpiLoading.value = false
  kpiUpdatedAt.value = new Date().toISOString()
}

function fmtVal(v: number | null): string {
  return v == null ? '—' : String(v)
}
function fmtPct(v: number | null): string {
  return v == null ? '—' : `${v.toFixed(1)}%`
}
const accuracyTone = computed<'success' | 'warning' | 'danger'>(() => {
  const v = accuracyPct.value ?? 0
  if (v >= 80) return 'success'
  if (v >= 60) return 'warning'
  return 'danger'
})
const fmtUpdatedAt = computed(() =>
  kpiUpdatedAt.value
    ? formatDateTime(kpiUpdatedAt.value, { locale: localeRef.value, options: { hour12: false } })
    : '',
)

const tabs = computed<SegTab[]>(() => {
  const list: SegTab[] = [
    { value: 'annotate', label: t('autoOps.tab.annotate'), icon: '✍️', badge: pendingToday.value ?? 0 },
    { value: 'stats', label: t('autoOps.tab.stats'), icon: '🧮' },
  ]
  if (isSuper) {
    list.push(
      { value: 'profiles', label: t('autoOps.tab.profiles'), icon: '🗂️' },
      { value: 'tuning', label: t('autoOps.tab.tuning'), icon: '🎛️', badge: pendingProposals.value ?? 0 },
    )
  }
  return list
})

// Deep-link: /routing-v2/auto-ops?tab=tuning（旧四路由 redirect 也落在这里）。
onMounted(() => {
  const tab = normalizeTab(route.query.tab)
  if (tab) activeTab.value = tab
  loadKpi()
})
watch(() => route.query.tab, (v) => {
  const tab = normalizeTab(v)
  if (tab) activeTab.value = tab
})
watch(activeTab, (v) => {
  if (normalizeTab(route.query.tab) !== v) {
    router.replace({ query: { ...route.query, tab: v } })
  }
})
</script>

<template>
  <div class="auto-ops-wrapper">
    <PageHeader :title="t('autoOps.title')" :subtitle="t('autoOps.desc')">
      <template #actions>
        <span v-if="fmtUpdatedAt" class="kpi-updated">{{ t('autoOps.kpi.updatedAt', { time: fmtUpdatedAt }) }}</span>
        <button class="btn btn-sm" :disabled="kpiLoading" @click="loadKpi">
          {{ kpiLoading ? t('autoOps.kpi.refreshing') : t('autoOps.kpi.refresh') }}
        </button>
      </template>
    </PageHeader>

    <StatsRow :cols="{ mobile: 2, tablet: 2, desktop: isSuper ? 5 : 4 }">
      <StatCard
        :label="t('autoOps.kpi.pendingToday')"
        :value="fmtVal(pendingToday)"
        :tone="(pendingToday ?? 0) > 0 ? 'warning' : 'success'"
        icon="✍️"
      />
      <StatCard :label="t('autoOps.kpi.totalAnnotations')" :value="fmtVal(totalAnnotations)" icon="🧾" />
      <StatCard :label="t('autoOps.kpi.accuracy')" :value="fmtPct(accuracyPct)" :tone="accuracyTone" icon="🎯" />
      <StatCard
        v-if="isSuper"
        :label="t('autoOps.kpi.pendingProposals')"
        :value="fmtVal(pendingProposals)"
        :tone="(pendingProposals ?? 0) > 0 ? 'warning' : 'success'"
        icon="🎛️"
      />
      <StatCard v-if="isSuper" :label="t('autoOps.kpi.suggestions')" :value="fmtVal(suggestedTiers)" icon="🗂️" />
      <StatCard v-if="!isSuper" :label="t('autoOps.kpi.annotators')" :value="fmtVal(numAnnotators)" icon="👥" />
    </StatsRow>

    <div class="view-tabs-container">
      <SegTabs v-model="activeTab" :tabs="tabs" />
    </div>

    <!-- 面板懒挂载：切页签即取最新数据 -->
    <AnnotationView v-if="activeTab === 'annotate'" />
    <AnnotationStatsView v-else-if="activeTab === 'stats'" />
    <TaskProfileView v-else-if="activeTab === 'profiles'" />
    <AutoTuningView v-else-if="activeTab === 'tuning'" />
  </div>
</template>

<style scoped>
.auto-ops-wrapper {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  min-height: 100vh;
  min-height: 100dvh;
}

.view-tabs-container {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 8px 12px;
}

.kpi-updated {
  font-size: 12px;
  color: var(--muted);
  white-space: nowrap;
}
</style>
