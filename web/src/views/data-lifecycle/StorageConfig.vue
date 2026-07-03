<script setup lang="ts">
import { ref, onMounted, computed, onUnmounted } from 'vue'
import {
  storageConfigGet, storageConfigUpdate, storageConfigTestPath,
  attachmentFilesystemStats,
  getStorageMigrationState,
  setAttachmentURLPrefix,
  type StorageConfig,
  type MigrationRun,
} from '../../api'

const config = ref<StorageConfig | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
const saveMsg = ref<string | null>(null)

// 编辑表单
const form = ref({
  attachment_dir_override: '',
  ttl_days: 30,
  max_file_size_mb: 20,
  disk_quota_percent: 80,
  auto_cleanup_enabled: false,
  auto_cleanup_threshold: 85,
})

// 路径测试
const testPath = ref('')
const testResult = ref<any>(null)
const testing = ref(false)

// 附件文件系统占用（复用现有端点）
const fsStats = ref<any>(null)

// ── 目录迁移进度跟踪 (2026-07-02) ──
// 修改目录后 PUT 会返回 migration_run_id，前端据此轮询迁移进度。
const migration = ref<MigrationRun | null>(null)
let migrationTimer: ReturnType<typeof setInterval> | null = null
let pollCount = 0
const MAX_POLLS = 900 // 30分钟 (900 * 2s)，防止迁移卡死时无限轮询

const isMigrating = computed(() => migration.value?.status === 'running')
const migrationPct = computed(() => {
  const m = migration.value
  if (!m || m.files_total <= 0) return 0
  return Math.min(100, Math.round((m.files_copied / m.files_total) * 100))
})

const diskUsageColor = computed(() => {
  const pct = config.value?.current_disk_usage || 0
  if (pct >= 90) return '#f85149'
  if (pct >= 80) return '#d29922'
  return '#3fb950'
})

// 目录是否相对当前生效目录有变更（决定 save 是否弹迁移确认框）
const dirChanged = computed(() => {
  const cur = config.value?.effective_dir || ''
  const editing = form.value.attachment_dir_override
  // 编辑值解析后与当前生效目录对比（粗略：去尾斜杠后字符串比较）
  const target = editing || config.value?.attachment_dir_env || './data/attachments'
  return normalizeDir(target) !== normalizeDir(cur)
})

function normalizeDir(s: string): string {
  return (s || '').replace(/\/+$/, '').trim()
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const [cfg, fs] = await Promise.all([
      storageConfigGet(),
      attachmentFilesystemStats().catch(() => null),
    ])
    config.value = cfg
    form.value = {
      attachment_dir_override: cfg.attachment_dir_override || '',
      ttl_days: cfg.ttl_days,
      max_file_size_mb: cfg.max_file_size_mb,
      disk_quota_percent: cfg.disk_quota_percent,
      auto_cleanup_enabled: cfg.auto_cleanup_enabled,
      auto_cleanup_threshold: cfg.auto_cleanup_threshold,
    }
    fsStats.value = fs
    // 注入附件下载 URL 前缀（供 logs.ts:attachmentURL 使用）
    if (cfg.download_url_prefix) setAttachmentURLPrefix(cfg.download_url_prefix)
    // 若响应带了 migration_run_id（刚触发迁移后），开始轮询
    if (cfg.migration_run_id) {
      await pollMigration()
    } else {
      // 启动时检查是否有未完成的迁移（如进程重启后恢复进度）
      await checkMigrationState()
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
    // 目录变更时弹确认框（说明将复制文件并删除旧目录）
    if (dirChanged.value) {
      const ok = confirm(
        '即将修改附件存储目录，系统会把现有文件复制到新目录，校验完成后删除旧目录。\n\n' +
        '· 复制期间下载/写入不受影响（仍走旧目录）\n' +
        '· 完成后自动切换到新目录\n' +
        '· 文件较多时可能耗时较长，进度会在下方显示\n\n确认修改目录并迁移？'
      )
      if (!ok) {
        saving.value = false
        saveMsg.value = '已取消（目录未修改）'
        return
      }
    }
    const cfg = await storageConfigUpdate({
      attachment_dir_override: form.value.attachment_dir_override || '',
      ttl_days: form.value.ttl_days,
      max_file_size_mb: form.value.max_file_size_mb,
      disk_quota_percent: form.value.disk_quota_percent,
      auto_cleanup_enabled: form.value.auto_cleanup_enabled,
      auto_cleanup_threshold: form.value.auto_cleanup_threshold,
    })
    config.value = cfg
    if (cfg.download_url_prefix) setAttachmentURLPrefix(cfg.download_url_prefix)
    if (cfg.migration_run_id) {
      saveMsg.value = '✅ 目录已修改，正在迁移文件…'
      await pollMigration()
    } else {
      saveMsg.value = '✅ 配置已保存'
    }
  } catch (e: any) {
    saveMsg.value = '❌ 保存失败：' + (e.message || '未知错误')
  } finally {
    saving.value = false
  }
}

// 检查迁移状态（不启动，仅读取 latest/running）
async function checkMigrationState() {
  try {
    const st = await getStorageMigrationState()
    if (st.running) {
      migration.value = st.running
      startMigrationPolling()
    } else if (st.latest) {
      migration.value = st.latest
    }
  } catch {
    // 静默忽略
  }
}

// 单次拉取迁移状态并决定是否继续轮询
async function pollMigration() {
  if (pollCount++ > MAX_POLLS) {
    stopMigrationPolling()
    saveMsg.value = '❌ 迁移超时（30分钟未完成），请检查服务端日志'
    return
  }
  try {
    const st = await getStorageMigrationState()
    migration.value = st.running || st.latest
    if (st.running) {
      startMigrationPolling()
    } else {
      stopMigrationPolling()
      // 迁移完成（成功/失败）后刷新配置，使 effective_dir 等字段更新
      const cfg = await storageConfigGet()
      config.value = cfg
      if (st.latest?.status === 'succeeded') {
        saveMsg.value = '✅ 文件迁移完成，已切换到新目录'
      } else if (st.latest?.status === 'failed') {
        saveMsg.value = '❌ 迁移失败：' + (st.latest.message || '未知错误')
      }
    }
  } catch {
    // 忽略单次轮询错误
  }
}

function startMigrationPolling() {
  if (migrationTimer) return
  pollCount = 0  // 重置计数
  migrationTimer = setInterval(pollMigration, 2000)
}

function stopMigrationPolling() {
  if (migrationTimer) {
    clearInterval(migrationTimer)
    migrationTimer = null
  }
}

async function testPathAvailable() {
  if (!testPath.value.trim()) {
    alert('请输入要测试的路径')
    return
  }
  testing.value = true
  testResult.value = null
  try {
    testResult.value = await storageConfigTestPath(testPath.value.trim())
  } catch (e: any) {
    alert('测试失败：' + (e.message || '未知错误'))
  } finally {
    testing.value = false
  }
}

function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

onMounted(() => {
  load()
})

onUnmounted(() => {
  stopMigrationPolling()
})

defineExpose({ load })
</script>

<template>
  <div class="storage-config">
    <div class="header">
      <h2>存储配置</h2>
      <button @click="load" :disabled="loading" class="btn-refresh">
        {{ loading ? '加载中...' : '刷新' }}
      </button>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>

    <!-- 区块1：磁盘与占用总览 -->
    <div v-if="config" class="card">
      <h3 class="card-title">磁盘占用总览</h3>
      <div class="usage-row">
        <div class="usage-bar-track">
          <div class="usage-bar-fill" :style="{ width: (config.current_disk_usage || 0) + '%', background: diskUsageColor }"></div>
        </div>
        <span class="usage-pct">{{ (config.current_disk_usage || 0).toFixed(1) }}%</span>
      </div>
      <div class="stats-grid">
        <div class="stat-item">
          <div class="stat-label">附件目录</div>
          <div class="stat-value mono">{{ config.effective_dir }}</div>
        </div>
        <div class="stat-item">
          <div class="stat-label">附件文件数</div>
          <div class="stat-value">{{ fsStats?.total_files?.toLocaleString('zh-CN') || '—' }}</div>
        </div>
        <div class="stat-item">
          <div class="stat-label">附件占用</div>
          <div class="stat-value">{{ fsStats?.total_size_human || '—' }}</div>
        </div>
        <div class="stat-item">
          <div class="stat-label">磁盘可用</div>
          <div class="stat-value">{{ fsStats ? formatBytes(fsStats.disk_avail_bytes) : '—' }}</div>
        </div>
      </div>
      <div v-if="config.needs_restart" class="warn-box">
        ⚠️ 配置中标记需重启（下方修改目录会触发文件自动迁移，保存后即生效）
      </div>
      <!-- 目录迁移进度卡片 -->
      <div v-if="migration" class="migration-box" :class="{ running: isMigrating, failed: migration.status === 'failed', done: migration.status === 'succeeded' }">
        <div class="migration-header">
          <span class="migration-title">
            <span v-if="isMigrating">🔄 正在迁移附件文件…</span>
            <span v-else-if="migration.status === 'succeeded'">✅ 迁移完成</span>
            <span v-else-if="migration.status === 'failed'">❌ 迁移失败</span>
          </span>
          <span v-if="migration.run_id" class="migration-runid mono">{{ migration.run_id.slice(0, 16) }}</span>
        </div>
        <div class="migration-paths mono">
          <span class="migration-from">{{ migration.from_dir }}</span>
          <span class="migration-arrow"> → </span>
          <span class="migration-to">{{ migration.to_dir }}</span>
        </div>
        <div v-if="isMigrating" class="migration-progress">
          <div class="migration-bar-track">
            <div class="migration-bar-fill" :style="{ width: migrationPct + '%' }"></div>
          </div>
          <span class="migration-pct">{{ migrationPct }}%</span>
        </div>
        <div class="migration-stats">
          <span>文件 {{ migration.files_copied }} / {{ migration.files_total }}</span>
          <span>· {{ formatBytes(migration.bytes_copied) }} / {{ formatBytes(migration.bytes_total) }}</span>
          <span v-if="migration.old_dir_purged">· 旧目录已删除</span>
          <span v-else-if="migration.status === 'succeeded'">· 旧目录保留</span>
        </div>
        <div v-if="migration.message" class="migration-msg">{{ migration.message }}</div>
        <div v-if="migration.errors && migration.errors.length" class="migration-errors">
          <div v-for="(err, i) in migration.errors" :key="i">· {{ err }}</div>
        </div>
      </div>
    </div>

    <!-- 区块2：存储位置配置 -->
    <div v-if="config" class="card">
      <h3 class="card-title">存储位置</h3>
      <div class="form-group">
        <label class="form-label">当前生效目录</label>
        <div class="readonly-value mono">{{ config.effective_dir }}</div>
        <div class="meta-hint">来源：{{ config.attachment_dir_override ? 'DB 覆盖' : (config.attachment_dir_env ? '环境变量 LLM_GATEWAY_ATTACHMENT_DIR' : '默认 ./data/attachments') }}</div>
      </div>
      <div class="form-group">
        <label class="form-label">覆盖目录（留空则用环境变量；修改后自动迁移文件，无需重启）</label>
        <input v-model="form.attachment_dir_override" class="form-input mono" placeholder="例如 /data/attachments" />
        <div v-if="dirChanged" class="meta-hint danger">⚠️ 目录已修改，保存后将复制文件到新目录并删除旧目录</div>
      </div>
      <div class="path-test-row">
        <input v-model="testPath" class="form-input mono" placeholder="测试某路径是否可用..." />
        <button @click="testPathAvailable" :disabled="testing" class="btn-secondary">
          {{ testing ? '测试中...' : '测试路径' }}
        </button>
      </div>
      <div v-if="testResult" class="test-result" :class="{ ok: testResult.ok, fail: !testResult.ok }">
        {{ testResult.message }}
        <span v-if="testResult.ok">（剩余 {{ formatBytes(testResult.disk_free_bytes) }}，占用 {{ testResult.disk_usage_pct.toFixed(1) }}%）</span>
      </div>
    </div>

    <!-- 区块3：保留策略 -->
    <div v-if="config" class="card">
      <h3 class="card-title">保留策略与自动清理</h3>
      <div class="form-grid">
        <div class="form-group">
          <label class="form-label">附件保留天数（TTL）</label>
          <input type="number" v-model.number="form.ttl_days" min="1" max="3650" class="form-input" />
          <div class="meta-hint">超过此天数的附件视为过期，可被清理或归档</div>
        </div>
        <div class="form-group">
          <label class="form-label">单个附件大小上限（MB）</label>
          <input type="number" v-model.number="form.max_file_size_mb" min="1" max="200" class="form-input" />
        </div>
        <div class="form-group">
          <label class="form-label">磁盘告警水位（%）</label>
          <input type="number" v-model.number="form.disk_quota_percent" min="50" max="99" class="form-input" />
          <div class="meta-hint">达到此水位时前端告警</div>
        </div>
        <div class="form-group">
          <label class="form-label">自动清理触发水位（%）</label>
          <input type="number" v-model.number="form.auto_cleanup_threshold" min="60" max="99" class="form-input" />
          <div class="meta-hint">超过此水位时 worker 自动按 LRU 清理最老文件</div>
        </div>
      </div>
      <div class="form-group switch-group">
        <label class="switch-label">
          <input type="checkbox" v-model="form.auto_cleanup_enabled" />
          <span>启用自动清理（后台 worker，默认关闭）</span>
        </label>
        <div class="meta-hint danger">⚠️ 开启后，磁盘超水位时会自动删除过期的附件和日志，请确认 TTL 设置合理</div>
      </div>
      <div class="form-actions">
        <button @click="save" :disabled="saving" class="btn-primary">
          {{ saving ? '保存中...' : '保存配置' }}
        </button>
      </div>
      <div v-if="saveMsg" class="save-msg">{{ saveMsg }}</div>
    </div>
  </div>
</template>

<style scoped>
.storage-config { padding: 0; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.header h2 { margin: 0; font-size: 18px; color: var(--text, #e6edf3); }
.btn-refresh { background: var(--card, #1c2128); border: 1px solid var(--border, #30363d); border-radius: var(--radius, 8px); padding: 4px 12px; cursor: pointer; color: var(--text, #e6edf3); transition: background .15s, border-color .15s; }
.btn-refresh:hover:not(:disabled) { background: #21262d; border-color: var(--muted, #8b949e); }
.btn-refresh:disabled { opacity: .4; cursor: not-allowed; }
.card { background: var(--card, #1c2128); border: 1px solid var(--border, #30363d); border-radius: var(--radius, 8px); padding: 16px 20px; margin-bottom: 16px; }
.card-title { margin: 0 0 12px; font-size: 15px; color: var(--text, #e6edf3); border-bottom: 1px solid var(--border, #30363d); padding-bottom: 8px; }
.usage-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.usage-bar-track { flex: 1; height: 20px; background: #0f1117; border-radius: 10px; overflow: hidden; }
.usage-bar-fill { height: 100%; border-radius: 10px; transition: width 0.3s; }
.usage-pct { font-weight: 600; min-width: 60px; text-align: right; color: var(--text, #e6edf3); }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; }
.stat-item { padding: 8px 0; }
.stat-label { font-size: 12px; color: var(--muted, #8b949e); margin-bottom: 4px; }
.stat-value { font-size: 15px; color: var(--text, #e6edf3); font-weight: 500; word-break: break-all; }
.mono { font-family: 'SF Mono', Consolas, monospace; font-size: 13px; }
.form-group { margin-bottom: 14px; }
.form-label { display: block; font-size: 13px; color: var(--muted, #8b949e); margin-bottom: 6px; font-weight: 500; }
.form-input { width: 100%; padding: 6px 10px; background: #0f1117; border: 1px solid var(--border, #30363d); border-radius: 4px; font-size: 14px; box-sizing: border-box; color: var(--text, #e6edf3); transition: border-color .15s; }
.form-input:focus { outline: none; border-color: var(--accent, #6366f1); }
.readonly-value { background: #0f1117; padding: 6px 10px; border-radius: 4px; font-size: 13px; word-break: break-all; border: 1px solid var(--border, #30363d); color: var(--text, #e6edf3); }
.meta-hint { font-size: 12px; color: var(--muted, #8b949e); margin-top: 4px; }
.meta-hint.danger { color: var(--danger, #f85149); }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.path-test-row { display: flex; gap: 8px; }
.path-test-row .form-input { flex: 1; }
.btn-secondary { background: var(--card, #1c2128); border: 1px solid var(--accent, #6366f1); color: var(--accent-h, #818cf8); border-radius: 4px; padding: 6px 16px; cursor: pointer; white-space: nowrap; transition: opacity .15s; }
.btn-secondary:hover:not(:disabled) { opacity: .85; }
.btn-primary { background: var(--accent, #6366f1); color: #fff; border: none; border-radius: 4px; padding: 8px 24px; cursor: pointer; font-size: 14px; transition: opacity .15s; }
.btn-primary:hover:not(:disabled) { opacity: .85; }
.btn-primary:disabled { opacity: .4; cursor: not-allowed; }
.test-result { padding: 8px 12px; border-radius: 4px; font-size: 13px; margin-top: 8px; }
.test-result.ok { background: rgba(63,185,80,.1); color: var(--success, #3fb950); border: 1px solid rgba(63,185,80,.3); }
.test-result.fail { background: rgba(248,81,73,.1); color: var(--danger, #f85149); border: 1px solid rgba(248,81,73,.3); }
.switch-group { padding: 12px; background: #0f1117; border: 1px solid var(--border, #30363d); border-radius: 4px; }
.switch-label { display: flex; align-items: center; gap: 8px; cursor: pointer; font-size: 14px; color: var(--text, #e6edf3); }
.switch-label input { width: 16px; height: 16px; accent-color: var(--accent, #6366f1); }
.form-actions { margin-top: 16px; }
.save-msg { margin-top: 10px; font-size: 13px; color: var(--accent-h, #818cf8); }
.warn-box { padding: 8px 12px; background: rgba(210,153,34,.1); border: 1px solid rgba(210,153,34,.3); border-radius: 4px; color: var(--warning, #d29922); font-size: 13px; margin-top: 12px; }
.error-box { padding: 8px 12px; background: rgba(248,81,73,.1); border: 1px solid rgba(248,81,73,.3); border-radius: 4px; color: var(--danger, #f85149); margin-bottom: 16px; }

/* ── 目录迁移进度卡片 ── */
.migration-box { margin-top: 12px; padding: 12px 14px; border-radius: 6px; border: 1px solid var(--border, #30363d); background: #0f1117; font-size: 13px; }
.migration-box.running { border-color: rgba(99,102,241,.45); background: rgba(99,102,241,.08); }
.migration-box.done { border-color: rgba(63,185,80,.45); background: rgba(63,185,80,.08); }
.migration-box.failed { border-color: rgba(248,81,73,.45); background: rgba(248,81,73,.08); }
.migration-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
.migration-title { font-weight: 600; color: var(--text, #e6edf3); }
.migration-runid { font-size: 11px; color: var(--muted, #8b949e); }
.migration-paths { font-size: 12px; color: var(--muted, #8b949e); word-break: break-all; margin-bottom: 8px; }
.migration-arrow { color: var(--accent-h, #818cf8); font-weight: 600; }
.migration-progress { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.migration-bar-track { flex: 1; height: 14px; background: #161b22; border-radius: 7px; overflow: hidden; }
.migration-bar-fill { height: 100%; background: var(--accent, #6366f1); border-radius: 7px; transition: width 0.3s; }
.migration-pct { font-weight: 600; min-width: 40px; text-align: right; color: var(--accent-h, #818cf8); }
.migration-stats { color: var(--muted, #8b949e); font-size: 12px; }
.migration-stats span { margin-right: 4px; }
.migration-msg { margin-top: 6px; color: var(--text, #e6edf3); }
.migration-errors { margin-top: 6px; color: var(--danger, #f85149); font-size: 12px; }
</style>
