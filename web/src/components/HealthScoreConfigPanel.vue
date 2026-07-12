<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
</script>

<template>
  <div class="health-panel">
    <section class="health-hero">
      <div><span class="eyebrow">{{ t('sessions.config.healthReadonly') }}</span><h2>{{ t('sessions.config.healthTitle') }}</h2><p>{{ t('sessions.config.healthReadonlyHint') }}</p></div>
      <span class="status-badge">{{ t('sessions.config.moduleEnabled') }}</span>
    </section>
    <section class="health-grid">
      <article><strong>{{ t('sessions.config.healthMetricToken') }}</strong><span>{{ t('sessions.config.healthMetricTokenHint') }}</span></article>
      <article><strong>{{ t('sessions.config.healthMetricLatency') }}</strong><span>{{ t('sessions.config.healthMetricLatencyHint') }}</span></article>
      <article><strong>{{ t('sessions.config.healthMetricCompliance') }}</strong><span>{{ t('sessions.config.healthMetricComplianceHint') }}</span></article>
      <article><strong>{{ t('sessions.config.healthMetricOutcome') }}</strong><span>{{ t('sessions.config.healthMetricOutcomeHint') }}</span></article>
    </section>
    <div class="health-footer"><span>{{ t('sessions.config.healthSource') }}</span><a href="/admin/session-analytics">{{ t('sessions.config.healthViewAnalytics') }}</a></div>
  </div>
</template>

<style scoped>
.health-panel { display:grid; gap:12px; max-width:980px; }
.health-hero { display:flex; align-items:center; justify-content:space-between; gap:16px; padding:16px; border:1px solid var(--border); border-radius:var(--radius); background:var(--card); }
.eyebrow { color:var(--accent-h); font-size:10px; text-transform:uppercase; letter-spacing:.06em; } h2 { margin:4px 0; color:var(--text); font-size:16px; } p { margin:0; color:var(--muted); font-size:12px; }
.status-badge { color:var(--success); border:1px solid color-mix(in srgb,var(--success) 40%,var(--border)); border-radius:999px; padding:4px 8px; font-size:11px; white-space:nowrap; }
.health-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:8px; } article { display:grid; gap:4px; padding:12px; background:var(--card); border:1px solid var(--border); border-radius:var(--radius); } article strong { color:var(--text); font-size:12px; } article span,.health-footer { color:var(--muted); font-size:11px; line-height:1.45; } .health-footer { display:flex; justify-content:space-between; gap:12px; padding:4px 2px; } a { color:var(--accent-h); } @media(max-width:560px){.health-hero{align-items:start;flex-direction:column}.health-grid{grid-template-columns:1fr}}
</style>
