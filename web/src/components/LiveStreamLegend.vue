<script setup lang="ts">
// LiveStreamLegend — colour key for the swim lane.
import { useI18n } from 'vue-i18n'
import {
  MODEL_COLORS,
  STATUS_COLORS,
  MODEL_FAMILY_LABELS,
  STATUS_LABELS,
} from '../composables/liveStreamColors'

const { t } = useI18n()

const modelEntries = (['openai', 'anthropic', 'domestic', 'oss', 'other'] as const).map((key) => ({
  key,
  color: MODEL_COLORS[key],
  label: t(`dashboard.liveStream.legend.${key}`, MODEL_FAMILY_LABELS[key]),
}))

const statusEntries = (['success', 'inProgress', 'failure'] as const).map((key) => ({
  key,
  color: STATUS_COLORS[key === 'inProgress' ? 'in_progress' : key],
  label: t(`dashboard.liveStream.legend.${key}`, STATUS_LABELS[key === 'inProgress' ? 'in_progress' : key]),
}))
</script>

<template>
  <div class="live-legend">
    <div class="live-legend__row">
      <span class="live-legend__heading">{{ t('dashboard.liveStream.legend.model') }}</span>
      <span
        v-for="m in modelEntries"
        :key="m.key"
        class="live-legend__item"
      >
        <span
          class="live-legend__swatch"
          :style="{ background: m.color }"
          aria-hidden="true"
        />
        <span class="live-legend__label">{{ m.label }}</span>
      </span>
    </div>
    <div class="live-legend__row">
      <span class="live-legend__heading">{{ t('dashboard.liveStream.legend.status') }}</span>
      <span
        v-for="s in statusEntries"
        :key="s.key"
        class="live-legend__item"
      >
        <span
          class="live-legend__swatch"
          :style="{ background: s.color }"
          aria-hidden="true"
        />
        <span class="live-legend__label">{{ s.label }}</span>
      </span>
    </div>
  </div>
</template>

<style scoped>
.live-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 18px;
  font-size: 12px;
  color: var(--muted, #8b949e);
  padding: 8px 4px 2px;
  margin-top: 4px;
  border-top: 1px solid var(--border, #30363d);
}

.live-legend__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
}

.live-legend__heading {
  font-weight: 600;
  color: var(--text, #e6edf3);
  margin-inline-end: 4px;
}

.live-legend__item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.live-legend__swatch {
  display: inline-block;
  width: 12px;
  height: 12px;
  border-radius: 3px;
  box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.08);
}

.live-legend__label {
  line-height: 1;
  color: var(--muted, #8b949e);
}
</style>
