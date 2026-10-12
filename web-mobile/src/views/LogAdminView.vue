<script setup lang="ts">
// LogAdminView — 日志管理读面（**admin 档**，四条只读端点，四面板合一页）。
//
//   GET /api/admin/logs/body-cache-stats
//   GET /api/admin/logs/files
//   GET /api/admin/logs/stats
//   GET /api/admin/logs/archive/list
//
// ★ 档位（admin/handler.go:959,1115,1116,1119）：四条**全是 `admin(...)`**
//   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//   ★ 但**同一前缀下混着三条 superAdmin**（config/archive/cleanup）⇒ 不能按前缀判权限。
//     本页三个都不碰：config 是写（热加载轮转配置）、archive/cleanup 是删日志。
//
// ★★★★★★ 头号陷阱：**同一个「文件日志没启用」在三个端点上是三个不同判据**。
//     · files         ⇒ `dir === ''`
//     · stats         ⇒ `log_dir === ''`
//     · archive/list  ⇒ **`dir` 键整个不存在**
//   页面必须**各按各的**判，不许抽一个 helper。
//
// ★★★★★★ `archive/list` 是异形端点：`dir` / `exists` 可能**整个不存在**。
//   ① 未启用        ⇒ {archives:[], total:0}                      ← 没有 dir、没有 exists
//   ② 归档目录读不到 ⇒ {archives:[], total:0, dir:D, exists:false}
//   ③ 正常          ⇒ {archives:[…], total:N, dir:D, exists:true}
//   ⇒ ① 与 ② 的 archives/total **完全一样**，只有「键在不在」能分开。
//
// ★★★★★ `files` 的 `is_archived` **恒为 false**（后端硬编码字面量），
//   而列表**本身也不含归档**（扫描只走顶层目录且跳过子目录）⇒ 归档只有 `/archive/list` 能看。
//
// ★★★ 三个「0 是二义的」：hit_rate / disk_usage_pct / （间接）exists:false。
//
// ★★ 抛错不许退化成「空数据」—— 空清单和「压根没启用」在页面上长得一样。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchBodyCacheStats,
  fetchLogFiles,
  fetchLogStats,
  fetchLogArchiveList,
  bodyCacheHasNoTraffic,
  bodyCacheIsGenuinelyZeroRate,
  bodyCacheHitRateMatches,
  bodyCacheSizeExceedsCap,
  bodyCacheNotInitialised,
  logStatsLoggingDisabled,
  logStatsDirMissing,
  logStatsHasNoFiles,
  logStatsDiskUsageIsAmbiguous,
  logStatsTotalIncludingArchive,
  logArchiveListState,
  humanBytesMatches,
  type BodyCacheStats,
  type LogFilesList,
  type LogStats,
  type LogArchiveList,
} from '@/api/logsAdmin'

useHyperPage({ title: () => t('logsAdmin.title') })

const cache = ref<BodyCacheStats | null>(null)
const files = ref<LogFilesList | null>(null)
const stats = ref<LogStats | null>(null)
const archives = ref<LogArchiveList | null>(null)

/** ★ 四个端点**各自**的失败原因，不合并。 */
const cacheError = ref<string | null>(null)
const filesError = ref<string | null>(null)
const statsError = ref<string | null>(null)
const archivesError = ref<string | null>(null)
const loading = ref(false)

/** ★★ 各端点的「未启用」判据形态不同，页面必须分开落。 */
const filesDisabled = computed(() => files.value !== null && files.value.dir === '')
const statsDisabled = computed(() => stats.value !== null && logStatsLoggingDisabled(stats.value))
const archivesState = computed(() => (archives.value ? logArchiveListState(archives.value) : null))
const archivesDisabled = computed(() => archivesState.value === 'disabled')

/** ★ 派生值核对：`size_human` / `total_size_human` 复算对不上就是异常。 */
const fileSizeMismatch = computed(() =>
  (files.value?.files ?? []).filter((f) => !humanBytesMatches(f.size_bytes, f.size_human)).map((f) => f.name),
)
const statsSizeMismatch = computed(() =>
  stats.value ? !humanBytesMatches(stats.value.total_size_bytes, stats.value.total_size_human) : false,
)

async function load(): Promise<void> {
  loading.value = true
  cacheError.value = null
  filesError.value = null
  statsError.value = null
  archivesError.value = null
  // ★ 四条独立取：一条失败不许拖垮另外三条，也**不许**把别的清成空
  const [c, f, s, a] = await Promise.allSettled([
    fetchBodyCacheStats(),
    fetchLogFiles(),
    fetchLogStats(),
    fetchLogArchiveList(),
  ])
  cache.value = c.status === 'fulfilled' ? c.value : null
  files.value = f.status === 'fulfilled' ? f.value : null
  stats.value = s.status === 'fulfilled' ? s.value : null
  archives.value = a.status === 'fulfilled' ? a.value : null
  if (c.status === 'rejected') cacheError.value = (c.reason as Error)?.message || t('common.error')
  if (f.status === 'rejected') filesError.value = (f.reason as Error)?.message || t('common.error')
  if (s.status === 'rejected') statsError.value = (s.reason as Error)?.message || t('common.error')
  if (a.status === 'rejected') archivesError.value = (a.reason as Error)?.message || t('common.error')
  loading.value = false
}

void load()

onBeforeUnmount(() => {
  cache.value = null
  files.value = null
  stats.value = null
  archives.value = null
  cacheError.value = null
  filesError.value = null
  statsError.value = null
  archivesError.value = null
})
</script>

<template>
  <div class="la">
    <p v-if="loading" class="la__msg">{{ t('common.loading') }}</p>

    <!-- ══ 面板 1：请求体缓存命中 ══ -->
    <section v-if="cache || cacheError" class="la__panel">
      <div class="la__head">
        <span class="la__panel-title">{{ t('logsAdmin.cacheTitle') }}</span>
        <span v-if="cache" class="la__badge">
          <StatusDot :tone="bodyCacheHasNoTraffic(cache) ? 'muted' : cache.hit_rate >= 0.5 ? 'success' : 'warning'" />
          <span class="la__badge-t">{{ (cache.hit_rate * 100).toFixed(1) }}%</span>
        </span>
      </div>

      <p v-if="cacheError" class="la__msg la__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ cacheError }}</span>
      </p>
      <!-- ★ 只有这一条有 503；代码写 initialized、注释写 initialised -->
      <p v-if="cacheError && bodyCacheNotInitialised(cacheError)" class="la__note la__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('logsAdmin.cacheNotInitNote') }}</span>
      </p>

      <template v-if="cache">
        <!-- ★★★ hit_rate 0 是二义的 -->
        <p v-if="bodyCacheHasNoTraffic(cache)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.noTrafficNote') }}</span>
        </p>
        <p v-else-if="bodyCacheIsGenuinelyZeroRate(cache)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.zeroRateNote') }}</span>
        </p>
        <p class="la__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('logsAdmin.hitRateNote') }}</span>
        </p>
        <p v-if="!bodyCacheHitRateMatches(cache)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.hitRateMismatchNote') }}</span>
        </p>
        <p v-if="bodyCacheSizeExceedsCap(cache)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.sizeExceedsCapNote', { size: cache.size, cap: cache.cap }) }}</span>
        </p>

        <div class="la__grid">
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.cacheSize') }}</span>
            <span class="la__cell-v">{{ cache.size }} / {{ cache.cap }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.cacheHits') }}</span>
            <span class="la__cell-v">{{ cache.hits }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.cacheMisses') }}</span>
            <span class="la__cell-v">{{ cache.misses }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.cacheEvictions') }}</span>
            <span class="la__cell-v">{{ cache.evictions }}</span>
          </span>
        </div>
      </template>
    </section>

    <!-- ══ 面板 2：目录统计 ══ -->
    <section v-if="stats || statsError" class="la__panel">
      <div class="la__head">
        <span class="la__panel-title">{{ t('logsAdmin.statsTitle') }}</span>
        <span v-if="stats" class="la__badge">
          <span class="la__badge-t">
            <template v-if="statsDisabled">{{ t('logsAdmin.stateDisabled') }}</template>
            <template v-else-if="logStatsDirMissing(stats)">{{ t('logsAdmin.stateDirMissing') }}</template>
            <template v-else>{{ t('logsAdmin.stateOk') }}</template>
          </span>
        </span>
      </div>

      <p v-if="statsError" class="la__msg la__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ statsError }}</span>
      </p>

      <template v-if="stats">
        <!-- ★★ ① 未启用 / ② 目录不存在：两者的字段**完全一样**，只有 log_dir 能分开 -->
        <p v-if="statsDisabled" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.statsDisabledNote') }}</span>
        </p>
        <p v-else-if="logStatsDirMissing(stats)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.statsDirMissingNote') }}</span>
        </p>
        <p class="la__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('logsAdmin.statsAmbiguousNote') }}</span>
        </p>
        <p v-if="statsSizeMismatch" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.sizeMismatchNote') }}</span>
        </p>

        <div class="la__grid">
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.logDir') }}</span>
            <span class="la__cell-v">{{ stats.log_dir || '—' }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.totalFiles') }}</span>
            <span class="la__cell-v">{{ stats.total_files }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.totalSize') }}</span>
            <span class="la__cell-v">{{ stats.total_size_human }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.archiveFiles') }}</span>
            <span class="la__cell-v">{{ stats.archive_files }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.archiveSize') }}</span>
            <span class="la__cell-v">{{ stats.archive_size }}</span>
          </span>
          <span class="la__cell">
            <span class="la__cell-l">{{ t('logsAdmin.diskUsage') }}</span>
            <span class="la__cell-v">{{ stats.disk_usage_pct }}%</span>
          </span>
        </div>

        <!-- ★★ total_files **不含**归档，页面自己说明口径 -->
        <p class="la__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('logsAdmin.archiveExcludedNote', { n: logStatsTotalIncludingArchive(stats) }) }}</span>
        </p>
        <!-- ★★ disk_usage_pct 0 是二义的 -->
        <p v-if="logStatsDiskUsageIsAmbiguous(stats)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.diskAmbiguousNote') }}</span>
        </p>
        <!-- ★ oldest/newest_mtime 无 omitempty ⇒ 没文件时是 null -->
        <p v-if="logStatsHasNoFiles(stats)" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.noFilesNote') }}</span>
        </p>
        <p v-else class="la__meta">{{ t('logsAdmin.mtimeLine', { old: relativeTime(stats.oldest_mtime!), now: relativeTime(stats.newest_mtime!) }) }}</p>
      </template>
    </section>

    <!-- ══ 面板 3：文件清单 ══ -->
    <section v-if="files || filesError" class="la__panel">
      <div class="la__head">
        <span class="la__panel-title">{{ t('logsAdmin.filesTitle') }}</span>
        <span v-if="files" class="la__badge">
          <span class="la__badge-t">{{ t('logsAdmin.shownCount', { n: files.files.length }) }}</span>
        </span>
      </div>

      <p v-if="filesError" class="la__msg la__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ filesError }}</span>
      </p>

      <template v-if="files">
        <!-- ★★ files 侧的「未启用」判据是 dir === ''（与 stats/archive 各不相同） -->
        <p v-if="filesDisabled" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.filesDisabledNote') }}</span>
        </p>
        <p v-else-if="!files.files.length" class="la__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('logsAdmin.filesEmptyNote') }}</span>
        </p>

        <!-- ★★★★★ is_archived 恒 false + 列表不含归档 -->
        <p class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.archivedHardcodedNote') }}</span>
        </p>
        <p class="la__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('logsAdmin.filesSuffixNote') }}</span>
        </p>
        <p v-if="fileSizeMismatch.length" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.sizeMismatchNote') }}</span>
        </p>

        <ul v-if="files.files.length" class="la__list">
          <li v-for="f in files.files" :key="f.name" class="la__item">
            <div class="la__head">
              <span class="la__title">{{ f.name }}</span>
              <span class="la__badge">
                <StatusDot :tone="f.is_current ? 'success' : 'muted'" />
                <span class="la__badge-t">{{ f.size_human }}</span>
              </span>
            </div>
            <p class="la__meta">{{ relativeTime(f.mod_time) }}</p>
            <p class="la__tail">
              <span v-if="f.is_current" class="la__chip la__chip--ok">{{ t('logsAdmin.isCurrent') }}</span>
              <!-- ★ is_compressed 是**真算的** .gz 后缀，与恒 false 的 is_archived 不同 -->
              <span v-if="f.is_compressed" class="la__chip la__chip--muted">{{ t('logsAdmin.isCompressed') }}</span>
            </p>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 面板 4：归档列表 ══ -->
    <section v-if="archives || archivesError" class="la__panel">
      <div class="la__head">
        <span class="la__panel-title">{{ t('logsAdmin.archiveTitle') }}</span>
        <span v-if="archives" class="la__badge">
          <span class="la__badge-t">{{ t('logsAdmin.shownCount', { n: archives.total }) }}</span>
        </span>
      </div>

      <p v-if="archivesError" class="la__msg la__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ archivesError }}</span>
      </p>

      <template v-if="archives">
        <!-- ★★ 三态；「未启用」是**键不存在**，不是 exists=false -->
        <p v-if="archivesDisabled" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.archiveDisabledNote') }}</span>
        </p>
        <p v-else-if="archivesState === 'dir-missing'" class="la__note la__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('logsAdmin.archiveDirMissingNote') }}</span>
        </p>
        <p class="la__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('logsAdmin.archiveShapeNote') }}</span>
        </p>

        <p v-if="archivesDisabled || archivesState === 'dir-missing'" class="la__msg">
          {{ t('logsAdmin.archiveEmpty') }}
        </p>
        <ul v-if="archives.archives.length" class="la__list">
          <li v-for="a in archives.archives" :key="a.name" class="la__item">
            <div class="la__head">
              <span class="la__title">{{ a.name }}</span>
              <span class="la__badge">
                <span class="la__badge-t">{{ a.size_human }}</span>
              </span>
            </div>
            <p class="la__meta">{{ relativeTime(a.mod_time) }}</p>
          </li>
        </ul>
      </template>
    </section>

    <p class="la__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('logsAdmin.readOnlyNote') }}</span>
    </p>
    <button type="button" class="la__btn" @click="load">
      {{ t('common.refresh') }}
    </button>
  </div>
</template>

<style scoped>
.la__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.la__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.la__head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.la__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.la__msg--err { color: var(--app-danger); }
.la__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.la__note--warn { color: var(--app-warning); }
.la__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
.la__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.la__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; flex-shrink: 0; }
.la__badge-t { color: var(--app-text-secondary); }
.la__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.la__cell { display: flex; flex-direction: column; }
.la__cell-l { font-size: 11px; color: var(--app-text-muted); }
.la__cell-v { font-size: 14px; font-weight: 600; word-break: break-all; }
.la__list { list-style: none; margin: 0; padding: 0; }
.la__item { padding: 10px 0; border-top: 1px solid var(--app-border); }
.la__tail { display: flex; gap: 6px; flex-wrap: wrap; margin: 6px 0 0; }
.la__chip { font-size: 11px; padding: 2px 6px; border-radius: 6px; border: 1px solid var(--app-border); }
.la__chip--ok { color: var(--success, #2e7d32); border-color: var(--success, #2e7d32); }
.la__chip--muted { color: var(--app-text-muted); }

/* ★ R1：新增交互控件 ≥48 CSS px */
.la__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-primary); background: transparent; color: var(--app-primary); }
</style>
