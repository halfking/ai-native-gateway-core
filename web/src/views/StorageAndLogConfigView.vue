<script setup lang="ts">
// StorageAndLogConfigView — 批 204（2026-10-17）
// 🟡 P0 storageAndLogConfig 孤儿接前端：附件存储 + 日志配置只读摘要
//
// 端点：3 段并发
//   GET /api/admin/storage/config（superAdmin 中间件，handler.go:1172）
//   GET /api/admin/attachments/filesystem/stats（admin 中间件）
//   GET /api/admin/log/config（admin 中间件）
//
// 17 跳 evidence（line 1062）原判"否（by-design）/ 0 工作量"——子视图
//   StorageConfig / FilesystemMaintenance / LogManagement 全在 DataLifecycleView 父视图下使用。
//
// 本批按用户拍板"全部 8"——建 1 个只读深链详细页（不复用 DataLifecycleView 的编辑功能）
import { onMounted, ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { getStorageAndLogSnapshot, type StorageAndLogSnapshot, type AttachmentFilesystemStats } from '../api/storageAndLogConfig'

const loading = ref(false)
const error = ref('')
const data = ref<StorageAndLogSnapshot | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await getStorageAndLogSnapshot()
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function formatBytes(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  if (n < 1024 * 1024 * 1024 * 1024) return (n / 1024 / 1024 / 1024).toFixed(1) + ' GB'
  return (n / 1024 / 1024 / 1024 / 1024).toFixed(1) + ' TB'
}

function formatTimestamp(s: string | undefined | null): string {
  if (!s) return '—'
  return s.replace('T', ' ').replace(/\.\d+Z$/, '').replace('Z', '')
}

function diskLevelLabel(l: AttachmentFilesystemStats['disk_warning_level'] | undefined): string {
  if (l === 'safe') return '安全'
  if (l === 'warning') return '警告'
  if (l === 'danger') return '危险'
  return '—'
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="storage-and-log-config-view">
    <header class="slc-header">
      <h1 class="slc-title">存储与日志配置</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="slc-error">{{ error }}</p>

    <div v-if="loading && !data" class="slc-loading">{{ $t('common.feedback.loading') }}</div>

    <template v-else-if="data">
      <!-- 1. 存储配置 -->
      <section class="slc-section">
        <h2 class="slc-section-title">存储配置</h2>
        <div class="slc-grid">
          <div class="slc-field">
            <span class="slc-label">DB 覆盖目录</span>
            <code>{{ data.storage.attachment_dir_override || '（空，用 env）' }}</code>
          </div>
          <div class="slc-field">
            <span class="slc-label">生效目录</span>
            <code>{{ data.storage.effective_dir }}</code>
          </div>
          <div class="slc-field">
            <span class="slc-label">env 回退</span>
            <code>{{ data.storage.attachment_dir_env || '—' }}</code>
          </div>
          <div class="slc-field">
            <span class="slc-label">保留 TTL（天）</span>
            <span>{{ data.storage.ttl_days }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">单文件上限（MB）</span>
            <span>{{ data.storage.max_file_size_mb }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">告警水位（%）</span>
            <span>{{ data.storage.disk_quota_percent }}%</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">自动清理</span>
            <span>{{ data.storage.auto_cleanup_enabled ? `${$t('common.terms.on')}（${data.storage.auto_cleanup_threshold}%）` : $t('common.terms.off') }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">需重启</span>
            <span :class="{ 'is-warning': data.storage.needs_restart }">{{ data.storage.needs_restart ? $t('common.button.yes') : $t('common.button.no') }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">当前磁盘占用</span>
            <span>{{ formatBytes(data.storage.current_disk_usage) }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">配置来源</span>
            <code>{{ data.storage.config_source }}</code>
          </div>
        </div>
      </section>

      <!-- 2. 附件文件系统统计 -->
      <section class="slc-section">
        <h2 class="slc-section-title">附件文件系统</h2>
        <div class="slc-grid">
          <div class="slc-field">
            <span class="slc-label">附件目录</span>
            <code>{{ data.filesystem.attachment_dir }}</code>
          </div>
          <div class="slc-field">
            <span class="slc-label">总文件数</span>
            <span>{{ data.filesystem.total_files }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">{{ $t('common.terms.totalSize') }}</span>
            <span>{{ data.filesystem.total_size_human || formatBytes(data.filesystem.total_size_bytes) }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">最旧文件</span>
            <span>{{ formatTimestamp(data.filesystem.oldest_file_time) }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">磁盘总量</span>
            <span>{{ formatBytes(data.filesystem.disk_total_bytes) }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">已用</span>
            <span>{{ formatBytes(data.filesystem.disk_used_bytes) }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">{{ $t('common.terms.available') }}</span>
            <span>{{ formatBytes(data.filesystem.disk_avail_bytes) }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">使用率</span>
            <span :class="`disk-level-${data.filesystem.disk_warning_level}`">
              {{ data.filesystem.disk_usage_percent.toFixed(1) }}%（{{ diskLevelLabel(data.filesystem.disk_warning_level) }}）
            </span>
          </div>
        </div>
      </section>

      <!-- 3. 日志配置 -->
      <section class="slc-section">
        <h2 class="slc-section-title">日志配置</h2>
        <div class="slc-grid">
          <div class="slc-field">
            <span class="slc-label">文件日志</span>
            <span :class="`log-enabled-${data.log.enabled_source}`">
              {{ data.log.enabled ? $t('common.status.enabled') : $t('common.status.disabled') }}（来源：{{ data.log.enabled_source }}）
            </span>
          </div>
          <div class="slc-field">
            <span class="slc-label">日志文件</span>
            <code>{{ data.log.log_file || data.log.file_path }}</code>
          </div>
          <div class="slc-field">
            <span class="slc-label">日志目录</span>
            <code>{{ data.log.log_dir }}</code>
          </div>
          <div class="slc-field">
            <span class="slc-label">单文件上限（MB）</span>
            <span>{{ data.log.max_size_mb }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">备份数</span>
            <span>{{ data.log.max_backups }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">最大保留（天）</span>
            <span>{{ data.log.max_age_days }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">压缩</span>
            <span>{{ data.log.compress ? $t('common.button.yes') : $t('common.button.no') }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">归档（天）</span>
            <span>{{ data.log.archive_days }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">删除（天）</span>
            <span>{{ data.log.delete_days }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">热加载</span>
            <span>{{ data.log.hot_reloadable ? $t('common.button.yes') : $t('common.button.no') }}</span>
          </div>
          <div class="slc-field">
            <span class="slc-label">配置来源</span>
            <code>{{ data.log.config_source }}</code>
          </div>
        </div>
      </section>

      <footer class="slc-footer">
        只读快照 · 编辑请用 /admin/data-lifecycle 父视图（StorageConfig / FilesystemMaintenance / LogManagement 三个 tab）<br>
        最近刷新: {{ data.refreshedAt }}
      </footer>
    </template>
  </div>
</template>

<style scoped>
.storage-and-log-config-view { padding: var(--space-4); }
.slc-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-3); }
.slc-title { font-size: var(--text-xl); font-weight: var(--font-semibold); }
.slc-error { color: var(--danger); padding: var(--space-2); background: var(--danger-bg); border-radius: var(--radius); margin-bottom: var(--space-3); }
.slc-loading { color: var(--text-muted); padding: var(--space-3); }
.slc-section { background: var(--bg-surface); border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-3); margin-bottom: var(--space-3); }
.slc-section-title { font-size: var(--text-md); font-weight: var(--font-semibold); margin-bottom: var(--space-2); }
.slc-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: var(--space-2); }
.slc-field { display: flex; flex-direction: column; gap: 2px; }
.slc-label { color: var(--text-muted); font-size: var(--text-sm); }
.slc-field code { font-size: var(--text-sm); }
.slc-field span:not(.slc-label) { font-size: var(--text-sm); }
.slc-field .is-warning { color: var(--warning); font-weight: var(--font-semibold); }
.slc-field .disk-level-safe { color: var(--success); }
.slc-field .disk-level-warning { color: var(--warning); }
.slc-field .disk-level-danger { color: var(--danger); }
.slc-field .log-enabled-db { color: var(--success); }
.slc-field .log-enabled-env { color: var(--text-muted); }
.slc-footer { margin-top: var(--space-3); color: var(--text-muted); font-size: var(--text-sm); line-height: 1.5; }
@media (max-width: 768px) { .slc-grid { grid-template-columns: 1fr; } }
</style>
