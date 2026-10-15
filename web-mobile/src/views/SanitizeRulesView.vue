<script setup lang="ts">
// 脱敏规则只读面（批144）。服务端逐操作鉴权，前端分区是体验兜底，不替代服务端。
// 注册与权限见 admin/sanitize_rules_handler.go；事件租户条件由服务端强制，不传tenant_id。
import { computed, onUnmounted, ref, watch } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSanitizeRulesCurrent, fetchSanitizeRuleEvents, fetchSanitizeRuleVersions, fetchSanitizeRuleInstances,
  sanitizeRulesCanManage, sanitizeRulesCanReadEvents,
  type SanitizeRuleCurrent, type SanitizeRuleEvent, type SanitizeRuleVersion, type SanitizeRuleInstance,
} from '@/api/sanitizeRules'

const auth = useAuthStore()
const manager = computed(() => sanitizeRulesCanManage(auth.role))
const allowed = computed(() => sanitizeRulesCanReadEvents(auth.role, auth.userInfo?.tenant_id ?? null))
useHyperPage({ title: () => (allowed.value ? t('sr.title') : t('common.pageDenied')) })
const current = ref<SanitizeRuleCurrent | null>(null)
const events = ref<SanitizeRuleEvent[] | null>(null)
const versions = ref<SanitizeRuleVersion[] | null>(null)
const instances = ref<SanitizeRuleInstance[] | null>(null)
const targetVersion = ref<number | null>(null)
const action = ref(''), status = ref(''), limit = ref(100)
type Section = 'events' | 'rules' | 'versions' | 'instances'
const loading = ref<Record<Section, boolean>>({ events: false, rules: false, versions: false, instances: false })
const errors = ref<Record<Section, string>>({ events: '', rules: '', versions: '', instances: '' })
const requests = new Map<Section, AbortController>()
let generation = 0, disposed = false
function clear() {
  generation++
  for (const r of requests.values()) r.abort()
  requests.clear()
  current.value = null; events.value = null; versions.value = null; instances.value = null; targetVersion.value = null
  loading.value = { events: false, rules: false, versions: false, instances: false }
  errors.value = { events: '', rules: '', versions: '', instances: '' }
}
watch(() => [auth.role, auth.userInfo?.id, auth.userInfo?.tenant_id], clear)
onUnmounted(() => { disposed = true; clear() })
async function load(section: Section) {
  if (!allowed.value || (section !== 'events' && !manager.value) || loading.value[section]) return
  const epoch = generation, request = new AbortController()
  requests.set(section, request); loading.value[section] = true; errors.value[section] = ''
  const live = () => !disposed && epoch === generation && !request.signal.aborted
  try {
    const options = { signal: request.signal }
    if (section === 'events') {
      const result = await fetchSanitizeRuleEvents({ action: action.value, status: status.value, limit: limit.value }, options)
      if (live()) events.value = result.events
    } else if (section === 'rules') {
      const result = await fetchSanitizeRulesCurrent(options)
      if (live()) current.value = result
    } else if (section === 'versions') {
      const result = await fetchSanitizeRuleVersions(options)
      if (live()) versions.value = result.versions
    } else {
      const result = await fetchSanitizeRuleInstances(options)
      if (live()) { instances.value = result.instances; targetVersion.value = result.target_version }
    }
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      errors.value[section] = message.startsWith('sanitizeRules.') ? t(message === 'sanitizeRules.invalidResponse' ? 'sr.invalidResponse' : 'sr.invalidFilter') : message || t('sr.requestFailed')
    }
  } finally {
    if (live()) { loading.value[section] = false; requests.delete(section) }
  }
}
</script>

<template>
  <div class="sr">
    <section v-if="!allowed" class="sr__card" data-testid="sr-nope">
      <h2>{{ t('common.pageDenied') }}</h2><p>{{ t('sr.denied') }}</p>
    </section>
    <template v-else>
      <p class="sr__note" data-testid="sr-readonly">{{ t('sr.readonly') }}</p>
      <section class="sr__card" data-testid="sr-events-section">
        <header><h2>{{ t('sr.events') }}</h2><button class="sr__btn" data-testid="sr-load" :disabled="loading.events" @click="load('events')">{{ loading.events ? t('common.loading') : t('sr.refresh') }}</button></header>
        <p data-testid="sr-scope">{{ manager ? t('sr.allTenants') : t('sr.ownTenant') }}</p>
        <div class="sr__filters">
          <label>{{ t('sr.action') }}<select v-model="action" data-testid="sr-action"><option value="">{{ t('sr.all') }}</option><option value="mask">mask</option><option value="block">block</option><option value="log">log</option><option value="exempted">exempted</option><option value="pending">pending</option></select></label>
          <label>{{ t('sr.status') }}<select v-model="status" data-testid="sr-status"><option value="">{{ t('sr.all') }}</option><option value="pending">pending</option><option value="confirmed_sensitive">confirmed_sensitive</option><option value="false_positive">false_positive</option><option value="ignored">ignored</option></select></label>
          <label>{{ t('sr.limit') }}<input v-model.number="limit" data-testid="sr-limit" type="number" min="1" max="1000" step="1" /></label>
        </div>
        <p v-if="errors.events" class="sr__error" role="alert" data-testid="sr-events-error">{{ errors.events }}</p>
        <p v-if="events === null">{{ t('sr.notLoaded') }}</p><p v-else-if="events.length === 0" data-testid="sr-events-empty">{{ t('sr.empty') }}</p>
        <article v-for="event in events ?? []" :key="event.id" class="sr__row" data-testid="sr-event">
          <strong>{{ event.rule_id }}</strong><span class="sr__mono">#{{ event.id }} · v{{ event.version }}</span>
          <dl><dt>{{ t('sr.tenant') }}</dt><dd>{{ event.tenant_id }}</dd><dt>{{ t('sr.action') }}</dt><dd>{{ event.phase }} / {{ event.action }}</dd><dt>{{ t('sr.status') }}</dt><dd>{{ event.status }}</dd><dt>{{ t('sr.count') }}</dt><dd>{{ event.count }}</dd><dt>{{ t('sr.sample') }}</dt><dd>{{ event.sample_masked }}</dd><dt>{{ t('sr.lastSeen') }}</dt><dd>{{ event.last_seen }}</dd></dl>
          <p v-if="event.type || event.field" class="sr__mono" data-testid="sr-event-structure">{{ event.type }} / {{ event.field }}</p>
          <p v-if="event.reviewer || event.review_comment">{{ event.reviewer }} · {{ event.review_comment }}</p>
        </article>
      </section>
      <template v-if="manager">
        <section class="sr__card" data-testid="sr-rules-section">
          <header><h2>{{ t('sr.rules') }}</h2><button class="sr__btn" data-testid="sr-rules-load" :disabled="loading.rules" @click="load('rules')">{{ loading.rules ? t('common.loading') : t('sr.refresh') }}</button></header>
          <p v-if="errors.rules" class="sr__error" role="alert" data-testid="sr-rules-error">{{ errors.rules }}</p>
          <p v-if="!current">{{ t('sr.notLoaded') }}</p>
          <template v-else><p class="sr__mono" data-testid="sr-current-version">v{{ current.version }} · {{ current.config_digest }}</p><p v-if="current.version === 0" data-testid="sr-fallback">{{ t('sr.fallback') }}</p>
            <article v-for="rule in current.config.rules" :key="rule.id" class="sr__row" data-testid="sr-rule"><strong>{{ rule.name }}</strong><p class="sr__mono">{{ rule.id }} · {{ rule.type }} / {{ rule.kind }} · {{ rule.action }}</p><p>{{ rule.enabled ? t('sr.enabled') : t('sr.disabled') }} · {{ t('sr.priority') }} {{ rule.priority }} · {{ rule.scopes.join(' / ') }}</p><pre>{{ rule.pattern }}</pre><details><summary>{{ t('sr.details') }}</summary><pre>{{ JSON.stringify(rule, null, 2) }}</pre></details></article>
            <details><summary>{{ t('sr.localInstance') }}</summary><pre>{{ JSON.stringify(current.status, null, 2) }}</pre></details>
          </template>
        </section>
        <section class="sr__card" data-testid="sr-versions-section">
          <header><h2>{{ t('sr.versions') }}</h2><button class="sr__btn" data-testid="sr-versions-load" :disabled="loading.versions" @click="load('versions')">{{ loading.versions ? t('common.loading') : t('sr.refresh') }}</button></header>
          <p v-if="errors.versions" class="sr__error" role="alert">{{ errors.versions }}</p><p v-if="versions === null">{{ t('sr.notLoaded') }}</p><p v-else-if="versions.length === 0">{{ t('sr.empty') }}</p>
          <article v-for="version in versions ?? []" :key="version.id" class="sr__row" data-testid="sr-version"><strong>v{{ version.id }} ← v{{ version.parent_id }}</strong><p>{{ version.actor }} · {{ version.created_at }}</p><p>{{ version.comment }}</p><p v-if="version.rollback_of">{{ t('sr.rollbackOf') }} v{{ version.rollback_of }}</p><details><summary>{{ t('sr.details') }}</summary><pre>{{ JSON.stringify(version, null, 2) }}</pre></details></article>
        </section>
        <section class="sr__card" data-testid="sr-instances-section">
          <header><h2>{{ t('sr.instances') }}</h2><button class="sr__btn" data-testid="sr-instances-load" :disabled="loading.instances" @click="load('instances')">{{ loading.instances ? t('common.loading') : t('sr.refresh') }}</button></header>
          <p v-if="errors.instances" class="sr__error" role="alert">{{ errors.instances }}</p><p v-if="instances === null">{{ t('sr.notLoaded') }}</p><template v-else><p data-testid="sr-target">{{ t('sr.target') }} v{{ targetVersion }}</p><p v-if="instances.length === 0">{{ t('sr.empty') }}</p><article v-for="instance in instances" :key="instance.instance_id" class="sr__row" data-testid="sr-instance"><strong>{{ instance.instance_id }}</strong><p>v{{ instance.version }} · {{ instance.version === targetVersion ? t('sr.sameVersion') : t('sr.versionDiffers') }}</p><p class="sr__mono">{{ instance.config_digest }}</p><p>{{ t('sr.lastSeen') }} {{ instance.last_seen }}</p><p>{{ t('sr.lastRefresh') }} {{ instance.last_refresh }}</p><p v-if="instance.error" class="sr__error">{{ instance.error }}</p></article></template>
          <p>{{ t('sr.instanceNote') }}</p>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.sr { display: grid; gap: 14px; padding: 16px; }
.sr__card { min-width: 0; border: 1px solid var(--app-border); border-radius: 12px; padding: 16px; background: var(--app-surface); }
.sr header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; }
.sr h2 { font-size: 17px; margin: 0; }
.sr p { overflow-wrap: anywhere; font-size: 13px; line-height: 1.6; }
.sr__btn, .sr select, .sr input { min-height: 48px; border: 1px solid var(--app-border); border-radius: 8px; background: var(--app-surface); color: var(--app-text); padding: 8px 12px; font: inherit; }
.sr__btn { color: var(--app-primary); }
.sr__btn:disabled { opacity: .5; }
.sr__btn:focus-visible, .sr select:focus-visible, .sr input:focus-visible, .sr summary:focus-visible { outline: 2px solid var(--app-primary); outline-offset: 2px; }
.sr__filters { display: grid; gap: 10px; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); }
.sr label { display: grid; gap: 6px; font-size: 13px; min-width: 0; }
.sr input, .sr select { width: 100%; box-sizing: border-box; min-width: 0; }
.sr__row { border-top: 1px solid var(--app-border); padding-top: 14px; margin-top: 14px; overflow-wrap: anywhere; }
.sr__mono, .sr pre { font-family: ui-monospace, monospace; font-size: 12px; }
.sr pre { white-space: pre-wrap; overflow-wrap: anywhere; }
.sr dl { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 6px; font-size: 13px; }
.sr dd { margin: 0; overflow-wrap: anywhere; }
.sr dt, .sr__note { color: var(--app-text-secondary); }
.sr__error { color: var(--app-danger); }
.sr summary { min-height: 48px; display: flex; align-items: center; cursor: pointer; color: var(--app-primary); }
</style>
