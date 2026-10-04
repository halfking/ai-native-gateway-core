<script setup lang="ts">
// AlertsView.vue — 告警时间线卡片。
import { onMounted, ref } from 'vue'
import { fetchAlerts, type CandidateFailureAlert } from '../api/alerts'
import { useHyperPage } from '../composables/useHyperPage'
import { useTitleStore } from '../stores/titleStore'
import SkeletonList from '../components/ui/SkeletonList.vue'
import ErrorRetry from '../components/ui/ErrorRetry.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import { t } from '../i18n'

const titleStore = useTitleStore()
useHyperPage({ routeTitle: t('alerts.title') })

const alerts = ref<CandidateFailureAlert[]>([])
const loading = ref(true)
const loadError = ref('')

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await fetchAlerts()
    alerts.value = res.data ?? []
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

onMounted(() => {
  titleStore.setRegistered(t('alerts.title'))
  void load()
})
</script>

<template>
  <section class="m-page">
    <SkeletonList v-if="loading" :lines="4" />
    <ErrorRetry v-else-if="loadError" :message="loadError" @retry="load" />
    <EmptyState v-else-if="alerts.length === 0" :message="t('alerts.empty')" />

    <template v-else>
      <div v-for="(a, i) in alerts" :key="`${a.ts}-${a.credential_id}-${i}`" class="m-card alert-card" :data-row-id="`alert-${i}`">
        <div class="m-card__row">
          <span class="alert-kind">{{ a.error_kind }}</span>
          <span class="alert-ts">{{ fmtTime(a.ts) }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ a.raw_model_name }}</span>
          <span class="m-kv__v num">{{ t('alerts.count').replace('{n}', String(a.count)) }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">credential</span>
          <span class="m-kv__v num">#{{ a.credential_id }}</span>
        </div>
        <p v-if="a.last_response_preview" class="alert-body">{{ a.last_response_preview.slice(0, 160) }}</p>
      </div>
    </template>
  </section>
</template>

<style scoped>
.alert-kind {
  font-weight: 600;
  color: var(--app-danger);
  font-size: 0.875rem;
  word-break: break-all;
}

.alert-ts {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.alert-body {
  margin-top: var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
  word-break: break-all;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
