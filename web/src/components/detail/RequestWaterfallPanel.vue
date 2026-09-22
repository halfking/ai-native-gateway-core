<script setup lang="ts">
// RequestWaterfallPanel — inline waterfall state wrapper around the shared
// selected-request content used by the dispatch right-side drawer.
import { useI18n } from 'vue-i18n'
import type { WaterfallAttempt, WaterfallRequest } from '../../api/dispatch'
import { credentialDisplayName, useCredentialLabels } from '../../composables/useCredentialLabels'
import { statusToneClass } from './statusTone'
import WaterfallRequestDetailContent from './WaterfallRequestDetailContent.vue'

const props = defineProps<{
  selected: WaterfallRequest | null
  attempts?: WaterfallAttempt[]
  loading?: boolean
  error?: string
}>()

const { t } = useI18n()
const { labelRevision } = useCredentialLabels()

function credentialLabel(id: number): string {
  void labelRevision.value
  return credentialDisplayName(id)
}
</script>

<template>
  <div class="rwf" data-testid="request-waterfall-panel">
    <div v-if="loading" class="muted" data-testid="request-waterfall-loading">
      {{ t('requestDetail.waterfall.loading') }}
    </div>
    <div v-else-if="error && !selected" class="err" data-testid="request-waterfall-error">
      {{ error }}
    </div>
    <WaterfallRequestDetailContent
      v-else-if="selected"
      :request="selected"
      :fallback-attempts="attempts"
      show-request-summary
    />
    <div v-else class="muted" data-testid="request-waterfall-empty">
      {{ t('requestDetail.waterfall.empty') }}
    </div>

    <section
      v-if="!selected && attempts?.length"
      class="attempts-only"
      data-testid="request-waterfall-attempts-only"
    >
      <h4>Attempts ({{ attempts.length }})</h4>
      <ul>
        <li
          v-for="attempt in attempts"
          :key="attempt.attempt_id || attempt.attempt_no"
          :class="statusToneClass(attempt.outcome, 'attempt')"
        >
          <span>#{{ attempt.attempt_no }}</span>
          <span>{{ attempt.model || '—' }}</span>
          <span :title="`credential_id: ${attempt.credential_id}`">{{ credentialLabel(attempt.credential_id) }}</span>
          <span class="pill" :class="statusToneClass(attempt.outcome, 'pill')">{{ attempt.outcome || '—' }}</span>
          <span v-if="attempt.error_kind" class="err">{{ attempt.error_kind }}</span>
        </li>
      </ul>
    </section>
    <p v-else-if="!selected && !loading" class="muted no-attempts">
      {{ t('requestDetail.waterfall.noAttempts') }}
    </p>
  </div>
</template>

<style scoped>
.rwf { font-size: 13px; }
.muted { color: var(--muted, var(--kx-muted)); font-size: 12px; }
.err { color: var(--danger, var(--kx-danger)); font-size: 12px; }
.attempts-only {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px dashed var(--border, var(--kx-border));
}
.attempts-only h4 { margin: 0 0 8px; font-size: 13px; }
.attempts-only ul {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.attempts-only li {
  display: grid;
  grid-template-columns: 40px minmax(0, 1fr) 90px 100px auto;
  gap: 10px;
  padding: 4px 6px;
  border-left: 3px solid transparent;
  border-radius: 4px;
  background: var(--bg-subtle, var(--kx-bg));
  font-size: 12px;
}
.attempt--ok { border-left-color: var(--kx-success); }
.attempt--err {
  border-left-color: var(--kx-danger);
  background: color-mix(in srgb, var(--kx-danger) 6%, transparent);
}
.attempt--warn { border-left-color: var(--kx-warning); }
.attempt--info { border-left-color: var(--kx-primary); }
.no-attempts { margin: 12px 0 0; }
</style>
