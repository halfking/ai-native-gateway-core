<script setup lang="ts">
// AutoRouteDecisionView — 单个请求的自动路由决策回放
// （GET /api/admin/auto-route/analytics/decision/{request_id}，**superAdmin 档**）。
//
// 回答的是排障里最难自证的那一问：「**它为什么被路由到那个凭据**」。
//
// ## ⚠️ 四处后端语义决定了 UI 的做法
//
// (1) ★★★★ **404 身兼两职**：L1 查不到 **与** 跨租户被过滤掉都返 404
//     `admin_request_not_found`（analytics.go:806-810）。
//     ⇒ 404 **不许**只说「这个请求不存在」—— 也可能是它不属于你可见的租户。
//
// (2) ★★★ **l2 是条件键，且缺失有三种原因**：
//     ① 后端**根本没查** —— `l2Lookup` 只在 id 能生成 dashed 变体时为真
//        （uuidVariants 只对 32 位 hex 产出两个变体，:1147-1166）。
//        探测 id / 带前缀 id 走这条路 ⇒ **形态上就永远没有 l2**。
//     ② 查了但 `routing_decision_log` 没有这行（ErrNoRows）。
//     ③ L2 查询真出错 ⇒ 500，走错误分支，不到这里。
//     ⇒ 页面必须把 ① 和 ② 分开说，否则用户在探测 id 上白等一场。
//
// (3) ★★★ **l1 被 auto_decision 的 JSON blob 逐键覆盖**（analytics.go:830-840），
//     且合并后**不可判定**。⇒ 页面上 `task_type`/`profile`/`confidence`
//     带一个「来源可能已被覆盖」的标记；**但没有标记不等于一定没被覆盖**
//     （blob 可以只带一个同名键而不带任何新键）—— 这一点必须写在免责文案里。
//
// (4) ★★ `client_model` / `outbound_model` 的 `""` 是 **NULL 还是真空串分不出来**
//     （`nullStringOrEmpty`，:823-824）⇒ 两者都空才说「没有模型信息」。
//
// ★ 本页只读，不碰任何写操作。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum, relativeTime } from '@/utils/format'
import {
  fetchAutoRouteDecision,
  autoRouteDecisionHasL2,
  autoRouteDecisionCanHaveL2,
  autoRouteDecisionModelsBothEmpty,
  autoRouteDecisionL1SplatKeys,
  type AutoRouteDecisionResponse,
} from '@/api/autoRouteRead'
// ★ 复用 requestDetail 的 id 校验器（SSOT 在那里，本页不另造一套）
import { isValidRequestId } from '@/api/requestDetail'

useHyperPage({ title: () => t('autoRouteDecision.title') })

const route = useRoute()
const idInput = ref(typeof route.params.id === 'string' ? route.params.id : '')
const detail = ref<AutoRouteDecisionResponse | null>(null)
const error = ref<string | null>(null)
const notFound = ref(false)
const loading = ref(false)
const loaded = ref(false)

const trimmedId = computed(() => idInput.value.trim())
const localIdValid = computed(() => isValidRequestId(trimmedId.value))
/** ★ 形态预判：不依赖响应，进页面就能告诉用户「这个 id 不可能有 L2 段」。 */
const canHaveL2 = computed(() => autoRouteDecisionCanHaveL2(trimmedId.value))
const loadedCanHaveL2 = computed(() =>
  detail.value ? autoRouteDecisionCanHaveL2(detail.value.request_id) : false,
)

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('autoRouteDecision.errForbidden')
  if (statusCode === 404) return t('autoRouteDecision.notFound')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(idOverride?: string): Promise<void> {
  const id = (idOverride ?? idInput.value).trim()
  if (!isValidRequestId(id)) {
    error.value = t('autoRouteDecision.errBadId')
    notFound.value = false
    detail.value = null
    loaded.value = true
    return
  }
  loading.value = true
  error.value = null
  notFound.value = false
  try {
    detail.value = await fetchAutoRouteDecision(id)
  } catch (err) {
    // ★ 404 身兼两职：不存在 / 跨租户被拒，都走这里。
    //   ★ 这里**不能**把 404 同时塞进 error —— 模板里 `v-else-if="error"`
    //   排在 `v-else-if="notFound"` 前面，error 一置位那条双职提示就成死分支
    //   （第一版就是这么写的，被用例直接顶出来）。
    const code = (err as { status?: number })?.status
    notFound.value = code === 404
    detail.value = null
    error.value = notFound.value ? null : describeError(err)
  } finally {
    loading.value = false
    loaded.value = true
  }
}

watch(
  () => route.params.id,
  (v) => {
    if (typeof v === 'string' && v) {
      idInput.value = v
      void load(v)
    }
  },
)

const L2_KEYS = [
  'chosen_credential_id', 'chosen_provider_id', 'tier', 'candidates_tried',
  'resolution_path', 'canonical_model',
] as const

const l2Rows = computed(() => {
  const l2 = detail.value?.l2
  if (!l2) return []
  return L2_KEYS.filter((k) => l2[k as keyof typeof l2] !== undefined).map((k) => [k, l2[k as keyof typeof l2]] as const)
})

/** ★ splat 键 = 三个库列之外的键 ⇒ blob 跑过的**可判定证据**。 */
const splatKeys = computed(() => (detail.value ? autoRouteDecisionL1SplatKeys(detail.value.l1) : []))
const modelsBothEmpty = computed(() => (detail.value ? autoRouteDecisionModelsBothEmpty(detail.value) : false))
const hasL2 = computed(() => (detail.value ? autoRouteDecisionHasL2(detail.value) : false))

onBeforeUnmount(() => {
  detail.value = null
  error.value = null
  notFound.value = false
})
</script>

<template>
  <div class="view-root ad">
    <section class="ad__search">
      <label class="ad__label">
        <span>{{ t('autoRouteDecision.idLabel') }}</span>
        <input
          v-model="idInput"
          type="text"
          class="ad__input"
          :placeholder="t('autoRouteDecision.idPlaceholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          @keyup.enter="load()"
        />
      </label>
      <button type="button" class="ad__go" :disabled="loading || !localIdValid" @click="load()">
        {{ t('autoRouteDecision.load') }}
      </button>
    </section>

    <!-- ★ 形态预判：进页面就能说清「这个 id 不可能有 L2 段」 -->
    <p v-if="trimmedId && !canHaveL2" class="ad__msg ad__msg--warn">
      {{ t('autoRouteDecision.noL2ByShape') }}
    </p>

    <!-- ★ 形态非法时明说，不发一个注定失败的请求 -->
    <p v-if="trimmedId && !localIdValid" class="ad__msg ad__msg--warn">
      {{ t('autoRouteDecision.errBadId') }}
    </p>

    <p v-if="loading" class="ad__msg">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="ad__msg ad__msg--err">{{ error }}</p>
    <!-- ★ 404 身兼两职：不能说成「这个请求不存在」 -->
    <p v-else-if="notFound" class="ad__msg ad__msg--err">
      {{ t('autoRouteDecision.notFoundHint') }}
    </p>

    <template v-if="detail">
      <!-- ── 概要 ─────────────────────────────────────────────── -->
      <section class="ad__section">
        <header class="ad__item-head">
          <StatusDot :tone="detail.success ? 'success' : 'danger'" />
          <span class="ad__title">{{ t('autoRouteDecision.summary') }}</span>
          <span class="ad__time">{{ relativeTime(detail.ts) }}</span>
        </header>
        <dl class="ad__kv">
          <div class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.requestId') }}</dt>
            <dd class="ad__mono">{{ detail.request_id }}</dd>
          </div>
          <div class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.success') }}</dt>
            <dd>{{ detail.success ? t('common.yes') : t('common.no') }}</dd>
          </div>
          <!-- ★ 两个模型都空才说「没有模型信息」；单侧空不合并显示 -->
          <div class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.models') }}</dt>
            <dd :class="{ 'ad__cell--nodata': modelsBothEmpty }">
              {{ modelsBothEmpty ? t('autoRouteDecision.noModelInfo') : `${detail.client_model || '—'} → ${detail.outbound_model || '—'}` }}
            </dd>
          </div>
          <div v-if="detail.api_key_id !== undefined" class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.apiKeyId') }}</dt>
            <dd>{{ fmtInt(detail.api_key_id) }}</dd>
          </div>
          <div v-if="detail.credential_id !== undefined" class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.credentialId') }}</dt>
            <dd>{{ fmtInt(detail.credential_id) }}</dd>
          </div>
          <div v-if="detail.latency_ms !== undefined" class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.latency') }}</dt>
            <dd>{{ fmtInt(detail.latency_ms) }}</dd>
          </div>
        </dl>
      </section>

      <!-- ── L1 ──────────────────────────────────────────────── -->
      <section class="ad__section">
        <header class="ad__item-head">
          <span class="ad__title">{{ t('autoRouteDecision.l1') }}</span>
        </header>
        <!-- ★ splat 跑过的可判定证据 -->
        <p v-if="splatKeys.length" class="ad__msg ad__msg--warn">
          {{ t('autoRouteDecision.splatWarn', { keys: splatKeys.join(', ') }) }}
        </p>
        <dl class="ad__kv">
          <div class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.taskType') }}</dt>
            <!-- ★ 有 splat 证据时给「来源可能已被覆盖」的标记 -->
            <dd :class="{ 'ad__shadowed': splatKeys.length > 0 }">
              {{ detail.l1.task_type || t('autoRoute.noData') }}
            </dd>
          </div>
          <div class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.profile') }}</dt>
            <dd :class="{ 'ad__shadowed': splatKeys.length > 0 }">
              {{ detail.l1.profile || t('autoRoute.noData') }}
            </dd>
          </div>
          <div v-if="detail.l1.confidence !== undefined" class="ad__kv-row">
            <dt>{{ t('autoRouteDecision.confidence') }}</dt>
            <dd :class="{ 'ad__shadowed': splatKeys.length > 0 }">
              {{ fmtNum(detail.l1.confidence as number, 3) }}
            </dd>
          </div>
        </dl>
        <!-- ★ 无 splat 证据**不等于**没被覆盖：blob 可以只带同名键 -->
        <p v-if="!splatKeys.length" class="ad__echo">{{ t('autoRouteDecision.splatUnproven') }}</p>
      </section>

      <!-- ── L2 ──────────────────────────────────────────────── -->
      <section class="ad__section">
        <header class="ad__item-head">
          <span class="ad__title">{{ t('autoRouteDecision.l2') }}</span>
        </header>
        <template v-if="hasL2 && detail.l2">
          <dl class="ad__kv">
            <div class="ad__kv-row">
              <dt>{{ t('autoRouteDecision.l2Success') }}</dt>
              <dd>{{ detail.l2.success ? t('common.yes') : t('common.no') }}</dd>
            </div>
            <div v-for="[k, v] in l2Rows" :key="k" class="ad__kv-row">
              <dt>{{ k }}</dt>
              <dd>{{ v }}</dd>
            </div>
          </dl>
          <p v-if="detail.l2.decision_trace" class="ad__echo">
            {{ t('autoRouteDecision.traceKeys', { n: Object.keys(detail.l2.decision_trace).length }) }}
          </p>
        </template>
        <!-- ★ 缺失分两种说法：形态上不可能 vs 查了但没有 -->
        <p v-else class="ad__msg ad__msg--warn">
          {{ loadedCanHaveL2 ? t('autoRouteDecision.noL2Record') : t('autoRouteDecision.noL2ByShape') }}
        </p>
      </section>
    </template>

    <p v-else-if="loaded && !loading && !error && !notFound" class="ad__msg">
      {{ t('autoRouteDecision.empty') }}
    </p>
  </div>
</template>

<style scoped>
.ad {
  padding: var(--app-space-3);
}
.ad__search {
  display: flex;
  align-items: flex-end;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.ad__label {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.ad__input {
  display: block;
  width: 100%;
  min-height: 48px;
  padding: 0 8px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
/* ★ R1：新增触控控件 ≥48 CSS px */
.ad__go {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-primary);
  background: var(--app-primary);
  color: var(--app-on-primary);
  font-size: 13px;
}
.ad__go:disabled {
  opacity: 0.6;
}
.ad__section {
  margin-bottom: var(--app-space-4);
}
.ad__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: var(--app-space-2);
}
.ad__title {
  font-size: 14px;
  font-weight: 600;
  color: var(--app-text);
}
.ad__time {
  margin-left: auto;
  font-size: 11px;
  color: var(--app-text-muted);
}
.ad__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.ad__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ad__msg--warn {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.ad__echo {
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
.ad__kv {
  margin: 0;
  display: grid;
  gap: 2px;
}
.ad__kv-row {
  display: flex;
  gap: var(--app-space-2);
  min-width: 0;
}
.ad__kv-row dt {
  font-size: 11px;
  color: var(--app-text-muted);
  min-width: 104px;
  flex-shrink: 0;
}
.ad__kv-row dd {
  margin: 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  min-width: 0;
  word-break: break-word;
}
.ad__mono {
  font-family: var(--app-font-mono, monospace);
}
.ad__cell--nodata {
  color: var(--app-text-muted);
  font-style: italic;
}
/* ★ 库列「来源可能已被 blob 覆盖」的标记 */
.ad__shadowed {
  color: var(--app-warning);
  text-decoration: underline dotted;
}
</style>