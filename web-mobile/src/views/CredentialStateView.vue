<script setup lang="ts">
// CredentialStateView — 凭据 × 模型实时状态读面（第四十九批，**superAdmin 档**，1 条只读端点）。
//
//   GET /api/credentials/{id}/models/{model}/state
//
// ★★★★★★ 这条是 **superAdmin** 档：`registerStateRoutes` 里 `wrap := h.superAdmin`
//   （admin/credential_state_handlers.go:175）⇒ 抽屉席**必须**设 requiresRole: 'super_admin'。
//   ★ 这是本批与前几批相反的方向：free-discovery / logs / modules 的抽屉席都**故意不设**。
//
// ★★★★★★★★★★ `state` **可以是 `null`** —— 三层缓存（内存 → Redis → DB）全 miss。
//   `manager.go:738-765` 在 L3 返回 `(nil, nil)` 时**不报错**，handler 只判 `err != nil`
//   ⇒ **200 + {"credential_id":N,"model":"M","state":null}**，**不是 404**。
//   ⇒ 页面必须把「没探测过」单独渲染，绝不显示成「可用」。
//
// ★★★★★★★★★★ 五条指标在**两条 DB 分支里根本没被赋值**（Go 零值 0）：
//   success_rate / avg_latency_ms / p95_latency_ms / active_sessions / concurrency_limit
//   而缓存命中时它们**有真值** ⇒ 同一个 (凭据,模型) 两次查询可能给出不同数字，都是 200，
//   响应里**没有任何字段**记录答案是从内存/Redis/DB 哪一层来的。
//
// ★★★★★★★★ 注释里的枚举不完整：source 实际多 `node_probe_db` / `db`；
//   health_status 实际多 `healthy_confirmed` / `probing` / `available` / 空串。
//   ⇒ 页面按「实际取值集合」渲染，并把「超出注释」这一态标出来。
//
// ★★★★ 错误响应是 **text/plain**（`http.Error`），不是本仓惯用的 JSON 信封
//   ⇒ 移动端拿到的报文是纯文本**且带尾换行** ⇒ 页面判文案时要容忍。
//
// ★★ 不碰的三个写端点（都是 superAdmin 且**真的触发一次探测**，有外部副作用）：
//   POST /api/credentials/{id}/test、POST /api/credentials/test-batch、
//   POST /api/credentials/{id}/models/{model}/test

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchCredState,
  parseCredentialIdRejects,
  modelNeedsEncoding,
  credStateNeverProbed,
  credStateMissingOptionalKeys,
  credStateUpdatedAtIsUnreadable,
  credStateUpdatedAtIsQueryTime,
  credStateHealthUnset,
  credStateHealthUndocumented,
  credStateSourceUndocumented,
  credStateMetricsAllZero,
  credStateSuccessRateIsIndeterminate,
  credStateSignalsDisagree,
  credStateAvailableFalseWithHealthyLabel,
  credStateHasFails,
  credStateLastErrorMissing,
  credStateErrorKind,
  stripHttpErrorNewline,
  CRED_STATE_METRIC_PLACEHOLDER_FIELDS,
  type CredStateEnvelope,
} from '@/api/credentialState'

useHyperPage({ title: () => t('cs.title') })

const rawCredId = ref('')
const model = ref('')
const envelope = ref<CredStateEnvelope | null>(null)
const errMsg = ref<string | null>(null)
const errKind = ref<string | null>(null)
const loading = ref(false)

/** ★ 后端 Atoi 失败**或** <= 0 ⇒ 同一句 400。 */
const idRejected = computed(() => rawCredId.value.trim() !== '' && parseCredentialIdRejects(rawCredId.value.trim()))
/** ★ 模型名里有 `/` 时不 encode 会打到别的路由上。 */
const modelNeedsEncodingFlag = computed(() => model.value !== '' && modelNeedsEncoding(model.value))
const canQuery = computed(() => rawCredId.value.trim() !== '' && model.value.trim() !== '')

const state = computed(() => envelope.value?.state ?? null)
const missingOptional = computed(() => (state.value ? credStateMissingOptionalKeys(state.value) : []))

async function query(): Promise<void> {
  if (!canQuery.value) return
  loading.value = true
  errMsg.value = null
  errKind.value = null
  envelope.value = null
  try {
    envelope.value = await fetchCredState(rawCredId.value.trim(), model.value.trim())
  } catch (e) {
    // ★★ 抛错不许退化成「没探测过」—— 那和服务没起来长得一样
    envelope.value = null
    const msg = (e as Error)?.message || t('common.error')
    errMsg.value = msg
    errKind.value = credStateErrorKind(msg)
  } finally {
    loading.value = false
  }
}

onBeforeUnmount(() => {
  envelope.value = null
  errMsg.value = null
  errKind.value = null
})
</script>

<template>
  <div class="cs">
    <section class="cs__panel">
      <span class="cs__panel-title">{{ t('cs.title') }}</span>

      <p class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.tierNote') }}</span>
      </p>
      <p class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.nullStateNote') }}</span>
      </p>
      <p class="cs__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('cs.layerBlindNote') }}</span>
      </p>
      <p class="cs__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('cs.enumNote') }}</span>
      </p>
      <p class="cs__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('cs.plainTextErrorNote') }}</span>
      </p>

      <label class="cs__label" for="cs-cred">{{ t('cs.credIdLabel') }}</label>
      <input
        id="cs-cred"
        v-model="rawCredId"
        class="cs__input"
        type="text"
        inputmode="numeric"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('cs.credIdPlaceholder')"
      />
      <!-- ★ Atoi 失败或 <= 0 都是同一句 400 -->
      <p v-if="idRejected" class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.badIdNote') }}</span>
      </p>

      <label class="cs__label" for="cs-model">{{ t('cs.modelLabel') }}</label>
      <input
        id="cs-model"
        v-model="model"
        class="cs__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('cs.modelPlaceholder')"
      />
      <p v-if="modelNeedsEncodingFlag" class="cs__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('cs.encodingNote') }}</span>
      </p>

      <button type="button" class="cs__btn" :disabled="!canQuery || idRejected" @click="query">
        {{ t('cs.query') }}
      </button>
      <p v-if="loading" class="cs__msg">{{ t('common.loading') }}</p>

      <p v-if="errMsg" class="cs__msg cs__msg--err">
        <AppIcon name="alert" :size="14" />
        <span data-cs="err-raw">{{ stripHttpErrorNewline(errMsg) }}</span>
      </p>
      <p v-if="errMsg" class="cs__meta" data-cs="err-stripped">
        {{ t('cs.errStrippedNote', { raw: errMsg.length }) }}
      </p>
      <p v-if="errKind === 'super-admin'" class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.forbiddenNote') }}</span>
      </p>
      <p v-else-if="errKind === 'service-unavailable'" class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.serviceMissingNote') }}</span>
      </p>
      <p v-else-if="errKind === 'internal'" class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.internalNote') }}</span>
      </p>
      <p v-else-if="errKind === 'invalid-id'" class="cs__note cs__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('cs.invalidIdServerNote') }}</span>
      </p>

      <template v-if="envelope">
        <div class="cs__head">
          <span class="cs__sub">{{ t('cs.resultTitle', { id: envelope.credential_id, model: envelope.model }) }}</span>
        </div>

        <!-- ★★★★ state 为 null：三层缓存全 miss，是 200 而不是 404 -->
        <p v-if="credStateNeverProbed(envelope)" class="cs__note cs__note--warn" data-cs="state-null">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('cs.neverProbed') }}</span>
        </p>

        <template v-else-if="state">
          <!-- ★★ 两个信号矛盾时单列一条（不可能由后端两条分支产生） -->
          <p v-if="credStateSignalsDisagree(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.signalsDisagreeNote') }}</span>
          </p>
          <p v-if="credStateAvailableFalseWithHealthyLabel(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.availableFalseHealthyNote') }}</span>
          </p>

          <div class="cs__grid">
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.available') }}</span>
              <span class="cs__cell-v">
                <StatusDot :tone="state.available ? 'success' : 'danger'" />
                <template v-if="state.available">{{ t('common.yes') }}</template>
                <template v-else>{{ t('common.no') }}</template>
              </span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.healthStatus') }}</span>
              <span class="cs__cell-v">
                <template v-if="credStateHealthUnset(state)">{{ t('cs.healthUnset') }}</template>
                <template v-else>{{ state.health_status }}</template>
              </span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.successRate') }}</span>
              <span class="cs__cell-v">{{ state.success_rate }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.consecutiveFails') }}</span>
              <span class="cs__cell-v">{{ state.consecutive_fails }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.avgLatency') }}</span>
              <span class="cs__cell-v">{{ state.avg_latency_ms }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.p95Latency') }}</span>
              <span class="cs__cell-v">{{ state.p95_latency_ms }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.activeSessions') }}</span>
              <span class="cs__cell-v">{{ state.active_sessions }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.concurrencyLimit') }}</span>
              <span class="cs__cell-v">{{ state.concurrency_limit }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.source') }}</span>
              <span class="cs__cell-v">{{ state.source }}</span>
            </span>
            <span class="cs__cell">
              <span class="cs__cell-l">{{ t('cs.lastUpdatedAt') }}</span>
              <span class="cs__cell-v">
                <template v-if="credStateUpdatedAtIsUnreadable(state)">{{ t('cs.updatedAtUnreadable') }}</template>
                <template v-else>{{ state.last_updated_at }}</template>
              </span>
            </span>
          </div>

          <!-- ★★ 五条指标全 0：可能是「没实现」（DB 直查），不是「真的是 0」 -->
          <p v-if="credStateMetricsAllZero(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.metricsZeroNote', { fields: CRED_STATE_METRIC_PLACEHOLDER_FIELDS.join(' / ') }) }}</span>
          </p>
          <p v-else-if="credStateSuccessRateIsIndeterminate(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.successRateZeroNote') }}</span>
          </p>

          <p v-if="credStateHealthUndocumented(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.healthUndocumentedNote') }}</span>
          </p>
          <p v-if="credStateSourceUndocumented(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.sourceUndocumentedNote') }}</span>
          </p>
          <!-- ★ node_probe 支把更新时间写成查询时刻 -->
          <p v-if="credStateUpdatedAtIsQueryTime(state)" class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.updatedAtIsQueryTimeNote') }}</span>
          </p>
          <p v-if="credStateHasFails(state)" class="cs__note">
            <AppIcon name="key" :size="13" />
            <span>{{ t('cs.hasFailsNote', { n: state.consecutive_fails }) }}</span>
          </p>
          <p v-if="credStateLastErrorMissing(state)" class="cs__meta">{{ t('cs.noLastErrorNote') }}</p>
          <p v-else class="cs__note cs__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('cs.lastErrorLine', { msg: state.last_error || '—' }) }}</span>
          </p>
          <!-- ★ 4 个 omitempty 键：缺失 ≠ null -->
          <p class="cs__meta" data-cs="missing-optional">
            {{ t('cs.missingOptionalNote', { keys: missingOptional.join(' / ') || '—' }) }}
          </p>
        </template>
      </template>
    </section>

    <p class="cs__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('cs.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.cs__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.cs__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.cs__sub { display: block; font-size: 13px; font-weight: 600; margin: 10px 0 4px; }
.cs__head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.cs__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }
.cs__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.cs__msg--err { color: var(--danger, #c0392b); }
.cs__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.cs__note--warn { color: var(--app-warning); }
.cs__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
.cs__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.cs__cell { display: flex; flex-direction: column; }
.cs__cell-l { font-size: 11px; color: var(--app-text-muted); }
.cs__cell-v { font-size: 14px; font-weight: 600; word-break: break-all; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.cs__input { width: 100%; min-height: 48px; padding: 8px 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }
.cs__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-primary); background: transparent; color: var(--app-primary); }
.cs__btn[disabled] { opacity: .5; }
</style>
