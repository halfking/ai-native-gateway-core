<script setup lang="ts">
// CompliancePolicyView — 输出合规「策略与词库」面（policy + keywords，**admin 档**）。
//
// 它答的是「**现在配的是什么、哪些词在拦**」——配置面，不是现象面。
// 现象面（命中与复核）在 /compliance-hits。
//
// ⚠️★★★ 五个后端语义（详见 api/outputCompliance.ts 文件头）：
//
// (1) ★★★★★★ **没配策略时，后端返回的是一份「合成的默认策略」，不是 404。**
//     `fetchPolicy` 在 `pgx.ErrNoRows` 时 `return defaultOutputCompliancePolicy(…)`。
//     ⇒ 「库里没配」与「配了但看着像默认值」在响应里**长得一模一样**。
//     判据是 `id === 0`（真实行 id 是 SERIAL 一定 >0）⇒ 本页据此标注。
// (2) ★★★★ 阈值与 `sampling_rate` 是 **0..1 比率** ⇒ 展示要 ×100，
//     与 stats 的整数计数完全不是一回事。
// (3) ★★★★ `keywords` 可能**整个端点 500**：可空列 `description` 被裸扫进 Go string，
//     扫失败就放弃整份清单。⇒ 500 时渲染「后端扫描失败」，绝不显示成「词库为空」。
// (4) ★★ `exception_rules` / `notification_channels` 是 `json.RawMessage`
//     ⇒ 形状不定，原样透传，不解析成固定类型。
// (5) ★ `llm_engine_id` / `last_detection_at` 是指针但 JSON tag 没有 omitempty
//     ⇒ **键一定存在**，值可能为 `null`。
//
// ★ 写操作（改策略、增删词、启停词）本页一条不碰。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt } from '@/utils/format'
import {
  fetchCompliancePolicy,
  fetchComplianceKeywords,
  formatComplianceThreshold,
  compliancePolicyIsSyntheticDefault,
  COMPLIANCE_THRESHOLD_FIELDS,
  COMPLIANCE_LIMIT_MAX,
  type CompliancePolicy,
  type ComplianceKeyword,
} from '@/api/outputCompliance'

useHyperPage({ title: () => t('compliancePolicy.title') })

const policy = ref<CompliancePolicy | null>(null)
const keywords = ref<ComplianceKeyword[]>([])
const category = ref('')

const policyError = ref<string | null>(null)
const kwError = ref<string | null>(null)
const loadingPolicy = ref(false)
const loadingKw = ref(false)

async function loadPolicy(): Promise<void> {
  loadingPolicy.value = true
  policyError.value = null
  try {
    policy.value = await fetchCompliancePolicy()
  } catch (e) {
    policy.value = null
    policyError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingPolicy.value = false
  }
}

async function loadKeywords(): Promise<void> {
  loadingKw.value = true
  kwError.value = null
  try {
    const r = await fetchComplianceKeywords({ category: category.value.trim() || undefined })
    keywords.value = r.keywords
  } catch (e) {
    // ★★ 绝不在这里退化成「词库为空」—— 那会把 500 读成「一个词都没配」。
    keywords.value = []
    kwError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingKw.value = false
  }
}

function submitCategory(): void {
  category.value = category.value.trim()
  void loadKeywords()
}

function clearCategory(): void {
  category.value = ''
  void loadKeywords()
}

void loadPolicy()
void loadKeywords()

const isSynthetic = computed(() => (policy.value ? compliancePolicyIsSyntheticDefault(policy.value) : false))

/** ★ 六个阈值字段（0..1 ⇒ ×100）。取自常量，不手抄列表。 */
const thresholdEntries = computed(() => {
  if (!policy.value) return []
  return COMPLIANCE_THRESHOLD_FIELDS.map((f) => ({
    key: f,
    label: t('compliancePolicy.th_' + f.replace('_threshold', '')),
    value: formatComplianceThreshold(policy.value![f] as number),
  }))
})

/** 策略里八个「是否检查」开关，按类别分组展示。 */
const CHECK_SWITCHES = [
  { key: 'check_pii', label: 'compliancePolicy.sw_pii' },
  { key: 'check_toxicity', label: 'compliancePolicy.sw_toxicity' },
  { key: 'check_secrets', label: 'compliancePolicy.sw_secrets' },
  { key: 'check_internal_ip', label: 'compliancePolicy.sw_internal_ip' },
  { key: 'check_bias', label: 'compliancePolicy.sw_bias' },
  { key: 'check_hallucination', label: 'compliancePolicy.sw_hallucination' },
  { key: 'check_jailbreak_response', label: 'compliancePolicy.sw_jailbreak_response' },
  { key: 'check_instruction_injection_response', label: 'compliancePolicy.sw_instruction_injection' },
] as const

/** 八个「脱敏哪几类」开关。 */
const REDACT_SWITCHES = [
  { key: 'redact_email', label: 'compliancePolicy.rd_email' },
  { key: 'redact_phone', label: 'compliancePolicy.rd_phone' },
  { key: 'redact_id_card', label: 'compliancePolicy.rd_id_card' },
  { key: 'redact_credit_card', label: 'compliancePolicy.rd_credit_card' },
  { key: 'redact_bank_card', label: 'compliancePolicy.rd_bank_card' },
  { key: 'redact_jwt', label: 'compliancePolicy.rd_jwt' },
  { key: 'redact_password', label: 'compliancePolicy.rd_password' },
] as const

function severityTone(s: number): 'danger' | 'warning' | 'muted' {
  if (s >= 8) return 'danger'
  if (s >= 5) return 'warning'
  return 'muted'
}

function actionTone(a: string): 'danger' | 'warning' | 'muted' {
  if (a === 'block') return 'danger'
  if (a === 'redact' || a === 'warn') return 'warning'
  return 'muted'
}

onBeforeUnmount(() => {
  policy.value = null
  keywords.value = []
  policyError.value = null
  kwError.value = null
})
</script>

<template>
  <div class="view-root cp">
    <!-- ══════ 当前策略 ══════ -->
    <section class="cp__panel">
      <span class="cp__panel-title">{{ t('compliancePolicy.currentPolicy') }}</span>

      <p v-if="loadingPolicy" class="cp__msg">{{ t('common.loading') }}</p>
      <p v-if="policyError" class="cp__msg cp__msg--err">{{ policyError }}</p>

      <template v-if="policy">
        <!-- ★★★★★★ 合成默认策略的提示 -->
        <p v-if="isSynthetic" class="cp__warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliancePolicy.syntheticDefaultNote') }}</span>
        </p>
        <p v-else class="cp__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliancePolicy.storedNote') }}</span>
        </p>

        <div class="cp__headline">
          <StatusDot :tone="policy.enabled ? 'success' : 'muted'" />
          <span class="cp__name">{{ policy.policy_name }}</span>
          <span class="cp__badge">{{ policy.enforcement_mode }}</span>
        </div>

        <div class="cp__grid">
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.enabled') }}</span>
            <span class="cp__cell-v">{{ policy.enabled ? t('common.yes') : t('common.no') }}</span>
          </span>
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.autoRedact') }}</span>
            <span class="cp__cell-v">{{ policy.auto_redact ? t('common.yes') : t('common.no') }}</span>
          </span>
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.strictMode') }}</span>
            <span class="cp__cell-v">{{ policy.strict_mode ? t('common.yes') : t('common.no') }}</span>
          </span>
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.llmEngine') }}</span>
            <!-- ★ 键一定存在，值可能为 null（指针 + 无 omitempty） -->
            <span class="cp__cell-v">{{ policy.llm_engine_id === null ? t('compliancePolicy.notSet') : policy.llm_engine_id }}</span>
          </span>
        </div>

        <!-- ★ 阈值是 0..1 ⇒ ×100，与 stats 的整数计数不是一回事 -->
        <p class="cp__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliancePolicy.thresholdNote') }}</span>
        </p>
        <div class="cp__tags">
          <span v-for="e in thresholdEntries" :key="e.key" class="cp__kv">
            <span class="cp__kv-l">{{ e.label }}</span>
            <span class="cp__kv-v">{{ e.value }}</span>
          </span>
          <span class="cp__kv">
            <span class="cp__kv-l">{{ t('compliancePolicy.samplingRate') }}</span>
            <span class="cp__kv-v">{{ formatComplianceThreshold(policy.sampling_rate) }}</span>
          </span>
        </div>

        <p class="cp__sub-title">{{ t('compliancePolicy.checkSwitches') }}</p>
        <div class="cp__tags">
          <span
            v-for="s in CHECK_SWITCHES"
            :key="s.key"
            class="cp__flag"
            :class="{ 'cp__flag--off': !(policy as unknown as Record<string, boolean>)[s.key] }"
          >
            {{ t(s.label) }}
          </span>
        </div>

        <p class="cp__sub-title">{{ t('compliancePolicy.redactSwitches') }}</p>
        <div class="cp__tags">
          <span
            v-for="s in REDACT_SWITCHES"
            :key="s.key"
            class="cp__flag"
            :class="{ 'cp__flag--off': !(policy as unknown as Record<string, boolean>)[s.key] }"
          >
            {{ t(s.label) }}
          </span>
        </div>

        <div class="cp__grid">
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.retentionDays') }}</span>
            <span class="cp__cell-v">{{ fmtInt(policy.retention_days) }}</span>
          </span>
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.totalDetections') }}</span>
            <span class="cp__cell-v">{{ fmtInt(policy.total_detections) }}</span>
          </span>
          <span class="cp__cell">
            <span class="cp__cell-l">{{ t('compliancePolicy.totalBlocks') }}</span>
            <span class="cp__cell-v">{{ fmtInt(policy.total_blocks) }}</span>
          </span>
        </div>

        <p v-if="policy.whitelist_keywords.length" class="cp__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliancePolicy.whitelist', { n: policy.whitelist_keywords.length }) }}</span>
        </p>
      </template>
    </section>

    <!-- ══════ 自定义违禁词 ══════ -->
    <section class="cp__panel">
      <span class="cp__panel-title">{{ t('compliancePolicy.keywords') }}</span>

      <p v-if="loadingKw" class="cp__msg">{{ t('common.loading') }}</p>
      <!-- ★★ 500 不是「词库为空」 -->
      <p v-if="kwError" class="cp__msg cp__msg--err">
        {{ kwError }}
        <span class="cp__sub">{{ t('compliancePolicy.scanFailHint') }}</span>
      </p>
      <p v-else-if="!keywords.length" class="cp__msg">{{ t('compliancePolicy.noKeywords') }}</p>

      <ul v-if="keywords.length" class="cp__list">
        <li v-for="k in keywords" :key="k.id" class="cp__item">
          <div class="cp__item-head">
            <StatusDot :tone="k.enabled ? severityTone(k.severity) : 'muted'" />
            <span class="cp__kw">{{ k.keyword }}</span>
            <span class="cp__act" :class="`cp__act--${actionTone(k.action)}`">{{ k.action }}</span>
            <span v-if="!k.enabled" class="cp__off">{{ t('compliancePolicy.disabled') }}</span>
          </div>
          <p class="cp__meta">
            {{ t('compliancePolicy.kwMeta', { cat: k.category, sev: k.severity }) }}
          </p>
          <!-- ★ description 是可空列：缺失显示「无备注」，不是空白 -->
          <p v-if="k.description" class="cp__meta">{{ k.description }}</p>
        </li>
      </ul>

      <form class="cp__form" @submit.prevent="submitCategory">
        <label class="cp__field">
          <span>{{ t('compliancePolicy.category') }}</span>
          <input
            v-model="category"
            class="cp__input"
            :placeholder="t('compliancePolicy.categoryHint')"
            autocomplete="off"
            spellcheck="false"
          />
        </label>
        <div class="cp__row">
          <button type="submit" class="cp__btn cp__btn--go">{{ t('compliancePolicy.query') }}</button>
          <button type="button" class="cp__btn" @click="clearCategory">{{ t('compliancePolicy.clearCategory') }}</button>
          <span class="cp__meta">{{ t('compliancePolicy.limitNote', { max: COMPLIANCE_LIMIT_MAX }) }}</span>
        </div>
      </form>
    </section>
  </div>
</template>

<style scoped>
.cp {
  padding: var(--app-space-3);
}
.cp__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.cp__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.cp__sub-title {
  margin: var(--app-space-3) 0 4px;
  font-size: 11px;
  font-weight: 700;
  color: var(--app-text-secondary);
}
.cp__headline {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.cp__name {
  font-size: 18px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-all;
}
.cp__badge {
  padding: 1px 8px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 11px;
  font-weight: 700;
  line-height: 1.8;
}
.cp__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.cp__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.cp__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.cp__cell-v {
  font-size: 15px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.cp__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
  color: var(--app-text-secondary);
}
.cp__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.cp__sub {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  opacity: 0.85;
}
.cp__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.cp__warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-size: 11px;
  line-height: 1.5;
}
.cp__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.cp__tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.cp__kv {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.cp__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.cp__kv-v {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.cp__flag {
  padding: 2px 8px;
  border-radius: var(--app-radius-pill);
  background: var(--app-success-soft);
  color: var(--app-success);
  font-size: 11px;
  font-weight: 600;
  line-height: 1.8;
}
.cp__flag--off {
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-weight: 400;
}
.cp__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.cp__item {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.cp__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.cp__kw {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-all;
}
.cp__act {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  font-size: 11px;
  font-weight: 700;
}
.cp__act--danger {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.cp__act--warning {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.cp__act--muted {
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
}
.cp__off {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 10px;
  line-height: 1.6;
}
.cp__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: var(--app-space-2) 0;
}
.cp__field {
  display: block;
}
.cp__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.cp__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.cp__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.cp__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.cp__btn {
  min-height: 48px;
  min-width: 96px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
</style>