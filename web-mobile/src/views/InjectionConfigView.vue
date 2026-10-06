<script setup lang="ts">
// InjectionConfigView — 提示词注入「配置面」（rules + engines + severity-matrix + canary-tokens，**admin 档**）。
//
// 它答的是「**现在用什么规则判、派到哪个模型、命中各级分别怎么处理、布了什么蜜罐**」。
// 现象面（有没有被命中）��� /injection。
//
// ⚠️★★★ 七个后端语义（详见 api/promptInjection.ts 文件头）：
//
// (1) ★★★★ rules / engines / canary-tokens **完全没有分页**：SQL 里没有 LIMIT/OFFSET，
//     响应的 `count` 是**本次返回的条数**，不是总数。
//     ⇒ 本页**不提供翻页控件**，也不把 count 说成总数。
// (2) ★★★★ `enabled` 的真值判定是 `== "true"` ⇒ 只发 true/false 字面量。
// (3) ★★★ rules 的 `category` 过滤是**两列 OR**（`category` 或 `category_new`）
//     ⇒ 新旧两套分类字段都要显示。
// (4) ★★★ rules 的 `search` 是 `ILIKE '%q%'` ⇒ 子串 + 不分大小写，匹配名称与描述。
// (5) ★★★ `is_system` 是 `COALESCE(is_system, true)` ⇒ **缺值按 true 算**。
// (6) ★★★ `severity-matrix` 的 `notify_channels` 由 `jsoncol.Decode` 解析，
//     而**它的返回值被丢弃** ⇒ 内容不是合法 JSON 时静默变成空数组。
//     客户端**无法**区分「没配通知渠道」与「这一列解析失败」⇒ 照实显示。
// (7) ★★ `action_override` 是空串 ⇒ 表示「沿用处置矩阵」，不是「无动作」。
//     ★ `engines.model_name` 来自 LEFT JOIN，未关联时是**空串**（不是 null）。

import { onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchInjectionRules,
  fetchInjectionEngines,
  fetchSeverityMatrix,
  fetchCanaryTokens,
  INJECTION_ACTIONS,
  INJECTION_SEVERITY_LEVELS,
  type InjectionRule,
  type InjectionEngine,
  type SeverityAction,
  type CanaryToken,
} from '@/api/promptInjection'

useHyperPage({ title: () => t('injectionConfig.title') })

const search = ref('')
const enabledFilter = ref<'' | 'true' | 'false'>('')

const rules = ref<InjectionRule[]>([])
const engines = ref<InjectionEngine[]>([])
const matrix = ref<SeverityAction[]>([])
const tokens = ref<CanaryToken[]>([])

const rulesError = ref<string | null>(null)
const enginesError = ref<string | null>(null)
const matrixError = ref<string | null>(null)
const tokensError = ref<string | null>(null)
const loadingRules = ref(false)
const loadingEngines = ref(false)
const loadingMatrix = ref(false)
const loadingTokens = ref(false)

async function loadRules(): Promise<void> {
  loadingRules.value = true
  rulesError.value = null
  try {
    const r = await fetchInjectionRules({
      search: search.value.trim() || undefined,
      enabled: enabledFilter.value === '' ? undefined : enabledFilter.value === 'true',
    })
    rules.value = r.rules
  } catch (e) {
    // ★ 500 不许退化成「没有规则」—— 安全规则的缺失等于检测被静默关闭。
    rules.value = []
    rulesError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingRules.value = false
  }
}

async function loadEngines(): Promise<void> {
  loadingEngines.value = true
  enginesError.value = null
  try {
    const r = await fetchInjectionEngines()
    engines.value = r.engines
  } catch (e) {
    engines.value = []
    enginesError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingEngines.value = false
  }
}

async function loadMatrix(): Promise<void> {
  loadingMatrix.value = true
  matrixError.value = null
  try {
    const r = await fetchSeverityMatrix()
    matrix.value = r.matrix
  } catch (e) {
    matrix.value = []
    matrixError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingMatrix.value = false
  }
}

async function loadTokens(): Promise<void> {
  loadingTokens.value = true
  tokensError.value = null
  try {
    const r = await fetchCanaryTokens()
    tokens.value = r.tokens
  } catch (e) {
    tokens.value = []
    tokensError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingTokens.value = false
  }
}

function applyEnabled(v: '' | 'true' | 'false'): void {
  enabledFilter.value = enabledFilter.value === v ? '' : v
  void loadRules()
}

function submitSearch(): void {
  void loadRules()
}

function clearFilters(): void {
  search.value = ''
  enabledFilter.value = ''
  void loadRules()
}

void loadRules()
void loadEngines()
void loadMatrix()
void loadTokens()

function severityTone(n: number): 'danger' | 'warning' | 'muted' {
  if (n >= 8) return 'danger'
  if (n >= 5) return 'warning'
  return 'muted'
}

function actionTone(a: string): 'danger' | 'warning' | 'muted' {
  if (a === 'block' || a === 'terminate' || a === 'reject') return 'danger'
  if (a === 'replace' || a === 'redact' || a === 'remove' || a === 'quarantine') return 'warning'
  if (a === 'pass' || a === 'log') return 'muted'
  return 'muted'
}

function levelTone(level: string): 'danger' | 'warning' | 'muted' {
  if (level === 'critical') return 'danger'
  if (level === 'high') return 'warning'
  return 'muted'
}

onBeforeUnmount(() => {
  rules.value = []
  engines.value = []
  matrix.value = []
  tokens.value = []
  rulesError.value = null
  enginesError.value = null
  matrixError.value = null
  tokensError.value = null
})
</script>

<template>
  <div class="view-root ic">
    <!-- ══════ 规则 ══════ -->
    <section class="ic__panel">
      <span class="ic__panel-title">{{ t('injectionConfig.rules') }}</span>

      <!-- ★★★★ 没有分页 -->
      <p class="ic__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injectionConfig.noPaginationNote') }}</span>
      </p>
      <p class="ic__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injectionConfig.categoryOrNote') }}</span>
      </p>
      <p class="ic__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injectionConfig.isSystemNote') }}</span>
      </p>

      <div class="ic__chips" role="group" :aria-label="t('injectionConfig.enabledLabel')">
        <button
          v-for="v in (['', 'true', 'false'] as const)"
          :key="v || 'all'"
          type="button"
          class="ic__chip"
          :class="{ 'ic__chip--on': enabledFilter === v }"
          @click="applyEnabled(v)"
        >
          {{ v === '' ? t('injectionConfig.all') : v === 'true' ? t('injectionConfig.enabled') : t('injectionConfig.disabled') }}
        </button>
      </div>

      <form class="ic__form" @submit.prevent="submitSearch">
        <label class="ic__field">
          <span>{{ t('injectionConfig.search') }}</span>
          <input
            v-model="search"
            class="ic__input"
            :placeholder="t('injectionConfig.searchHint')"
            autocomplete="off"
            spellcheck="false"
          />
        </label>
        <div class="ic__row">
          <button type="submit" class="ic__btn ic__btn--go">{{ t('injectionConfig.query') }}</button>
          <button type="button" class="ic__btn" @click="clearFilters">{{ t('common.clearFilters') }}</button>
        </div>
      </form>

      <p v-if="loadingRules" class="ic__msg">{{ t('common.loading') }}</p>
      <p v-if="rulesError" class="ic__msg ic__msg--err">
        {{ rulesError }}
        <span class="ic__sub">{{ t('injectionConfig.scanFailHint') }}</span>
      </p>
      <p v-else-if="!rules.length" class="ic__msg">{{ t('injectionConfig.noRules') }}</p>

      <ul v-if="rules.length" class="ic__list">
        <li v-for="r in rules" :key="r.id" class="ic__item">
          <div class="ic__item-head">
            <StatusDot :tone="r.enabled ? severityTone(r.severity) : 'muted'" />
            <span class="ic__name">{{ r.rule_name }}</span>
            <span class="ic__sev">{{ t('injectionConfig.severity', { n: r.severity }) }}</span>
            <span v-if="!r.enabled" class="ic__off">{{ t('injectionConfig.disabled') }}</span>
            <span v-if="r.is_system" class="ic__sys">{{ t('injectionConfig.systemRule') }}</span>
          </div>
          <!-- ★★ 新旧两套分类都要显示（过滤是两列 OR） -->
          <div class="ic__tags">
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.categoryOld') }}</span>
              <span class="ic__kv-v">{{ r.category || '—' }}</span>
            </span>
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.categoryNew') }}</span>
              <span class="ic__kv-v">{{ r.category_new || '—' }}</span>
            </span>
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.type') }}</span>
              <span class="ic__kv-v">{{ r.rule_type || '—' }}</span>
            </span>
          </div>
          <p v-if="r.description" class="ic__meta">{{ r.description }}</p>
          <!-- ★★ 空 action_override = 沿用矩阵，不是「无动作」 -->
          <p class="ic__meta">
            {{ t('injectionConfig.actionOverride', { v: r.action_override || t('injectionConfig.followMatrix') }) }}
            <span v-if="r.case_sensitive"> · {{ t('injectionConfig.caseSensitive') }}</span>
          </p>
          <div v-if="r.tags.length" class="ic__tags">
            <span v-for="tag in r.tags" :key="tag" class="ic__tag">{{ tag }}</span>
          </div>
        </li>
      </ul>
    </section>

    <!-- ══════ 严重度处置矩阵 ══════ -->
    <section class="ic__panel">
      <span class="ic__panel-title">{{ t('injectionConfig.matrix') }}</span>

      <!-- ★★★ notify_channels 解析失败被吞 -->
      <p class="ic__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injectionConfig.notifyChannelsNote') }}</span>
      </p>
      <p class="ic__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injectionConfig.levelsNote', { levels: INJECTION_SEVERITY_LEVELS.join(' / ') }) }}</span>
      </p>

      <p v-if="loadingMatrix" class="ic__msg">{{ t('common.loading') }}</p>
      <p v-if="matrixError" class="ic__msg ic__msg--err">
        {{ matrixError }}
        <span class="ic__sub">{{ t('injectionConfig.scanFailHint') }}</span>
      </p>
      <p v-else-if="!matrix.length" class="ic__msg">{{ t('injectionConfig.noMatrix') }}</p>

      <ul v-if="matrix.length" class="ic__list">
        <li v-for="m in matrix" :key="m.id" class="ic__item">
          <div class="ic__item-head">
            <StatusDot :tone="levelTone(m.severity_level)" />
            <span class="ic__name">{{ t('injectionConfig.level_' + m.severity_level) }}</span>
          </div>
          <div class="ic__tags">
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.observeAction') }}</span>
              <span class="ic__kv-v ic__act" :class="`ic__act--${actionTone(m.observe_action)}`">{{ m.observe_action }}</span>
            </span>
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.enforceAction') }}</span>
              <span class="ic__kv-v ic__act" :class="`ic__act--${actionTone(m.enforce_action)}`">{{ m.enforce_action }}</span>
            </span>
          </div>
          <p class="ic__meta">
            {{ t('injectionConfig.approval', { v: m.require_approval ? t('injectionConfig.yes') : t('injectionConfig.no') }) }}
            <span v-if="m.require_approval"> · {{ t('injectionConfig.timeout', { n: m.approval_timeout_minutes }) }}</span>
          </p>
          <p class="ic__meta">
            {{ t('injectionConfig.notify', { v: m.notify_on_detect ? t('injectionConfig.yes') : t('injectionConfig.no') }) }}
            <span v-if="m.notify_channels.length">
              · {{ t('injectionConfig.channels', { list: m.notify_channels.join(' / ') }) }}
            </span>
            <span v-else> · {{ t('injectionConfig.noChannels') }}</span>
          </p>
          <p class="ic__meta">
            {{ t('injectionConfig.sessionHealth', { v: m.affect_session_health ? t('injectionConfig.yes') : t('injectionConfig.no') }) }}
            <span v-if="m.affect_session_health"> · {{ t('injectionConfig.penalty', { n: m.session_health_penalty }) }}</span>
          </p>
          <p v-if="m.terminate_on_repeat" class="ic__meta">
            {{ t('injectionConfig.terminateOnRepeat', { n: m.repeat_threshold }) }}
          </p>
        </li>
      </ul>

      <p class="ic__foot">{{ t('injectionConfig.actionsNote', { list: INJECTION_ACTIONS.join(' / ') }) }}</p>
    </section>

    <!-- ══════ LLM 引擎 ══════ -->
    <section class="ic__panel">
      <span class="ic__panel-title">{{ t('injectionConfig.engines') }}</span>

      <p v-if="loadingEngines" class="ic__msg">{{ t('common.loading') }}</p>
      <p v-if="enginesError" class="ic__msg ic__msg--err">
        {{ enginesError }}
        <span class="ic__sub">{{ t('injectionConfig.scanFailHint') }}</span>
      </p>
      <p v-else-if="!engines.length" class="ic__msg">{{ t('injectionConfig.noEngines') }}</p>

      <ul v-if="engines.length" class="ic__list">
        <li v-for="e of engines" :key="e.id" class="ic__item">
          <div class="ic__item-head">
            <StatusDot :tone="e.enabled ? 'success' : 'muted'" />
            <span class="ic__name">{{ e.engine_name }}</span>
            <span class="ic__prio">{{ t('injectionConfig.priority', { n: e.priority }) }}</span>
            <span v-if="!e.enabled" class="ic__off">{{ t('injectionConfig.disabled') }}</span>
          </div>
          <div class="ic__tags">
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.model') }}</span>
              <!-- ★ 未关联时是空串，不是 null -->
              <span class="ic__kv-v">{{ e.model_name || t('injectionConfig.modelUnlinked') }}</span>
            </span>
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.credential') }}</span>
              <span class="ic__kv-v">{{ e.credential_id === null ? t('injectionConfig.notSet') : e.credential_id }}</span>
            </span>
            <span class="ic__kv">
              <span class="ic__kv-l">{{ t('injectionConfig.temperature') }}</span>
              <span class="ic__kv-v">{{ e.temperature }}</span>
            </span>
          </div>
          <p class="ic__meta">
            {{ t('injectionConfig.engineMeta', { calls: e.total_calls, det: e.total_detections, err: e.error_count }) }}
          </p>
          <p v-if="e.last_called_at" class="ic__meta">
            {{ t('injectionConfig.lastCalled', { t: relativeTime(e.last_called_at) }) }}
          </p>
        </li>
      </ul>
    </section>

    <!-- ══════ 蜜罐 token ══════ -->
    <section class="ic__panel">
      <span class="ic__panel-title">{{ t('injectionConfig.canary') }}</span>

      <!-- ★★ 蜜罐值是诱饵凭据，不是用户凭据 -->
      <p class="ic__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injectionConfig.canaryNote') }}</span>
      </p>

      <p v-if="loadingTokens" class="ic__msg">{{ t('common.loading') }}</p>
      <p v-if="tokensError" class="ic__msg ic__msg--err">
        {{ tokensError }}
        <span class="ic__sub">{{ t('injectionConfig.scanFailHint') }}</span>
      </p>
      <p v-else-if="!tokens.length" class="ic__msg">{{ t('injectionConfig.noTokens') }}</p>

      <ul v-if="tokens.length" class="ic__list">
        <li v-for="tok of tokens" :key="tok.id" class="ic__item">
          <div class="ic__item-head">
            <StatusDot :tone="tok.active ? (tok.times_leaked > 0 ? 'danger' : 'success') : 'muted'" />
            <span class="ic__name">{{ tok.token_name || tok.token_type }}</span>
            <span class="ic__act" :class="`ic__act--${actionTone(tok.leak_action)}`">{{ tok.leak_action }}</span>
            <span v-if="!tok.active" class="ic__off">{{ t('injectionConfig.disabled') }}</span>
          </div>
          <p class="ic__meta ic__value">{{ tok.token_value }}</p>
          <p class="ic__meta">
            {{ t('injectionConfig.canaryMeta', { inj: tok.times_injected, leak: tok.times_leaked }) }}
            <span v-if="tok.expires_at"> · {{ t('injectionConfig.expiresAt', { t: relativeTime(tok.expires_at) }) }}</span>
          </p>
          <p v-if="tok.last_leaked_at" class="ic__warnline">
            {{ t('injectionConfig.lastLeakedAt', { t: relativeTime(tok.last_leaked_at) }) }}
          </p>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.ic {
  padding: var(--app-space-3);
}
.ic__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ic__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.ic__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ic__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ic__sub {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  opacity: 0.85;
}
.ic__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ic__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.ic__warnline {
  margin: 4px 0 0;
  font-size: 11px;
  font-weight: 700;
  color: var(--app-danger);
}
.ic__value {
  color: var(--app-text-secondary);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  word-break: break-all;
}
.ic__foot {
  margin: var(--app-space-2) 0 0;
  font-size: 10px;
  color: var(--app-text-muted);
  line-height: 1.5;
  word-break: break-word;
}
.ic__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: var(--app-space-2) 0;
}
.ic__field {
  display: block;
}
.ic__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ic__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ic__chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.ic__chip {
  min-height: 48px;
  min-width: 72px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ic__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.ic__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.ic__btn {
  min-height: 48px;
  min-width: 72px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ic__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.ic__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.ic__item {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.ic__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ic__name {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-all;
}
.ic__sev,
.ic__prio {
  font-size: 11px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
.ic__off {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 10px;
  line-height: 1.6;
}
.ic__sys {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-info-soft);
  color: var(--app-info);
  font-size: 10px;
  line-height: 1.6;
}
.ic__tags {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: 4px;
}
.ic__kv {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.ic__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ic__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}
.ic__tag {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 11px;
  font-weight: 600;
  word-break: break-all;
}
.ic__act {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  font-size: 11px;
  font-weight: 700;
}
.ic__act--danger {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ic__act--warning {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.ic__act--muted {
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
}
</style>