<script setup lang="ts">
// OpsOverviewView — 批 158e（2026-10-17）
// P4 opsOverview 独立详细页：12 路 payload 卡片布局。
//
// 端点：GET /api/admin/ops/overview（superAdmin 中间件，批 158e 从 admin 降级）
// 12 路：center_stats / region_stats / deployment_nodes / data_plane_tables
//      / license_total / offline_requests / fault_stats / recent_faults
//      / recent_upgrades / download_stats / runtime_metrics_summary
// 后端 cache 15s TTL（ops_overview.go:5）
//
// 数据来源：getOpsOverview（web/src/api/opsOverview.ts，本批新建）
// 抽屉入口已存在（appNav.ts:229 /maintain/ops/overview external）—— 本视图
// 是 web/ 内部深链 /admin/ops-overview 独立详细页，与 maintain SPA 平级
import { onMounted, ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { getOpsOverview, type OpsOverviewPayload, type RegionItem } from '../api/opsOverview'

const loading = ref(false)
const error = ref('')
const data = ref<OpsOverviewPayload | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await getOpsOverview()
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

function formatBytes(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(1) + ' GB'
}

function formatUptime(s: number | undefined | null): string {
  if (s === undefined || s === null) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}天 ${h}小时`
  if (h > 0) return `${h}小时 ${m}分钟`
  return `${m}分钟`
}

function formatTimestamp(s: string | undefined | null): string {
  if (!s) return '—'
  return s.replace('T', ' ').replace(/\.\d+Z$/, '').replace('Z', '')
}

const regionList = (): RegionItem[] => {
  const rs = data.value?.region_stats
  if (!rs) return []
  return Object.values(rs.byRegion ?? {}).sort((a, b) => a.region.localeCompare(b.region))
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="ops-overview-view">
    <header class="oo-header">
      <h1 class="oo-title">运维总览</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="oo-error">{{ error }}</p>

    <div v-if="loading && !data" class="oo-loading">{{ $t('common.feedback.loading') }}</div>

    <template v-else-if="data">
      <!-- 1. 中心实例统计 -->
      <section class="oo-section">
        <h2 class="oo-section-title">中心实例</h2>
        <div class="oo-cards">
          <div class="oo-card">
            <h3 class="oo-card-title">总实例数</h3>
            <p class="oo-card-metric">{{ formatNumber(data.center_stats?.total_instances) }}</p>
          </div>
          <div class="oo-card oo-card-online">
            <h3 class="oo-card-title">在线</h3>
            <p class="oo-card-metric">{{ formatNumber(data.center_stats?.online_instances) }}</p>
          </div>
          <div class="oo-card oo-card-offline">
            <h3 class="oo-card-title">离线</h3>
            <p class="oo-card-metric">{{ formatNumber(data.center_stats?.offline_instances) }}</p>
          </div>
          <div class="oo-card oo-card-degraded">
            <h3 class="oo-card-title">降级</h3>
            <p class="oo-card-metric">{{ formatNumber(data.center_stats?.degraded_instances) }}</p>
          </div>
        </div>
      </section>

      <!-- 2. 区域分布 -->
      <section v-if="regionList().length" class="oo-section">
        <h2 class="oo-section-title">区域分布</h2>
        <table class="oo-table">
          <thead>
            <tr>
              <th>区域</th>
              <th>总数</th>
              <th>在线</th>
              <th>离线</th>
              <th>降级</th>
              <th>{{ $t('common.terms.recentHeartbeat') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in regionList()" :key="r.region">
              <td><code>{{ r.region }}</code></td>
              <td>{{ formatNumber(r.total_instances) }}</td>
              <td>{{ formatNumber(r.online_instances) }}</td>
              <td>{{ formatNumber(r.offline_instances) }}</td>
              <td>{{ formatNumber(r.degraded_instances) }}</td>
              <td>{{ formatTimestamp(r.last_heartbeat) }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- 3. 部署节点 -->
      <section v-if="data.deployment_nodes?.length" class="oo-section">
        <h2 class="oo-section-title">部署节点（最近 {{ data.deployment_nodes.length }}）</h2>
        <table class="oo-table">
          <thead>
            <tr>
              <th>节点 ID</th>
              <th>区域</th>
              <th>{{ $t('common.terms.version') }}</th>
              <th>{{ $t('common.terms.status') }}</th>
              <th>启动时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in data.deployment_nodes" :key="n.node_id">
              <td><code>{{ n.node_id }}</code></td>
              <td>{{ n.region ?? '—' }}</td>
              <td>{{ n.version ?? '—' }}</td>
              <td>{{ n.status ?? '—' }}</td>
              <td>{{ formatTimestamp(n.started_at) }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- 4. 数据面表规模 -->
      <section v-if="data.data_plane_tables?.length" class="oo-section">
        <h2 class="oo-section-title">数据面表规模</h2>
        <ul class="oo-list">
          <li v-for="t in data.data_plane_tables" :key="t.name" class="oo-list-row">
            <code>{{ t.name }}</code>
            <span>{{ formatNumber(t.count) }} 行</span>
          </li>
        </ul>
      </section>

      <!-- 5. License 与离线激活 -->
      <section class="oo-section">
        <h2 class="oo-section-title">许可证</h2>
        <div class="oo-cards">
          <div class="oo-card">
            <h3 class="oo-card-title">有效 License 总数</h3>
            <p class="oo-card-metric">{{ formatNumber(data.license_total) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">离线激活请求</h3>
            <p class="oo-card-metric">{{ formatNumber(data.offline_requests?.length) }}</p>
          </div>
        </div>
      </section>

      <!-- 6. 故障统计与最近事件 -->
      <section v-if="data.fault_stats" class="oo-section">
        <h2 class="oo-section-title">故障</h2>
        <div class="oo-cards">
          <div class="oo-card">
            <h3 class="oo-card-title">总故障数</h3>
            <p class="oo-card-metric">{{ formatNumber(data.fault_stats?.total) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">已解决</h3>
            <p class="oo-card-metric">{{ formatNumber(data.fault_stats?.resolved) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">{{ $t('common.status.pending') }}</h3>
            <p class="oo-card-metric">{{ formatNumber(data.fault_stats?.pending) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">最近故障</h3>
            <p class="oo-card-metric">{{ data.recent_faults?.length ?? 0 }} 条</p>
          </div>
        </div>
        <table v-if="data.recent_faults?.length" class="oo-table">
          <thead>
            <tr><th>{{ $t('common.terms.severity') }}</th><th>{{ $t('common.terms.message') }}</th><th>{{ $t('common.terms.status') }}</th><th>{{ $t('common.terms.triggeredAt') }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="f in data.recent_faults" :key="f.id">
              <td>{{ f.severity ?? '—' }}</td>
              <td>{{ f.message ?? '—' }}</td>
              <td>{{ f.status ?? '—' }}</td>
              <td>{{ formatTimestamp(f.triggered_at) }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- 7. 升级 -->
      <section v-if="data.recent_upgrades?.length" class="oo-section">
        <h2 class="oo-section-title">最近升级</h2>
        <table class="oo-table">
          <thead>
            <tr><th>{{ $t('common.terms.fromVersion') }}</th><th>{{ $t('common.terms.targetVersion') }}</th><th>{{ $t('common.terms.status') }}</th><th>{{ $t('common.terms.start') }}</th><th>{{ $t('common.terms.finish') }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="u in data.recent_upgrades" :key="u.id">
              <td><code>{{ u.version_from ?? '—' }}</code></td>
              <td><code>{{ u.version_to ?? '—' }}</code></td>
              <td>{{ u.status ?? '—' }}</td>
              <td>{{ formatTimestamp(u.started_at) }}</td>
              <td>{{ formatTimestamp(u.completed_at) }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- 8. 下载统计 -->
      <section v-if="data.download_stats" class="oo-section">
        <h2 class="oo-section-title">下载统计</h2>
        <div class="oo-cards">
          <div class="oo-card">
            <h3 class="oo-card-title">总下载数</h3>
            <p class="oo-card-metric">{{ formatNumber(data.download_stats.total_downloads) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">近 24 小时</h3>
            <p class="oo-card-metric">{{ formatNumber(data.download_stats.last_24h) }}</p>
          </div>
        </div>
      </section>

      <!-- 9. 运行时指标摘要 -->
      <section v-if="data.runtime_metrics_summary" class="oo-section">
        <h2 class="oo-section-title">运行时指标</h2>
        <div class="oo-cards">
          <div class="oo-card">
            <h3 class="oo-card-title">运行时长</h3>
            <p class="oo-card-metric">{{ formatUptime(data.runtime_metrics_summary.uptime_seconds) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">内存 RSS</h3>
            <p class="oo-card-metric">{{ formatBytes(data.runtime_metrics_summary.memory_rss_bytes) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">Goroutine</h3>
            <p class="oo-card-metric">{{ formatNumber(data.runtime_metrics_summary.goroutines) }}</p>
          </div>
          <div class="oo-card">
            <h3 class="oo-card-title">CPU</h3>
            <p class="oo-card-metric">{{ data.runtime_metrics_summary.cpu_percent?.toFixed(1) ?? '—' }}%</p>
          </div>
        </div>
      </section>

      <footer class="oo-footer">
        <span>服务端缓存 15s TTL（不实时刷新）</span>
        <span>·</span>
        <span>最近刷新: {{ data.refreshedAt }}</span>
      </footer>
    </template>
  </div>
</template>

<style scoped>
.ops-overview-view { padding: var(--space-4); }
.oo-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-3); }
.oo-title { font-size: var(--text-xl); font-weight: var(--font-semibold); }
.oo-error { color: var(--danger); padding: var(--space-2); background: var(--danger-bg); border-radius: var(--radius); margin-bottom: var(--space-3); }
.oo-loading { color: var(--text-muted); padding: var(--space-3); }
.oo-section { background: var(--bg-surface); border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-3); margin-bottom: var(--space-3); }
.oo-section-title { font-size: var(--text-md); font-weight: var(--font-semibold); margin-bottom: var(--space-2); }
.oo-cards { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--space-3); }
.oo-card { background: var(--bg-surface); border: 1px solid var(--border-muted); border-radius: var(--radius); padding: var(--space-3); }
.oo-card-title { font-size: var(--text-sm); color: var(--text-muted); margin-bottom: var(--space-1); }
.oo-card-metric { font-size: var(--text-2xl); font-weight: var(--font-semibold); }
.oo-card-online { border: 1px solid var(--success); }
.oo-card-offline { border: 1px solid var(--danger); }
.oo-card-degraded { border: 1px solid var(--warning); }
.oo-table { width: 100%; border-collapse: collapse; margin-top: var(--space-2); }
.oo-table th, .oo-table td { text-align: left; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border-muted); font-size: var(--text-sm); }
.oo-table th { color: var(--text-muted); font-weight: var(--font-semibold); }
.oo-list { list-style: none; margin: 0; padding: 0; }
.oo-list-row { display: flex; justify-content: space-between; padding: var(--space-1) 0; border-bottom: 1px solid var(--border-muted); font-size: var(--text-sm); }
.oo-list-row:last-child { border-bottom: none; }
.oo-footer { margin-top: var(--space-3); color: var(--text-muted); font-size: var(--text-sm); display: flex; gap: var(--space-2); }
@media (max-width: 768px) { .oo-cards { grid-template-columns: 1fr 1fr; } }
</style>
