<script setup lang="ts">
// StorageMigrationStateView — 批 202（2026-10-17）
// 🟢 加 router/nav：storageMigrationState 独立深链详细页
//
// 端点：GET /api/admin/storage/migration-state（admin 中间件，handler.go:1175）
// 类型：MigrationStateResponse（api/tuning.ts:834）+ MigrationRun 14 字段
//
// 17 跳 evidence（line 1023 / 1041）："不查 DB（内存 migrationStatus 状态机）" by-design
//
// 真 orphan 性：getStorageMigrationState API 已 export + StorageConfig.vue 已用，
//   缺独立深链详细页让用户能直接打开 /admin/data-lifecycle/migration-state
import { onMounted, onUnmounted, ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { getStorageMigrationState, type MigrationStateResponse, type MigrationRun, type MigrationStatus } from '../api/tuning'
import { VideoPause, Timer } from '@element-plus/icons-vue'

const loading = ref(false)
const error = ref('')
const state = ref<MigrationStateResponse | null>(null)
const refreshing = ref(false)
let pollHandle: number | null = null

async function load(silent = false) {
  if (!silent) loading.value = true
  if (silent) refreshing.value = true
  error.value = ''
  try {
    state.value = await getStorageMigrationState()
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function formatBytes(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(1) + ' GB'
}

function formatTimestamp(s: string | undefined | null): string {
  if (!s) return '—'
  return s.replace('T', ' ').replace(/\.\d+Z$/, '').replace('Z', '')
}

function statusLabel(s: MigrationStatus | undefined): string {
  if (!s) return '未知'
  if (s === 'idle') return '空闲'
  if (s === 'running') return '运行中'
  if (s === 'succeeded') return '成功'
  if (s === 'failed') return '失败'
  return s
}

function progressPct(run: MigrationRun | null | undefined): number {
  if (!run || !run.files_total) return 0
  return Math.min(100, Math.round((run.files_copied / run.files_total) * 100))
}

onMounted(() => {
  void load()
  // 轮询：迁移运行中每 5s 拉一次最新状态
  pollHandle = window.setInterval(() => {
    if (state.value?.running) void load(true)
  }, 5000)
})

onUnmounted(() => {
  if (pollHandle !== null) {
    clearInterval(pollHandle)
    pollHandle = null
  }
})
</script>

<template>
  <div class="storage-migration-state-view">
    <header class="sms-header">
      <h1 class="sms-title">存储迁移状态</h1>
      <span class="sms-polling" v-if="state?.running"><Timer /> 自动轮询中（5s）</span>
      <span class="sms-polling" v-else><VideoPause /> 空闲</span>
      <button class="btn btn-ghost btn-sm" :disabled="loading || refreshing" @click="load()">
        {{ loading ? $t('common.feedback.loading') : refreshing ? $t('common.terms.refreshing') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="sms-error">{{ error }}</p>

    <div v-if="loading && !state" class="sms-loading">{{ $t('common.feedback.loading') }}</div>

    <template v-else-if="state">
      <!-- 1. 当前运行中迁移 -->
      <section v-if="state.running" class="sms-section sms-section-running">
        <header class="sms-section-header">
          <h2 class="sms-section-title">当前迁移（运行中）</h2>
          <span class="sms-status-badge is-running">{{ statusLabel(state.running.status) }}</span>
        </header>
        <div class="sms-row"><span class="sms-label">run_id</span><code>{{ state.running.run_id }}</code></div>
        <div class="sms-row"><span class="sms-label">从</span><code>{{ state.running.from_dir }}</code></div>
        <div class="sms-row"><span class="sms-label">到</span><code>{{ state.running.to_dir }}</code></div>
        <div class="sms-row"><span class="sms-label">{{ $t('common.terms.startTime') }}</span>{{ formatTimestamp(state.running.started_at) }}</div>
        <div class="sms-row"><span class="sms-label">{{ $t('common.terms.recentHeartbeat') }}</span>{{ formatTimestamp(state.running.heartbeat_at) }}</div>
        <div class="sms-row">
          <span class="sms-label">文件进度</span>
          <span>{{ state.running.files_copied }} / {{ state.running.files_total }}（{{ progressPct(state.running) }}%）</span>
        </div>
        <div class="sms-progress"><div class="sms-progress-bar" :style="{ width: progressPct(state.running) + '%' }"></div></div>
        <div class="sms-row">
          <span class="sms-label">字节进度</span>
          <span>{{ formatBytes(state.running.bytes_copied) }} / {{ formatBytes(state.running.bytes_total) }}</span>
        </div>
        <p v-if="state.running.errors?.length" class="sms-errors">
          错误：{{ state.running.errors.join('; ') }}
        </p>
      </section>

      <!-- 2. 历史最新迁移 -->
      <section v-if="state.latest" class="sms-section">
        <header class="sms-section-header">
          <h2 class="sms-section-title">历史最新迁移</h2>
          <span class="sms-status-badge" :class="`is-${state.latest.status}`">{{ statusLabel(state.latest.status) }}</span>
        </header>
        <div class="sms-row"><span class="sms-label">run_id</span><code>{{ state.latest.run_id }}</code></div>
        <div class="sms-row"><span class="sms-label">从</span><code>{{ state.latest.from_dir }}</code></div>
        <div class="sms-row"><span class="sms-label">到</span><code>{{ state.latest.to_dir }}</code></div>
        <div class="sms-row"><span class="sms-label">{{ $t('common.terms.start') }}</span>{{ formatTimestamp(state.latest.started_at) }}</div>
        <div class="sms-row"><span class="sms-label">{{ $t('common.terms.finish') }}</span>{{ formatTimestamp(state.latest.finished_at) }}</div>
        <div class="sms-row"><span class="sms-label">文件</span>{{ state.latest.files_copied }} / {{ state.latest.files_total }}</div>
        <div class="sms-row"><span class="sms-label">字节</span>{{ formatBytes(state.latest.bytes_copied) }} / {{ formatBytes(state.latest.bytes_total) }}</div>
        <div class="sms-row"><span class="sms-label">已删除文件</span>{{ state.latest.files_deleted }}</div>
        <div class="sms-row"><span class="sms-label">旧目录已清</span>{{ state.latest.old_dir_purged ? $t('common.button.yes') : $t('common.button.no') }}</div>
        <p v-if="state.latest.errors?.length" class="sms-errors">
          错误：{{ state.latest.errors.join('; ') }}
        </p>
      </section>

      <p v-if="!state.running && !state.latest" class="sms-empty">
        暂无迁移记录
      </p>
    </template>
  </div>
</template>

<style scoped>
.storage-migration-state-view { padding: var(--space-4); }
.sms-header { display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-3); }
.sms-title { font-size: var(--text-xl); font-weight: var(--font-semibold); }
.sms-polling { color: var(--text-muted); font-size: var(--text-sm); }
.sms-error { color: var(--danger); padding: var(--space-2); background: var(--danger-bg); border-radius: var(--radius); margin-bottom: var(--space-3); }
.sms-loading { color: var(--text-muted); padding: var(--space-3); }
.sms-empty { color: var(--text-muted); padding: var(--space-3); font-style: italic; }
.sms-section { background: var(--bg-surface); border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-3); margin-bottom: var(--space-3); }
.sms-section-running { border: 1px solid var(--kx-primary); }
.sms-section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-2); }
.sms-section-title { font-size: var(--text-md); font-weight: var(--font-semibold); }
.sms-status-badge { padding: 2px 8px; border-radius: 4px; font-size: var(--text-sm); }
.sms-status-badge.is-running { background: var(--kx-primary-soft, var(--bg-subtle)); color: var(--kx-primary); }
.sms-status-badge.is-succeeded { background: var(--success-bg); color: var(--success); }
.sms-status-badge.is-failed { background: var(--danger-bg); color: var(--danger); }
.sms-row { display: grid; grid-template-columns: 120px 1fr; gap: var(--space-2); padding: var(--space-1) 0; font-size: var(--text-sm); }
.sms-label { color: var(--text-muted); }
.sms-progress { height: 6px; background: var(--bg-subtle); border-radius: 3px; overflow: hidden; margin: var(--space-1) 0; }
.sms-progress-bar { height: 100%; background: var(--kx-primary); transition: width 0.3s var(--kx-motion-ease-out); }
.sms-errors { margin-top: var(--space-2); padding: var(--space-2); background: var(--danger-bg); color: var(--danger); border-radius: var(--radius); font-size: var(--text-sm); }
</style>
