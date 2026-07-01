<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { attachmentFilesystemStats, attachmentFilesystemCleanup } from '../../api'

interface FilesystemStats {
  attachment_dir: string
  total_files: number
  total_size_bytes: number
  total_size_human: string
  oldest_file_time: string | null
  disk_total_bytes: number
  disk_used_bytes: number
  disk_avail_bytes: number
  disk_usage_percent: number
  disk_warning_level: 'safe' | 'warning' | 'danger'
}

interface CleanupPreview {
  dry_run: boolean
  files_deleted: number
  bytes_freed: number
  bytes_freed_human: string
  deleted_paths?: string[]
  error?: string
}

const stats = ref<FilesystemStats | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

// 清理表单
const cleanupForm = ref({
  olderThanDays: 30,
  reason: ''
})
const cleanupPreview = ref<CleanupPreview | null>(null)
const cleanupLoading = ref(false)
const showPreviewModal = ref(false)
const showExecuteConfirm = ref(false)

const diskUsageColor = computed(() => {
  if (!stats.value) return '#52c41a'
  const level = stats.value.disk_warning_level
  if (level === 'danger') return '#ff4d4f'
  if (level === 'warning') return '#faad14'
  return '#52c41a'
})

const diskUsageText = computed(() => {
  if (!stats.value) return '安全'
  const level = stats.value.disk_warning_level
  if (level === 'danger') return '危险'
  if (level === 'warning') return '警告'
  return '安全'
})

async function load() {
  loading.value = true
  error.value = null
  try {
    stats.value = await attachmentFilesystemStats()
  } catch (e: any) {
    error.value = e.message || '加载失败'
    console.error('load filesystem stats failed', e)
  } finally {
    loading.value = false
  }
}

async function previewCleanup() {
  if (cleanupForm.value.olderThanDays <= 0) {
    alert('请输入有效的天数')
    return
  }
  cleanupLoading.value = true
  cleanupPreview.value = null
  try {
    const result = await attachmentFilesystemCleanup({
      older_than_days: cleanupForm.value.olderThanDays,
      dry_run: true,
      reason: cleanupForm.value.reason || '预览清理'
    })
    cleanupPreview.value = result
    showPreviewModal.value = true
  } catch (e: any) {
    alert('预览失败: ' + (e.message || '未知错误'))
  } finally {
    cleanupLoading.value = false
  }
}

async function executeCleanup() {
  if (!cleanupForm.value.reason.trim()) {
    alert('请填写清理原因')
    return
  }
  if (!confirm(`确认删除 ${cleanupForm.value.olderThanDays} 天前的文件？\n原因：${cleanupForm.value.reason}`)) {
    return
  }
  cleanupLoading.value = true
  try {
    const result = await attachmentFilesystemCleanup({
      older_than_days: cleanupForm.value.olderThanDays,
      dry_run: false,
      reason: cleanupForm.value.reason
    })
    alert(`清理完成！\n删除文件数：${result.files_deleted}\n释放空间：${result.bytes_freed_human}`)
    showPreviewModal.value = false
    showExecuteConfirm.value = false
    cleanupPreview.value = null
    await load() // 重新加载统计
  } catch (e: any) {
    alert('清理失败: ' + (e.message || '未知错误'))
  } finally {
    cleanupLoading.value = false
  }
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

function formatDate(dateStr: string | null): string {
  if (!dateStr) return '-'
  return new Date(dateStr).toLocaleString('zh-CN')
}

onMounted(() => {
  load()
})

defineExpose({ load })
</script>

<template>
  <div class="filesystem-maintenance">
    <div class="header">
      <h2>文件系统维护</h2>
      <button @click="load" :disabled="loading" class="btn-refresh">
        {{ loading ? '加载中...' : '刷新' }}
      </button>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>

    <div v-if="stats" class="stats-grid">
      <!-- 附件目录统计 -->
      <div class="stat-card">
        <div class="stat-label">附件存储目录</div>
        <div class="stat-value-small">{{ stats.attachment_dir }}</div>
      </div>

      <div class="stat-card">
        <div class="stat-label">文件总数</div>
        <div class="stat-value">{{ stats.total_files.toLocaleString() }}</div>
      </div>

      <div class="stat-card">
        <div class="stat-label">总大小</div>
        <div class="stat-value">{{ stats.total_size_human }}</div>
        <div class="stat-hint">{{ formatBytes(stats.total_size_bytes) }}</div>
      </div>

      <div class="stat-card">
        <div class="stat-label">最早文件时间</div>
        <div class="stat-value-small">{{ formatDate(stats.oldest_file_time) }}</div>
      </div>

      <!-- 磁盘空间统计 -->
      <div class="stat-card disk-card">
        <div class="stat-label">磁盘使用率</div>
        <div class="disk-usage">
          <div class="disk-bar">
            <div
              class="disk-bar-fill"
              :style="{ width: stats.disk_usage_percent + '%', backgroundColor: diskUsageColor }"
            ></div>
          </div>
          <div class="disk-percent" :style="{ color: diskUsageColor }">
            {{ stats.disk_usage_percent.toFixed(1) }}% ({{ diskUsageText }})
          </div>
        </div>
      </div>

      <div class="stat-card">
        <div class="stat-label">磁盘总容量</div>
        <div class="stat-value">{{ formatBytes(stats.disk_total_bytes) }}</div>
      </div>

      <div class="stat-card">
        <div class="stat-label">磁盘已用</div>
        <div class="stat-value">{{ formatBytes(stats.disk_used_bytes) }}</div>
      </div>

      <div class="stat-card">
        <div class="stat-label">磁盘可用</div>
        <div class="stat-value">{{ formatBytes(stats.disk_avail_bytes) }}</div>
      </div>
    </div>

    <!-- 清理操作区 -->
    <div class="cleanup-section">
      <h3>按时间清理附件文件</h3>
      <div class="cleanup-form">
        <div class="form-row">
          <label>清理天数：</label>
          <input
            type="number"
            v-model.number="cleanupForm.olderThanDays"
            min="1"
            placeholder="删除 N 天前的文件"
          />
          <span class="hint">天前的文件</span>
        </div>

        <div class="form-row">
          <label>清理原因：</label>
          <input
            type="text"
            v-model="cleanupForm.reason"
            placeholder="例如：释放磁盘空间 / 定期清理"
            style="flex: 1"
          />
        </div>

        <div class="form-actions">
          <button @click="previewCleanup" :disabled="cleanupLoading" class="btn-preview">
            {{ cleanupLoading ? '预览中...' : '预览清理' }}
          </button>
          <button
            @click="showExecuteConfirm = true"
            :disabled="cleanupLoading || !cleanupForm.reason.trim()"
            class="btn-execute"
          >
            执行清理
          </button>
        </div>
      </div>
    </div>

    <!-- 预览模态框 -->
    <div v-if="showPreviewModal" class="modal-overlay" @click.self="showPreviewModal = false">
      <div class="modal-content">
        <h3>清理预览</h3>
        <div v-if="cleanupPreview">
          <div class="preview-stats">
            <div class="preview-item">
              <strong>预计删除文件数：</strong>
              <span>{{ cleanupPreview.files_deleted }}</span>
            </div>
            <div class="preview-item">
              <strong>预计释放空间：</strong>
              <span>{{ cleanupPreview.bytes_freed_human }}</span>
            </div>
          </div>
          <div v-if="cleanupPreview.deleted_paths && cleanupPreview.deleted_paths.length > 0" class="preview-paths">
            <strong>待删除文件（前 20 个）：</strong>
            <ul>
              <li v-for="(path, i) in cleanupPreview.deleted_paths.slice(0, 20)" :key="i">
                {{ path }}
              </li>
            </ul>
            <div v-if="cleanupPreview.deleted_paths.length > 20" class="preview-more">
              还有 {{ cleanupPreview.deleted_paths.length - 20 }} 个文件...
            </div>
          </div>
        </div>
        <div class="modal-actions">
          <button @click="showPreviewModal = false" class="btn-cancel">关闭</button>
        </div>
      </div>
    </div>

    <!-- 执行确认模态框 -->
    <div v-if="showExecuteConfirm" class="modal-overlay" @click.self="showExecuteConfirm = false">
      <div class="modal-content modal-danger">
        <h3>⚠️ 确认删除</h3>
        <p><strong>操作不可逆！</strong>即将删除 <strong>{{ cleanupForm.olderThanDays }}</strong> 天前的所有附件文件。</p>
        <p><strong>原因：</strong>{{ cleanupForm.reason }}</p>
        <div class="modal-actions">
          <button @click="showExecuteConfirm = false" class="btn-cancel">取消</button>
          <button @click="executeCleanup" :disabled="cleanupLoading" class="btn-danger">
            {{ cleanupLoading ? '清理中...' : '确认删除' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.filesystem-maintenance {
  padding: 20px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.btn-refresh {
  padding: 8px 16px;
  background: #1890ff;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
}

.btn-refresh:hover:not(:disabled) {
  background: #40a9ff;
}

.btn-refresh:disabled {
  background: #d9d9d9;
  cursor: not-allowed;
}

.error-box {
  padding: 12px;
  background: #fff2f0;
  border: 1px solid #ffccc7;
  border-radius: 4px;
  color: #ff4d4f;
  margin-bottom: 20px;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
  gap: 16px;
  margin-bottom: 30px;
}

.stat-card {
  padding: 16px;
  background: white;
  border: 1px solid #e8e8e8;
  border-radius: 8px;
}

.stat-label {
  font-size: 14px;
  color: #8c8c8c;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
  color: #262626;
}

.stat-value-small {
  font-size: 14px;
  color: #262626;
  word-break: break-all;
}

.stat-hint {
  font-size: 12px;
  color: #8c8c8c;
  margin-top: 4px;
}

.disk-card {
  grid-column: span 2;
}

.disk-usage {
  margin-top: 8px;
}

.disk-bar {
  height: 24px;
  background: #f0f0f0;
  border-radius: 12px;
  overflow: hidden;
  margin-bottom: 8px;
}

.disk-bar-fill {
  height: 100%;
  transition: width 0.3s, background-color 0.3s;
}

.disk-percent {
  font-size: 16px;
  font-weight: 600;
}

.cleanup-section {
  background: white;
  border: 1px solid #e8e8e8;
  border-radius: 8px;
  padding: 20px;
}

.cleanup-section h3 {
  margin: 0 0 16px 0;
  font-size: 16px;
  font-weight: 600;
}

.cleanup-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.form-row label {
  min-width: 100px;
  font-weight: 500;
}

.form-row input[type="number"],
.form-row input[type="text"] {
  padding: 8px 12px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 14px;
}

.form-row input[type="number"] {
  width: 120px;
}

.hint {
  color: #8c8c8c;
  font-size: 14px;
}

.form-actions {
  display: flex;
  gap: 12px;
  margin-top: 8px;
}

.btn-preview,
.btn-execute,
.btn-cancel,
.btn-danger {
  padding: 8px 20px;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  font-weight: 500;
}

.btn-preview {
  background: #1890ff;
  color: white;
}

.btn-preview:hover:not(:disabled) {
  background: #40a9ff;
}

.btn-execute {
  background: #ff4d4f;
  color: white;
}

.btn-execute:hover:not(:disabled) {
  background: #ff7875;
}

.btn-preview:disabled,
.btn-execute:disabled {
  background: #d9d9d9;
  cursor: not-allowed;
}

.btn-cancel {
  background: #ffffff;
  color: #262626;
  border: 1px solid #d9d9d9;
}

.btn-cancel:hover {
  border-color: #40a9ff;
  color: #40a9ff;
}

.btn-danger {
  background: #ff4d4f;
  color: white;
}

.btn-danger:hover:not(:disabled) {
  background: #ff7875;
}

.btn-danger:disabled {
  background: #d9d9d9;
  cursor: not-allowed;
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-content {
  background: white;
  border-radius: 8px;
  padding: 24px;
  max-width: 600px;
  width: 90%;
  max-height: 80vh;
  overflow-y: auto;
}

.modal-content h3 {
  margin: 0 0 16px 0;
  font-size: 18px;
  font-weight: 600;
}

.modal-danger {
  border: 2px solid #ff4d4f;
}

.modal-danger p {
  margin: 12px 0;
}

.preview-stats {
  margin-bottom: 20px;
}

.preview-item {
  display: flex;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid #f0f0f0;
}

.preview-paths {
  margin-top: 16px;
}

.preview-paths ul {
  list-style: none;
  padding: 0;
  margin: 8px 0;
  max-height: 300px;
  overflow-y: auto;
  background: #fafafa;
  border-radius: 4px;
  padding: 12px;
}

.preview-paths li {
  font-size: 12px;
  font-family: monospace;
  color: #595959;
  padding: 4px 0;
}

.preview-more {
  font-size: 12px;
  color: #8c8c8c;
  margin-top: 8px;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 20px;
}
</style>
