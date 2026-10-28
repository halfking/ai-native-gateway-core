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
      <!--
        2026-10-28 第 30 增量：原来这里是 4 张同尺寸同构的「小标签 + 巨数字」卡并排
        （DESIGN.md §6 明禁「同尺寸卡片当页面骨架」「四个数字并排」；impeccable
        craft-floor 的 hero-metric 模板也是同一件事）。
        ⇒ 改成一行一行的「标签 → 值」：值班台看的是「总实例数 10 / 在线 7」，
        不是四个并排的盒子。★ 顺带修掉两个真缺陷：
          ① 「在线/离线/降级」原来靠 1px 彩色描边区分（DESIGN.md §6 状态表达要求
             软填充 + 文字颜色 + 文字本身），现在改成值位的软填充 + 语义色；
          ② 中心实例的 4 个数字与下面「区域分布」表的 4 列**完全重复** ——
             并排放 4 张卡等于把同一组事实说两遍。
      -->
      <section class="oo-section">
        <h2 class="oo-section-title">中心实例</h2>
        <ul class="oo-metrics">
          <li class="oo-metric">
            <span class="oo-metric-label">总实例数</span>
            <span class="oo-metric-value">{{ formatNumber(data.center_stats?.total_instances) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">在线</span>
            <span class="oo-metric-value oo-metric-value--ok">{{ formatNumber(data.center_stats?.online_instances) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">离线</span>
            <span class="oo-metric-value oo-metric-value--bad">{{ formatNumber(data.center_stats?.offline_instances) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">降级</span>
            <span class="oo-metric-value oo-metric-value--warn">{{ formatNumber(data.center_stats?.degraded_instances) }}</span>
          </li>
        </ul>
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
        <ul class="oo-metrics">
          <li class="oo-metric">
            <span class="oo-metric-label">有效 License 总数</span>
            <span class="oo-metric-value">{{ formatNumber(data.license_total) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">离线激活请求</span>
            <span class="oo-metric-value">{{ formatNumber(data.offline_requests?.length) }}</span>
          </li>
        </ul>
      </section>

      <!-- 6. 故障统计与最近事件 -->
      <section v-if="data.fault_stats" class="oo-section">
        <h2 class="oo-section-title">故障</h2>
        <ul class="oo-metrics">
          <li class="oo-metric">
            <span class="oo-metric-label">总故障数</span>
            <span class="oo-metric-value">{{ formatNumber(data.fault_stats?.total) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">已解决</span>
            <span class="oo-metric-value oo-metric-value--ok">{{ formatNumber(data.fault_stats?.resolved) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">{{ $t('common.status.pending') }}</span>
            <span class="oo-metric-value">{{ formatNumber(data.fault_stats?.pending) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">最近故障</span>
            <span class="oo-metric-value">{{ data.recent_faults?.length ?? 0 }} 条</span>
          </li>
        </ul>
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
        <ul class="oo-metrics">
          <li class="oo-metric">
            <span class="oo-metric-label">总下载数</span>
            <span class="oo-metric-value">{{ formatNumber(data.download_stats.total_downloads) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">近 24 小时</span>
            <span class="oo-metric-value">{{ formatNumber(data.download_stats.last_24h) }}</span>
          </li>
        </ul>
      </section>

      <!-- 9. 运行时指标摘要 -->
      <section v-if="data.runtime_metrics_summary" class="oo-section">
        <h2 class="oo-section-title">运行时指标</h2>
        <ul class="oo-metrics">
          <li class="oo-metric">
            <span class="oo-metric-label">运行时长</span>
            <span class="oo-metric-value">{{ formatUptime(data.runtime_metrics_summary.uptime_seconds) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">内存 RSS</span>
            <span class="oo-metric-value">{{ formatBytes(data.runtime_metrics_summary.memory_rss_bytes) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">Goroutine</span>
            <span class="oo-metric-value">{{ formatNumber(data.runtime_metrics_summary.goroutines) }}</span>
          </li>
          <li class="oo-metric">
            <span class="oo-metric-label">CPU</span>
            <!-- ★ 2026-10-28 第 30 增量：原来写的是
                 `{{ cpu?.toFixed(1) ?? '—' }}%` ⇒ 后端没上报 CPU 时**渲染成「—%」**
                 （一个悬空的百分号）。⇒ 百分号跟着数值走，没有数就没有百分号。 -->
            <span class="oo-metric-value">{{ data.runtime_metrics_summary.cpu_percent != null ? data.runtime_metrics_summary.cpu_percent.toFixed(1) + '%' : '—' }}</span>
          </li>
        </ul>
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
/* ── 指标区（2026-10-28 第 30 增量）─────────────────────────────────────
 * 原来这里是一套 `.oo-cards` 网格（4 张同尺寸的「小标签 + 巨数字」卡并排）。
 * 那是 DESIGN.md §6 明禁的「同尺寸卡片当页面骨架」+ impeccable craft-floor 的
 * hero-metric 模板（大数字 / 小标签 / 辅助统计 / 强调色）。
 * ⇒ 改成「标签 → 值」的行：一行一个事实，值在右、标签在左；
 *   语义状态落在**值位**（软填充 + 文字颜色 + 文字本身三者同进，DESIGN.md §3）。
 * ★ 刻意不给行加底色或四边描边 —— 只有下边框的**行**不是卡片（§6「卡片是懒容器」）。
 */
.oo-metrics {
  list-style: none;
  margin: 0;
  padding: 0;
}
.oo-metric {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-1) 0;
  border-bottom: 1px solid var(--border-muted);
  font-size: var(--text-sm);
}
.oo-metric:last-child {
  border-bottom: none;
}
.oo-metric-label {
  color: var(--text-muted);
}
.oo-metric-value {
  color: var(--text);
  font-size: var(--text-lg);
  font-weight: var(--font-semibold);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
/* 状态值：软填充 + 语义色文字（颜色之外还有形状与文字本身 ⇒ 状态不只靠颜色区分） */
.oo-metric-value--ok {
  color: var(--success);
  background: var(--success-soft);
  border-radius: 4px;
  padding: 0 var(--space-1);
}
.oo-metric-value--bad {
  color: var(--danger);
  background: var(--danger-soft);
  border-radius: 4px;
  padding: 0 var(--space-1);
}
.oo-metric-value--warn {
  color: var(--warning);
  background: var(--warning-soft);
  border-radius: 4px;
  padding: 0 var(--space-1);
}
.oo-table { width: 100%; border-collapse: collapse; margin-top: var(--space-2); }
.oo-table th, .oo-table td { text-align: left; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border-muted); font-size: var(--text-sm); }
.oo-table th { color: var(--text-muted); font-weight: var(--font-semibold); }
.oo-list { list-style: none; margin: 0; padding: 0; }
.oo-list-row { display: flex; justify-content: space-between; padding: var(--space-1) 0; border-bottom: 1px solid var(--border-muted); font-size: var(--text-sm); }
.oo-list-row:last-child { border-bottom: none; }
.oo-footer { margin-top: var(--space-3); color: var(--text-muted); font-size: var(--text-sm); display: flex; gap: var(--space-2); }
@media (max-width: 768px) { .oo-metric { font-size: var(--text-xs); } }
</style>
