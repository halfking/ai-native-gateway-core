<script setup lang="ts">
// 脱敏规则四GET、事件复核与草稿验证（批144–146）。服务端逐操作鉴权，前端分区是体验兜底，不替代服务端。
// 注册与权限见 admin/sanitize_rules_handler.go；事件租户条件由服务端强制，不传tenant_id。
import { computed, onUnmounted, ref, watch } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import { fetchSettingsList, updateSanitizeRuleSettings } from '@/api/platformSettings'
import {
  fetchSanitizeRulesCurrent, fetchSanitizeRuleEvents, fetchSanitizeRuleVersions, fetchSanitizeRuleInstances,
  sanitizeRulesCanManage, sanitizeRulesCanReadEvents, reviewSanitizeRuleEvent, validateSanitizeRules, publishSanitizeRules, rollbackSanitizeRules,
  type SanitizeRuleCurrent, type SanitizeRuleEvent, type SanitizeRuleVersion, type SanitizeRuleInstance, type SanitizeRuleConfig,
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
const draft = ref(''), draftError = ref(''), validatedDraft = ref(''), validatedDigest = ref(''), validating = ref(false)
let draftRevision = 0, validateRequest: AbortController | null = null
const draftValid = computed(() => validatedDigest.value !== '' && validatedDraft.value === draft.value)
function invalidateDraft() {
  draftRevision++; validateRequest?.abort(); validateRequest = null
  validating.value = false; validatedDraft.value = ''; validatedDigest.value = ''; draftError.value = ''
}
watch(draft, invalidateDraft, { flush: 'sync' })
function copyCurrent() {
  if (!manager.value || !current.value || draft.value !== '') return
  invalidateDraft(); draft.value = JSON.stringify(current.value.config, null, 2)
}
function clearDraft() { invalidateDraft(); draft.value = '' }
async function validateDraft() {
  if (!manager.value || validating.value || draft.value === '') return
  const text = draft.value, epoch = generation, revision = draftRevision
  let config: unknown
  try { config = JSON.parse(text) } catch { draftError.value = t('sr.invalidDraft'); return }
  const request = new AbortController(); validateRequest = request; validating.value = true; draftError.value = ''
  validatedDraft.value = ''; validatedDigest.value = ''
  const live = () => !disposed && epoch === generation && revision === draftRevision && draft.value === text && !request.signal.aborted
  try {
    const result = await validateSanitizeRules(config as SanitizeRuleConfig, { signal: request.signal })
    if (live()) { validatedDraft.value = text; validatedDigest.value = result.config_digest }
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      draftError.value = message === 'sanitizeRules.invalidDraft' ? t('sr.invalidDraft') : message === 'sanitizeRules.invalidResponse' ? t('sr.invalidResponse') : message || t('sr.requestFailed')
    }
  } finally { if (live()) { validating.value = false; validateRequest = null } }
}

const publishComment = ref(''), publishConfirm = ref(false), publishing = ref(false), publishError = ref(''), publishSuccess = ref('')
let publishRequest: AbortController | null = null
const rollbackTargetID = ref<number | null>(null), rollbackComment = ref(''), rollbackConfirm = ref(false), rollingBack = ref(false), rollbackError = ref(''), rollbackSuccess = ref('')
let rollbackRequest: AbortController | null = null
function resetPublish() { publishRequest?.abort(); publishRequest = null; publishComment.value = ''; publishConfirm.value = false; publishing.value = false; publishError.value = ''; publishSuccess.value = '' }
function resetRollback() { rollbackRequest?.abort(); rollbackRequest = null; rollbackTargetID.value = null; rollbackComment.value = ''; rollbackConfirm.value = false; rollingBack.value = false; rollbackError.value = ''; rollbackSuccess.value = '' }
function openRollback(id: number) {
  if (!manager.value || rollingBack.value || publishing.value || !versions.value?.some(v => v.id === id)) return
  resetRollback(); rollbackTargetID.value = id
}
function cancelRollback() { resetRollback() }
function preparePublish() {
  if (!manager.value || !current.value || publishing.value || rollingBack.value || !draftValid.value) return
  publishError.value = ''
  if (new TextEncoder().encode(publishComment.value.trim()).length === 0 || new TextEncoder().encode(publishComment.value).length > 2000) {
    publishError.value = t('sr.invalidPublishComment'); return
  }
  publishConfirm.value = true
}
function prepareRollback() {
  if (!manager.value || rollingBack.value || publishing.value || rollbackTargetID.value === null || !current.value) return
  rollbackError.value = ''
  if (new TextEncoder().encode(rollbackComment.value.trim()).length === 0 || new TextEncoder().encode(rollbackComment.value).length > 2000) {
    rollbackError.value = t('sr.invalidPublishComment'); return
  }
  rollbackConfirm.value = true
}
async function submitPublish() {
  if (!manager.value || !publishConfirm.value || publishing.value || !current.value) return
  const epoch = generation, request = new AbortController(); publishRequest = request; publishing.value = true; publishError.value = ''; publishSuccess.value = ''
  const text = draft.value, digest = validatedDigest.value
  const live = () => !disposed && epoch === generation && draft.value === text && validatedDigest.value === digest && !request.signal.aborted
  try {
    let config: SanitizeRuleConfig
    try { config = JSON.parse(text) } catch { if (live()) publishError.value = t('sr.invalidDraft'); return }
    const result = await publishSanitizeRules({ base_version: current.value.version, config, comment: publishComment.value }, { signal: request.signal })
    if (live()) {
      current.value = { version: result.id, config: result.config, config_digest: digest, status: current.value.status }
      resetPublish(); clearDraft(); publishSuccess.value = t('sr.publishSaved')
      void load('versions'); void load('instances'); void load('events')
    }
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      publishError.value = message === 'sanitizeRules.invalidPublish' ? t('sr.invalidPublish') : message === 'sanitizeRules.invalidResponse' ? t('sr.invalidResponse') : message || t('sr.requestFailed')
      publishConfirm.value = false
    }
  } finally { if (live()) { publishing.value = false; publishRequest = null } }
}
async function submitRollback() {
  if (!manager.value || !rollbackConfirm.value || rollingBack.value || rollbackTargetID.value === null || !current.value) return
  const epoch = generation, request = new AbortController(); rollbackRequest = request; rollingBack.value = true; rollbackError.value = ''; rollbackSuccess.value = ''
  const id = rollbackTargetID.value, base = current.value.version
  const live = () => !disposed && epoch === generation && current.value?.version === base && !request.signal.aborted
  try {
    const result = await rollbackSanitizeRules({ base_version: base, target_version: id, comment: rollbackComment.value }, { signal: request.signal })
    if (live()) {
      current.value = { version: result.id, config: result.config, config_digest: result.config_digest ?? current.value.config_digest, status: current.value.status }
      rollbackComment.value = ''; rollbackConfirm.value = false
      rollbackRequest?.abort(); rollbackRequest = null
      rollingBack.value = false
      rollbackTargetID.value = null
      rollbackSuccess.value = t('sr.rollbackSaved')
      void load('versions'); void load('instances'); void load('events')
    }
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      rollbackError.value = message === 'sanitizeRules.invalidPublish' ? t('sr.invalidPublish') : message === 'sanitizeRules.invalidResponse' ? t('sr.invalidResponse') : message || t('sr.requestFailed')
      rollbackConfirm.value = false
    }
  } finally { if (live()) { rollingBack.value = false; rollbackRequest = null } }
}
const reviewID = ref<number | null>(null), reviewStatus = ref('confirmed_sensitive'), reviewComment = ref('')
const reviewing = ref(false), reviewConfirm = ref(false), reviewError = ref(''), reviewSuccess = ref('')
let reviewRequest: AbortController | null = null
function resetReview() {
  reviewRequest?.abort(); reviewRequest = null
  reviewID.value = null; reviewStatus.value = 'confirmed_sensitive'; reviewComment.value = ''
  reviewing.value = false; reviewConfirm.value = false; reviewError.value = ''; reviewSuccess.value = ''
}
function beginReview(id: number) {
  if (!allowed.value || reviewing.value || loading.value.events || !events.value?.some(e => e.id === id)) return
  resetReview(); reviewID.value = id
}
function prepareReview() {
  if (!allowed.value || reviewID.value === null || reviewing.value) return
  reviewError.value = ''
  if (!['confirmed_sensitive', 'false_positive', 'ignored'].includes(reviewStatus.value) || new TextEncoder().encode(reviewComment.value).length > 2000) {
    reviewError.value = t('sr.invalidReview'); return
  }
  reviewConfirm.value = true
}
async function submitReview() {
  if (!allowed.value || !reviewConfirm.value || reviewID.value === null || reviewing.value) return
  const id = reviewID.value, epoch = generation, request = new AbortController()
  reviewRequest = request; reviewing.value = true; reviewError.value = ''; reviewSuccess.value = ''
  const live = () => !disposed && epoch === generation && !request.signal.aborted
  try {
    const result = await reviewSanitizeRuleEvent(id, { status: reviewStatus.value as 'confirmed_sensitive' | 'false_positive' | 'ignored', comment: reviewComment.value }, { signal: request.signal })
    if (live()) {
      events.value = (events.value ?? []).map(e => e.id === id ? result : e)
      reviewID.value = null; reviewComment.value = ''; reviewConfirm.value = false; reviewSuccess.value = t('sr.reviewSaved')
    }
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      reviewError.value = message === 'sanitizeRules.invalidReview' ? t('sr.invalidReview') : message === 'sanitizeRules.invalidResponse' ? t('sr.invalidResponse') : message || t('sr.requestFailed')
      reviewConfirm.value = false
    }
  } finally {
    if (live()) { reviewing.value = false; reviewRequest = null }
  }
}

// ── 脱敏规则运行参数（sanitize.rules.refresh_interval_seconds / audit_enabled / event_limit）
// 与桌面端 settings.ts 的 updateSetting 同源；前端的并发三 PUT 与写后刷新是体验层路径，
// 服务端 settingsPut 仍按 dangerLevel 在 PUT 端点逐项校验（见 admin/settings.go:225-289）。
interface SanitizeRuleParam { key: 'refresh_interval_seconds' | 'audit_enabled' | 'event_limit'; type: 'int' | 'bool'; min: number; max: number; value: number | boolean | null }
const paramDefs: { key: SanitizeRuleParam['key']; label: string; type: 'int' | 'bool'; min: number; max: number; unit: string }[] = [
  { key: 'refresh_interval_seconds', label: '刷新间隔', type: 'int', min: 1, max: 300, unit: '秒' },
  { key: 'audit_enabled', label: '审计启用', type: 'bool', min: 0, max: 0, unit: '' },
  { key: 'event_limit', label: '事件保留量', type: 'int', min: 100, max: 100000, unit: '条' },
]
const paramFullKey = (key: string) => `sanitize.rules.${key}`
const paramsList = ref<SanitizeRuleParam[] | null>(null)
const refreshDraft = ref<number | null>(null)
const auditDraft = ref<boolean | null>(null)
const limitDraft = ref<number | null>(null)
const loadingParams = ref(false), savingParams = ref(false)
const paramsError = ref(''), paramsSuccess = ref('')
let paramsRequest: AbortController | null = null
const paramsDirty = computed(() => {
  if (!paramsList.value) return false
  for (const def of paramDefs) {
    const current = currentParamValue(def.key)
    if (def.key === 'refresh_interval_seconds' && typeof current === 'number' && refreshDraft.value !== null && refreshDraft.value !== current) return true
    if (def.key === 'audit_enabled' && typeof current === 'boolean' && auditDraft.value !== null && auditDraft.value !== current) return true
    if (def.key === 'event_limit' && typeof current === 'number' && limitDraft.value !== null && limitDraft.value !== current) return true
  }
  return false
})
function currentParamValue(key: SanitizeRuleParam['key']): number | boolean | null {
  return paramsList.value?.find(p => p.key === key)?.value ?? null
}
function paramInputValue(key: SanitizeRuleParam['key']): number | boolean | '' {
  if (key === 'refresh_interval_seconds') return refreshDraft.value ?? currentParamValue(key) ?? ''
  if (key === 'audit_enabled') return auditDraft.value ?? currentParamValue(key) ?? ''
  if (key === 'event_limit') return limitDraft.value ?? currentParamValue(key) ?? ''
  return ''
}
function setParamDraft(key: SanitizeRuleParam['key'], value: number | boolean) {
  if (key === 'refresh_interval_seconds') refreshDraft.value = value as number
  else if (key === 'audit_enabled') auditDraft.value = value as boolean
  else if (key === 'event_limit') limitDraft.value = value as number
}
function resetParams() {
  paramsRequest?.abort(); paramsRequest = null
  paramsList.value = null; refreshDraft.value = null; auditDraft.value = null; limitDraft.value = null
  loadingParams.value = false; savingParams.value = false
  paramsError.value = ''; paramsSuccess.value = ''
}
async function loadParams() {
  if (!manager.value || loadingParams.value || savingParams.value) return
  paramsError.value = ''; paramsSuccess.value = ''
  const epoch = generation, request = new AbortController(); paramsRequest = request; loadingParams.value = true
  const live = () => !disposed && epoch === generation && !request.signal.aborted
  try {
    const resp = await fetchSettingsList({ category: 'modules' }, { signal: request.signal })
    if (!live()) return
    const list: SanitizeRuleParam[] = []
    for (const item of resp.items) {
      const def = paramDefs.find(d => paramFullKey(d.key) === item.key)
      if (!def) continue
      if (def.type === 'int' && typeof item.value === 'number' && Number.isFinite(item.value)) {
        list.push({ key: def.key, type: def.type, min: def.min, max: def.max, value: item.value })
      } else if (def.type === 'bool' && typeof item.value === 'boolean') {
        list.push({ key: def.key, type: def.type, min: 0, max: 0, value: item.value })
      }
    }
    paramsList.value = list
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      paramsError.value = message === 'settings.invalidResponse' ? t('sr.invalidResponse') : message || t('sr.requestFailed')
    }
  } finally { if (live()) { loadingParams.value = false; paramsRequest = null } }
}
function revertParams() {
  refreshDraft.value = null; auditDraft.value = null; limitDraft.value = null
  paramsError.value = ''; paramsSuccess.value = ''
}
async function saveParams() {
  if (!manager.value || savingParams.value || !paramsList.value) return
  paramsError.value = ''; paramsSuccess.value = ''
  const updates: { refresh_interval_seconds?: number; audit_enabled?: boolean; event_limit?: number } = {}
  for (const def of paramDefs) {
    if (def.key === 'refresh_interval_seconds' && refreshDraft.value !== null) {
      const v = refreshDraft.value
      if (!Number.isInteger(v) || v < def.min || v > def.max) { paramsError.value = t('sr.paramsInvalid'); return }
      updates.refresh_interval_seconds = v
    } else if (def.key === 'audit_enabled' && auditDraft.value !== null) {
      updates.audit_enabled = auditDraft.value
    } else if (def.key === 'event_limit' && limitDraft.value !== null) {
      const v = limitDraft.value
      if (!Number.isInteger(v) || v < def.min || v > def.max) { paramsError.value = t('sr.paramsInvalid'); return }
      updates.event_limit = v
    }
  }
  if (Object.keys(updates).length === 0) return
  const epoch = generation, request = new AbortController(); paramsRequest = request; savingParams.value = true
  const live = () => !disposed && epoch === generation && !request.signal.aborted
  try {
    await updateSanitizeRuleSettings(updates, { signal: request.signal })
    if (live()) {
      paramsSuccess.value = t('sr.paramsSaved')
      // ★ 必须先松开 savingParams——否则下面的 loadParams 在自己的入队守门里看到 savingParams=true 直接 early-return，不会发起第二次 GET（与「成功→自动重读」的 UI 契约脱钩，对应 SR-1 仓库里该分支 4 条用例的隐性回归面）
      savingParams.value = false
      await loadParams()
      revertParams()
    }
  } catch (e) {
    if (live()) {
      const message = e instanceof Error ? e.message : ''
      paramsError.value = message === 'settings.invalidResponse' ? t('sr.invalidResponse') : message || t('sr.requestFailed')
    }
  } finally { if (live()) { savingParams.value = false; paramsRequest = null } }
}
function clear() {
  resetPublish()
  resetRollback()
  clearDraft()
  resetReview()
  resetParams()
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
  if (!allowed.value || (section !== 'events' && !manager.value) || loading.value[section] || (section === 'events' && reviewing.value) || publishing.value || rollingBack.value) return
  if (section === 'events') resetReview()
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
      <p class="sr__note" data-testid="sr-readonly">{{ t(manager ? 'sr.readonlyManager' : 'sr.readonly') }}</p>
      <section class="sr__card" data-testid="sr-events-section">
        <header><h2>{{ t('sr.events') }}</h2><button class="sr__btn" data-testid="sr-load" :disabled="loading.events || reviewing" @click="load('events')">{{ loading.events ? t('common.loading') : t('sr.refresh') }}</button></header>
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
          <button class="sr__btn" data-testid="sr-review-open" :disabled="reviewing || loading.events" @click="beginReview(event.id)">{{ t('sr.review') }}</button>
          <div v-if="reviewID === event.id" data-testid="sr-review-form" class="sr__filters">
            <p>{{ t('sr.reviewNote') }}</p>
            <label>{{ t('sr.status') }}<select v-model="reviewStatus" data-testid="sr-review-status" :disabled="reviewConfirm || reviewing"><option value="confirmed_sensitive">{{ t('sr.confirmedSensitive') }}</option><option value="false_positive">{{ t('sr.falsePositive') }}</option><option value="ignored">{{ t('sr.ignored') }}</option></select></label>
            <label>{{ t('sr.reviewComment') }}<textarea v-model="reviewComment" data-testid="sr-review-comment" :disabled="reviewConfirm || reviewing" /></label>
            <p v-if="reviewError" class="sr__error" role="alert" data-testid="sr-review-error">{{ reviewError }}</p>
            <template v-if="reviewConfirm"><p data-testid="sr-review-confirm-note">{{ t('sr.reviewConfirm') }} #{{ event.id }} · {{ reviewStatus }}</p><button class="sr__btn" data-testid="sr-review-submit" :disabled="reviewing" @click="submitReview">{{ reviewing ? t('common.loading') : t('common.confirm') }}</button><button class="sr__btn" data-testid="sr-review-back" :disabled="reviewing" @click="reviewConfirm = false">{{ t('common.cancel') }}</button></template>
            <template v-else><button class="sr__btn" data-testid="sr-review-prepare" @click="prepareReview">{{ t('sr.review') }}</button><button class="sr__btn" data-testid="sr-review-cancel" @click="resetReview">{{ t('common.cancel') }}</button></template>
          </div>
        </article>
        <p v-if="reviewSuccess" role="status" data-testid="sr-review-success">{{ reviewSuccess }}</p>
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
        <section class="sr__card" data-testid="sr-draft-section">
          <h2>{{ t('sr.draftTitle') }}</h2><p>{{ t('sr.draftNote') }}</p>
          <button class="sr__btn" data-testid="sr-draft-copy" :disabled="!current || draft !== ''" @click="copyCurrent">{{ t('sr.copyCurrent') }}</button>
          <label>{{ t('sr.draftJson') }}<textarea v-model="draft" data-testid="sr-draft-input" class="sr__draft" spellcheck="false" /></label>
          <button class="sr__btn" data-testid="sr-draft-validate" :disabled="validating || draft === ''" @click="validateDraft">{{ validating ? t('common.loading') : t('sr.validateDraft') }}</button>
          <button class="sr__btn" data-testid="sr-draft-clear" @click="clearDraft">{{ t('sr.clearDraft') }}</button>
          <p v-if="draftError" class="sr__error" role="alert" data-testid="sr-draft-error">{{ draftError }}</p>
          <p v-if="draftValid" role="status" data-testid="sr-draft-valid">{{ t('sr.draftValidated') }} <span class="sr__mono">{{ validatedDigest }}</span></p>
          <div data-testid="sr-publish-form">
            <label>{{ t('sr.publishComment') }}<textarea v-model="publishComment" data-testid="sr-publish-comment" :disabled="publishConfirm || publishing" /></label>
            <p v-if="publishError" class="sr__error" role="alert" data-testid="sr-publish-error">{{ publishError }}</p>
            <p v-if="publishSuccess" role="status" data-testid="sr-publish-success">{{ publishSuccess }}</p>
            <template v-if="publishConfirm"><p data-testid="sr-publish-confirm-note">{{ t('sr.publishConfirmNote') }} {{ current?.version ?? '?' }} → {{ publishComment }}</p><button class="sr__btn" data-testid="sr-publish-submit" :disabled="publishing" @click="submitPublish">{{ publishing ? t('common.loading') : t('common.confirm') }}</button><button class="sr__btn" data-testid="sr-publish-back" :disabled="publishing" @click="publishConfirm = false">{{ t('common.cancel') }}</button></template>
            <template v-else><button class="sr__btn" data-testid="sr-publish-prepare" :disabled="!draftValid || publishing" @click="preparePublish">{{ t('sr.publishDraft') }}</button></template>
          </div>
        </section>
        <section class="sr__card" data-testid="sr-versions-section">
          <header><h2>{{ t('sr.versions') }}</h2><button class="sr__btn" data-testid="sr-versions-load" :disabled="loading.versions" @click="load('versions')">{{ loading.versions ? t('common.loading') : t('sr.refresh') }}</button></header>
          <p v-if="errors.versions" class="sr__error" role="alert">{{ errors.versions }}</p><p v-if="versions === null">{{ t('sr.notLoaded') }}</p><p v-else-if="versions.length === 0">{{ t('sr.empty') }}</p>
          <article v-for="version in versions ?? []" :key="version.id" class="sr__row" data-testid="sr-version"><strong>v{{ version.id }} ← v{{ version.parent_id }}</strong><p>{{ version.actor }} · {{ version.created_at }}</p><p>{{ version.comment }}</p><p v-if="version.rollback_of">{{ t('sr.rollbackOf') }} v{{ version.rollback_of }}</p><button class="sr__btn" data-testid="sr-rollback-open" :disabled="rollingBack || publishing" @click="openRollback(version.id)">{{ t('sr.rollback') }}</button>
            <div v-if="rollbackTargetID === version.id" data-testid="sr-rollback-form">
              <label>{{ t('sr.rollbackComment') }}<textarea v-model="rollbackComment" data-testid="sr-rollback-comment" :disabled="rollbackConfirm || rollingBack" /></label>
              <p v-if="rollbackError" class="sr__error" role="alert" data-testid="sr-rollback-error">{{ rollbackError }}</p>
              <template v-if="rollbackConfirm"><p data-testid="sr-rollback-confirm-note">{{ t('sr.rollbackConfirmNote') }} v{{ version.id }} → v{{ current?.version ?? '?' }} · {{ rollbackComment }}</p><button class="sr__btn" data-testid="sr-rollback-submit" :disabled="rollingBack" @click="submitRollback">{{ rollingBack ? t('common.loading') : t('common.confirm') }}</button><button class="sr__btn" data-testid="sr-rollback-back" :disabled="rollingBack" @click="rollbackConfirm = false">{{ t('common.cancel') }}</button></template>
              <template v-else><button class="sr__btn" data-testid="sr-rollback-prepare" :disabled="rollingBack" @click="prepareRollback">{{ t('sr.rollback') }}</button><button class="sr__btn" data-testid="sr-rollback-cancel" :disabled="rollingBack" @click="cancelRollback">{{ t('common.cancel') }}</button></template>
            </div>
          </article>
          <p v-if="rollbackSuccess" role="status" data-testid="sr-rollback-success">{{ rollbackSuccess }}</p>
        </section>
        <section class="sr__card" data-testid="sr-instances-section">
          <header><h2>{{ t('sr.instances') }}</h2><button class="sr__btn" data-testid="sr-instances-load" :disabled="loading.instances" @click="load('instances')">{{ loading.instances ? t('common.loading') : t('sr.refresh') }}</button></header>
          <p v-if="errors.instances" class="sr__error" role="alert">{{ errors.instances }}</p><p v-if="instances === null">{{ t('sr.notLoaded') }}</p><template v-else><p data-testid="sr-target">{{ t('sr.target') }} v{{ targetVersion }}</p><p v-if="instances.length === 0">{{ t('sr.empty') }}</p><article v-for="instance in instances" :key="instance.instance_id" class="sr__row" data-testid="sr-instance"><strong>{{ instance.instance_id }}</strong><p>v{{ instance.version }} · {{ instance.version === targetVersion ? t('sr.sameVersion') : t('sr.versionDiffers') }}</p><p class="sr__mono">{{ instance.config_digest }}</p><p>{{ t('sr.lastSeen') }} {{ instance.last_seen }}</p><p>{{ t('sr.lastRefresh') }} {{ instance.last_refresh }}</p><p v-if="instance.error" class="sr__error">{{ instance.error }}</p></article></template>
          <p>{{ t('sr.instanceNote') }}</p>
        </section>
        <section class="sr__card" data-testid="sr-params-section">
          <header><h2>{{ t('sr.paramsTitle') }}</h2><button class="sr__btn" data-testid="sr-params-load" :disabled="loadingParams || savingParams" @click="loadParams">{{ loadingParams ? t('common.loading') : t('sr.refresh') }}</button></header>
          <p>{{ t('sr.paramsNote') }}</p>
          <p v-if="paramsError" class="sr__error" role="alert" data-testid="sr-params-error">{{ paramsError }}</p>
          <p v-if="paramsSuccess" role="status" data-testid="sr-params-success">{{ paramsSuccess }}</p>
          <p v-if="!paramsList">{{ t('sr.notLoaded') }}</p>
          <article v-else class="sr__row" data-testid="sr-params">
            <div v-for="def in paramDefs" :key="def.key" data-testid="sr-params-row">
              <strong>{{ def.label }}</strong><p class="sr__mono">{{ paramFullKey(def.key) }}</p>
              <template v-if="def.type === 'int'">
                <p class="sr__mono">{{ t('sr.paramsCurrent') }} {{ currentParamValue(def.key) }}</p>
                <input type="number" :value="paramInputValue(def.key)" @input="setParamDraft(def.key, Number(($event.target as HTMLInputElement).value))" :min="def.min" :max="def.max" :disabled="savingParams || loadingParams" :data-testid="`sr-params-input-${def.key}`" />
              </template>
              <template v-else>
                <p class="sr__mono">{{ t('sr.paramsCurrent') }} {{ String(currentParamValue(def.key)) }}</p>
                <select :value="String(paramInputValue(def.key))" @change="setParamDraft(def.key, ($event.target as HTMLSelectElement).value === 'true')" :disabled="savingParams || loadingParams" :data-testid="`sr-params-input-${def.key}`"><option value="true">true</option><option value="false">false</option></select>
              </template>
            </div>
            <button class="sr__btn" data-testid="sr-params-save" :disabled="!paramsDirty || savingParams || loadingParams" @click="saveParams">{{ savingParams ? t('common.loading') : t('sr.paramsSave') }}</button>
            <button class="sr__btn" data-testid="sr-params-revert" :disabled="!paramsDirty || savingParams || loadingParams" @click="revertParams">{{ t('common.cancel') }}</button>
          </article>
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
.sr__btn, .sr select, .sr input, .sr textarea { min-height: 48px; border: 1px solid var(--app-border); border-radius: 8px; background: var(--app-surface); color: var(--app-text); padding: 8px 12px; font: inherit; }
.sr__btn { color: var(--app-primary); }
.sr__btn:disabled { opacity: .5; }
.sr__btn:focus-visible, .sr select:focus-visible, .sr input:focus-visible, .sr summary:focus-visible { outline: 2px solid var(--app-primary); outline-offset: 2px; }
.sr__filters { display: grid; gap: 10px; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); }
.sr label { display: grid; gap: 6px; font-size: 13px; min-width: 0; }
.sr input, .sr select, .sr textarea { width: 100%; box-sizing: border-box; min-width: 0; }
.sr textarea { min-height: 96px; resize: vertical; }
.sr textarea.sr__draft { min-height: 240px; font-family: ui-monospace, monospace; }
.sr__row { border-top: 1px solid var(--app-border); padding-top: 14px; margin-top: 14px; overflow-wrap: anywhere; }
.sr__mono, .sr pre { font-family: ui-monospace, monospace; font-size: 12px; }
.sr pre { white-space: pre-wrap; overflow-wrap: anywhere; }
.sr dl { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 6px; font-size: 13px; }
.sr dd { margin: 0; overflow-wrap: anywhere; }
.sr dt, .sr__note { color: var(--app-text-secondary); }
.sr__error { color: var(--app-danger); }
.sr summary { min-height: 48px; display: flex; align-items: center; cursor: pointer; color: var(--app-primary); }
</style>
