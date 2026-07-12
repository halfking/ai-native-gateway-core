<script setup lang="ts">
// PromptInjectionConfigPanel.vue
//
// 紧凑的「策略 + 47 条规则」配置面板,嵌入到两个入口:
//   1. /admin/session-config 的「提示词注入」标签页
//   2. /admin/modules 的「提示词注入检测」模块的「配置」标签
// 高级功能(LLM 引擎 / Canary Token / 严重度矩阵 / 统计)请前往
// /admin/prompt-injection 完整页处理。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  getCategoryMeta,
  getSeverityTagType,
  getPolicy,
  listRules,
  toggleRule,
  updatePolicy,
  updateRule,
  type PromptInjectionRule,
  type PromptInjectionPolicy,
} from '../api/promptInjection'

// Props:
//   - moduleEnabled: when false, the panel shows a banner warning that all
//     settings will be inert. Defaults to true so the panel works standalone
//     (e.g. when dropped into /admin/modules without an explicit prop).
const { moduleEnabled = true } = defineProps<{ moduleEnabled?: boolean }>()

const { t } = useI18n()
const router = useRouter()

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
const loading = ref(true)
const savingPolicy = ref(false)
const error = ref('')
const success = ref('')

const policy = ref<PromptInjectionPolicy | null>(null)
const rules = ref<PromptInjectionRule[]>([])
// id → severity; debounced flush. NEVER cleared before processing — see
// flushSeverityUpdates() for the safe clear-after-success pattern.
const dirtyRules = new Map<number, number>()

const search = ref('')
type FilterKind = 'all' | 'system' | 'custom' | 'enabled' | 'disabled'
const filterKind = ref<FilterKind>('all')
const expandedRules = ref<Set<number>>(new Set())

let policySaveTimer: ReturnType<typeof setTimeout> | null = null
let ruleSeverityTimer: ReturnType<typeof setTimeout> | null = null
let successClearTimer: ReturnType<typeof setTimeout> | null = null
const ruleBusy = new Set<number>()

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------
async function load() {
  loading.value = true
  error.value = ''
  // Reset state on reload so we don't auto-save the just-loaded policy.
  flushPolicySave(true)
  flushSeverityUpdates(true)
  try {
    const [policyRes, rulesRes] = await Promise.all([getPolicy(), listRules()])
    policy.value = policyRes
    rules.value = rulesRes.rules
  } catch (e: any) {
    error.value = e?.message || t('sessions.config.promptInjectionLoadError')
  } finally {
    loading.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => {
  flushPolicySave(true)
  flushSeverityUpdates(true)
  if (successClearTimer) clearTimeout(successClearTimer)
})

// ---------------------------------------------------------------------------
// Derived
// ---------------------------------------------------------------------------
const filteredRules = computed<PromptInjectionRule[]>(() => {
  let list = rules.value
  switch (filterKind.value) {
    case 'system':   list = list.filter((r) => r.is_system); break
    case 'custom':   list = list.filter((r) => !r.is_system); break
    case 'enabled':  list = list.filter((r) => r.enabled); break
    case 'disabled': list = list.filter((r) => !r.enabled); break
  }
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter(
      (r) =>
        r.rule_name.toLowerCase().includes(q) ||
        (r.description || '').toLowerCase().includes(q),
    )
  }
  // Group by category_new (fall back to legacy category), then by severity desc,
  // then by rule_name. Returns a fresh array so callers can rely on order.
  return [...list].sort((a, b) => {
    const ca = a.category_new || a.category || ''
    const cb = b.category_new || b.category || ''
    if (ca !== cb) return ca.localeCompare(cb)
    if (a.severity !== b.severity) return b.severity - a.severity
    return a.rule_name.localeCompare(b.rule_name)
  })
})

const statsSummary = computed(() => {
  const total = rules.value.length
  const enabled = rules.value.filter((r) => r.enabled).length
  return {
    total,
    enabled,
    disabled: total - enabled,
    categories: new Set(
      rules.value.map((r) => r.category_new || r.category).filter(Boolean),
    ).size,
  }
})

// ---------------------------------------------------------------------------
// Save policy (debounced on every change). Initial policy load does NOT trigger
// a save because the watch below only fires when the policy object reference or
// deep contents change after `policy` becomes non-null (we guard inside
// schedulePolicySave).
// ---------------------------------------------------------------------------
function flushPolicySave(cancelOnly = false) {
  if (policySaveTimer) {
    clearTimeout(policySaveTimer)
    policySaveTimer = null
  }
  if (cancelOnly) return
}

function showSavedFlash() {
  success.value = t('sessions.config.promptInjectionPolicySaveSuccess')
  if (successClearTimer) clearTimeout(successClearTimer)
  successClearTimer = setTimeout(() => {
    success.value = ''
    successClearTimer = null
  }, 1800)
}

function schedulePolicySave() {
  if (!policy.value || savingPolicy.value) return
  flushPolicySave(true)
  policySaveTimer = setTimeout(async () => {
    policySaveTimer = null
    if (!policy.value) return
    savingPolicy.value = true
    try {
      await updatePolicy(policy.value)
      showSavedFlash()
    } catch (e: any) {
      error.value = e?.message || t('sessions.config.promptInjectionSaveError')
    } finally {
      savingPolicy.value = false
    }
  }, 500)
}

let policyWatchReady = false
watch(
  policy,
  () => {
    if (!policyWatchReady) {
      // First invocation is the initial assignment in load(); ignore.
      policyWatchReady = true
      return
    }
    if (policy.value) schedulePolicySave()
  },
  { deep: true },
)

// ---------------------------------------------------------------------------
// Rule actions
// ---------------------------------------------------------------------------
async function onToggleRule(rule: PromptInjectionRule, value: boolean) {
  if (ruleBusy.has(rule.id)) return
  ruleBusy.add(rule.id)
  const prev = rule.enabled
  rule.enabled = value // optimistic
  try {
    await toggleRule(rule.id, value)
    ElMessage.success(t('sessions.config.promptInjectionRuleSaveSuccess'))
  } catch (e: any) {
    rule.enabled = prev // revert
    ElMessage.error(e?.message || t('sessions.config.promptInjectionSaveError'))
  } finally {
    ruleBusy.delete(rule.id)
  }
}

function flushSeverityUpdates(cancelOnly = false) {
  if (ruleSeverityTimer) {
    clearTimeout(ruleSeverityTimer)
    ruleSeverityTimer = null
  }
  if (cancelOnly) return
}

function scheduleSeveritySave(rule: PromptInjectionRule, value: number) {
  rule.severity = value
  dirtyRules.set(rule.id, value)
  flushSeverityUpdates(true)
  ruleSeverityTimer = setTimeout(async () => {
    ruleSeverityTimer = null
    // Snapshot before processing so we keep un-flushed updates if one fails.
    const pending = Array.from(dirtyRules.entries())
    let failed = false
    for (const [id, severity] of pending) {
      try {
        await updateRule(id, { severity })
        dirtyRules.delete(id)
      } catch (e: any) {
        failed = true
        ElMessage.error(e?.message || t('sessions.config.promptInjectionSaveError'))
        // Revert the failing rule by reloading from server; other pending
        // updates stay in dirtyRules and will retry on next change.
        await load()
        break
      }
    }
    if (!failed && pending.length) {
      ElMessage.success(t('sessions.config.promptInjectionRuleSaveSuccess'))
    }
  }, 600)
}

function onSeverityChange(rule: PromptInjectionRule, value: number) {
  scheduleSeveritySave(rule, value)
}

function toggleExpanded(id: number) {
  const next = new Set(expandedRules.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedRules.value = next
}

// ---------------------------------------------------------------------------
// Display helpers
// ---------------------------------------------------------------------------
function categoryDisplay(raw: string) {
  const safeRaw = raw || 'unknown'
  const meta = getCategoryMeta(safeRaw)
  // vue-i18n v9: pass the fallback via the `default` option so missing keys
  // (or missing translations) render a sensible Chinese label instead of the
  // raw dotted key path.
  return {
    label: t(`sessions.config.${meta.i18nKey}`, { default: meta.zhFallback || safeRaw }),
    tagType: meta.tagType,
  }
}

function openFullConfig() {
  router.push('/admin/prompt-injection')
}

// Threshold validation: 0..10 and log ≤ warn ≤ sanitize ≤ block.
const thresholdInvalid = computed(() => {
  const p = policy.value
  if (!p) return false
  const vals = [p.score_threshold_log, p.score_threshold_warn, p.score_threshold_sanitize, p.score_threshold_block]
  if (vals.some((v) => v < 0 || v > 10)) return true
  for (let i = 1; i < vals.length; i++) if (vals[i] < vals[i - 1]) return true
  return false
})
</script>

<template>
  <div class="pi-panel" :aria-busy="loading || savingPolicy">
    <div v-if="error" class="banner banner-error" role="alert">{{ error }}</div>
    <div v-if="success" class="banner banner-success" role="status">{{ success }}</div>

    <!-- ─── Top action bar ──────────────────────────────────────────────── -->
    <div class="action-bar">
      <div>
        <strong>{{ t('sessions.config.promptInjectionPolicyTitle') }}</strong>
        <span class="meta">
          {{ t('sessions.config.promptInjectionStatsSummary', {
            total: statsSummary.total,
            enabled: statsSummary.enabled,
          }) }}
        </span>
      </div>
      <button class="btn btn-primary btn-sm" type="button" @click="openFullConfig">
        {{ t('sessions.config.promptInjectionOpenFull') }}
      </button>
    </div>
    <p class="panel-subtitle">{{ t('sessions.config.promptInjectionSubtitle') }}</p>

    <!-- ─── Module-disabled warning ─────────────────────────────────────── -->
    <div v-if="!moduleEnabled" class="banner banner-warn" role="status">
      {{ t('sessions.config.promptInjectionModuleDisabled') }}
    </div>

    <!-- ─── Loading skeleton ────────────────────────────────────────────── -->
    <div v-if="loading && !policy" class="state">{{ t('sessions.config.loading') }}</div>

    <!-- ─── Policy section ─────────────────────────────────────────────── -->
    <section v-if="policy" class="config-section">
      <div class="section-head">
        <div>
          <h2>{{ t('sessions.config.promptInjectionPolicyTitle') }}</h2>
          <p>{{ t('sessions.config.promptInjectionPolicyHint') }}</p>
        </div>
      </div>

      <!-- Master switch + mode -->
      <div class="field-row">
        <div class="field-label">
          <label>{{ t('sessions.config.promptInjectionFieldEnabled') }}</label>
          <span>{{ t('sessions.config.promptInjectionFieldEnabledHint') }}</span>
        </div>
        <label class="switch">
          <input
            type="checkbox"
            v-model="policy.enabled"
            :aria-label="t('sessions.config.promptInjectionFieldEnabled')"
          />
          <span class="track"><span class="knob" /></span>
        </label>
      </div>

      <div class="field-row">
        <div class="field-label">
          <label>{{ t('sessions.config.promptInjectionFieldMode') }}</label>
          <span>{{ t('sessions.config.promptInjectionFieldModeHint') }}</span>
        </div>
        <div class="radio-group">
          <label class="radio-pill">
            <input type="radio" v-model="policy.detection_mode" value="observe" />
            <span>{{ t('sessions.config.promptInjectionFieldModeObserve') }}</span>
          </label>
          <label class="radio-pill">
            <input type="radio" v-model="policy.detection_mode" value="enforce" />
            <span>{{ t('sessions.config.promptInjectionFieldModeEnforce') }}</span>
          </label>
        </div>
      </div>

      <!-- Detection layers -->
      <div class="subsection-title">
        {{ t('sessions.config.promptInjectionSectionLayers') }}
      </div>

      <div class="field-row" v-for="layer in [
        { key: 'enable_basic_rules',      labelKey: 'promptInjectionFieldBasic',      hintKey: 'promptInjectionFieldBasicHint' },
        { key: 'enable_advanced_rules',   labelKey: 'promptInjectionFieldAdvanced',   hintKey: 'promptInjectionFieldAdvancedHint' },
        { key: 'enable_heuristics',       labelKey: 'promptInjectionFieldHeuristics', hintKey: 'promptInjectionFieldHeuristicsHint' },
        { key: 'enable_llm_detection',    labelKey: 'promptInjectionFieldLLM',        hintKey: 'promptInjectionFieldLLMHint' },
        { key: 'enable_canary_detection', labelKey: 'promptInjectionFieldCanary',     hintKey: 'promptInjectionFieldCanaryHint' },
        { key: 'enable_vector_similarity',labelKey: 'promptInjectionFieldVector',     hintKey: 'promptInjectionFieldVectorHint' },
      ]" :key="layer.key">
        <div class="field-label">
          <label>{{ t(`sessions.config.${layer.labelKey}`) }}</label>
          <span>{{ t(`sessions.config.${layer.hintKey}`) }}</span>
        </div>
        <label class="switch">
          <input
            type="checkbox"
            v-model="(policy as any)[layer.key]"
            :aria-label="t(`sessions.config.${layer.labelKey}`)"
          />
          <span class="track"><span class="knob" /></span>
        </label>
      </div>

      <!-- Thresholds -->
      <div class="subsection-title">
        {{ t('sessions.config.promptInjectionSectionThresholds') }}
      </div>
      <p class="hint-row">{{ t('sessions.config.promptInjectionFieldThresholdHint') }}</p>

      <div class="threshold-grid">
        <div class="threshold-row">
          <label>{{ t('sessions.config.promptInjectionFieldThresholdLog') }}</label>
          <input
            class="compact-number"
            type="number"
            min="0"
            max="10"
            step="1"
            v-model.number="policy.score_threshold_log"
          />
        </div>
        <div class="threshold-row">
          <label>{{ t('sessions.config.promptInjectionFieldThresholdWarn') }}</label>
          <input
            class="compact-number"
            type="number"
            min="0"
            max="10"
            step="1"
            v-model.number="policy.score_threshold_warn"
          />
        </div>
        <div class="threshold-row">
          <label>{{ t('sessions.config.promptInjectionFieldThresholdSanitize') }}</label>
          <input
            class="compact-number"
            type="number"
            min="0"
            max="10"
            step="1"
            v-model.number="policy.score_threshold_sanitize"
          />
        </div>
        <div class="threshold-row">
          <label>{{ t('sessions.config.promptInjectionFieldThresholdBlock') }}</label>
          <input
            class="compact-number"
            type="number"
            min="0"
            max="10"
            step="1"
            v-model.number="policy.score_threshold_block"
          />
        </div>
      </div>
      <p v-if="thresholdInvalid" class="threshold-error" role="alert">
        {{ t('sessions.config.promptInjectionThresholdInvalid') }}
      </p>
    </section>

    <!-- ─── Rules section ──────────────────────────────────────────────── -->
    <section v-if="!loading || rules.length" class="config-section">
      <div class="section-head">
        <div>
          <h2>{{ t('sessions.config.promptInjectionRulesTitle', { count: rules.length }) }}</h2>
          <p>{{ t('sessions.config.promptInjectionRulesHint') }}</p>
        </div>
      </div>

      <!-- Filters -->
      <div class="rule-toolbar">
        <input
          v-model="search"
          type="search"
          class="search-input"
          :placeholder="t('sessions.config.promptInjectionRuleSearchPlaceholder')"
        />
        <div class="filter-chips">
          <button
            v-for="f in (['all','system','custom','enabled','disabled'] as FilterKind[])"
            :key="f"
            type="button"
            class="chip-btn"
            :class="{ active: filterKind === f }"
            @click="filterKind = f"
          >
            {{ t(`sessions.config.promptInjectionRuleFilter${f.charAt(0).toUpperCase()}${f.slice(1)}`) }}
          </button>
        </div>
      </div>

      <!-- Rules list grouped by category -->
      <div v-if="!filteredRules.length" class="state">
        <template v-if="search || filterKind !== 'all'">
          {{ t('sessions.config.promptInjectionEmptyFiltered') }}
        </template>
        <template v-else>
          {{ t('sessions.config.loading') }}
        </template>
      </div>
      <div v-else class="rules-list">
        <article
          v-for="rule in filteredRules"
          :key="rule.id"
          class="rule-card"
          :class="{ disabled: !rule.enabled }"
        >
          <header class="rule-head">
            <div class="rule-title">
              <el-tag
                :type="categoryDisplay(rule.category_new || rule.category || '').tagType as any"
                size="small"
                effect="light"
              >
                {{ categoryDisplay(rule.category_new || rule.category || '').label }}
              </el-tag>
              <code class="rule-name">{{ rule.rule_name }}</code>
              <el-tag
                size="small"
                :type="rule.is_system ? 'info' : 'success'"
                effect="plain"
              >
                {{
                  rule.is_system
                    ? t('sessions.config.promptInjectionRuleSystem')
                    : t('sessions.config.promptInjectionRuleCustom')
                }}
              </el-tag>
            </div>
            <div class="rule-actions">
              <label class="switch">
                <input
                  type="checkbox"
                  :checked="rule.enabled"
                  :disabled="ruleBusy.has(rule.id)"
                  @change="onToggleRule(rule, ($event.target as HTMLInputElement).checked)"
                />
                <span class="track"><span class="knob" /></span>
              </label>
            </div>
          </header>

          <p class="rule-desc">{{ rule.description }}</p>

          <div class="rule-controls">
            <div class="severity-control">
              <span class="control-label">{{ t('sessions.config.promptInjectionRuleSeverity') }}</span>
              <input
                type="range"
                min="1"
                max="10"
                step="1"
                :value="rule.severity"
                @input="onSeverityChange(rule, Number(($event.target as HTMLInputElement).value))"
              />
              <el-tag :type="getSeverityTagType(rule.severity) as any" size="small" effect="dark">
                {{ rule.severity }} / 10
              </el-tag>
            </div>
            <button
              type="button"
              class="expand-btn"
              @click="toggleExpanded(rule.id)"
              :aria-expanded="expandedRules.has(rule.id)"
            >
              {{ expandedRules.has(rule.id) ? '−' : '+' }}
              {{ t('sessions.config.promptInjectionRulePattern') }}
            </button>
          </div>

          <div v-if="expandedRules.has(rule.id)" class="rule-details">
            <div class="detail-row">
              <span class="detail-label">{{ t('sessions.config.promptInjectionRulePattern') }}</span>
              <code class="detail-value regex">{{ rule.pattern }}</code>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('sessions.config.promptInjectionRuleExamples') }}</span>
              <div class="example-tags">
                <el-tag
                  v-for="(ex, idx) in rule.examples"
                  :key="idx"
                  size="small"
                  effect="plain"
                  class="example-tag"
                >
                  {{ ex }}
                </el-tag>
                <span v-if="!rule.examples?.length" class="empty">
                  {{ t('sessions.config.promptInjectionRuleNoExamples') }}
                </span>
              </div>
            </div>
          </div>
        </article>
      </div>
    </section>

    <!-- ─── Advanced links ─────────────────────────────────────────────── -->
    <section class="config-section">
      <div class="section-head">
        <div>
          <h2>{{ t('sessions.config.promptInjectionSectionAdvanced') }}</h2>
          <p>{{ t('sessions.config.promptInjectionSubtitle') }}</p>
        </div>
      </div>
      <div class="link-grid">
        <button
          v-for="link in [
            { id: 'engines', route: '/admin/prompt-injection?tab=engines',  labelKey: 'promptInjectionLinkEngines', hintKey: 'promptInjectionLinkEnginesHint' },
            { id: 'canary',  route: '/admin/prompt-injection?tab=canary',   labelKey: 'promptInjectionLinkCanary',  hintKey: 'promptInjectionLinkCanaryHint' },
            { id: 'matrix',  route: '/admin/prompt-injection?tab=severity', labelKey: 'promptInjectionLinkMatrix',  hintKey: 'promptInjectionLinkMatrixHint' },
            { id: 'stats',   route: '/admin/prompt-injection?tab=stats',    labelKey: 'promptInjectionLinkStats',   hintKey: 'promptInjectionLinkStatsHint' },
          ]"
          :key="link.id"
          type="button"
          class="link-card"
          @click="router.push(link.route)"
        >
          <span class="link-title">{{ t(`sessions.config.${link.labelKey}`) }}</span>
          <span class="link-hint">{{ t(`sessions.config.${link.hintKey}`) }}</span>
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.pi-panel { display: grid; gap: 12px; max-width: 1080px; }
.action-bar { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 34px; }
.action-bar strong { font-size: 14px; }
.meta { color: var(--muted); font-size: 11px; margin-left: 8px; }
.panel-subtitle { color: var(--muted); font-size: 12px; margin: 0; line-height: 1.5; }
.config-section { background: var(--card); border: 1px solid var(--border); border-radius: var(--radius); padding: 14px 16px; }
.section-head { margin-bottom: 8px; }
.section-head h2 { font-size: 13px; margin: 0; color: var(--text); }
.section-head p { margin: 3px 0 0; color: var(--muted); font-size: 11px; }
.subsection-title { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); margin: 14px 0 6px; }
.hint-row { color: var(--muted); font-size: 11px; margin: 0 0 6px; }

.field-row { display: grid; grid-template-columns: minmax(220px, 1fr) minmax(180px, 360px); align-items: center; gap: 18px; padding: 9px 0; border-bottom: 1px solid var(--border); }
.field-row:last-child { border-bottom: 0; }
.field-label { min-width: 0; display: grid; gap: 2px; }
.field-label label { color: var(--text); font-size: 12px; font-weight: 600; }
.field-label span { color: var(--muted); font-size: 11px; line-height: 1.35; }

.compact-number { box-sizing: border-box; width: 100%; min-width: 0; height: 32px; padding: 5px 8px; color: var(--text); background: var(--bg); border: 1px solid var(--border); border-radius: 6px; font: inherit; font-size: 12px; }
.compact-number:focus { outline: 2px solid color-mix(in srgb, var(--accent) 45%, transparent); border-color: var(--accent); }

.switch { justify-self: end; cursor: pointer; position: relative; display: inline-block; }
.switch input { position: absolute; opacity: 0; pointer-events: none; }
.track { display: block; width: 34px; height: 19px; background: var(--border); border-radius: 10px; padding: 2px; transition: background 0.15s; }
.knob { display: block; width: 15px; height: 15px; border-radius: 50%; background: white; transition: transform 0.15s; }
.switch input:checked + .track { background: var(--accent); }
.switch input:checked + .track .knob { transform: translateX(15px); }
.switch input:disabled + .track { opacity: 0.5; cursor: not-allowed; }

.radio-group { display: flex; gap: 8px; justify-self: end; }
.radio-pill { display: inline-flex; align-items: center; gap: 6px; padding: 5px 10px; border: 1px solid var(--border); border-radius: 999px; cursor: pointer; font-size: 12px; color: var(--text); background: var(--bg); }
.radio-pill input { accent-color: var(--accent); }
.radio-pill:has(input:checked) { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 14%, var(--bg)); }

.threshold-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 8px; }
.threshold-row { display: grid; gap: 4px; }
.threshold-row label { color: var(--muted); font-size: 11px; }

/* Rule toolbar */
.rule-toolbar { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-bottom: 10px; }
.search-input { flex: 1 1 220px; min-width: 200px; height: 30px; padding: 5px 10px; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; color: var(--text); font: inherit; font-size: 12px; }
.search-input:focus { outline: 2px solid color-mix(in srgb, var(--accent) 45%, transparent); border-color: var(--accent); }
.filter-chips { display: flex; gap: 4px; flex-wrap: wrap; }
.chip-btn { appearance: none; border: 1px solid var(--border); background: var(--bg); color: var(--muted); font: inherit; font-size: 11px; padding: 4px 9px; border-radius: 999px; cursor: pointer; }
.chip-btn:hover { color: var(--text); }
.chip-btn.active { background: color-mix(in srgb, var(--accent) 14%, var(--bg)); color: var(--text); border-color: var(--accent); }

/* Rule list */
.rules-list { display: grid; gap: 8px; }
.rule-card { border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; background: var(--bg); transition: opacity 0.15s; }
.rule-card.disabled { opacity: 0.55; }
.rule-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.rule-title { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; min-width: 0; }
.rule-name { font-family: ui-monospace, SFMono-Regular, monospace; font-size: 12px; color: var(--text); }
.rule-desc { color: var(--muted); font-size: 12px; margin: 4px 0 8px; line-height: 1.5; }
.rule-controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.severity-control { display: flex; align-items: center; gap: 8px; flex: 1 1 240px; }
.severity-control input[type=range] { flex: 1; accent-color: var(--accent); }
.control-label { color: var(--muted); font-size: 11px; white-space: nowrap; }
.expand-btn { appearance: none; background: transparent; border: 1px solid var(--border); border-radius: 6px; color: var(--muted); font: inherit; font-size: 11px; padding: 4px 9px; cursor: pointer; }
.expand-btn:hover { color: var(--text); border-color: var(--accent); }
.rule-details { margin-top: 8px; padding-top: 8px; border-top: 1px dashed var(--border); display: grid; gap: 6px; }
.detail-row { display: grid; grid-template-columns: 80px 1fr; gap: 10px; align-items: start; }
.detail-label { color: var(--muted); font-size: 11px; }
.detail-value { font-size: 12px; color: var(--text); word-break: break-all; }
.detail-value.regex { font-family: ui-monospace, SFMono-Regular, monospace; font-size: 11px; background: var(--card); padding: 6px 8px; border-radius: 4px; border: 1px solid var(--border); }
.example-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.example-tag { font-family: ui-monospace, SFMono-Regular, monospace; }
.empty { color: var(--muted); font-size: 11px; font-style: italic; }

/* Links */
.link-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 8px; }
.link-card { appearance: none; background: var(--bg); border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; text-align: left; cursor: pointer; display: grid; gap: 4px; color: var(--text); }
.link-card:hover { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, var(--bg)); }
.link-title { font-size: 13px; font-weight: 600; }
.link-hint { color: var(--muted); font-size: 11px; }

/* Misc */
.btn { border-radius: 6px; border: 1px solid var(--border); cursor: pointer; font: inherit; }
.btn-sm { padding: 5px 9px; font-size: 11px; }
.btn-primary { color: white; background: var(--accent); border-color: var(--accent); }
.banner { padding: 8px 10px; border-radius: 6px; font-size: 12px; }
.banner-error { color: var(--danger); border: 1px solid color-mix(in srgb, var(--danger) 35%, var(--border)); background: color-mix(in srgb, var(--danger) 8%, transparent); }
.banner-success { color: var(--success); border: 1px solid color-mix(in srgb, var(--success) 35%, var(--border)); background: color-mix(in srgb, var(--success) 8%, transparent); }
.banner-warn { color: #b8821a; border: 1px solid color-mix(in srgb, #d29922 35%, var(--border)); background: color-mix(in srgb, #d29922 8%, transparent); }
.threshold-error { color: var(--danger); font-size: 11px; margin: 6px 0 0; }
.state { color: var(--muted); font-size: 12px; padding: 10px 0; }

@media (max-width: 800px) {
  .field-row { grid-template-columns: 1fr; gap: 7px; }
  .switch { justify-self: start; }
  .radio-group { justify-self: start; }
  .detail-row { grid-template-columns: 1fr; }
}
</style>