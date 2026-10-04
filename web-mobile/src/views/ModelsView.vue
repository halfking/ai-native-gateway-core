<script setup lang="ts">
// ModelsView.vue — 模型目录：搜索（250ms debounce）过滤家族分组。
// 数据一次性拉取（家族目录总量有限），组内版本列表本地分片渲染。
import { computed, onMounted, ref } from 'vue'
import { fetchAvailableModels, type AvailableFamily, type AvailableVersion } from '../api/models'
import { useHyperPage } from '../composables/useHyperPage'
import { useTitleStore } from '../stores/titleStore'
import SkeletonList from '../components/ui/SkeletonList.vue'
import ErrorRetry from '../components/ui/ErrorRetry.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import Icon from '../components/Icon.vue'
import { t } from '../i18n'

const titleStore = useTitleStore()
useHyperPage({ routeTitle: t('models.title'), filter: () => query.value })

const families = ref<AvailableFamily[]>([])
const totalRaw = ref(0)
const loading = ref(true)
const loadError = ref('')
const query = ref('')
const debounced = ref('')
let debounceTimer: ReturnType<typeof setTimeout> | null = null

function onSearchInput(): void {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    debounced.value = query.value.trim().toLowerCase()
  }, 250)
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await fetchAvailableModels()
    families.value = res.families ?? []
    totalRaw.value = res.total_raw ?? 0
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

function versionMatch(v: AvailableVersion, q: string): boolean {
  return (
    v.canonical_name.toLowerCase().includes(q) ||
    v.display_name.toLowerCase().includes(q) ||
    v.tags.some((tag) => tag.toLowerCase().includes(q))
  )
}

const filtered = computed(() => {
  const q = debounced.value
  if (!q) return families.value
  return families.value
    .map((f) => ({
      ...f,
      versions: f.versions.filter((v) => versionMatch(v, q)),
    }))
    .filter((f) => f.versions.length > 0 || f.display_name.toLowerCase().includes(q))
})

const versionCount = computed(() => filtered.value.reduce((acc, f) => acc + f.versions.length, 0))

onMounted(() => {
  titleStore.setRegistered(t('models.title'))
  void load()
})
</script>

<template>
  <section class="m-page">
    <div class="m-search">
      <Icon name="search" />
      <input
        v-model="query"
        class="m-input"
        type="search"
        :placeholder="t('models.search')"
        @input="onSearchInput"
      />
    </div>

    <SkeletonList v-if="loading" :lines="5" />
    <ErrorRetry v-else-if="loadError" :message="loadError" @retry="load" />
    <EmptyState v-else-if="filtered.length === 0" :message="t('models.noResults')" />

    <template v-else>
      <p class="total-note">{{ t('common.loaded').replace('{loaded}', String(versionCount)).replace('{total}', String(totalRaw)) }}</p>
      <section v-for="family in filtered" :key="family.id" :data-row-id="`family-${family.id}`">
        <p class="m-group-label">
          {{ family.display_name }}
          <span class="vendor">{{ family.vendor }}</span>
        </p>
        <div class="m-card">
          <button
            v-for="v in family.versions"
            :key="v.canonical_name"
            type="button"
            class="ver-row"
            :data-row-id="`model-${v.canonical_name}`"
          >
            <div class="ver-main">
              <span class="ver-name">{{ v.display_name || v.canonical_name }}</span>
              <span class="ver-meta">
                {{ t('models.providerCount').replace('{n}', String(v.provider_count)) }}
                <template v-if="v.context_window"> · {{ t('models.contextWindow').replace('{n}', String(Math.round(v.context_window / 1024))) }}</template>
              </span>
            </div>
            <Icon name="chevron" :size="16" />
          </button>
        </div>
      </section>
    </template>
  </section>
</template>

<style scoped>
.total-note {
  font-size: 0.75rem;
  color: var(--app-text-muted);
  margin-bottom: var(--app-space-2);
}

.vendor {
  font-weight: 400;
  color: var(--app-text-muted);
  margin-left: var(--app-space-2);
}

.ver-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  min-height: 52px;
  padding: var(--app-space-2) 0;
  text-align: left;
  color: var(--app-text);
  border-bottom: 1px solid var(--app-border-subtle);
}

.ver-row:last-child {
  border-bottom: none;
}

.ver-name {
  display: block;
  font-size: 0.9375rem;
  font-weight: 550;
  word-break: break-all;
}

.ver-meta {
  display: block;
  font-size: 0.75rem;
  color: var(--app-text-muted);
  margin-top: 2px;
}
</style>
