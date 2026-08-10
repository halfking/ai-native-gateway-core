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
  fetchSelfCheckTriggerAvailability,
  triggerSelfCheck,
  fetchProbeSystemHealth,
  fetchProbeQueueTasks,
  fetchProbeNodeTasks,
  type SelfCheckSettings,
  type SelfCheckStats,
  type SelfCheckRun,
  type SelfCheckRunDetail,
  type SelfCheckTriggerAvailability,
  type ProbeSystemHealth,
  type ProbeQueueTaskRow,
  type NodeProbeTaskRow,
} from '../api-selfcheck'
import { fetchSystemMonitorStats, type SystemMonitorStats } from '../api/api-system-monitor'
import SwimLane from '../components/SwimLane.vue'
import type { SwimLane as SwimLaneType, RequestTile } from '../types/swimlane'
import { getFeaturedModelsDynamic } from '../api/system'
import type { FeaturedModel } from '../api/system'

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
// Trigger availability — drives enable/disable of the "触发测试" / "手动触发"
// buttons. Default to "available" so the first paint isn't broken if the
// availability endpoint is slow; we re-fetch immediately on mount and on
// every poll cycle.
const triggerAvailability = ref<SelfCheckTriggerAvailability>({ available: true, new_probe_mode: true })
const range = ref<'1h' | '6h' | '24h' | '7d'>('24h')

// 2026-07-23: 新探测模式下的系统健康（子项④）与队列任务（子项③）
// 2026-07-24: 并入系统监测指标（队列长度 / 并发）
const probeHealth = ref<ProbeSystemHealth | null>(null)
const queueTasks = ref<ProbeQueueTaskRow[]>([])
// 2026-08-10: 错误触发的节点自检队列（NodeProbeWorker，与 credential_probe_queue
// 完整性探测分开维护），并入统一泳道展示。
const nodeTasks = ref<NodeProbeTaskRow[]>([])
const monitorStats = ref<SystemMonitorStats | null>(null)

let pollTimer: number | undefined
let queueTimer: number | undefined

// ── 数据加载 ──────────────────────────────────────────

async function loadAll() {
  loading.value = true
  error.value = null
  try {
    const [s, st, ru, mo, avail] = await Promise.all([
      fetchSelfCheckSettings(),
      fetchSelfCheckStats(range.value),
      fetchSelfCheckRuns({ limit: 30 }),
      fetchSelfCheckModels(),
      fetchSelfCheckTriggerAvailability().catch(() => triggerAvailability.value),
    ])
    settings.value = s
    // Go nil slices encode as JSON null — normalize to [] so template .length is safe.
    stats.value = {
      ...st,
      by_model: st.by_model ?? [],
      error_breakdown: st.error_breakdown ?? [],
      trend: st.trend ?? [],
    }
    recentRuns.value = ru.items ?? []
    models.value = mo.models ?? []
    if (avail) triggerAvailability.value = avail

    // 2026-07-23: 新探测模式下补充读取系统健康统计（子项④）
    if (triggerAvailability.value.new_probe_mode) {
      fetchProbeSystemHealth()
        .then((h) => { probeHealth.value = h })
        .catch(() => { /* 新探测接口可能不存在，静默 */ })
    }
    // 队列任务（子项③）独立拉取
    void refreshQueueTasks()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

// 2026-07-23: 队列任务拉取（子项③），随执行更新状态
// 2026-08-10: 并行拉取 node-tasks（错误触发自检队列），两套队列独立失败，
// 互不影响彼此展示。
async function refreshQueueTasks() {
  try {
    const [res, nodeRes, stats] = await Promise.all([
      fetchProbeQueueTasks(120),
      fetchProbeNodeTasks(120).catch(() => ({ tasks: [] as NodeProbeTaskRow[], total: 0 })),
      fetchSystemMonitorStats().catch(() => null),
    ])
    queueTasks.value = res.tasks ?? []
    nodeTasks.value = nodeRes.tasks ?? []
    if (stats) monitorStats.value = stats
  } catch {
    queueTasks.value = []
    nodeTasks.value = []
  }
}

function startPoll() {
  stopPoll()
  pollTimer = window.setInterval(() => {
    void loadAll()
  }, 60_000) // 60秒刷新（自检数据变化较慢）
  // 2026-07-23: 队列任务变化更快，单独 15s 轮询（子项③）
  queueTimer = window.setInterval(() => {
    void refreshQueueTasks()
  }, 15_000)
}

function stopPoll() {
  if (pollTimer) clearInterval(pollTimer)
  if (queueTimer) clearInterval(queueTimer)
  pollTimer = undefined
  queueTimer = undefined
}

onMounted(() => {
  void loadAll()
  void loadSystemFeaturedModels()
  startPoll()
})

onUnmounted(() => {
  stopPoll()
})

watch(range, () => {
  void loadAll()
})

// ── 系统特色模型（从系统设置读取，不可编辑） ─────────────
const systemFeaturedModels = ref<FeaturedModel[]>([])
const systemFeaturedLoading = ref(false)

async function loadSystemFeaturedModels() {
  systemFeaturedLoading.value = true
  try {
    const res = await getFeaturedModelsDynamic()
    systemFeaturedModels.value = res.models ?? []
  } catch {
    systemFeaturedModels.value = []
  }
  systemFeaturedLoading.value = false
}

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
    })
    showSettings.value = false
    await loadAll()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  }
}

// ── 手动触发 ──────────────────────────────────────────

async function onTrigger(model = '') {
  // Front-end gate mirrors the back-end worker state so we don't fire
  // requests the server is guaranteed to 410 on.
  if (!triggerAvailability.value.available) {
    error.value = triggerAvailability.value.reason ?? '自检触发功能不可用'
    return
  }
  triggerBusy.value = true
  try {
    await triggerSelfCheck(model)
    setTimeout(() => void loadAll(), 1000)
  } catch (e: unknown) {
    // 410 Gone = server explicitly retired this endpoint (new probe mode).
    // Surface the friendly reason from the payload instead of the raw 410
    // banner, then refresh availability so the buttons disable.
    const err = e as Error & { status?: number; body?: string }
    if (err.status === 410) {
      let parsed: { message?: string; reason?: string } = {}
      try {
        parsed = JSON.parse(err.body || '{}') as { message?: string; reason?: string }
      } catch {
        // ignore JSON parse errors, fall back to defaults
      }
      error.value = parsed.message || parsed.reason || triggerAvailability.value.reason || '该触发功能已下线'
      try {
        triggerAvailability.value = await fetchSelfCheckTriggerAvailability()
      } catch {
        // best-effort refresh; ignore failure
      }
    } else {
      error.value = e instanceof Error ? e.message : '触发失败'
    }
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

// 与深色主题语义色对齐的色板（用于文字着色，在深色卡片背景上有良好对比度）
// good=success / warn=warning / danger=danger / neutral=muted / accent=blue
const COLOR = {
  good: '#3fb950',
  warn: '#d29922',
  danger: '#f85149',
  neutral: '#8b949e',
  accent: '#3b82f6',
}

function statusColor(status: string): string {
  switch (status) {
    case 'success': return COLOR.good
    case 'partial': return COLOR.warn
    case 'failed': return COLOR.danger
    case 'running': return COLOR.accent
    default: return COLOR.neutral
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

const errorColor = (t: string): string => {
  if (t.startsWith('http_000') || t === 'timeout') return COLOR.danger
  if (t.startsWith('http_5')) return COLOR.warn
  if (t === 'none') return COLOR.good
  return COLOR.neutral
}

// formatSelfCheckModelLabel distinguishes the synthetic "cred-<id>"
// model_name rows the credential_selfcheck worker writes from real
// model names. In new probe mode most rows are per-credential labels;
// the suffix indicates which credential's daily check produced the
// run. Real upstream models (e.g. "gpt-5.6-luna") pass through.
function formatSelfCheckModelLabel(label: string): string {
  if (!label) return label
  if (label.startsWith('cred-')) {
    const id = label.slice(5)
    if (/^\d+$/.test(id)) return `凭据 #${id}`
    return label
  }
  return label
}

const summaryCards = computed(() => {
  if (!stats.value) return []
  const s = stats.value.summary
  return [
    { label: '总运行数', value: s.total_runs, color: COLOR.accent },
    { label: '成功率', value: fmtPct(s.success_rate), color: s.success_rate >= 0.9 ? COLOR.good : s.success_rate >= 0.7 ? COLOR.warn : COLOR.danger },
    { label: '成功', value: s.success_runs, color: COLOR.good },
    { label: '部分', value: s.partial_runs, color: COLOR.warn },
    { label: '失败', value: s.failed_runs, color: COLOR.danger },
  ]
})

const modelsByHealth = computed(() => {
  return [...models.value].sort((a, b) => {
    const aRate = a.total > 0 ? a.success / a.total : 0
    const bRate = b.total > 0 ? b.success / b.total : 0
    return aRate - bRate
  })
})

// 根据成功率返回柔和的语义色
function healthColor(rate: number): string {
  if (rate >= 0.9) return COLOR.good
  if (rate >= 0.7) return COLOR.warn
  return COLOR.danger
}

// ── 2026-07-23 子项④: 新探测模式下的系统健康统计卡片 ──────────────────
const useProbeStats = computed(() => triggerAvailability.value.new_probe_mode && !!probeHealth.value)
const probeSummaryCards = computed(() => {
  const h = probeHealth.value
  if (!h) return []
  return [
    { label: '总节点数', value: h.total_nodes ?? 0, color: COLOR.accent },
    { label: '健康', value: h.healthy_nodes ?? 0, color: COLOR.good },
    { label: '失败', value: h.failing_nodes ?? 0, color: COLOR.danger },
    { label: '可疑', value: h.suspicious_nodes ?? 0, color: COLOR.warn },
    { label: '探测中', value: h.probing_nodes ?? 0, color: COLOR.accent },
  ]
})

// ── 2026-07-24: 探测队列固定三泳道（已执行 / 正在执行 / 待执行），FIFO
const QUEUE_LANE_LIMIT = 40

function taskStandardModel(t: ProbeQueueTaskRow): string {
  return (t.standardized_name || t.raw_model || '').trim()
}

function taskToTile(t: ProbeQueueTaskRow): RequestTile {
  const status =
    t.status === 'running' ? 'in_progress'
      : t.status === 'success' ? 'success'
        : (t.status === 'failed' || t.status === 'expired') ? 'failure'
          : 'idle'
  return {
    request_id: `q-${t.id}`,
    timestamp: t.updated_at || t.next_run_at || new Date().toISOString(),
    model: taskStandardModel(t),
    vendor: '__unknown__',
    provider: t.provider_name || String(t.provider_id),
    status,
    is_probe: true,
    probe_origin: 'direct',
    latency_ms: t.result_latency_ms > 0 ? t.result_latency_ms : undefined,
  }
}

// 2026-08-10: NodeProbeWorker 错误触发队列的 tile 映射。node_probe_state
// 是常驻状态行（非一次性任务），三态对应 running/pending/done 泳道：
// running = 正被 pickDueAtomically 租用；pending = 等待下次退避 tick；
// paused = 达到 7 步退避上限，归入「已执行」泳道并标 failure（长期搁置）。
function nodeTaskStandardModel(t: NodeProbeTaskRow): string {
  return (t.standardized_name || t.raw_model || '').trim()
}

function nodeTaskToTile(t: NodeProbeTaskRow): RequestTile {
  const status =
    t.status === 'running' ? 'in_progress'
      : t.status === 'paused' ? 'failure'
        : 'idle'
  return {
    request_id: `n-${t.credential_id}-${t.raw_model}`,
    timestamp: t.updated_at || t.next_retry_at || new Date().toISOString(),
    model: nodeTaskStandardModel(t),
    vendor: '__unknown__',
    provider: t.provider_name || String(t.provider_id),
    status,
    is_probe: true,
    probe_origin: 'direct',
    latency_ms: t.last_latency_ms && t.last_latency_ms > 0 ? t.last_latency_ms : undefined,
    error_kind: t.last_err_code || undefined,
  }
}

function tasksForLane(statuses: string[], fifoAsc: boolean, nodeStatuses: string[] = []): RequestTile[] {
  const matchedQueue = queueTasks.value.filter((t) => statuses.includes(t.status))
  const matchedNode = nodeTasks.value.filter((t) => nodeStatuses.includes(t.status))
  const combined = [
    ...matchedQueue.map((t) => ({
      tile: taskToTile(t),
      ts: new Date(t.updated_at || t.next_run_at || 0).getTime(),
    })),
    ...matchedNode.map((t) => ({
      tile: nodeTaskToTile(t),
      ts: new Date(t.updated_at || t.next_retry_at || 0).getTime(),
    })),
  ]
  const sorted = combined.sort((a, b) => (fifoAsc ? a.ts - b.ts : b.ts - a.ts))
  const slice = fifoAsc
    ? sorted.slice(0, QUEUE_LANE_LIMIT)
    : sorted.slice(0, QUEUE_LANE_LIMIT).reverse()
  return slice.map((x) => x.tile)
}

const queueSwimLanes = computed<SwimLaneType[]>(() => {
  const pending = tasksForLane(['ready'], true, ['pending'])
  const running = tasksForLane(['running'], true, ['running'])
  const done = tasksForLane(['success', 'failed', 'expired'], false, ['paused'])
  return [
    {
      id: 'done',
      name: '已执行',
      dimension: 'provider',
      requests: done,
      stats: { total: done.length, success: done.filter((r) => r.status === 'success').length, failure: done.filter((r) => r.status === 'failure').length },
      isOthers: false,
    },
    {
      id: 'running',
      name: '正在执行',
      dimension: 'provider',
      requests: running,
      stats: { total: running.length, success: 0, failure: 0 },
      isOthers: false,
    },
    {
      id: 'pending',
      name: '待执行',
      dimension: 'provider',
      requests: pending,
      stats: { total: pending.length, success: 0, failure: 0 },
      isOthers: false,
    },
  ]
})

const queueLengthDisplay = computed(() => {
  if (monitorStats.value) return monitorStats.value.queue_size
  return queueTasks.value.filter((t) => t.status === 'ready').length
})
const runningCountDisplay = computed(() => {
  if (monitorStats.value) return monitorStats.value.running_size
  return queueTasks.value.filter((t) => t.status === 'running').length
})
const concurrencyDisplay = computed(() => monitorStats.value?.monitor_concurrency ?? '—')

const queueLaneSelectedLegends = ref<Set<string>>(new Set())
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
        <button
          class="btn btn-primary"
          :disabled="triggerBusy || !triggerAvailability.available"
          :title="triggerAvailability.available ? '' : (triggerAvailability.reason || '触发功能不可用')"
          @click="onTrigger('')"
        >
          {{
            triggerBusy
              ? '⏳ 触发中'
              : triggerAvailability.available
                ? '▶ 手动触发'
                : '⛔ 触发已下线'
          }}
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
    <!-- 2026-07-23 子项④: 新探测模式下优先显示系统健康统计，避免旧表为空时空白 -->
    <div v-if="useProbeStats" class="summary-cards">
      <div v-for="c in probeSummaryCards" :key="c.label" class="summary-card">
        <div class="card-label">{{ c.label }}</div>
        <div class="card-value" :style="{ color: c.color }">{{ c.value }}</div>
      </div>
    </div>
    <div v-else-if="stats" class="summary-cards">
      <div v-for="c in summaryCards" :key="c.label" class="summary-card">
        <div class="card-label">{{ c.label }}</div>
        <div class="card-value" :style="{ color: c.color }">{{ c.value }}</div>
      </div>
    </div>

    <!-- 2026-07-24: 当前探测队列（系统监测并入） -->
    <div class="queue-swimlanes-section">
      <div class="queue-head">
        <h4 class="section-title">当前探测队列</h4>
        <div class="queue-metrics">
          <span class="queue-metric">队列长度 <strong>{{ queueLengthDisplay }}</strong></span>
          <span class="queue-metric">运行中 <strong>{{ runningCountDisplay }}</strong></span>
          <span class="queue-metric">并发上限 <strong>{{ concurrencyDisplay }}</strong></span>
        </div>
      </div>
      <div class="queue-swimlanes">
        <SwimLane
          v-for="lane in queueSwimLanes"
          :key="lane.id"
          :lane="lane"
          group-by="provider"
          mode="small"
          :selected-legends="queueLaneSelectedLegends"
        />
      </div>
    </div>

    <!-- 模型状态卡片 -->
    <h4 class="section-title">模型实时状态</h4>
    <div class="model-grid">
      <div v-for="m in modelsByHealth" :key="m.model_name" class="model-card">
        <div class="model-name">{{ formatSelfCheckModelLabel(m.model_name) }}</div>
        <div class="model-stats">
          <div class="stat-row">
            <span class="stat-label">成功率</span>
            <span class="stat-value" :style="{ color: m.total === 0 ? COLOR.neutral : healthColor(m.success / m.total) }">
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
        <button
          class="btn btn-tiny"
          :disabled="triggerBusy || !triggerAvailability.available"
          :title="triggerAvailability.available ? '' : (triggerAvailability.reason || '触发功能不可用')"
          @click="onTrigger(m.model_name)"
        >
          触发测试
        </button>
      </div>
      <div v-if="models.length === 0" class="empty-state">暂无模型</div>
    </div>

    <!-- 错误分类 -->
    <div v-if="stats?.error_breakdown?.length" class="error-section">
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
    <div v-if="stats?.trend?.length" class="trend-section">
      <h4 class="section-title">成功率趋势 ({{ stats.trend.length }} 小时)</h4>
      <div class="trend-chart">
        <div v-for="(p, idx) in stats.trend" :key="idx" class="trend-bar-wrap">
          <div class="trend-bar"
               :style="{
                 height: Math.max(p.success_rate * 100, 5) + '%',
                 backgroundColor: healthColor(p.success_rate)
               }"
               :title="`${p.timestamp}: ${fmtPct(p.success_rate)} (${p.total} 次)`"
          ></div>
          <div class="trend-label">{{ p.timestamp?.slice(11, 16) ?? '' }}</div>
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
          <div class="col-model" :title="r.model_name">{{ formatSelfCheckModelLabel(r.model_name) }}</div>
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
            <span :style="{ color: r.upstream_result === 'success' ? COLOR.good : COLOR.danger }">
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
                <td :style="{ color: rd.success ? COLOR.good : COLOR.danger }">{{ rd.success ? '✅' : '❌' }}</td>
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
          <label>系统特色模型（只读，在 模型管理 → 特色模型 中配置）</label>
          <div v-if="systemFeaturedLoading" class="muted">加载中…</div>
          <div v-else-if="systemFeaturedModels.length === 0" class="muted">暂无系统特色模型</div>
          <div v-else class="featured-tags">
            <span v-for="m in systemFeaturedModels" :key="m.name" class="featured-tag">{{ m.name }}</span>
          </div>
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
  flex-wrap: nowrap;
  gap: 8px;
  overflow-x: auto;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
  white-space: nowrap;
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
  border: 1px solid transparent;
}

.status-badge.enabled {
  background: rgba(63, 185, 80, 0.15);
  color: var(--success);
  border: 1px solid rgba(63, 185, 80, 0.4);
}

.status-badge.disabled {
  background: rgba(248, 81, 73, 0.15);
  color: var(--danger);
  border: 1px solid rgba(248, 81, 73, 0.4);
}

.interval-info {
  font-size: 12px;
  color: var(--muted);
  font-family: monospace;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: nowrap;
  white-space: nowrap;
}

.btn {
  padding: 6px 16px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--card);
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
  white-space: nowrap;
}

.btn:hover {
  background: var(--bg-subtle);
}

.btn-primary {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
}

.btn-primary:hover {
  background: var(--accent-h);
  border-color: var(--accent-h);
}

.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-secondary {
  background: var(--bg-subtle);
  color: var(--text);
}

.btn-tiny {
  padding: 3px 8px;
  font-size: 11px;
}

.range-select {
  padding: 5px 12px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--card);
  color: var(--text);
  white-space: nowrap;
  font-size: 13px;
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
  background: rgba(248, 81, 73, 0.12);
  color: var(--danger);
  border: 1px solid rgba(248, 81, 73, 0.35);
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
  background: var(--card);
  border: 1px solid var(--border);
}

.card-label {
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 4px;
}

.card-value {
  font-size: 20px;
  font-weight: 600;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin: 20px 0 12px 0;
  padding-bottom: 4px;
  border-bottom: 1px solid var(--border);
}

.model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}

.model-card {
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--card);
}

.model-name {
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 8px;
  word-break: break-all;
  color: var(--text);
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
  color: var(--muted);
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
  background: var(--bg-subtle);
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
  color: var(--muted);
  margin-top: 4px;
  font-family: monospace;
}

/* 最近运行记录列表 */
.runs-table {
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  background: var(--card);
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
  background: var(--bg-subtle);
  font-weight: 600;
  color: var(--muted);
}

.runs-summary {
  cursor: pointer;
  border-top: 1px solid var(--border);
}

.runs-summary:hover {
  background: var(--bg-subtle);
}

.runs-row.expanded .runs-summary {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}

.col-action {
  text-align: center;
}

.expand-icon {
  color: var(--muted);
}

.runs-detail {
  padding: 16px;
  background: var(--bg-subtle);
  border-top: 1px solid var(--border);
}

.runs-detail h5 {
  margin: 12px 0 8px 0;
  font-size: 12px;
  color: var(--muted);
}

.upstream-info, .error-detail {
  font-size: 12px;
  margin-bottom: 8px;
}

.upstream-error {
  margin-top: 4px;
  padding: 4px 8px;
  background: rgba(248, 81, 73, 0.12);
  border: 1px solid rgba(248, 81, 73, 0.35);
  border-radius: 3px;
  font-family: monospace;
  font-size: 11px;
  color: var(--danger);
}

.rounds-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
}

.rounds-table th, .rounds-table td {
  padding: 4px 8px;
  border: 1px solid var(--border);
  text-align: left;
}

.rounds-table th {
  background: var(--card);
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
  color: var(--muted);
  font-size: 13px;
}

/* 2026-07-24: 当前探测队列 */
.queue-swimlanes-section {
  margin-bottom: 24px;
}
.queue-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.queue-head .section-title {
  margin: 0;
}
.queue-metrics {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 14px;
  font-size: 12px;
  color: var(--muted);
}
.queue-metric strong {
  color: var(--text);
  font-variant-numeric: tabular-nums;
  margin-left: 4px;
}
.queue-swimlanes {
  display: flex;
  flex-direction: column;
  gap: 6px;
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
  background: var(--card);
  color: var(--text);
  padding: 24px;
  border-radius: 8px;
  border: 1px solid var(--border);
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
  color: var(--muted);
}

.form-group input,
.form-group select,
.form-group textarea {
  width: 100%;
  padding: 6px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  font-family: inherit;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
.featured-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.featured-tag {
  display: inline-block;
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 12px;
  background: var(--accent);
  color: #fff;
}
.muted {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>