<template>
  <div class="degradation-recovery">
    <div class="card warning-card">
      <div>
        <h3>数据库降级恢复与清理</h3>
        <p>降级只切换会话、请求日志和 WAL 的持久化方式，不会停止 LLM 服务。恢复前请确认数据库已稳定可用。</p>
      </div>
      <span class="status" :class="statusClass">{{ statusLabel }}</span>
    </div>

    <div class="card grid">
      <div><span>数据库状态</span><strong>{{ status.status || 'unknown' }}</strong></div>
      <div><span>当前写入模式</span><strong>{{ status.manual ? '手动降级' : (status.ttl_mode || '正常') }}</strong></div>
      <div><span>备份文件</span><strong>{{ status.backups?.total_files || 0 }}</strong></div>
      <div><span>待处理记录</span><strong>{{ status.backups?.total_records || 0 }}</strong></div>
    </div>

    <div class="card actions">
      <button class="btn btn-danger" :disabled="busy || status.manual" @click="control('enter')">强制进入降级</button>
      <button class="btn btn-primary" :disabled="busy || !status.manual" @click="control('exit')">恢复数据库写入</button>
      <button class="btn btn-ghost" :disabled="busy" @click="load">刷新状态</button>
    </div>

    <div class="card">
      <h3>文件回放与清理</h3>
      <p class="hint">仅包含 request log/WAL 的记录会由此 Tab 回放；session 快照仍使用原有恢复接口。</p>
      <div v-if="!status.backups?.files?.length" class="empty">暂无待处理备份文件</div>
      <div v-for="file in status.backups?.files || []" :key="file.filename" class="file-row">
        <div><code>{{ file.filename }}</code><small>{{ file.record_count }} 条记录 · {{ formatBytes(file.size) }}</small></div>
        <div class="file-actions">
          <button class="btn btn-sm btn-ghost" :disabled="busy" @click="recover(file.filename, false)">回放</button>
          <button class="btn btn-sm btn-danger" :disabled="busy" @click="recover(file.filename, true)">回放并归档</button>
        </div>
      </div>
    </div>

    <div v-if="task" class="card task">
      <h3>回放任务</h3>
      <div class="progress"><div :style="{ width: `${task.progress || 0}%` }" /></div>
      <p>{{ task.status }} · {{ task.processed_records || 0 }}/{{ task.total_records || 0 }} · 成功 {{ task.success_count || 0 }} · 失败 {{ task.failure_count || 0 }}</p>
      <p v-if="task.error" class="error">{{ task.error }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { req } from '@/api/_core'

const status = ref<any>({})
const task = ref<any>(null)
const busy = ref(false)
let pollTimer: ReturnType<typeof setInterval> | undefined

const statusClass = computed(() => status.value.status === 'degraded' || status.value.manual ? 'degraded' : 'available')
const statusLabel = computed(() => statusClass.value === 'degraded' ? '降级写入' : '数据库可用')

async function load() {
  status.value = await req<any>('GET', '/api/admin/data-lifecycle/degradation/status')
}

async function control(action: 'enter' | 'exit') {
  const text = action === 'enter'
    ? '确认进入降级写入？LLM 服务继续运行，但会话和日志将写入备份文件。'
    : '确认恢复数据库写入？请确保数据库已稳定可用。'
  if (!window.confirm(text)) return
  busy.value = true
  try {
    await req('POST', '/api/admin/data-lifecycle/degradation/control', { action })
    await load()
  } finally { busy.value = false }
}

async function recover(filename: string, archive: boolean) {
  if (!window.confirm(archive ? `确认回放并归档 ${filename}？` : `确认回放 ${filename}？`)) return
  busy.value = true
  try {
    const res = await req<any>('POST', '/api/admin/data-lifecycle/degradation/recover', { filename, archive })
    task.value = { id: res.task_id, status: res.status, progress: 0 }
    startPolling(res.task_id)
  } finally { busy.value = false }
}

function startPolling(taskID: string) {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = setInterval(async () => {
    task.value = await req<any>('GET', `/api/admin/data-lifecycle/degradation/tasks/${taskID}`)
    if (['completed', 'completed_with_errors', 'failed'].includes(task.value.status)) {
      if (pollTimer) clearInterval(pollTimer)
      pollTimer = undefined
      await load()
    }
  }, 1000)
}

function formatBytes(value: number) {
  if (!value) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let n = value; let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(i ? 1 : 0)} ${units[i]}`
}

onMounted(load)
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })
defineExpose({ load })
</script>

<style scoped>
.degradation-recovery { padding: 20px; }
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 18px;
  margin-bottom: 16px;
  color: var(--text);
}
.warning-card { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
h3 { margin: 0 0 8px; font-size: 15px; }
p { margin: 0; color: var(--muted); font-size: 13px; line-height: 1.6; }
.status { padding: 5px 10px; border-radius: 6px; font-size: 12px; white-space: nowrap; }
.status.available { color: var(--success); background: var(--success-soft); }
.status.degraded { color: var(--warning); background: var(--warning-soft); }
.grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; }
.grid div { display: flex; flex-direction: column; gap: 5px; color: var(--muted); font-size: 12px; }
.grid strong { color: var(--text); font-size: 16px; }
.actions, .file-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.btn { border: 1px solid var(--border); border-radius: 6px; padding: 7px 12px; color: var(--text); background: var(--bg-subtle); cursor: pointer; }
.btn-primary { background: var(--accent); border-color: var(--accent); color: var(--on-primary); }
.btn-danger { background: var(--danger); border-color: var(--danger); color: var(--on-primary); }
.btn:disabled { opacity: .45; cursor: not-allowed; }
.btn-sm { padding: 5px 9px; font-size: 12px; }
.hint, .empty { color: var(--muted); margin-bottom: 12px; }
.file-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 12px 0; border-top: 1px solid var(--border);
}
.file-row code { display: block; color: var(--accent-h); }
.file-row small { display: block; color: var(--muted); margin-top: 4px; }
.progress { height: 8px; background: var(--bg-subtle); border-radius: 4px; overflow: hidden; margin: 12px 0; }
.progress div { height: 100%; background: var(--accent); transition: width .2s; }
.error { color: var(--danger); }
@media (max-width: 800px) { .grid { grid-template-columns: repeat(2, 1fr); } .file-row { align-items: flex-start; flex-direction: column; } }
</style>
