<script setup lang="ts">
// DataLifecycleView — 数据生命周期（/data-lifecycle/{partitions,storage/tables}，**admin 档**）。
//
// 它答的是「数据库这一层现在什么状态」：分区清单 + 体积榜。
// 与已上移的各面分工明确：凭据/模型/探针/会话那些面答「业务健康」，
// 本页答「**存储与保留**」。
//
// ⚠️★★★ 五个后端语义（详见 api/dataLifecycle.ts 文件头）：
//
// (1) ★★★★ 体积榜的 `total_bytes` / `total_human` 是**返回行之和**，不是整库大小
//     （后端只累加 LIMIT 之后的行）。⇒ 页首不能说「本库共 X」。
// (2) ★★★★ `percent_of_db` 的分母是**榜内之和** ⇒ 这一列是「榜内占比」，
//     与「占全库百分比」无关。字段名在骗人，UI 必须改口径。
// (3) ★★★ `rows` 是 planner 估计（`n_live_tup`），不是 COUNT(*)
//     ⇒ 标「估计行数」。
// (4) ★★★ 分区父表与其分区可能**同时在榜**，父表体积**已包含**子分区
//     ⇒ 标出父表，并提示不可与分区行相加。
// (5) ★★★ `row_count = -1` 是后端明确的「未知」哨兵 ⇒ 显示「未知」不是「-1 行」。
//
// ★★ `partitions` 里某张表**整个消失**是查不出来的（出错只 slog.Warn + continue）
//   ⇒ 页面显示「本次返回 N 张表」，不假装是全集。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchStorageTableSizes,
  fetchPartitionStatuses,
  storageAtCap,
  partitionRowCountUnknown,
  partitionCountsAsArchived,
  tableOverlapsItsPartitions,
  STORAGE_TABLES_LIMIT_DEFAULT,
  STORAGE_TABLES_LIMIT_MAX,
  STORAGE_ROWS_ARE_ESTIMATES,
  STORAGE_TOTAL_IS_TOPN_SUM,
  STORAGE_PERCENT_IS_TOPN_RELATIVE,
  STORAGE_MAY_DOUBLE_COUNT_PARTITIONS,
  PARTITION_ARCHIVED_COUNT_INCLUDES_COLUMNAR,
  type TableSizesResponse,
  type PartitionTableStatus,
} from '@/api/dataLifecycle'

useHyperPage({ title: () => t('lifecycle.title') })

const LIMIT_CHOICES = [STORAGE_TABLES_LIMIT_DEFAULT, 50, 100, STORAGE_TABLES_LIMIT_MAX]

const limit = ref<number>(STORAGE_TABLES_LIMIT_DEFAULT)
const sizes = ref<TableSizesResponse | null>(null)
const partitions = ref<PartitionTableStatus[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const partitionsError = ref<string | null>(null)

/** 展开了哪些分区表。 */
const openTables = ref<string[]>([])

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  partitionsError.value = null
  // ★ 两个端点独立取：一个失败不清空另一个。
  const [s, p] = await Promise.allSettled([
    fetchStorageTableSizes({ limit: limit.value }),
    fetchPartitionStatuses(),
  ])
  if (s.status === 'fulfilled') sizes.value = s.value
  else {
    sizes.value = null
    error.value = (s.reason as Error)?.message || t('common.error')
  }
  if (p.status === 'fulfilled') partitions.value = Array.isArray(p.value) ? p.value : []
  else {
    partitions.value = []
    partitionsError.value = (p.reason as Error)?.message || t('common.error')
  }
  loading.value = false
  loaded.value = true
}
void load()

const tables = computed(() => sizes.value?.tables ?? [])
const atCap = computed(() => storageAtCap(sizes.value, limit.value))
const hasTables = computed(() => tables.value.length > 0)
const hasPartitions = computed(() => partitions.value.length > 0)

function isOpen(name: string): boolean {
  return openTables.value.includes(name)
}
function toggleTable(name: string): void {
  openTables.value = isOpen(name) ? openTables.value.filter((x) => x !== name) : [...openTables.value, name]
}
function shortDate(v: string): string {
  return v.slice(0, 10)
}

onBeforeUnmount(() => {
  sizes.value = null
  partitions.value = []
  error.value = null
  partitionsError.value = null
  openTables.value = []
})
</script>

<template>
  <div class="view-root dl">
    <!-- ══════ 分区清单 ══════ -->
    <section class="dl__panel">
      <span class="dl__panel-title">{{ t('lifecycle.partitions') }}</span>

      <p v-if="partitionsError" class="dl__msg dl__msg--err">{{ partitionsError }}</p>
      <p v-else-if="loading" class="dl__msg">{{ t('common.loading') }}</p>
      <p v-else-if="!hasPartitions" class="dl__msg">{{ t('lifecycle.noPartitions') }}</p>

      <template v-else>
        <!-- ★★ 某张表查不到会整个消失 ⇒ 必须说清「返回了几张」而不是假装全集 -->
        <p class="dl__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('lifecycle.returnedTables', { n: partitions.length }) }}</span>
        </p>
        <p v-if="PARTITION_ARCHIVED_COUNT_INCLUDES_COLUMNAR" class="dl__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('lifecycle.archivedCountNote') }}</span>
        </p>

        <ul class="dl__plist">
          <li v-for="tb in partitions" :key="tb.table_name" class="dl__ptable">
            <button type="button" class="dl__ptable-head" @click="toggleTable(tb.table_name)">
              <AppIcon name="chevron" :size="14" :class="{ 'dl__chev--open': isOpen(tb.table_name) }" />
              <span class="dl__pname">{{ tb.table_name }}</span>
              <span class="badge badge--muted">{{ tb.total_partitions }}</span>
            </button>
            <p class="dl__pdesc">{{ tb.description }}</p>
            <div class="dl__pstats">
              <span class="dl__kv-item">
                <span class="dl__kv-l">{{ t('lifecycle.pSize') }}</span>
                <span class="dl__kv-v">{{ tb.total_size_human }}</span>
              </span>
              <span class="dl__kv-item">
                <span class="dl__kv-l">{{ t('lifecycle.archived') }}</span>
                <span class="dl__kv-v">{{ tb.archived_count }}</span>
              </span>
              <span class="dl__kv-item">
                <span class="dl__kv-l">{{ t('lifecycle.archivable') }}</span>
                <span class="dl__kv-v">{{ tb.archivable_count }}</span>
              </span>
            </div>

            <ul v-if="isOpen(tb.table_name)" class="dl__parts">
              <li v-for="p in tb.partitions" :key="p.partition_name" class="dl__part">
                <div class="dl__part-head">
                  <StatusDot :tone="partitionCountsAsArchived(p) ? 'muted' : 'success'" />
                  <span class="dl__part-name">{{ p.partition_name }}</span>
                  <span v-if="p.is_columnar" class="badge badge--muted">{{ t('lifecycle.columnar') }}</span>
                  <span v-if="p.can_archive" class="badge badge--warning">{{ t('lifecycle.canArchive') }}</span>
                </div>
                <p class="dl__part-meta">
                  {{ shortDate(p.start_date) }} → {{ shortDate(p.end_date) }} · {{ p.size_human }}
                </p>
                <!-- ★★ row_count=-1 是后端明确的「未知」哨兵 -->
                <p class="dl__part-meta">
                  {{ t('lifecycle.pRows') }}:
                  {{ partitionRowCountUnknown(p.row_count) ? t('lifecycle.unknown') : fmtInt(p.row_count) }}
                </p>
              </li>
            </ul>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══════ 体积榜 ══════ -->
    <section class="dl__panel">
      <span class="dl__panel-title">{{ t('lifecycle.storage') }}</span>

      <!-- ★★ 三个口径必须挂在榜的上方，不能塞进底部小字 -->
      <p v-if="STORAGE_TOTAL_IS_TOPN_SUM" class="dl__note dl__note--strong">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('lifecycle.totalIsTopN') }}</span>
      </p>
      <p v-if="STORAGE_PERCENT_IS_TOPN_RELATIVE" class="dl__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('lifecycle.percentIsTopN') }}</span>
      </p>

      <div class="dl__limits" role="group" :aria-label="t('lifecycle.limitLabel')">
        <button
          v-for="n in LIMIT_CHOICES"
          :key="n"
          type="button"
          class="dl__chip"
          :class="{ 'dl__chip--on': limit === n }"
          @click="((limit = n), load())"
        >
          {{ t('lifecycle.limitN', { n }) }}
        </button>
      </div>

      <p v-if="error" class="dl__msg dl__msg--err">{{ error }}</p>
      <p v-else-if="loading" class="dl__msg">{{ t('common.loading') }}</p>
      <p v-else-if="!hasTables" class="dl__msg">{{ t('lifecycle.noTables') }}</p>

      <template v-else>
        <p v-if="atCap" class="dl__note dl__note--strong">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('lifecycle.truncated', { n: tables.length, limit }) }}</span>
        </p>
        <!-- ★ 上榜这些张合计（不是整库） -->
        <p class="dl__total">
          {{ t('lifecycle.listedSum', { n: tables.length, size: sizes?.total_human ?? '' }) }}
        </p>
        <p class="dl__meta">{{ t('lifecycle.collectedAt', { t: relativeTime(sizes?.collected_at ?? '') }) }}</p>

        <ul class="dl__tlist">
          <li v-for="tb in tables" :key="tb.schema + '.' + tb.table" class="dl__titem">
            <div class="dl__titem-head">
              <span class="dl__tname">{{ tb.table }}</span>
              <!-- ★★ 父表体积已含子分区 ⇒ 标出，且不可与分区行相加 -->
              <span v-if="STORAGE_MAY_DOUBLE_COUNT_PARTITIONS && tableOverlapsItsPartitions(tb)" class="badge badge--warning">
                {{ t('lifecycle.parentTable') }}
              </span>
            </div>
            <p class="dl__tsize">{{ tb.total_human }}</p>
            <div class="dl__tmeta">
              <!-- ★ 估计行数，不是精确行数 -->
              <span v-if="STORAGE_ROWS_ARE_ESTIMATES">{{ t('lifecycle.estRows', { n: fmtInt(tb.rows) }) }}</span>
              <span>{{ t('lifecycle.indexBytes', { n: tb.index_bytes }) }}</span>
              <span>{{ tb.toast_human }}</span>
              <!-- ★ 榜内占比，不是占全库 -->
              <span>{{ t('lifecycle.topPercent', { n: tb.percent_of_db }) }}</span>
            </div>
          </li>
        </ul>
      </template>
    </section>
  </div>
</template>

<style scoped>
.dl {
  padding: var(--app-space-3);
}
.dl__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.dl__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.dl__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.dl__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.dl__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.dl__note--strong {
  color: var(--app-warning);
  font-weight: 600;
}
.dl__plist,
.dl__tlist,
.dl__parts {
  list-style: none;
  margin: 0;
  padding: 0;
}
.dl__ptable {
  border-top: 1px solid var(--app-border);
  padding-top: var(--app-space-2);
  margin-top: var(--app-space-2);
}
.dl__ptable-head {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-height: 48px;
  padding: 0;
  border: 0;
  background: transparent;
  text-align: left;
}
.dl__chev--open {
  transform: rotate(90deg);
}
.dl__pname {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-all;
  min-width: 0;
}
.dl__pdesc {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.dl__pstats {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: 6px;
}
.dl__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.dl__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.dl__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.dl__parts {
  margin-top: var(--app-space-2);
}
.dl__part {
  padding: 6px 8px;
  border-left: 2px solid var(--app-border);
  margin-bottom: 4px;
}
.dl__part-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.dl__part-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
  min-width: 0;
}
.dl__part-meta {
  margin: 2px 0 0;
  font-size: 10px;
  color: var(--app-text-muted);
}
.dl__limits {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.dl__chip {
  min-height: 48px;
  min-width: 60px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.dl__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.dl__total {
  margin: var(--app-space-2) 0 0;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.dl__meta {
  margin: 2px 0 var(--app-space-2);
  font-size: 11px;
  color: var(--app-text-muted);
}
.dl__titem {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.dl__titem-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.dl__tname {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
  min-width: 0;
}
.dl__tsize {
  margin: 2px 0 0;
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.dl__tmeta {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: 2px;
  font-size: 11px;
  color: var(--app-text-muted);
}
</style>