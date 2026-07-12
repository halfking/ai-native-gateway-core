<script setup lang="ts">
// SelfCheckPanel.vue — 系统自检模块 UI
// 显示当前各模型的可用率、延迟、错误情况，以及自检配置
// 2026-07-12 创建

import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  fetchSelfCheckSettings,
  updateSelfCheckSettings,
  fetchSelfCheckStats,
  fetchSelfCheckRuns,
  fetchSelfCheckModels,
  fetchSelfCheckRunDetail,
  triggerSelfCheck,
  type SelfCheckSettings,
  type SelfCheckStats,
  type SelfCheckRun,
  type SelfCheckRunDetail,
} from '../api-selfcheck'

const loading = ref(false)
const error = ref<string | null>(null)

const settings = ref<SelfCheckSettings | null>(null)
const stats = ref<SelfCheckStats | null>(null)
const recentRuns = ref<SelfCheckRun[]>([])
const models = ref<{ model_name: string; total: number; success: number; failed: number; last_run?: string }[]>([])
const expandedRunId = ref<number | null>(null)
const runDetail = ref<SelfCheckRunDetail | null>(null)
const showSettings = ref(false)
const editingSettings = ref<SelfCheckSettings | null>(null)
const triggerBusy = ref(false)
const range = ref<'1h' | '6h' | '24h' | '7d'>('24h')

let pollTimer: number | undefined

// ── 数据加载 ──────────────────────────────────────────

async function loadAll() {
  loading.value = true
  error.value = null
  try {
    const [s, st, ru, mo] = await Promise.all([
      fetchSelfCheckSettings(),
      fetchSelfCheckStats(range.value),
      fetchSelfCheckRuns({ limit: 30 }),
      fetchSelfCheckModels(),
    ])
    settings.value = s
    stats.value = st
    recentRuns.value = ru.items
    models.value = mo.models
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function startPoll() {
  stopPoll()
  pollTimer = window.setInterval(() => {
    void loadAll()
  }, 60_000) // 60秒刷新（自检数据变化较慢）
}

function stopPoll() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = undefined
}

onMounted(() => {
  void loadAll()
  startPoll()
})

onUnmounted(() => {
  stopPoll()
})

watch(range, () => {
  void loadAll()
})

// ── 详情展开 ──────────────────────────────────────────

async function toggleRunDetail(id: number) {
  if (expandedRunId.value === id) {
    expandedRunId.value = null
    runDetail.value = null
    return
  }
  expandedRunId.value = id
  try {
    runDetail.value = await fetchSelfCheckRunDetail(id)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载详情失败'
  }
}

// ── 设置编辑 ──────────────────────────────────────────

function openSettings() {
  if (!settings.value) return
  editingSettings.value = { ...settings.value }
  showSettings.value = true
}

async function saveSettings() {
  if (!editingSettings.value) return
  try {
    await updateSelfCheckSettings({
      enabled: editingSettings.value.enabled,
      normal_interval_seconds: editingSettings.value.normal_interval_seconds,
      fault_interval_seconds: editingSettings.value.fault_interval_seconds,
      max_models: editingSettings.value.max_models,
      max_tokens_per_run: editingSettings.value.max_tokens_per_run,
      model_source: editingSettings.value.model_source,
      featured_model_ids: editingSettings.value.featured_model_ids,
    })
    showSettings.value = false
    await loadAll()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  }
}

// ── 手动触发 ──────────────────────────────────────────

async function onTrigger(model = '') {
  triggerBusy.value = true
  try {
    await triggerSelfCheck(model)
    setTimeout(() => void loadAll(), 1000)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '触发失败'
  } finally {
    triggerBusy.value = false
  }
}

// ── 计算属性 ──────────────────────────────────────────

function fmtPct(v: number | undefined): string {
  if (v === undefined || v === null) return '—'
  return (v * 100).toFixed(1) + '%'
}

function fmtMs(v: number | undefined): string {
  if (v === undefined || v === null) return '—'
  if (v >= 1000) return (v / 1000).toFixed(2) + 's'
  return v + 'ms'
}

function fmtTime(s: string | undefined): string {
  if (!s) return '—'
  return new Date(s).toLocaleString()
}

function fmtRelative(s: string | undefined): string {
  if (!s) return '—'
  const ms = Date.now() - new Date(s).getTime()
  if (ms < 60_000) return Math.round(ms / 1000) + 's 前'
  if (ms < 3600_000) return Math.round(ms / 60_000) + 'm 前'
  if (ms < 86400_000) return Math.round(ms / 3600_000) + 'h 前'
  return Math.round(ms / 86400_000) + 'd 前'
}

function statusColor(status: string): string {
  switch (status) {
    case 'success': return '#22c55e'
    case 'partial': return '#f59e0b'
    case 'failed': return '#ef4444'
    case 'running': return '#3b82f6'
    default: return '#94a3b8'
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case 'success': return '✅ 正常'
    case 'partial': return '⚠️ 部分成功'
    case 'failed': return '❌ 失败'
    case 'running': return '🔄 进行中'
    default: return status
  }
}

const featuredModelInput = computed({
  get() {
    return editingSettings.value?.featured_model_ids?.join(', ') || ''
  },
  set(v: string) {
    if (editingSettings.value) {
      editingSettings.value.featured_model_ids = v.split(',').map(s => s.trim()).filter(Boolean)
    }
  },
})

const errorColor = (t: string): string => {
  if (t.startsWith('http_000') || t === 'timeout') return '#ef4444'
  if (t.startsWith('http_5')) return '#f97316'
  if (t === 'none') return '#22c55e'
  return '#94a3b8'
}

const summaryCards = computed(() => {
  if (!stats.value) return []
  const s = stats.value.summary
  return [
    { label: '总运行数', value: s.total_runs, color: '#3b82f6' },
    { label: '成功率', value: fmtPct(s.success_rate), color: s.success_rate >= 0.9 ? '#22c55e' : s.success_rate >= 0.7 ? '#f59e0b' : '#ef4444' },
    { label: '成功', value: s.success_runs, color: '#22c55e' },
    { label: '部分', value: s.partial_runs, color: '#f59e0b' },
    { label: '失败', value: s.failed_runs, color: '#ef4444' },
  ]
})

const modelsByHealth = computed(() => {
  return [...models.value].sort((a, b) => {
    const aRate = a.total > 0 ? a.success / a.total : 0
    const bRate = b.total > 0 ? b.success / b.total : 0
    return aRate - bRate
  })
})
</script>

<template>
  <div class="selfcheck-panel">
    <!-- 顶部工具栏 -->
    <div class="panel-header">
      <div class="header-left">
        <h3>系统自检</h3>
        <span class="status-badge" :class="{ enabled: settings?.enabled, disabled: !settings?.enabled }">
          {{ settings?.enabled ? '已启用' : '已停用' }}
        </span>
        <span v-if="settings" class="interval-info">
          {{ settings.normal_interval_seconds }}s/{{ settings.fault_interval_seconds }}s
        </span>
      </div>
      <div class="header-right">
        <select v-model="range" class="range-select">
          <option value="1h">1h</option>
          <option value="6h">6h</option>
          <option value="24h">24h</option>
          <option value="7d">7d</option>
        </select>
        <button class="btn btn-secondary" @click="openSettings">⚙ 设置</button>
        <button class="btn btn-primary" :disabled="triggerBusy" @click="onTrigger('')">
          {{ triggerBusy ? '⏳ 触发中' : '▶ 手动触发' }}
        </button>
        <button class="btn btn-secondary" @click="loadAll">🔄</button>
      </div>
    </div>

    <!-- 错误提示 -->
    <div v-if="error" class="alert alert-danger">
      ⚠️ {{ error }}
      <button class="btn-close" @click="error = null">×</button>
    </div>

    <!-- 摘要卡片 -->
    <div v-if="stats" class="summary-cards">
      <div v-for="c in summaryCards" :key="c.label" class="summary-card">
        <div class="card-label">{{ c.label }}</div>
        <div class="card-value" :style="{ color: c.color }">{{ c.value }}</div>
      </div>
    </div>

    <!-- 模型状态卡片 -->
    <h4 class="section-title">模型实时状态</h4>
    <div class="model-grid">
      <div v-for="m in modelsByHealth" :key="m.model_name" class="model-card">
        <div class="model-name">{{ m.model_name }}</div>
        <div class="model-stats">
          <div class="stat-row">
            <span class="stat-label">成功率</span>
            <span class="stat-value" :style="{ color: m.total === 0 ? '#94a3b8' : m.success / m.total >= 0.9 ? '#22c55e' : m.success / m.total >= 0.7 ? '#f59e0b' : '#ef4444' }">
              {{ m.total === 0 ? '—' : fmtPct(m.success / m.total) }}
            </span>
          </div>
          <div class="stat-row">
            <span class="stat-label">成功/失败</span>
            <span class="stat-value">{{ m.success }} / {{ m.failed }}</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">总次数</span>
            <span class="stat-value">{{ m.total }}</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">最近</span>
            <span class="stat-value">{{ fmtRelative(m.last_run) }}</span>
          </div>
        </div>
        <button class="btn btn-tiny" @click="onTrigger(m.model_name)">触发测试</button>
      </div>
      <div v-if="models.length === 0" class="empty-state">暂无模型</div>
    </div>

    <!-- 错误分类 -->
    <div v-if="stats && stats.error_breakdown.length > 0" class="error-section">
      <h4 class="section-title">错误分类</h4>
      <div class="error-bars">
        <div v-for="e in stats.error_breakdown" :key="e.error_type" class="error-bar">
          <div class="error-label" :style="{ color: errorColor(e.error_type) }">{{ e.error_type }}</div>
          <div class="error-bar-track">
            <div class="error-bar-fill" :style="{
              width: stats.summary.failed_runs + stats.summary.partial_runs > 0
                ? ((e.count / (stats.summary.failed_runs + stats.summary.partial_runs)) * 100) + '%'
                : '0%',
              backgroundColor: errorColor(e.error_type)
            }"></div>
          </div>
          <div class="error-count">{{ e.count }}</div>
        </div>
      </div>
    </div>

    <!-- 趋势图（纯 CSS 柱状图） -->
    <div v-if="stats && stats.trend.length > 0" class="trend-section">
      <h4 class="section-title">成功率趋势 ({{ stats.trend.length }} 小时)</h4>
      <div class="trend-chart">
        <div v-for="(p, idx) in stats.trend" :key="idx" class="trend-bar-wrap">
          <div class="trend-bar"
               :style="{
                 height: Math.max(p.success_rate * 100, 5) + '%',
                 backgroundColor: p.success_rate >= 0.9 ? '#22c55e' : p.success_rate >= 0.7 ? '#f59e0b' : '#ef4444'
               }"
               :title="`${p.timestamp}: ${fmtPct(p.success_rate)} (${p.total} 次)`"
          ></div>
          <div class="trend-label">{{ p.timestamp.slice(11, 16) }}</div>
        </div>
      </div>
    </div>

    <!-- 最近运行记录 -->
    <h4 class="section-title">最近运行记录</h4>
    <div class="runs-table">
      <div class="runs-header">
        <div class="col-model">模型</div>
        <div class="col-status">状态</div>
        <div class="col-time">开始时间</div>
        <div class="col-duration">耗时</div>
        <div class="col-tokens">Tokens</div>
        <div class="col-error">错误</div>
        <div class="col-action"></div>
      </div>
      <div v-for="r in recentRuns" :key="r.id" class="runs-row" :class="{ expanded: expandedRunId === r.id }">
        <div class="runs-summary" @click="toggleRunDetail(r.id)">
          <div class="col-model">{{ r.model_name }}</div>
          <div class="col-status" :style="{ color: statusColor(r.status) }">{{ statusLabel(r.status) }}</div>
          <div class="col-time">{{ fmtTime(r.started_at) }}</div>
          <div class="col-duration">{{ fmtMs(r.duration_ms) }}</div>
          <div class="col-tokens">{{ r.total_tokens }}</div>
          <div class="col-error">{{ r.error_type || '—' }}</div>
          <div class="col-action">
            <span class="expand-icon">{{ expandedRunId === r.id ? '▼' : '▶' }}</span>
          </div>
        </div>
        <div v-if="expandedRunId === r.id && runDetail" class="runs-detail">
          <div v-if="r.upstream_tested" class="upstream-info">
            <strong>上游隔离测试:</strong>
            <span :style="{ color: r.upstream_result === 'success' ? '#22c55e' : '#ef4444' }">
              {{ r.upstream_result }} ({{ fmtMs(r.upstream_latency_ms || 0) }})
            </span>
            <div v-if="r.upstream_error" class="upstream-error">{{ r.upstream_error }}</div>
          </div>
          <div class="error-detail" v-if="r.error_detail">
            <strong>错误详情:</strong> {{ r.error_detail }}
          </div>
          <h5>轮次详情</h5>
          <table class="rounds-table">
            <thead>
              <tr>
                <th>轮次</th><th>类型</th><th>延迟</th><th>状态</th><th>HTTP</th><th>Tokens</th><th>错误</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="rd in runDetail.rounds" :key="rd.id">
                <td>{{ rd.round_index }}</td>
                <td>{{ rd.is_ping ? 'ping' : rd.is_tool_call ? 'tool' : 'chat' }}</td>
                <td>{{ fmtMs(rd.latency_ms) }}</td>
                <td :style="{ color: rd.success ? '#22c55e' : '#ef4444' }">{{ rd.success ? '✅' : '❌' }}</td>
                <td>{{ rd.http_code || '—' }}</td>
                <td>{{ rd.total_tokens }}</td>
                <td class="error-cell">{{ rd.error_message || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-if="recentRuns.length === 0" class="empty-state">暂无运行记录</div>
    </div>

    <!-- 设置弹窗 -->
    <div v-if="showSettings && editingSettings" class="modal-overlay" @click.self="showSettings = false">
      <div class="modal-content">
        <h3>自检设置</h3>
        <div class="form-group">
          <label>启用</label>
          <input type="checkbox" v-model="editingSettings.enabled" />
        </div>
        <div class="form-group">
          <label>正常周期（秒）</label>
          <input type="number" v-model.number="editingSettings.normal_interval_seconds" min="10" max="600" />
        </div>
        <div class="form-group">
          <label>故障周期（秒）</label>
          <input type="number" v-model.number="editingSettings.fault_interval_seconds" min="5" max="300" />
        </div>
        <div class="form-group">
          <label>最大模型数</label>
          <input type="number" v-model.number="editingSettings.max_models" min="1" max="50" />
        </div>
        <div class="form-group">
          <label>每次会话最大 token</label>
          <input type="number" v-model.number="editingSettings.max_tokens_per_run" min="1000" max="1000000" />
        </div>
        <div class="form-group">
          <label>模型来源</label>
          <select v-model="editingSettings.model_source">
            <option value="top10">Top 10</option>
            <option value="featured">特色模型</option>
            <option value="both">两者结合</option>
          </select>
        </div>
        <div class="form-group">
          <label>特色模型列表（逗号分隔）</label>
          <textarea v-model="featuredModelInput" rows="3"></textarea>
        </div>
        <div class="modal-actions">
          <button class="btn btn-secondary" @click="showSettings = false">取消</button>
          <button class="btn btn-primary" @click="saveSettings">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.selfcheck-panel {
  padding: 16px;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
  flex-wrap: wrap;
  gap: 8px;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.header-left h3 {
  margin: 0;
  font-size: 18px;
}

.status-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.status-badge.enabled {
  background: #dcfce7;
  color: #166534;
}

.status-badge.disabled {
  background: #fee2e2;
  color: #991b1b;
}

.interval-info {
  font-size: 12px;
  color: #64748b;
  font-family: monospace;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.btn {
  padding: 6px 12px;
  border-radius: 4px;
  border: 1px solid #cbd5e1;
  background: white;
  cursor: pointer;
  font-size: 13px;
}

.btn:hover {
  background: #f8fafc;
}

.btn-primary {
  background: #3b82f6;
  color: white;
  border-color: #3b82f6;
}

.btn-primary:hover {
  background: #2563eb;
}

.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-secondary {
  background: #f1f5f9;
}

.btn-tiny {
  padding: 3px 8px;
  font-size: 11px;
}

.range-select {
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid #cbd5e1;
  background: white;
}

.alert {
  padding: 8px 12px;
  border-radius: 4px;
  margin-bottom: 12px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.alert-danger {
  background: #fee2e2;
  color: #991b1b;
}

.btn-close {
  background: none;
  border: none;
  font-size: 18px;
  cursor: pointer;
  color: inherit;
}

.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
  margin-bottom: 24px;
}

.summary-card {
  padding: 12px 16px;
  border-radius: 6px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
}

.card-label {
  font-size: 11px;
  color: #64748b;
  margin-bottom: 4px;
}

.card-value {
  font-size: 22px;
  font-weight: 600;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: #475569;
  margin: 20px 0 12px 0;
  padding-bottom: 4px;
  border-bottom: 1px solid #e2e8f0;
}

.model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}

.model-card {
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: white;
}

.model-name {
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 8px;
  word-break: break-all;
}

.model-stats {
  margin-bottom: 8px;
}

.stat-row {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  margin-bottom: 4px;
}

.stat-label {
  color: #64748b;
}

.stat-value {
  font-weight: 500;
  font-family: monospace;
}

.error-section {
  margin-bottom: 24px;
}

.error-bars {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.error-bar {
  display: grid;
  grid-template-columns: 120px 1fr 60px;
  align-items: center;
  gap: 12px;
  font-size: 12px;
}

.error-label {
  font-family: monospace;
  font-weight: 500;
}

.error-bar-track {
  height: 16px;
  background: #f1f5f9;
  border-radius: 3px;
  overflow: hidden;
}

.error-bar-fill {
  height: 100%;
  transition: width 0.3s;
}

.error-count {
  text-align: right;
  font-family: monospace;
}

.trend-section {
  margin-bottom: 24px;
}

.trend-chart {
  display: flex;
  align-items: flex-end;
  height: 100px;
  gap: 2px;
  padding: 8px 0;
}

.trend-bar-wrap {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  height: 100%;
}

.trend-bar {
  width: 100%;
  min-height: 5px;
  border-radius: 2px 2px 0 0;
  transition: height 0.3s;
}

.trend-label {
  font-size: 9px;
  color: #94a3b8;
  margin-top: 4px;
  font-family: monospace;
}

.runs-table {
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  overflow: hidden;
  background: white;
}

.runs-header, .runs-summary {
  display: grid;
  grid-template-columns: 1.5fr 1fr 1.5fr 0.7fr 0.7fr 1fr 40px;
  gap: 8px;
  padding: 8px 12px;
  align-items: center;
  font-size: 12px;
}

.runs-header {
  background: #f8fafc;
  font-weight: 600;
  color: #475569;
}

.runs-summary {
  cursor: pointer;
  border-top: 1px solid #e2e8f0;
}

.runs-summary:hover {
  background: #f8fafc;
}

.runs-row.expanded .runs-summary {
  background: #eff6ff;
}

.col-action {
  text-align: center;
}

.expand-icon {
  color: #94a3b8;
}

.runs-detail {
  padding: 16px;
  background: #f8fafc;
  border-top: 1px solid #e2e8f0;
}

.runs-detail h5 {
  margin: 12px 0 8px 0;
  font-size: 12px;
  color: #475569;
}

.upstream-info, .error-detail {
  font-size: 12px;
  margin-bottom: 8px;
}

.upstream-error {
  margin-top: 4px;
  padding: 4px 8px;
  background: #fef2f2;
  border-radius: 3px;
  font-family: monospace;
  font-size: 11px;
  color: #991b1b;
}

.rounds-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
}

.rounds-table th, .rounds-table td {
  padding: 4px 8px;
  border: 1px solid #e2e8f0;
  text-align: left;
}

.rounds-table th {
  background: #f1f5f9;
  font-weight: 600;
}

.error-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: monospace;
}

.empty-state {
  padding: 24px;
  text-align: center;
  color: #94a3b8;
  font-size: 13px;
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 1000;
}

.modal-content {
  background: white;
  padding: 24px;
  border-radius: 8px;
  width: 480px;
  max-width: 90vw;
}

.modal-content h3 {
  margin: 0 0 16px 0;
}

.form-group {
  margin-bottom: 12px;
}

.form-group label {
  display: block;
  margin-bottom: 4px;
  font-size: 13px;
  color: #475569;
}

.form-group input,
.form-group select,
.form-group textarea {
  width: 100%;
  padding: 6px 8px;
  border-radius: 4px;
  border: 1px solid #cbd5e1;
  font-size: 13px;
  font-family: inherit;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>