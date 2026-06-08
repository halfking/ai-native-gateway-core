<script setup lang="ts">
defineProps<{
  provider: any
}>()

function fmt(v: any) { return v ?? '—' }
function fmtPct(v: any) { return v != null ? Number(v).toFixed(1) + '%' : '—' }
function timeText(v?: string | null) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { hour12: false })
}
</script>

<template>
  <div class="overview-grid">
    <div class="card">
      <h4>基础信息</h4>
      <dl>
        <dt>目录代码</dt><dd><code>{{ fmt(provider?.catalog_code) }}</code></dd>
        <dt>Base URL</dt><dd><code style="word-break:break-all">{{ fmt(provider?.base_url) }}</code></dd>
        <dt>协议</dt><dd>{{ fmt(provider?.protocol) }}</dd>
        <dt>Header Profile</dt><dd>{{ fmt(provider?.header_profile_code) }}</dd>
        <dt>厂商</dt><dd>{{ fmt(provider?.vendor_name) }}</dd>
        <dt>状态</dt><dd><span :class="provider?.enabled ? 'badge badge-green' : 'badge badge-gray'">{{ provider?.enabled ? '已启用' : '已禁用' }}</span></dd>
        <dt>最近检查</dt><dd>{{ timeText(provider?.health_checked_at) }}</dd>
        <dt v-if="provider?.notes">备注</dt><dd v-if="provider?.notes">{{ provider.notes }}</dd>
      </dl>
    </div>
    <div class="card">
      <h4>凭据概览</h4>
      <div class="metric-grid">
        <div class="metric"><b>{{ provider?.active_credential_count ?? 0 }}</b><span>可用</span></div>
        <div class="metric"><b>{{ provider?.healthy_count ?? 0 }}</b><span>健康</span></div>
        <div class="metric"><b>{{ provider?.cooling_count ?? 0 }}</b><span>冷却</span></div>
        <div class="metric"><b>{{ provider?.unreachable_count ?? 0 }}</b><span>不可达</span></div>
      </div>
    </div>
    <div class="card">
      <h4>模型 & 错误</h4>
      <div class="metric-grid">
        <div class="metric"><b>{{ provider?.available_model_count ?? 0 }}</b><span>可用模型</span></div>
        <div class="metric"><b>{{ provider?.unavailable_model_count ?? 0 }}</b><span>不可用</span></div>
        <div class="metric"><b>{{ fmtPct(provider?.error_rate_24h) }}</b><span>24h 错误率</span></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overview-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; margin-bottom: 20px; }
.overview-grid dl { display: grid; grid-template-columns: auto 1fr; gap: 4px 12px; font-size: 13px; margin: 8px 0; }
.overview-grid dt { color: var(--muted, #94a3b8); white-space: nowrap; }
.overview-grid dd { margin: 0; }
.metric-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 12px; margin-top: 8px; }
.metric { text-align: center; padding: 8px; background: var(--surface-secondary, #1e1e2e); border-radius: 6px; }
.metric b { display: block; font-size: 20px; }
.metric span { font-size: 11px; color: var(--muted, #94a3b8); }
</style>
