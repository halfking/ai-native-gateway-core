<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { startDiagnose, getDiagnoseResult, getTask, type BackgroundTask } from '../../api'

const props = defineProps<{ providerId: number }>()

const taskStatus = ref<BackgroundTask | null>(null)
const cachedResult = ref<any>(null)
const loading = ref(false)
const error = ref('')
const polling = ref(false)

async function runDiagnose() {
  loading.value = true
  error.value = ''
  try {
    const { task_id } = await startDiagnose(props.providerId)
    polling.value = true
    const deadline = Date.now() + 120000
    while (Date.now() < deadline) {
      const task = await getTask(task_id)
      taskStatus.value = task
      if (task.status !== 'running') {
        polling.value = false
        if (task.status === 'succeeded') {
          cachedResult.value = task.result
        } else {
          error.value = task.error || '诊断失败'
        }
        return
      }
      await new Promise(r => setTimeout(r, 2000))
    }
    polling.value = false
    error.value = '诊断超时'
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '诊断失败'
    polling.value = false
  } finally {
    loading.value = false
  }
}

async function loadCached() {
  try {
    const data = await getDiagnoseResult(props.providerId)
    if (data?.result) cachedResult.value = data.result
  } catch { /* no cached result */ }
}

onMounted(loadCached)

function scoreColor(score: number): string {
  if (score >= 80) return '#4caf50'
  if (score >= 50) return '#f0b429'
  return '#f44336'
}
</script>

<template>
  <div>
    <div style="display:flex;gap:12px;align-items:center;margin-bottom:16px">
      <button class="btn btn-primary" @click="runDiagnose" :disabled="loading">
        {{ loading ? (polling ? '诊断中...' : '启动中...') : '运行完整诊断' }}
      </button>
      <span v-if="polling" style="color:var(--muted);font-size:12px">正在探测凭据，请稍候...</span>
    </div>
    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="taskStatus?.status === 'running'" style="text-align:center;padding:40px;color:var(--muted)">
      诊断任务 #{{ taskStatus.id }} 执行中，通常需要 30-60 秒...
    </div>

    <template v-if="cachedResult">
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:16px">
        <div style="background:var(--bg-subtle);border:1px solid var(--border);border-radius:8px;padding:14px">
          <div style="font-size:12px;color:var(--muted)">凭据总数</div>
          <div style="font-size:20px;font-weight:600">{{ cachedResult.summary?.total_credentials ?? 0 }}</div>
          <div style="font-size:11px;color:var(--muted)">
            <span style="color:#4caf50">健康 {{ cachedResult.summary?.healthy ?? 0 }}</span> ·
            <span style="color:#f0b429">降级 {{ cachedResult.summary?.degraded ?? 0 }}</span> ·
            <span style="color:#f44336">不可达 {{ cachedResult.summary?.unreachable ?? 0 }}</span>
          </div>
        </div>
        <div style="background:var(--bg-subtle);border:1px solid var(--border);border-radius:8px;padding:14px">
          <div style="font-size:12px;color:var(--muted)">模型覆盖率</div>
          <div style="font-size:20px;font-weight:600">{{ (cachedResult.summary?.models_coverage_pct ?? 0).toFixed(1) }}%</div>
        </div>
        <div style="background:var(--bg-subtle);border:1px solid var(--border);border-radius:8px;padding:14px">
          <div style="font-size:12px;color:var(--muted)">平均延迟</div>
          <div style="font-size:20px;font-weight:600">{{ (cachedResult.summary?.avg_latency_ms ?? 0).toFixed(0) }} ms</div>
        </div>
      </div>

      <div v-if="cachedResult.error_classification" style="margin-bottom:16px">
        <h4 style="margin:0 0 8px;font-size:14px">24h 错误分类</h4>
        <div style="display:flex;gap:16px;flex-wrap:wrap">
          <span v-if="cachedResult.error_classification.auth_errors">认证: {{ cachedResult.error_classification.auth_errors }}</span>
          <span v-if="cachedResult.error_classification.rate_limit_errors">限流: {{ cachedResult.error_classification.rate_limit_errors }}</span>
          <span v-if="cachedResult.error_classification.timeout_errors">超时: {{ cachedResult.error_classification.timeout_errors }}</span>
          <span v-if="cachedResult.error_classification.model_not_found_errors">模型不存在: {{ cachedResult.error_classification.model_not_found_errors }}</span>
          <span v-if="cachedResult.error_classification.other_errors">其他: {{ cachedResult.error_classification.other_errors }}</span>
          <span v-if="!cachedResult.error_classification.auth_errors && !cachedResult.error_classification.rate_limit_errors && !cachedResult.error_classification.timeout_errors && !cachedResult.error_classification.model_not_found_errors && !cachedResult.error_classification.other_errors" style="color:var(--muted)">无错误</span>
        </div>
      </div>

      <div v-if="cachedResult.health_scores?.length" style="margin-bottom:16px">
        <h4 style="margin:0 0 8px;font-size:14px">凭据健康分数</h4>
        <div style="display:flex;gap:12px;flex-wrap:wrap">
          <div v-for="s in cachedResult.health_scores" :key="s.credential_id" style="display:flex;align-items:center;gap:6px;min-width:120px">
            <span style="color:var(--muted);font-size:11px">#{{ s.credential_id }}</span>
            <div style="flex:1;height:6px;background:var(--bg-subtle);border-radius:3px;overflow:hidden">
              <div :style="{ width: s.score + '%', background: scoreColor(s.score), height: '100%', borderRadius: '3px' }"></div>
            </div>
            <span :style="{ color: scoreColor(s.score), fontWeight: 600, fontSize: '13px', minWidth: '24px' }">{{ s.score.toFixed(0) }}</span>
          </div>
        </div>
      </div>

      <div v-if="cachedResult.credentials?.length">
        <h4 style="margin:0 0 8px;font-size:14px">凭据详细探测</h4>
        <table style="width:100%;border-collapse:collapse;font-size:12px">
          <thead>
            <tr style="text-align:left;border-bottom:1px solid var(--border)">
              <th style="padding:6px">凭据</th>
              <th style="padding:6px">状态</th>
              <th style="padding:6px">熔断</th>
              <th style="padding:6px">Models 探测</th>
              <th style="padding:6px">Chat 探测</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="cd in cachedResult.credentials" :key="cd.credential_id" style="border-bottom:1px solid var(--border)">
              <td style="padding:6px">#{{ cd.credential_id }} {{ cd.label }}</td>
              <td style="padding:6px"><span class="badge" :class="cd.status === 'active' ? 'badge-green' : 'badge-red'">{{ cd.status }}</span></td>
              <td style="padding:6px"><span class="badge" :class="cd.circuit_state === 'closed' ? 'badge-green' : 'badge-amber'">{{ cd.circuit_state }}</span></td>
              <td style="padding:6px">
                <span v-if="cd.models_probe?.error" class="badge badge-red">失败</span>
                <span v-else class="badge" :class="cd.models_probe?.status_code === 200 ? 'badge-green' : 'badge-red'">
                  {{ cd.models_probe?.status_code || '—' }} · {{ cd.models_probe?.models_count ?? 0 }} 模型 · {{ cd.models_probe?.latency_ms ?? 0 }}ms
                </span>
              </td>
              <td style="padding:6px">
                <span v-if="cd.chat_probe?.error" class="badge badge-red">失败</span>
                <span v-else class="badge" :class="cd.chat_probe?.status_code === 200 ? 'badge-green' : 'badge-red'">
                  {{ cd.chat_probe?.status_code || '—' }} · {{ cd.chat_probe?.latency_ms ?? 0 }}ms
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>
