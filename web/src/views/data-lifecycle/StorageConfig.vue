<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import {
  storageConfigGet, storageConfigUpdate, storageConfigTestPath,
  attachmentFilesystemStats,
  type StorageConfig,
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

const diskUsageColor = computed(() => {
  const pct = config.value?.current_disk_usage || 0
  if (pct >= 90) return '#ff4d4f'
  if (pct >= 80) return '#faad14'
  return '#52c41a'
})

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
    const cfg = await storageConfigUpdate({
      attachment_dir_override: form.value.attachment_dir_override || '',
      ttl_days: form.value.ttl_days,
      max_file_size_mb: form.value.max_file_size_mb,
      disk_quota_percent: form.value.disk_quota_percent,
      auto_cleanup_enabled: form.value.auto_cleanup_enabled,
      auto_cleanup_threshold: form.value.auto_cleanup_threshold,
    })
    config.value = cfg
    saveMsg.value = '✅ 配置已保存（轮转/水位类参数即时生效，目录变更需重启）'
  } catch (e: any) {
    saveMsg.value = '❌ 保存失败：' + (e.message || '未知错误')
  } finally {
    saving.value = false
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
        ⚠️ 目录覆盖已更改，需重启服务进程生效（不会自动迁移已有文件）
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
        <label class="form-label">覆盖目录（留空则用环境变量，改后需重启）</label>
        <input v-model="form.attachment_dir_override" class="form-input mono" placeholder="例如 /data/attachments" />
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
.header h2 { margin: 0; font-size: 18px; color: #1f2329; }
.btn-refresh { background: #f0f2f5; border: 1px solid #d9d9d9; border-radius: 4px; padding: 4px 12px; cursor: pointer; color: #595959; }
.card { background: #fff; border: 1px solid #e8e8e8; border-radius: 6px; padding: 16px 20px; margin-bottom: 16px; }
.card-title { margin: 0 0 12px; font-size: 15px; color: #1f2329; border-bottom: 1px solid #f0f0f0; padding-bottom: 8px; }
.usage-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.usage-bar-track { flex: 1; height: 20px; background: #f0f2f5; border-radius: 10px; overflow: hidden; }
.usage-bar-fill { height: 100%; border-radius: 10px; transition: width 0.3s; }
.usage-pct { font-weight: 600; min-width: 60px; text-align: right; color: #1f2329; }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; }
.stat-item { padding: 8px 0; }
.stat-label { font-size: 12px; color: #8c8c8c; margin-bottom: 4px; }
.stat-value { font-size: 15px; color: #1f2329; font-weight: 500; word-break: break-all; }
.mono { font-family: 'SF Mono', Consolas, monospace; font-size: 13px; }
.form-group { margin-bottom: 14px; }
.form-label { display: block; font-size: 13px; color: #595959; margin-bottom: 6px; font-weight: 500; }
.form-input { width: 100%; padding: 6px 10px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 14px; box-sizing: border-box; }
.form-input:focus { outline: none; border-color: #1890ff; }
.readonly-value { background: #f5f5f5; padding: 6px 10px; border-radius: 4px; font-size: 13px; word-break: break-all; }
.meta-hint { font-size: 12px; color: #8c8c8c; margin-top: 4px; }
.meta-hint.danger { color: #ff4d4f; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.path-test-row { display: flex; gap: 8px; }
.path-test-row .form-input { flex: 1; }
.btn-secondary { background: #fff; border: 1px solid #1890ff; color: #1890ff; border-radius: 4px; padding: 6px 16px; cursor: pointer; white-space: nowrap; }
.btn-primary { background: #1890ff; color: #fff; border: none; border-radius: 4px; padding: 8px 24px; cursor: pointer; font-size: 14px; }
.btn-primary:disabled { background: #91d5ff; }
.test-result { padding: 8px 12px; border-radius: 4px; font-size: 13px; margin-top: 8px; }
.test-result.ok { background: #f6ffed; color: #52c41a; border: 1px solid #b7eb8f; }
.test-result.fail { background: #fff2f0; color: #ff4d4f; border: 1px solid #ffccc7; }
.switch-group { padding: 12px; background: #fafafa; border-radius: 4px; }
.switch-label { display: flex; align-items: center; gap: 8px; cursor: pointer; font-size: 14px; }
.switch-label input { width: 16px; height: 16px; }
.form-actions { margin-top: 16px; }
.save-msg { margin-top: 10px; font-size: 13px; color: #1890ff; }
.warn-box { padding: 8px 12px; background: #fffbe6; border: 1px solid #ffe58f; border-radius: 4px; color: #ad6800; font-size: 13px; margin-top: 12px; }
.error-box { padding: 8px 12px; background: #fff2f0; border: 1px solid #ffccc7; border-radius: 4px; color: #ff4d4f; margin-bottom: 16px; }
</style>
