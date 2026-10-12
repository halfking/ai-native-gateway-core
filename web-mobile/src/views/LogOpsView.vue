<script setup lang="ts">
// LogOpsView — 日志运维面（/log-ops，**admin 档**）。
//
// 数据源（三条，全部 admin 档，api/logOps.ts 文件头有逐行注册表）：
//   GET /api/admin/logs/stats              日志目录体积 / 时间跨度 / 磁盘占比
//   GET /api/admin/logs/files              当前 + 轮转 + 归档文件清单
//   GET /api/admin/logs/body-cache-stats   详情 body 缓存快照
//
// ## ⚠️ 权限档位
//
// `/api/admin/logs/*` 同一前缀下**混着两种档位**：
//   admin(...)：body-cache-stats / files / stats / archive-list
//   h.superAdmin(...)：config / archive / cleanup
// ⇒ 本页三条都是 admin 档，抽屉席**不设** `requiresRole`。
// ★ 但这是**前缀级的巧合**：往后往本页加 config/archive/cleanup 任何一条，
//   整页档位必须跟着升到 super_admin，不能只加端点不改抽屉席。
//
// ## ★★★★★ 五处「不能都渲染成同一个东西」
//
// (1) ★★★★ **三种「什么都没有」长得不一样**（stats 的三条早退路径）：
//      - `log_dir === ''`    ⇒ **文件日志根本没启用**（`cur.File == ""`）
//      - `exists === false`  ⇒ 配了路径但**目录不存在**
//      - `total_files === 0` ⇒ 目录在，**真的一个文件都没有**
//      三者若都渲染成「没有数据」，运维会去查一个根本不存在的目录问题。
//
// (2) ★★★ **`disk_usage_pct` 的 0 是 Go 零值**：
//      `if pct, …, err := diskUsageAt(dir); err == nil { … }`（log_management.go:367）
//      查失败时只是不赋值 ⇒ 与「真的 0%」不可分。
//      ⇒ 页面**不**把 0% 渲染成「磁盘没有被日志占」，而是照原样回显并标注口径。
//
// (3) ★★★ **`hit_rate` 同样**：分母为 0 时后端留 0.0（logs_body_cache.go:139-142）
//      ⇒ 「无样本」与「0% 命中率」必须分开。
//
// (4) ★★★ **files 的空列表也要分两种**：
//      `logging.ListFiles()` 未启用时返回**空切片 + nil 错误**
//      （internal/logging/logging.go:408-410 注释原文）
//      ⇒ 唯一区分信号是 `dir` 是否为空串。
//
// (5) ★★ **`oldest_mtime` / `newest_mtime` 是 `*time.Time` 指针**：
//      目录里一个文件都没有时是 `null` ⇒ 时间跨度**算不出来**，
//      不能拿「现在」或某个默认值顶上去。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum, relativeTime } from '@/utils/format'
import {
  fetchLogStats,
  fetchLogFiles,
  fetchBodyCacheStats,
  logStatsNotEnabled,
  logStatsDirMissing,
  logStatsEmpty,
  logStatsTimeRange,
  logStatsArchiveRatio,
  logFilesNotEnabled,
  logFilesEmpty,
  logFilesTotalDisagrees,
  bodyCacheRateMeaningless,
  bodyCacheSaturated,
  type LogStatsResponse,
  type LogFilesListResponse,
  type BodyCacheStats,
  type LogFileInfo,
} from '@/api/logOps'

useHyperPage({ title: () => t('logOps.title') })

/** 无数据占位。字形与 0 不同，且带独立 class（判据锚在原因上）。 */
const NO_DATA = '—'

type SectionKey = 'stats' | 'files' | 'bodyCache'

const stats = ref<LogStatsResponse | null>(null)
const files = ref<LogFilesListResponse | null>(null)
const bodyCache = ref<BodyCacheStats | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({ stats: false, files: false, bodyCache: false })
const error = ref<Record<SectionKey, string | null>>({ stats: null, files: null, bodyCache: null })

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('logOps.errForbidden')
  // ★ 503 在本族是有语义的：body cache 未初始化（logs_body_cache.go:133-136）
  if (statusCode === 503) return t('logOps.bodyCache.notInitialized')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    if (section === 'stats') stats.value = await fetchLogStats()
    else if (section === 'files') files.value = await fetchLogFiles()
    else bodyCache.value = await fetchBodyCacheStats()
  } catch (err) {
    // ★ 失败 ⇒ 清空该段，绝不保留上一次的成功结果冒充本次。
    if (section === 'stats') stats.value = null
    if (section === 'files') files.value = null
    if (section === 'bodyCache') bodyCache.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

/* ── stats (1)(2)(5) ───────────────────────────────────────────────── */

/** ★ 三种空态互斥，且都必须渲染出**不同的**文案。 */
const statsState = computed<'notEnabled' | 'dirMissing' | 'empty' | 'ok'>(() => {
  if (!stats.value) return 'ok'
  if (logStatsNotEnabled(stats.value)) return 'notEnabled'
  if (logStatsDirMissing(stats.value)) return 'dirMissing'
  if (logStatsEmpty(stats.value)) return 'empty'
  return 'ok'
})

const timeRangeText = computed(() => {
  const s = stats.value
  if (!s) return NO_DATA
  const r = logStatsTimeRange(s)
  // ★ 算不出来就说算不出来，不拿「现在」顶
  if (!r) return t('logOps.stats.noTimeRange')
  return `${relativeTime(r.from)} → ${relativeTime(r.to)}`
})

/** ★ 归档占比分母为 0 时是 null（算不出），不是 0%。 */
const archiveRatioText = computed(() => {
  const s = stats.value
  if (!s) return NO_DATA
  const r = logStatsArchiveRatio(s)
  return r === null ? t('logOps.stats.ratioUnknown') : fmtNum(r * 100, 1) + '%'
})

/* ── files (4) ─────────────────────────────────────────────────────── */

const filesState = computed<'notEnabled' | 'empty' | 'ok'>(() => {
  if (!files.value) return 'ok'
  if (logFilesNotEnabled(files.value)) return 'notEnabled'
  if (logFilesEmpty(files.value)) return 'empty'
  return 'ok'
})

const fileRows = computed<LogFileInfo[]>(() => files.value?.files ?? [])
const filesTotalDisagrees = computed(() => (files.value ? logFilesTotalDisagrees(files.value) : false))

/** ★ 归档与压缩是两个独立布尔，各自有独立文案，不合成一个「已归档」。 */
function fileFlags(f: LogFileInfo): string[] {
  const out: string[] = []
  if (f.is_current) out.push(t('logOps.files.current'))
  if (f.is_archived) out.push(t('logOps.files.archived'))
  if (f.is_compressed) out.push(t('logOps.files.compressed'))
  return out
}

/* ── body-cache (3) ────────────────────────────────────────────────── */

const cacheRateText = computed(() => {
  const c = bodyCache.value
  if (!c) return NO_DATA
  if (bodyCacheRateMeaningless(c)) return t('logOps.bodyCache.noSample')
  return fmtNum(c.hit_rate * 100, 1) + '%'
})

const cacheSaturated = computed(() => (bodyCache.value ? bodyCacheSaturated(bodyCache.value) : false))

onBeforeUnmount(() => {
  stats.value = null
  files.value = null
  bodyCache.value = null
})
</script>

<template>
  <div class="view-root lo">
    <!-- ══ 1. 日志目录统计 ═══════════════════════════════════════════ -->
    <section class="lo__section">
      <header class="lo__head">
        <h2 class="lo__title">{{ t('logOps.stats.title') }}</h2>
        <button
          type="button"
          class="lo__load"
          :disabled="loading === 'stats'"
          @click="load('stats')"
        >
          {{ loaded.stats && !error.stats ? t('logOps.reload') : t('logOps.load') }}
        </button>
      </header>

      <p v-if="error.stats" class="lo__msg lo__msg--err">{{ error.stats }}</p>
      <p v-if="loading === 'stats'" class="lo__msg">{{ t('common.loading') }}</p>

      <template v-if="stats">
        <!-- ★★ 三种空态：三段不同文案，不能都写「没有数据」 -->
        <p v-if="statsState === 'notEnabled'" class="lo__msg lo__msg--warn">
          {{ t('logOps.stats.notEnabled') }}
        </p>
        <p v-else-if="statsState === 'dirMissing'" class="lo__msg lo__msg--err">
          {{ t('logOps.stats.dirMissing', { dir: stats.log_dir }) }}
        </p>
        <p v-else-if="statsState === 'empty'" class="lo__msg">
          {{ t('logOps.stats.empty') }}
        </p>

        <template v-else>
          <div class="lo__kpis">
            <div class="lo__kpi">
              <span class="lo__kpi-label">{{ t('logOps.stats.totalFiles') }}</span>
              <span class="lo__kpi-value">{{ fmtInt(stats.total_files) }}</span>
            </div>
            <div class="lo__kpi">
              <span class="lo__kpi-label">{{ t('logOps.stats.totalSize') }}</span>
              <span class="lo__kpi-value">{{ stats.total_size_human || NO_DATA }}</span>
            </div>
          </div>

          <dl class="lo__kv">
            <div class="lo__kv-row">
              <dt>{{ t('logOps.stats.dir') }}</dt>
              <dd>{{ stats.log_dir }}</dd>
            </div>
            <div class="lo__kv-row">
              <dt>{{ t('logOps.stats.timeRange') }}</dt>
              <!-- ★ 时间跨度算不出来时不显示 0 / 不显示「现在」 -->
              <dd :class="{ 'lo__nodata': logStatsTimeRange(stats) === null }">
                {{ timeRangeText }}
              </dd>
            </div>
            <div class="lo__kv-row">
              <dt>{{ t('logOps.stats.archive') }}</dt>
              <dd>{{ fmtInt(stats.archive_files) }} · {{ fmtNum(Number(stats.archive_size) / 1024 / 1024, 2) }} MB</dd>
            </div>
            <div class="lo__kv-row">
              <dt>{{ t('logOps.stats.archiveRatio') }}</dt>
              <dd :class="{ 'lo__nodata': logStatsArchiveRatio(stats) === null }">
                {{ archiveRatioText }}
              </dd>
            </div>
            <!-- ★★ disk_usage_pct 的 0 可能是「查失败」，口径随值一起说明 -->
            <div class="lo__kv-row">
              <dt>{{ t('logOps.stats.diskUsage') }}</dt>
              <dd :class="{ 'lo__nodata': stats.disk_usage_pct === 0 }">
                {{ fmtNum(stats.disk_usage_pct, 2) }}%{{ stats.disk_usage_pct === 0 ? `（${t('logOps.stats.diskUsageZero')}）` : '' }}
              </dd>
            </div>
          </dl>
        </template>
      </template>
    </section>

    <!-- ══ 2. 日志文件清单 ═══════════════════════════════════════════ -->
    <section class="lo__section">
      <header class="lo__head">
        <h2 class="lo__title">{{ t('logOps.files.title') }}</h2>
        <button
          type="button"
          class="lo__load"
          :disabled="loading === 'files'"
          @click="load('files')"
        >
          {{ loaded.files && !error.files ? t('logOps.reload') : t('logOps.load') }}
        </button>
      </header>

      <p v-if="error.files" class="lo__msg lo__msg--err">{{ error.files }}</p>
      <p v-if="loading === 'files'" class="lo__msg">{{ t('common.loading') }}</p>

      <template v-if="files">
        <!-- ★ files 的空列表同样要分「未启用」与「启用但没文件」 -->
        <p v-if="filesState === 'notEnabled'" class="lo__msg lo__msg--warn">
          {{ t('logOps.files.notEnabled') }}
        </p>
        <p v-else-if="filesState === 'empty'" class="lo__msg">
          {{ t('logOps.files.empty') }}
        </p>

        <!-- ★ total 与实际条数不一致 = 契约漂移 -->
        <p v-if="filesTotalDisagrees" class="lo__msg lo__msg--err">
          {{ t('logOps.files.totalDisagrees', { total: files.total, shown: fileRows.length }) }}
        </p>

        <ul v-if="fileRows.length" class="lo__list">
          <li v-for="f in fileRows" :key="f.name" class="lo__item">
            <div class="lo__item-head">
              <span class="lo__item-title">{{ f.name }}</span>
            </div>
            <div class="lo__flags">
              <span v-for="flag in fileFlags(f)" :key="flag" class="lo__flag">{{ flag }}</span>
            </div>
            <dl class="lo__kv">
              <div class="lo__kv-row">
                <dt>{{ t('logOps.files.size') }}</dt>
                <dd>{{ f.size_human || NO_DATA }}</dd>
              </div>
              <div class="lo__kv-row">
                <dt>{{ t('logOps.files.modTime') }}</dt>
                <dd>{{ relativeTime(f.mod_time) }}</dd>
              </div>
            </dl>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 3. 详情 body 缓存 ════════════════════════════════════════ -->
    <section class="lo__section">
      <header class="lo__head">
        <h2 class="lo__title">{{ t('logOps.bodyCache.title') }}</h2>
        <button
          type="button"
          class="lo__load"
          :disabled="loading === 'bodyCache'"
          @click="load('bodyCache')"
        >
          {{ loaded.bodyCache && !error.bodyCache ? t('logOps.reload') : t('logOps.load') }}
        </button>
      </header>

      <p v-if="error.bodyCache" class="lo__msg lo__msg--err">{{ error.bodyCache }}</p>
      <p v-if="loading === 'bodyCache'" class="lo__msg">{{ t('common.loading') }}</p>

      <template v-if="bodyCache">
        <!-- ★ 容量用满：还要有 evictions 才能说「在淘汰」 -->
        <p v-if="cacheSaturated" class="lo__msg lo__msg--warn">
          {{ bodyCache.evictions > 0
            ? t('logOps.bodyCache.evicting')
            : t('logOps.bodyCache.saturated') }}
        </p>

        <div class="lo__kpis">
          <div class="lo__kpi">
            <span class="lo__kpi-label">{{ t('logOps.bodyCache.rate') }}</span>
            <!-- ★ 分母为 0 时显示「无样本」，不显示 0% 命中率 -->
            <span class="lo__kpi-value" :class="{ 'lo__nodata': bodyCacheRateMeaningless(bodyCache) }">
              {{ cacheRateText }}
            </span>
          </div>
          <div class="lo__kpi">
            <span class="lo__kpi-label">{{ t('logOps.bodyCache.usage') }}</span>
            <span class="lo__kpi-value">{{ fmtInt(bodyCache.size) }} / {{ fmtInt(bodyCache.cap) }}</span>
          </div>
        </div>

        <dl class="lo__kv">
          <div class="lo__kv-row">
            <dt>{{ t('logOps.bodyCache.hits') }}</dt>
            <dd>{{ fmtInt(bodyCache.hits) }}</dd>
          </div>
          <div class="lo__kv-row">
            <dt>{{ t('logOps.bodyCache.misses') }}</dt>
            <dd>{{ fmtInt(bodyCache.misses) }}</dd>
          </div>
          <div class="lo__kv-row">
            <dt>{{ t('logOps.bodyCache.evictions') }}</dt>
            <dd>{{ fmtInt(bodyCache.evictions) }}</dd>
          </div>
        </dl>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* LogOpsView — 触控热区一律 ≥48 CSS px（UI规范 17 §4-R1）。 */
.lo__section {
  margin-bottom: 16px;
}
.lo__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.lo__title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
}
.lo__load {
  min-height: 48px;
  min-width: 88px;
  padding: 0 16px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.lo__load:disabled {
  opacity: 0.6;
  cursor: default;
}
.lo__msg {
  font-size: 13px;
  margin: 4px 0;
  opacity: 0.8;
}
.lo__msg--err {
  color: var(--danger, #cf222e);
  opacity: 1;
}
.lo__msg--warn {
  color: var(--warning, #9a6700);
  opacity: 1;
}
.lo__kpis {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.lo__kpi {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
}
.lo__kpi-label {
  font-size: 12px;
  opacity: 0.75;
}
.lo__kpi-value {
  font-size: 18px;
  font-weight: 600;
}
/* ★ 无数据/无意义占位：字形与 class 双锚，避免「—」被当成 0 */
.lo__nodata {
  opacity: 0.55;
  font-style: italic;
}
.lo__kv {
  margin: 8px 0 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.lo__kv-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 13px;
}
.lo__kv dt {
  opacity: 0.75;
}
.lo__kv dd {
  margin: 0;
  text-align: right;
  word-break: break-all;
}
.lo__list {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.lo__item {
  border: 1px solid var(--app-border);
  border-radius: 8px;
  padding: 10px;
}
.lo__item-head {
  margin-bottom: 4px;
}
.lo__item-title {
  font-size: 14px;
  font-weight: 600;
  word-break: break-all;
}
.lo__flags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}
.lo__flag {
  font-size: 12px;
  padding: 8px 10px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  min-height: 48px;
  display: inline-flex;
  align-items: center;
}
</style>
