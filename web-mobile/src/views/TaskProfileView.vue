<script setup lang="ts">
// TaskProfileView — 任务类型档案 + 人工修正反馈闭环（2026-10-08，第一百批）。
//
// GET /api/admin/task-profile
// GET /api/admin/task-profile/corrections/stats
//
// ★★ admin 档（`admin/handler.go:1413` 用 `admin` 挂载 `RegisterTaskProfileRoutes`）
//   ⇒ 抽屉席**不设** requiresRole（与 `credential-model-state` 那一席相反）。
//
// ⚠️★ 这一页存在的理由：批 98/99 把这两个端点的 API 层做完了，
//   但**两个模块都没被任何 UI 引用** —— 全仓 94 个 API 模块里有 32 个是这种孤儿。
//   API 层做完 ≠ 功能复制到移动端；用户点不到的东西不算数。
//
// ★★ 本页所有标签都用**显式查表**，不用 `t('前缀' + v)` 那种动态键：
//   动态键必须登记进 `i18n/dynamicKeys.spec.ts` 的 DYNAMIC_KEYS 清单，
//   而 `tier_source` 并没有导出常量 ⇒ 与其手抄一份（必然漂），不如显式查表。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtTime } from '@/utils/format'
import {
  fetchTaskProfile,
  type TaskProfileEntry,
  type TaskProfileResponse,
} from '@/api/taskProfile'
import {
  fetchTaskTypeCorrectionStats,
  type CorrectionStatsResponse,
} from '@/api/taskTypeCorrectionStats'

useHyperPage({ title: () => t('tp.title') })

const profile = ref<TaskProfileResponse | null>(null)
const stats = ref<CorrectionStatsResponse | null>(null)
const error = ref<string | null>(null)
const loading = ref(false)
const keyword = ref('')

/** ★ `tier_source` 的三个可达值 → i18n 键。**枚举外的值原样显示，不猜**。 */
const TIER_SOURCE_KEYS: Record<string, string> = {
  registry: 'tp.srcRegistry',
  correction_escalation: 'tp.srcCorrection',
  confidence_escalation: 'tp.srcConfidence',
}

function sourceLabel(source: string): string {
  const key = TIER_SOURCE_KEYS[source]
  return key === undefined ? source : t(key)
}

/** ★ 三档 tier 的色调；**枚举外的值走 danger**，与 TenantsView 的 statusTone 同口径。 */
function tierTone(tier: string): 'success' | 'warning' | 'danger' {
  if (tier === 'tier-a') return 'success'
  if (tier === 'tier-b') return 'warning'
  return 'danger'
}

/**
 * ★ 档案总数。`task_types` 与 `profiles[].task_type` 是**同集合且同序**的
 * （批 98 文件头 (5)），所以两个数字必须相等 —— 不等就是响应被截断或版本漂移。
 */
const registryCount = computed(() => profile.value?.profiles.length ?? 0)

const rows = computed<TaskProfileEntry[]>(() => {
  const all = profile.value?.profiles ?? []
  const q = keyword.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(
    (p) =>
      p.task_type.toLowerCase().includes(q) ||
      p.description.toLowerCase().includes(q) ||
      p.preferred_tier.toLowerCase().includes(q),
  )
})

/** ★ 有修正记录的任务类型（`stats` 是对象，取键）。 */
const correctedTypes = computed(() => Object.keys(stats.value?.stats ?? {}))

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    // 两个端点都是 admin 档 GET，失败各自独立 ⇒ 用 allSettled 而不是 all：
    // 一边 503 不该让另一边已经取到的数据一起丢掉。
    const [p, s] = await Promise.allSettled([fetchTaskProfile(), fetchTaskTypeCorrectionStats()])
    if (p.status === 'fulfilled') profile.value = p.value
    if (s.status === 'fulfilled') stats.value = s.value
    const firstRejected = [p, s].find((r) => r.status === 'rejected') as PromiseRejectedResult | undefined
    // ★ 两边都失败才算这一页失败；只有一边失败时用已有数据继续渲染。
    if (firstRejected && p.status === 'rejected' && s.status === 'rejected') {
      error.value = (firstRejected.reason as Error)?.message || t('common.error')
    }
  } catch (e) {
    // ★ 抛错不许退化成空档案集 —— 那与「后端真的返回 0 个档案」在 UI 上无法区分。
    profile.value = null
    stats.value = null
    error.value = (e as Error)?.message || t('common.error')
  } finally {
    loading.value = false
  }
}

void load()

onBeforeUnmount(() => {
  profile.value = null
  stats.value = null
  error.value = null
})
</script>

<template>
  <div class="tp">
    <section v-if="error" class="tp__panel">
      <p class="tp__msg tp__msg--err">{{ error }}</p>
    </section>

    <p v-if="loading" class="tp__msg">{{ t('common.loading') }}</p>

    <!-- ── 档案总览 ── -->
    <template v-if="profile">
      <section class="tp__panel">
        <h2 class="tp__h">{{ t('tp.registryTitle') }}</h2>
        <p class="tp__note">
          {{ t('tp.registryMeta', { version: profile.registry_version, count: registryCount }) }}
        </p>
        <input
          v-model="keyword"
          class="tp__input"
          type="search"
          :placeholder="t('tp.filterHint')"
          :aria-label="t('tp.filterHint')"
        />
      </section>

      <p v-if="rows.length === 0" class="tp__msg">{{ t('tp.emptyProfile') }}</p>

      <article v-for="p in rows" :key="p.task_type" class="tp__card">
        <div class="card-row">
          <span class="badge" :class="`badge--${tierTone(p.preferred_tier)}`">{{ p.preferred_tier }}</span>
          <span class="tp__name">{{ p.task_type }}</span>
          <span class="num tp__conf">{{ p.min_confidence.toFixed(2) }}</span>
        </div>
        <!-- ★ 空 description 是「该类型不在 registry 里」的信号（批 98 文件头 (12)） -->
        <p class="tp__desc" :class="{ 'tp__desc--unknown': p.description === '' }">
          {{ p.description === '' ? t('tp.unknownType') : p.description }}
        </p>
        <div class="tp__meta">
          <span>{{ t('tp.suggestTier') }} {{ p.suggestion.tier }}</span>
          <span>{{ t('tp.source') }} {{ sourceLabel(p.suggestion.tier_source) }}</span>
          <span>{{ t('tp.suggestConf') }} {{ p.suggestion.min_confidence.toFixed(2) }}</span>
        </div>
        <p v-if="p.suggestion.fallback_tiers.length > 0" class="tp__fallbacks">
          {{ t('tp.fallbacks') }} {{ p.suggestion.fallback_tiers.join(' → ') }}
        </p>
        <p v-if="p.correction_stats" class="tp__stat">
          {{ t('tp.correctionStat') }}
          {{ p.correction_stats.corrected }}/{{ p.correction_stats.total }}
          （{{ (p.correction_stats.correction_rate * 100).toFixed(1) }}%）
        </p>
      </article>
    </template>

    <!-- ── 修正统计 ── -->
    <template v-if="stats">
      <section class="tp__panel">
        <h2 class="tp__h">{{ t('tp.statsTitle') }}</h2>
        <p class="tp__note">{{ t('tp.statsSince', { since: stats.since }) }}</p>
        <p v-if="correctedTypes.length === 0" class="tp__msg">{{ t('tp.noCorrection') }}</p>
        <p v-else class="tp__note">{{ t('tp.correctedTypes', { count: correctedTypes.length }) }}</p>
      </section>

      <p v-if="stats.recent.length === 0" class="tp__msg">{{ t('tp.emptyRecent') }}</p>

      <article v-for="c in stats.recent" :key="c.id" class="tp__card">
        <div class="card-row">
          <span class="badge" :class="c.agrees ? 'badge--success' : 'badge--warning'">
            {{ c.agrees ? t('tp.agrees') : t('tp.corrected') }}
          </span>
          <span class="tp__name">{{ c.auto_task_type }} → {{ c.human_task_type }}</span>
        </div>
        <!-- ★ request_id 是回查那条请求的连接键 ⇒ 必须渲染，否则这条修正记录无法追到源头 -->
        <p class="tp__reqid">{{ c.request_id }}</p>
        <p class="tp__meta">
          <span>{{ fmtTime(c.created_at) }}</span>
          <span>{{ c.annotator }}</span>
        </p>
        <p class="tp__desc">{{ c.reason }}</p>
        <!-- ★ 这两键**键恒在、值可为 null**（批 99 文件头 (13)）——
             模板里不能直接把 null 当字符串渲染出来。 -->
        <p v-if="c.classifier_confidence !== null || c.profile !== null" class="tp__meta">
          <span v-if="c.classifier_confidence !== null">
            {{ t('tp.confidence') }} {{ c.classifier_confidence.toFixed(2) }}
          </span>
          <span v-if="c.profile !== null">{{ t('tp.profile') }} {{ c.profile }}</span>
        </p>
      </article>
    </template>

    <p v-if="!loading && !error && !profile && !stats" class="tp__msg">{{ t('tp.empty') }}</p>
  </div>
</template>

<style scoped>
.tp {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.tp__panel {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.tp__h {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
}

.tp__msg {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.tp__msg--err {
  color: var(--app-danger);
}

.tp__note {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
  word-break: break-all;
}

/* R1：新增触控控件一律 ≥48 CSS px */
.tp__input {
  width: 100%;
  min-height: 48px;
  padding: 0 12px;
  font-size: 14px;
  color: inherit;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.tp__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.tp__name {
  font-size: 0.875rem;
  font-weight: 500;
  word-break: break-all;
}

.tp__conf {
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.tp__desc {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

/* 空 description = 该类型不在 registry 里 ⇒ 用虚线边框把它标出来 */
.tp__desc--unknown {
  color: var(--app-text-muted);
  font-style: italic;
}

.tp__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

/* request_id 是长十六进制串，必须能断行，否则窄屏会撑破卡片 */
.tp__reqid {
  margin: 0;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  word-break: break-all;
}

.tp__fallbacks,
.tp__stat {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}
</style>