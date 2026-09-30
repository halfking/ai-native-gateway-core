<script setup lang="ts">
/**
 * SessionCatalogPanel — 统计 tab 的会话目录。
 * 数据源是生产 GET /api/admin/sessions（session manager），
 * 带标题、轮次、token、费用、健康度。q 交给服务端检索会话摘要，
 * 同时在已返回行上按会话键、标题、模型、标签即时过滤。结果上限 200。
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { listCatalogSessions, type CatalogSession } from '../../api/session'
import { formatDateTime } from '../../utils/datetime'

const emit = defineEmits<{ (e: 'select', sessionId: string): void }>()
const { t } = useI18n()

type StatusFilter = 'all' | 'active' | 'stopped'

const status = ref<StatusFilter>('all')
const query = ref('')
const rows = ref<CatalogSession[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')

// 代际号防竞态（样板见 useReconciliationPage.ts 的 fetchGen）：快速切换
// status chips / 防抖搜索时，旧响应后到不得覆盖新结果。
let fetchGen = 0
async function load() {
  const gen = ++fetchGen
  loading.value = true
  error.value = ''
  const q = query.value.trim()
  try {
    const r = await listCatalogSessions({
      status: status.value,
      limit: 200,
      q: q || undefined,
    })
    if (gen !== fetchGen) return
    rows.value = r.sessions ?? []
    loaded.value = true
  } catch (e) {
    if (gen !== fetchGen) return
    error.value = e instanceof Error ? e.message : String(e)
    rows.value = []
  } finally {
    if (gen === fetchGen) loading.value = false
  }
}

let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    void load()
  }, 300)
})
onUnmounted(() => {
  if (searchTimer) clearTimeout(searchTimer)
})

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return rows.value
  return rows.value.filter((s) => {
    const hay = [s.session_id, s.title, s.current_model, s.tags].join(' ').toLowerCase()
    return hay.includes(q)
  })
})

watch(status, () => {
  void load()
})
onMounted(() => {
  void load()
})

function tokenTotal(s: CatalogSession): number {
  return (s.total_prompt_tokens ?? 0) + (s.total_completion_tokens ?? 0)
}

function formatCost(value: number | undefined): string {
  return `$${(value ?? 0).toFixed(4)}`
}

function formatWhen(iso: string | undefined): string {
  if (!iso) return '—'
  return formatDateTime(iso)
}
</script>

<template>
  <div class="scp" data-testid="session-catalog-panel">
    <div class="scp-toolbar">
      <div class="scp-status" role="group">
        <button
          v-for="opt in (['all', 'active', 'stopped'] as const)"
          :key="opt"
          type="button"
          class="scp-chip"
          :class="{ 'scp-chip--active': status === opt }"
          :data-status="opt"
          @click="status = opt"
        >
          {{ t(`sessions.catalog.status.${opt}`) }}
        </button>
      </div>
      <input
        v-model="query"
        class="scp-search"
        type="search"
        data-testid="catalog-search"
        :placeholder="t('sessions.catalog.searchPlaceholder')"
        :aria-label="t('sessions.catalog.searchPlaceholder')"
      />
      <button type="button" class="scp-refresh" :disabled="loading" @click="load">
        {{ loading ? t('sessions.catalog.loading') : t('sessions.catalog.refresh') }}
      </button>
    </div>
    <p class="scp-hint">{{ t('sessions.catalog.searchHint') }}</p>

    <div v-if="loading && !loaded" class="scp-skeleton" data-testid="catalog-skeleton">
      <div v-for="i in 4" :key="i" class="scp-skeleton-row" />
    </div>
    <div v-else-if="error" class="scp-error" role="alert">
      <span>{{ error }}</span>
      <button type="button" :disabled="loading" @click="load">{{ t('sessions.catalog.retry') }}</button>
    </div>
    <div v-else-if="loaded && filtered.length === 0" class="scp-empty" data-testid="catalog-empty">
      {{ t('sessions.catalog.empty') }}
    </div>
    <div v-else class="scp-table">
      <div class="scp-row scp-row--head">
        <span>{{ t('sessions.catalog.colTitle') }}</span>
        <span>{{ t('sessions.catalog.colStatus') }}</span>
        <span>{{ t('sessions.catalog.colModel') }}</span>
        <span>{{ t('sessions.catalog.colTurns') }}</span>
        <span>{{ t('sessions.catalog.colTokens') }}</span>
        <span>{{ t('sessions.catalog.colCost') }}</span>
        <span>{{ t('sessions.catalog.colHealth') }}</span>
        <span>{{ t('sessions.catalog.colLastActive') }}</span>
      </div>
      <button
        v-for="s in filtered"
        :key="s.session_id"
        type="button"
        class="scp-row"
        :data-session-id="s.session_id"
        @click="emit('select', s.session_id)"
      >
        <span class="scp-title" :title="s.title || s.session_id">
          {{ s.title || s.session_id }}
        </span>
        <span>{{ s.status || '—' }}</span>
        <span>{{ s.current_model || '—' }}</span>
        <span>{{ s.total_turns ?? 0 }}</span>
        <span>{{ tokenTotal(s).toLocaleString() }}</span>
        <span>{{ formatCost(s.total_cost_usd) }}</span>
        <span>{{ s.health_grade || '—' }}</span>
        <span>{{ formatWhen(s.last_active || s.last_request_at) }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.scp { border: 1px solid var(--kx-border); border-radius: 10px; background: var(--kx-surface); padding: 12px; }
.scp-toolbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.scp-status { display: inline-flex; border: 1px solid var(--kx-border); border-radius: 6px; overflow: hidden; }
.scp-chip, .scp-refresh {
  border: 0; background: transparent; color: var(--kx-text); padding: 6px 12px; font-size: 13px; cursor: pointer;
}
.scp-chip--active { background: var(--kx-primary); color: var(--on-primary); }
.scp-search {
  flex: 1; min-width: 180px; border: 1px solid var(--kx-border); border-radius: 6px;
  background: var(--kx-bg); color: var(--kx-text); padding: 6px 10px; font-size: 13px;
}
.scp-refresh { border: 1px solid var(--kx-border); border-radius: 6px; }
.scp-hint { margin: 8px 0 12px; font-size: 12px; color: var(--kx-text-muted, var(--kx-text)); }
.scp-skeleton-row { height: 28px; margin-bottom: 8px; border-radius: 6px; background: var(--kx-border); opacity: 0.45; }
.scp-error, .scp-empty { padding: 16px; font-size: 13px; color: var(--kx-text); }
.scp-error button { margin-left: 8px; }
.scp-table { display: flex; flex-direction: column; gap: 2px; }
.scp-row {
  display: grid; grid-template-columns: minmax(140px, 1.6fr) 0.7fr 1fr 0.5fr 0.7fr 0.7fr 0.5fr 1fr;
  gap: 8px; align-items: center; width: 100%; text-align: left;
  border: 0; background: transparent; color: var(--kx-text); font-size: 13px; padding: 8px 4px; cursor: pointer;
}
.scp-row--head { cursor: default; font-size: 12px; color: var(--kx-text-muted, var(--kx-text)); }
.scp-row:not(.scp-row--head):hover { background: var(--kx-bg); }
.scp-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
