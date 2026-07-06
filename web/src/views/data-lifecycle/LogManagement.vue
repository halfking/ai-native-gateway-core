<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { localeRef } from '../../i18n'
import {
  logConfigGet, logConfigUpdate, logFilesList, logStats,
  logArchive, logCleanup, logArchiveList,
  type LogConfig, type LogFile, type LogStats, type LogOpResult,
} from '../../api'

const config = ref<LogConfig | null>(null)
const stats = ref<LogStats | null>(null)
const files = ref<LogFile[]>([])
const archives = ref<any[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const saveMsg = ref<string | null>(null)

// 编辑表单
const form = ref({
  max_size_mb: 100,
  max_backups: 10,
  max_age_days: 7,
  compress: true,
  archive_days: 7,
  delete_days: 30,
})

// 操作表单
const archiveForm = ref({ older_than_days: 7, dry_run: true })
const cleanupForm = ref({ older_than_days: 30, dry_run: true, scope: 'all' })
const opResult = ref<LogOpResult | null>(null)
const opLoading = ref(false)

const diskUsageColor = computed(() => {
  const pct = stats.value?.disk_usage_pct || 0
  if (pct >= 90) return '#f85149'
  if (pct >= 80) return '#d29922'
  return '#3fb950'
})

async function load() {
  loading.value = true
  error.value = null
  try {
    const [cfg, st, fls, arc] = await Promise.all([
      logConfigGet(),
      logStats().catch(() => null),
      logFilesList().catch(() => ({ files: [], total: 0, dir: '' })),
      logArchiveList().catch(() => ({ archives: [], total: 0 })),
    ])
    config.value = cfg
    stats.value = st
    files.value = fls.files || []
    archives.value = arc.archives || []
    form.value = {
      max_size_mb: cfg.max_size_mb,
      max_backups: cfg.max_backups,
      max_age_days: cfg.max_age_days,
      compress: cfg.compress,
      archive_days: cfg.archive_days,
      delete_days: cfg.delete_days,
    }
  } catch (e: any) {
    error.value = e.message || '加载失败'
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  saveMsg.value = null
  try {
    const cfg = await logConfigUpdate({
      max_size_mb: form.value.max_size_mb,
      max_backups: form.value.max_backups,
      max_age_days: form.value.max_age_days,
      compress: form.value.compress,
      archive_days: form.value.archive_days,
      archive_delete_days: form.value.delete_days,
    })
    config.value = cfg
    saveMsg.value = '✅ 已保存并热加载生效（无需重启）'
  } catch (e: any) {
    saveMsg.value = '❌ 保存失败：' + (e.message || '未知错误')
  } finally {
    saving.value = false
  }
}

async function doArchive() {
  opLoading.value = true
  opResult.value = null
  try {
    opResult.value = await logArchive({
      older_than_days: archiveForm.value.older_than_days,
      dry_run: archiveForm.value.dry_run,
    })
    if (!archiveForm.value.dry_run) {
      await load()
    }
  } catch (e: any) {
    alert('操作失败：' + (e.message || '未知错误'))
  } finally {
    opLoading.value = false
  }
}

async function doCleanup() {
  if (!cleanupForm.value.dry_run) {
    if (!confirm(`确认删除 ${cleanupForm.value.older_than_days} 天前的日志文件？此操作不可恢复。`)) return
  }
  opLoading.value = true
  opResult.value = null
  try {
    opResult.value = await logCleanup({
      older_than_days: cleanupForm.value.older_than_days,
      dry_run: cleanupForm.value.dry_run,
      scope: cleanupForm.value.scope,
    })
    if (!cleanupForm.value.dry_run) {
      await load()
    }
  } catch (e: any) {
    alert('操作失败：' + (e.message || '未知错误'))
  } finally {
    opLoading.value = false
  }
}

function formatTime(s: string | null): string {
  if (!s) return '-'
  return new Date(s).toLocaleString(localeRef.value)
}

onMounted(() => {
  load()
})

defineExpose({ load })

function fmtNum(n: number) {
  return n.toLocaleString(localeRef.value)
}
</script>

<template>
  <div class="log-management">
    <div class="header">
      <h2>日志管理</h2>
      <button @click="load" :disabled="loading" class="btn-refresh">
        {{ loading ? '加载中...' : '刷新' }}
      </button>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>

    <!-- 区块1：日志目录占用 -->
    <div v-if="stats" class="card">
      <h3 class="card-title">日志目录占用</h3>
      <div v-if="!stats.exists" class="warn-box">⚠️ 日志目录不存在或文件日志未启用</div>
      <template v-else>
        <div class="usage-row">
          <div class="usage-bar-track">
            <div class="usage-bar-fill" :style="{ width: (stats.disk_usage_pct || 0) + '%', background: diskUsageColor }"></div>
          </div>
          <span class="usage-pct">磁盘占用 {{ (stats.disk_usage_pct || 0).toFixed(1) }}%</span>
        </div>
        <div class="stats-grid">
          <div class="stat-item">
            <div class="stat-label">日志目录</div>
            <div class="stat-value mono">{{ stats.log_dir }}</div>
          </div>
          <div class="stat-item">
            <div class="stat-label">文件数</div>
            <div class="stat-value">{{ stats.total_files && fmtNum(stats.total_files) }}</div>
          </div>
          <div class="stat-item">
            <div class="stat-label">总大小</div>
            <div class="stat-value">{{ stats.total_size_human }}</div>
          </div>
          <div class="stat-item">
            <div class="stat-label">归档大小</div>
            <div class="stat-value">{{ archives.length }} 个归档</div>
          </div>
          <div class="stat-item">
            <div class="stat-label">最旧文件</div>
            <div class="stat-value">{{ formatTime(stats.oldest_mtime) }}</div>
          </div>
          <div class="stat-item">
            <div class="stat-label">最新文件</div>
            <div class="stat-value">{{ formatTime(stats.newest_mtime) }}</div>
          </div>
        </div>
      </template>
    </div>

    <!-- 区块2：轮转配置（热加载） -->
    <div v-if="config" class="card">
      <h3 class="card-title">
        日志轮转配置
        <span v-if="config.hot_reloadable" class="badge ok">支持热加载</span>
        <span v-if="!config.enabled" class="badge warn">文件日志未启用</span>
      </h3>
      <div class="form-grid">
        <div class="form-group">
          <label class="form-label">单文件大小上限（MB）</label>
          <input type="number" v-model.number="form.max_size_mb" min="1" max="1000" class="form-input" :disabled="!config.enabled" />
        </div>
        <div class="form-group">
          <label class="form-label">备份保留数量</label>
          <input type="number" v-model.number="form.max_backups" min="0" max="100" class="form-input" :disabled="!config.enabled" />
        </div>
        <div class="form-group">
          <label class="form-label">备份最大保留天数</label>
          <input type="number" v-model.number="form.max_age_days" min="0" max="365" class="form-input" :disabled="!config.enabled" />
        </div>
        <div class="form-group">
          <label class="form-label">归档天数（超此打包）</label>
          <input type="number" v-model.number="form.archive_days" min="1" max="365" class="form-input" />
        </div>
        <div class="form-group">
          <label class="form-label">删除天数（超此删除归档）</label>
          <input type="number" v-model.number="form.delete_days" min="7" max="3650" class="form-input" />
        </div>
        <div class="form-group switch-group">
          <label class="switch-label">
            <input type="checkbox" v-model="form.compress" :disabled="!config.enabled" />
            <span>轮转时 gzip 压缩</span>
          </label>
        </div>
      </div>
      <div class="form-actions">
        <button @click="save" :disabled="saving || !config.enabled" class="btn-primary">
          {{ saving ? '保存中...' : '保存并热加载' }}
        </button>
      </div>
      <div v-if="saveMsg" class="save-msg">{{ saveMsg }}</div>
    </div>

    <!-- 区块3：日志文件列表 -->
    <div class="card">
      <h3 class="card-title">日志文件列表（{{ files.length }}）</h3>
      <div v-if="files.length === 0" class="empty-hint">暂无日志文件</div>
      <div v-else class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>文件名</th>
              <th>大小</th>
              <th>修改时间</th>
              <th>状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in files" :key="f.name">
              <td class="mono">{{ f.name }}</td>
              <td>{{ f.size_human }}</td>
              <td>{{ formatTime(f.mod_time) }}</td>
              <td>
                <span v-if="f.is_current" class="badge ok">当前</span>
                <span v-if="f.is_compressed" class="badge">已压缩</span>
                <span v-if="!f.is_current && !f.is_compressed" class="badge muted">备份</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 区块4：归档与删除 -->
    <div class="card">
      <h3 class="card-title">归档与删除（分层保留）</h3>
      <div class="op-grid">
        <div class="op-box">
          <h4>归档（温层）</h4>
          <p class="op-desc">把超期日志打包移到 archive/ 子目录，可恢复</p>
          <div class="form-group">
            <label class="form-label">归档 N 天前的文件</label>
            <input type="number" v-model.number="archiveForm.older_than_days" min="1" class="form-input" />
          </div>
          <label class="switch-label">
            <input type="checkbox" v-model="archiveForm.dry_run" />
            <span>预览模式（不实际执行）</span>
          </label>
          <button @click="doArchive" :disabled="opLoading" class="btn-secondary">
            {{ archiveForm.dry_run ? '预览归档' : '执行归档' }}
          </button>
        </div>
        <div class="op-box">
          <h4>删除（冷层）</h4>
          <p class="op-desc danger">永久删除超期文件，不可恢复</p>
          <div class="form-group">
            <label class="form-label">删除 N 天前的文件</label>
            <input type="number" v-model.number="cleanupForm.older_than_days" min="7" class="form-input" />
          </div>
          <div class="form-group">
            <label class="form-label">范围</label>
            <select v-model="cleanupForm.scope" class="form-input">
              <option value="all">全部（备份 + 归档）</option>
              <option value="backups">仅轮转备份</option>
              <option value="archives">仅归档文件</option>
            </select>
          </div>
          <label class="switch-label">
            <input type="checkbox" v-model="cleanupForm.dry_run" />
            <span>预览模式</span>
          </label>
          <button @click="doCleanup" :disabled="opLoading" class="btn-danger">
            {{ cleanupForm.dry_run ? '预览删除' : '执行删除' }}
          </button>
        </div>
      </div>
      <div v-if="opResult" class="op-result">
        <div class="op-result-row">
          <span>影响文件数：<strong>{{ opResult.files_affected }}</strong></span>
          <span>释放空间：<strong class="highlight">{{ opResult.bytes_freed_human }}</strong></span>
          <span v-if="opResult.archive_file">归档至：<code>{{ opResult.archive_file }}</code></span>
        </div>
        <div v-if="opResult.dry_run && opResult.affected_paths?.length" class="path-list">
          <div v-for="p in opResult.affected_paths.slice(0, 20)" :key="p" class="path-item mono">{{ p }}</div>
          <div v-if="opResult.affected_paths.length > 20" class="path-more">...共 {{ opResult.affected_paths.length }} 个文件</div>
        </div>
        <div v-if="opResult.error" class="error-text">{{ opResult.error }}</div>
      </div>
    </div>

    <!-- 区块5：已归档文件 -->
    <div v-if="archives.length > 0" class="card">
      <h3 class="card-title">已归档文件（{{ archives.length }}）</h3>
      <div class="table-wrap">
        <table class="data-table">
          <thead>
            <tr><th>归档文件</th><th>大小</th><th>归档时间</th></tr>
          </thead>
          <tbody>
            <tr v-for="a in archives" :key="a.name">
              <td class="mono">{{ a.name }}</td>
              <td>{{ a.size_human }}</td>
              <td>{{ formatTime(a.mod_time) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.log-management { padding: 0; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.header h2 { margin: 0; font-size: 18px; color: var(--text, #e6edf3); }
.btn-refresh { background: var(--card, #1c2128); border: 1px solid var(--border, #30363d); border-radius: var(--radius, 8px); padding: 4px 12px; cursor: pointer; color: var(--text, #e6edf3); transition: background .15s, border-color .15s; }
.btn-refresh:hover:not(:disabled) { background: #21262d; border-color: var(--muted, #8b949e); }
.btn-refresh:disabled { opacity: .4; cursor: not-allowed; }
.card { background: var(--card, #1c2128); border: 1px solid var(--border, #30363d); border-radius: var(--radius, 8px); padding: 16px 20px; margin-bottom: 16px; }
.card-title { margin: 0 0 12px; font-size: 15px; color: var(--text, #e6edf3); border-bottom: 1px solid var(--border, #30363d); padding-bottom: 8px; display: flex; align-items: center; gap: 8px; }
.badge { font-size: 11px; padding: 2px 8px; border-radius: 10px; font-weight: normal; }
.badge.ok { background: rgba(63,185,80,.15); color: var(--success, #3fb950); border: 1px solid rgba(63,185,80,.3); }
.badge.warn { background: rgba(210,153,34,.15); color: var(--warning, #d29922); border: 1px solid rgba(210,153,34,.3); }
.badge.muted { background: rgba(139,148,158,.15); color: var(--muted, #8b949e); }
.usage-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.usage-bar-track { flex: 1; height: 20px; background: #0f1117; border-radius: 10px; overflow: hidden; }
.usage-bar-fill { height: 100%; border-radius: 10px; }
.usage-pct { font-weight: 600; min-width: 140px; text-align: right; color: var(--text, #e6edf3); font-size: 13px; }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
.stat-item { padding: 4px 0; }
.stat-label { font-size: 12px; color: var(--muted, #8b949e); margin-bottom: 4px; }
.stat-value { font-size: 14px; color: var(--text, #e6edf3); font-weight: 500; word-break: break-all; }
.mono { font-family: 'SF Mono', Consolas, monospace; font-size: 12px; color: var(--text, #e6edf3); }
.form-group { margin-bottom: 12px; }
.form-label { display: block; font-size: 13px; color: var(--muted, #8b949e); margin-bottom: 4px; }
.form-input { width: 100%; padding: 6px 10px; background: #0f1117; border: 1px solid var(--border, #30363d); border-radius: 4px; font-size: 14px; box-sizing: border-box; color: var(--text, #e6edf3); transition: border-color .15s; }
.form-input:focus { outline: none; border-color: var(--accent, #6366f1); }
.form-input:disabled { background: #161b22; color: var(--muted, #8b949e); opacity: .5; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 16px; }
.switch-group { display: flex; align-items: flex-end; }
.switch-label { display: flex; align-items: center; gap: 6px; cursor: pointer; font-size: 13px; color: var(--text, #e6edf3); }
.switch-label input { width: 15px; height: 15px; accent-color: var(--accent, #6366f1); }
.form-actions { margin-top: 12px; }
.btn-primary { background: var(--accent, #6366f1); color: #fff; border: none; border-radius: 4px; padding: 8px 24px; cursor: pointer; font-size: 14px; transition: opacity .15s; }
.btn-primary:hover:not(:disabled) { opacity: .85; }
.btn-primary:disabled { opacity: .4; cursor: not-allowed; }
.btn-secondary { background: var(--card, #1c2128); border: 1px solid var(--accent, #6366f1); color: var(--accent-h, #818cf8); border-radius: 4px; padding: 6px 16px; cursor: pointer; margin-top: 8px; transition: opacity .15s; }
.btn-secondary:hover:not(:disabled) { opacity: .85; }
.btn-secondary:disabled { opacity: .4; cursor: not-allowed; }
.btn-danger { background: var(--card, #1c2128); border: 1px solid var(--danger, #f85149); color: var(--danger, #f85149); border-radius: 4px; padding: 6px 16px; cursor: pointer; margin-top: 8px; transition: opacity .15s; }
.btn-danger:hover:not(:disabled) { background: rgba(248,81,73,.12); }
.btn-danger:disabled { opacity: .4; cursor: not-allowed; }
.save-msg { margin-top: 10px; font-size: 13px; color: var(--accent-h, #818cf8); }
.warn-box { padding: 8px 12px; background: rgba(210,153,34,.1); border: 1px solid rgba(210,153,34,.3); border-radius: 4px; color: var(--warning, #d29922); font-size: 13px; }
.error-box { padding: 8px 12px; background: rgba(248,81,73,.1); border: 1px solid rgba(248,81,73,.3); border-radius: 4px; color: var(--danger, #f85149); margin-bottom: 16px; }
.empty-hint { color: var(--muted, #8b949e); font-size: 13px; padding: 16px 0; text-align: center; }
.table-wrap { overflow-x: auto; }
.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table th { text-align: left; padding: 8px 12px; background: #161b22; border-bottom: 1px solid var(--border, #30363d); color: var(--muted, #8b949e); font-weight: 500; }
.data-table td { padding: 8px 12px; border-bottom: 1px solid var(--border, #30363d); color: var(--text, #e6edf3); }
.op-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.op-box { padding: 12px; background: #0f1117; border: 1px solid var(--border, #30363d); border-radius: 4px; }
.op-box h4 { margin: 0 0 4px; font-size: 14px; color: var(--text, #e6edf3); }
.op-desc { margin: 0 0 10px; font-size: 12px; color: var(--muted, #8b949e); }
.op-desc.danger { color: var(--danger, #f85149); }
.op-result { margin-top: 16px; padding: 12px; background: rgba(63,185,80,.08); border: 1px solid rgba(63,185,80,.3); border-radius: 4px; color: var(--text, #e6edf3); }
.op-result-row { display: flex; gap: 24px; font-size: 13px; flex-wrap: wrap; }
.op-result-row code { background: #0f1117; border: 1px solid var(--border, #30363d); padding: 1px 6px; border-radius: 4px; color: var(--accent-h, #818cf8); font-family: ui-monospace, SFMono-Regular, monospace; }
.highlight { color: var(--success, #3fb950); }
.path-list { margin-top: 8px; max-height: 150px; overflow-y: auto; }
.path-item { font-size: 11px; color: var(--muted, #8b949e); padding: 2px 0; }
.path-more { font-size: 12px; color: var(--muted, #8b949e); padding: 4px 0; }
.error-text { margin-top: 8px; color: var(--danger, #f85149); font-size: 13px; }
</style>
