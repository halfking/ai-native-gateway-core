<script setup lang="ts">
import { computed, onMounted, provide, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  useSessionFilters,
  useSessionList,
  listBackQueryFromRoute,
} from '../composables/useSessionContext'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const filters = useSessionFilters()
const list = useSessionList()
const { meta, loading } = list

const isDetail = computed(() => !!route.params.taskId)
const activeListTab = computed(() => (route.query.section === 'no-topic' ? 'no-topic' : 'topic'))
const backListQuery = computed(() => listBackQueryFromRoute(route))

provide('sessionContextFilters', filters)
provide('sessionContextList', list)

async function refreshList() {
  try {
    await list.loadSessions({
      q: filters.searchQ.value || undefined,
      owner_user: filters.searchOwner.value || undefined,
      hours: filters.hours.value,
      no_topic_window: filters.noTopicWindow.value,
    })
  } catch {
    // error surfaced in list.error
  }
}

function goListTab(section: 'topic' | 'no-topic') {
  router.push({ path: '/session-context', query: section === 'no-topic' ? { section: 'no-topic' } : {} })
}

onMounted(() => {
  if (!isDetail.value) refreshList()
})

watch(
  () => route.path,
  (path) => {
    if (path === '/session-context' || path === '/session-context/') refreshList()
  },
)

defineExpose({ refreshList })
</script>

<template>
  <div class="sc-layout">
    <div class="top-bar">
      <div class="top-bar-head">
        <router-link
          v-if="isDetail"
          :to="{ path: '/session-context', query: backListQuery }"
          class="back-link"
        >{{ t('sessions.contextLayout.backToList') }}</router-link>
        <h2>{{ t('nav.item.sessionContext') }}</h2>
        <div v-if="!isDetail" class="seg-tabs">
          <button
            class="seg-tab"
            :class="{ active: activeListTab === 'topic' }"
            @click="goListTab('topic')"
          >{{ t('sessions.contextLayout.tabTopic') }}</button>
          <button
            class="seg-tab"
            :class="{ active: activeListTab === 'no-topic' }"
            @click="goListTab('no-topic')"
          >{{ t('sessions.contextLayout.tabNoTopic') }}</button>
        </div>
        <button
          v-if="!isDetail"
          class="btn btn-sm btn-ghost refresh-btn"
          :disabled="loading"
          :title="t('sessions.contextLayout.refreshTitle')"
          @click="refreshList"
        >↻</button>
      </div>
      <div v-if="!isDetail && meta" class="hero-stats">
        <span class="chip">{{ t('sessions.contextLayout.statHours') }} <strong>{{ meta.hours }}h</strong></span>
        <span class="chip">{{ t('sessions.contextLayout.statTopic') }} <strong>{{ meta.topic_count }}</strong></span>
        <span class="chip">{{ t('sessions.contextLayout.statNoTopic') }} <strong>{{ meta.no_topic_count }}</strong></span>
        <span class="chip">{{ t('sessions.contextLayout.statWindow') }} <strong>{{ meta.no_topic_window }}h</strong></span>
      </div>
      <p v-if="!isDetail" class="sub">{{ t('sessions.contextLayout.subtitle') }}</p>
    </div>

    <RouterView />
  </div>
</template>

<style scoped>
.sc-layout { max-width: 1200px; }

.top-bar {
  margin-bottom: 8px;
  padding: 8px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.top-bar-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 4px;
}
.top-bar-head h2 { font-size: 15px; margin: 0; }
.back-link { font-size: 11px; color: var(--muted); text-decoration: none; }
.back-link:hover { color: var(--accent-h); }
.refresh-btn { margin-left: auto; }
.sub { margin: 4px 0 0; font-size: 11px; color: var(--muted); }

.seg-tabs {
  display: inline-flex;
  gap: 1px;
  padding: 2px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.seg-tab {
  padding: 3px 10px;
  border: none;
  border-radius: 4px;
  background: transparent;
  font-size: 11px;
  color: var(--muted);
  cursor: pointer;
}
.seg-tab.active {
  background: var(--card);
  color: var(--text);
  font-weight: 600;
  box-shadow: 0 1px 2px rgba(0,0,0,.12);
}

.hero-stats { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 4px; }
.chip {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
  color: var(--muted);
}
.chip strong { color: var(--text); font-weight: 600; }
</style>
