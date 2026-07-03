<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useFormat } from '../../i18n/useFormat'

const { t: td } = useI18n()
const pd = (k: string, params?: Record<string, unknown>): string =>
  td(`providerDetail.${k}` as never, params as never)
const { fmtDateTime } = useFormat()

defineProps<{
  provider: any
}>()

/** Detail API uses *_cred_count; list API uses *_credential_count — accept both. */
function credCount(provider: any, detailKey: string, listKey: string): number {
  const v = provider?.[detailKey] ?? provider?.[listKey]
  return typeof v === 'number' ? v : 0
}

function fmt(v: any) { return v ?? '—' }
function fmtPct(v: any) { return v != null ? Number(v).toFixed(1) + '%' : '—' }
function timeText(v?: string | null) {
  return v ? fmtDateTime(v) : '—'
}
</script>

<template>
  <div class="overview-grid provider-detail-grid">
    <div class="card">
      <h4>{{ pd('overviewCards.basicInfo') }}</h4>
      <dl>
        <dt>{{ pd('overviewCards.labelCatalogCode') }}</dt><dd><code>{{ fmt(provider?.catalog_code) }}</code></dd>
        <dt>{{ pd('overviewCards.labelBaseUrl') }}</dt><dd><code style="word-break:break-all">{{ fmt(provider?.base_url) }}</code></dd>
        <dt>{{ pd('overviewCards.labelProtocol') }}</dt><dd>{{ fmt(provider?.protocol) }}</dd>
        <dt>{{ pd('overviewCards.labelHeaderProfile') }}</dt><dd>{{ fmt(provider?.header_profile_code) }}</dd>
        <dt>{{ pd('overviewCards.labelVendor') }}</dt><dd>{{ fmt(provider?.vendor_name) }}</dd>
        <dt>{{ pd('overviewCards.labelStatus') }}</dt><dd><span :class="provider?.enabled ? 'badge badge-green' : 'badge badge-gray'">{{ provider?.enabled ? pd('settings.statusEnabled') : pd('settings.statusDisabled') }}</span></dd>
        <dt>{{ pd('overviewCards.labelLastCheck') }}</dt><dd>{{ timeText(provider?.health_checked_at) }}</dd>
        <dt v-if="provider?.notes">{{ pd('overviewCards.labelNotes') }}</dt><dd v-if="provider?.notes">{{ provider.notes }}</dd>
      </dl>
    </div>
    <div class="card">
      <h4>{{ pd('overviewCards.credOverview') }}</h4>
      <div class="metric-grid">
        <div class="metric"><b>{{ credCount(provider, 'active_cred_count', 'active_credential_count') }}</b><span>{{ pd('overviewCards.credAvailable') }}</span></div>
        <div class="metric"><b>{{ credCount(provider, 'healthy_cred_count', 'healthy_credential_count') }}</b><span>{{ pd('overviewCards.credHealthy') }}</span></div>
        <div class="metric"><b>{{ credCount(provider, 'cooling_cred_count', 'cooling_credential_count') }}</b><span>{{ pd('overviewCards.credCooling') }}</span></div>
        <div class="metric"><b>{{ credCount(provider, 'unreachable_cred_count', 'unreachable_credential_count') }}</b><span>{{ pd('overviewCards.credUnreachable') }}</span></div>
      </div>
    </div>
    <div class="card">
      <h4>{{ pd('overviewCards.modelErrors') }}</h4>
      <div class="metric-grid">
        <div class="metric"><b>{{ provider?.available_model_count ?? 0 }}</b><span>{{ pd('overviewCards.modelAvailable') }}</span></div>
        <div class="metric"><b>{{ provider?.unavailable_model_count ?? 0 }}</b><span>{{ pd('overviewCards.modelUnavailable') }}</span></div>
        <div class="metric"><b>{{ fmtPct(provider?.error_rate_24h) }}</b><span>{{ pd('overviewCards.errorRate24h') }}</span></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overview-grid {
  margin-bottom: 20px;
}
.overview-grid dl { display: grid; grid-template-columns: auto 1fr; gap: 4px 12px; font-size: 13px; margin: 8px 0; }
.overview-grid dt { color: var(--muted, #94a3b8); white-space: nowrap; }
.overview-grid dd { margin: 0; }
.metric-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 12px; margin-top: 8px; }
.metric { text-align: center; padding: 8px; background: var(--surface-secondary, #1e1e2e); border-radius: 6px; }
.metric b { display: block; font-size: 20px; }
.metric span { font-size: 11px; color: var(--muted, #94a3b8); }
</style>