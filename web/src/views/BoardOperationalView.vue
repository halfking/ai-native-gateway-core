<script setup lang="ts">
// BoardOperationalView — 批 158c（2026-10-17）
// P2 boardOperational 独立详细页：完整呈现 background_tasks + selfcheck 两块面板。
//
// 端点：GET /api/admin/dashboard/operational（admin 中间件）
// 类型：BoardOperationalPayload（api/board.ts:77-80）
//
// ⚠️ 后端 tenant 隔离未落实（属主拍板，2026-10-17）
// dashboard_operational.go 实证：
//   - boardOperationalCache.get()/set() 不带 caller 维度（cache struct:13-17）
//   - queryBoardBackgroundTasks/queryBoardSelfCheck 签名不带 tenantID
//   - 3 处 SQL (model_discovery_runs / credential_health_checks / self_check_runs)
//     缺 ($N = '' OR tenant_id = $N) 过滤
// ⇒ tenant_admin 角色将读到跨租户混合数据
// 视图层不做兜底——服务端是唯一防线；本视图仅"呈现已就位数据"，不参与授权判断
// （与 SessionsView / TurnsListView 等"前端严于后端是兜底不是修复"族一致）
//
// 调研：/tmp/co153-wt-evidence.txt 33 跳
// 数据来源：fetchBoardOperational（已存在，api/board.ts:183-185）
import { onMounted, ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import {
  fetchBoardOperational,
  type BoardOperationalPayload,
  type BoardBackgroundTasks,
  type BoardSelfcheck,
} from '../api/board'

const loading = ref(false)
const error = ref('')
const data = ref<BoardOperationalPayload | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await fetchBoardOperational()
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function formatTimestamp(s: string | undefined | null): string {
  if (!s) return '—'
  // 后端给 RFC3339；截到秒即可
  return s.replace('T', ' ').replace(/\.\d+Z$/, 'Z').replace('Z', '')
}

function statusLabel(s: string | undefined | null): string {
  if (!s) return '未知'
  if (s === 'running') return '运行中'
  if (s === 'success') return '成功'
  if (s === 'failed') return '失败'
  if (s === 'pending') return '等待中'
  if (s === 'cancelled') return '已取消'
  return s
}

function formatPct(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return (n * 100).toFixed(1) + '%'
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="board-operational-view">
    <header class="bo-header">
      <h1 class="bo-title">运营面板</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="bo-error">{{ error }}</p>

    <div v-if="loading && !data" class="bo-loading">{{ $t('common.feedback.loading') }}</div>

    <template v-else-if="data">
      <!-- 1. background_tasks 块 -->
      <section class="bo-section">
        <h2 class="bo-section-title">后台任务</h2>

        <!-- 1a. discovery 卡片 -->
        <div class="bo-cards">
          <div class="bo-card">
            <h3 class="bo-card-title">模型发现</h3>
            <div v-if="data.background_tasks?.discovery" class="bo-card-body">
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('common.terms.status') }}</span>
                <span class="bo-row-value" :class="{ 'is-running': data.background_tasks.discovery.running }">
                  {{ data.background_tasks.discovery.running ? $t('common.status.running') : statusLabel(data.background_tasks.discovery.status) }}
                </span>
              </div>
              <div class="bo-row">
                <span class="bo-row-label">触发方式</span>
                <span class="bo-row-value">{{ data.background_tasks.discovery.trigger ?? '—' }}</span>
              </div>
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('common.terms.startTime') }}</span>
                <span class="bo-row-value">{{ formatTimestamp(data.background_tasks.discovery.started_at) }}</span>
              </div>
              <div class="bo-row">
                <span class="bo-row-label">{{ $t('common.terms.recentHeartbeat') }}</span>
                <span class="bo-row-value">{{ formatTimestamp(data.background_tasks.discovery.heartbeat_at) }}</span>
              </div>
            </div>
            <p v-else class="bo-card-empty">{{ $t('dashboard.board.emptyNoResultDiscovery') }}</p>
            <p v-if="data.background_tasks?.degraded" class="bo-card-degraded">
              降级：{{ data.background_tasks.degraded_reason ?? '原因未知' }}
            </p>
          </div>

          <!-- 1b. probe_loop 卡片 -->
          <div class="bo-card">
            <h3 class="bo-card-title">探针巡检</h3>
            <div v-if="data.background_tasks?.probe_loop" class="bo-card-body">
              <div class="bo-row">
                <span class="bo-row-label">10 分钟内检查次数</span>
                <span class="bo-row-value">{{ formatNumber(data.background_tasks.probe_loop.checks_last_10m) }}</span>
              </div>
            </div>
            <p v-else class="bo-card-empty">{{ $t('dashboard.board.emptyNoResultProbe') }}</p>
            <p v-if="data.background_tasks?.probe_degraded" class="bo-card-degraded">
              探针降级
            </p>
          </div>
        </div>
      </section>

      <!-- 2. selfcheck 块 -->
      <section class="bo-section">
        <h2 class="bo-section-title">自检（最近 24 小时）</h2>

        <div class="bo-cards">
          <div class="bo-card">
            <h3 class="bo-card-title">总运行次数</h3>
            <p class="bo-card-metric">{{ formatNumber(data.selfcheck?.total_runs_24h) }}</p>
          </div>
          <div class="bo-card">
            <h3 class="bo-card-title">{{ $t('common.terms.successRate') }}</h3>
            <p class="bo-card-metric">{{ formatPct(data.selfcheck?.success_rate) }}</p>
          </div>
          <div class="bo-card">
            <h3 class="bo-card-title">最近状态</h3>
            <p class="bo-card-metric">{{ statusLabel(data.selfcheck?.last_status) }}</p>
          </div>
          <div class="bo-card">
            <h3 class="bo-card-title">最近运行</h3>
            <p class="bo-card-metric">{{ formatTimestamp(data.selfcheck?.last_run_at) }}</p>
          </div>
        </div>

        <p v-if="data.selfcheck?.degraded" class="bo-section-degraded">
          自检数据降级：{{ data.selfcheck.degraded_reason ?? '原因未知' }}
        </p>
      </section>

      <footer class="bo-footer">
        提示：运营面板数据来自服务端缓存（30s TTL），不实时刷新。
      </footer>
    </template>
  </div>
</template>

<style scoped>
.board-operational-view {
  padding: var(--space-4);
}
.bo-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-3);
}
.bo-title {
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
}
.bo-error {
  color: var(--danger);
  padding: var(--space-2);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}
.bo-loading {
  color: var(--text-muted);
  padding: var(--space-3);
}
.bo-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
  margin-bottom: var(--space-3);
}
.bo-section-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-2);
}
.bo-section-degraded {
  margin-top: var(--space-2);
  color: var(--danger);
  font-size: var(--text-sm);
}
.bo-cards {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--space-3);
}
.bo-card {
  background: var(--bg-surface);
  border: 1px solid var(--border-muted);
  border-radius: var(--radius);
  padding: var(--space-3);
}
.bo-card-title {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-2);
}
.bo-card-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.bo-row {
  display: grid;
  grid-template-columns: 1fr 2fr;
  gap: var(--space-2);
  align-items: center;
  font-size: var(--text-sm);
}
.bo-row-label {
  color: var(--text-muted);
}
.bo-row-value {
  color: var(--text);
}
.bo-row-value.is-running {
  color: var(--success);
  font-weight: var(--font-semibold);
}
.bo-card-empty {
  color: var(--text-muted);
  font-style: italic;
}
.bo-card-degraded {
  margin-top: var(--space-2);
  color: var(--danger);
  font-size: var(--text-sm);
}
.bo-card-metric {
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
}
.bo-footer {
  margin-top: var(--space-3);
  color: var(--text-muted);
  font-size: var(--text-sm);
}
@media (max-width: 768px) {
  .bo-cards { grid-template-columns: 1fr; }
}
</style>
