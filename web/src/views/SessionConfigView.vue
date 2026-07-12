<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ApprovalConfigPanel from '../components/ApprovalConfigPanel.vue'
import CompressionConfigPanel from '../components/CompressionConfigPanel.vue'
import HealthScoreConfigPanel from '../components/HealthScoreConfigPanel.vue'
import PromptInjectionConfigPanel from '../components/PromptInjectionConfigPanel.vue'
import { getModuleEnabled } from '../api/modules'

const { t } = useI18n()
type TabKey = 'approval' | 'compression' | 'promptInjection' | 'health'

const activeTab = ref<TabKey>('approval')
const healthEnabled = ref(false)
const promptInjectionEnabled = ref(false)
const moduleLoading = ref(true)

const tabs = computed<{ key: TabKey; label: string }[]>(() => {
  const items: { key: TabKey; label: string }[] = [
    { key: 'approval', label: t('sessions.config.approvalTab') },
    { key: 'compression', label: t('sessions.config.compressionTab') },
  ]
  if (promptInjectionEnabled.value) items.push({ key: 'promptInjection', label: t('sessions.config.promptInjectionTab') })
  if (healthEnabled.value) items.push({ key: 'health', label: t('sessions.config.healthTab') })
  return items
})

function setActive(key: TabKey) {
  if (!tabs.value.some((tab) => tab.key === key)) return
  activeTab.value = key
}

function focusTab(direction: -1 | 1) {
  const current = tabs.value.findIndex((tab) => tab.key === activeTab.value)
  if (current < 0) return
  const key = tabs.value[(current + direction + tabs.value.length) % tabs.value.length]!.key
  setActive(key)
  void nextTick(() => document.getElementById(`session-config-tab-${key}`)?.focus())
}

function handleTabKey(event: KeyboardEvent) {
  if (event.key === 'ArrowLeft') { event.preventDefault(); focusTab(-1) }
  if (event.key === 'ArrowRight') { event.preventDefault(); focusTab(1) }
  if (event.key === 'Home') { event.preventDefault(); setActive(tabs.value[0]!.key) }
  if (event.key === 'End') { event.preventDefault(); setActive(tabs.value[tabs.value.length - 1]!.key) }
}

onMounted(async () => {
  try {
    const [health, promptInjection] = await Promise.all([
      getModuleEnabled('session_inspector').catch(() => false),
      getModuleEnabled('prompt_injection').catch(() => false),
    ])
    healthEnabled.value = health
    promptInjectionEnabled.value = promptInjection
  } finally {
    moduleLoading.value = false
    if (!healthEnabled.value && activeTab.value === 'health') activeTab.value = 'approval'
    if (!promptInjectionEnabled.value && activeTab.value === 'promptInjection') activeTab.value = 'approval'
  }
})
</script>

<template>
  <main class="session-config-view" :aria-busy="moduleLoading">
    <header class="page-header">
      <div>
        <div class="eyebrow">{{ t('sessions.config.title') }}</div>
        <h1>{{ t('sessions.config.subtitle') }}</h1>
      </div>
      <span class="scope-badge">{{ t('sessions.config.platformScope') }}</span>
    </header>

    <div class="tab-nav" role="tablist" :aria-label="t('sessions.config.tabsLabel')">
      <button
        v-for="tab in tabs"
        :id="`session-config-tab-${tab.key}`"
        :key="tab.key"
        type="button"
        class="tab-button"
        :class="{ active: activeTab === tab.key }"
        role="tab"
        :aria-selected="activeTab === tab.key"
        :aria-controls="`session-config-panel-${tab.key}`"
        :tabindex="activeTab === tab.key ? 0 : -1"
        @click="setActive(tab.key)"
        @keydown="handleTabKey"
      >
        {{ tab.label }}
      </button>
    </div>

    <section
      :id="`session-config-panel-${activeTab}`"
      class="tab-content"
      role="tabpanel"
      :aria-labelledby="`session-config-tab-${activeTab}`"
    >
      <ApprovalConfigPanel v-if="activeTab === 'approval'" />
      <CompressionConfigPanel v-else-if="activeTab === 'compression'" />
      <PromptInjectionConfigPanel v-else-if="activeTab === 'promptInjection'" />
      <HealthScoreConfigPanel v-else-if="healthEnabled" />
    </section>
  </main>
</template>

<style scoped>
.session-config-view {
  width: min(100%, 1180px);
  margin: 0 auto;
  padding: 16px 20px 32px;
  color: var(--text);
}
.page-header {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 14px;
}
.eyebrow { color: var(--muted); font-size: 12px; letter-spacing: .04em; text-transform: uppercase; }
.page-header h1 { margin: 4px 0 0; font-size: 19px; font-weight: 650; color: var(--text); }
.scope-badge { border: 1px solid var(--border); border-radius: 999px; color: var(--muted); font-size: 11px; padding: 4px 9px; white-space: nowrap; }
.tab-nav { display: flex; gap: 4px; border-bottom: 1px solid var(--border); margin-bottom: 14px; overflow-x: auto; }
.tab-button { appearance: none; border: 0; border-bottom: 2px solid transparent; color: var(--muted); background: transparent; cursor: pointer; font: inherit; font-size: 13px; padding: 8px 12px; white-space: nowrap; }
.tab-button:hover { color: var(--text); background: rgba(99,102,241,.08); }
.tab-button.active { border-bottom-color: var(--accent); color: var(--text); }
.tab-button:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.tab-content { min-width: 0; }
@media (max-width: 640px) {
  .session-config-view { padding: 12px 12px 24px; }
  .page-header { align-items: start; flex-direction: column; gap: 8px; }
}
</style>