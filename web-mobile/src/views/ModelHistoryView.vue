<script setup lang="ts">
// ModelHistoryView — 凭据模型状态变更历史（2026-10-14，第一百一十四批）。
//
// GET /api/credentials/model-history?credential_id=X&raw_model_name=Y&limit=50
//
// ★★★★★★ 权限档：**`requiresRole: 'admin'`**，不是缺省。推导见
//   `api/modelHistory.ts` 文件头 (1b)，四步全部当场读码核过：
//   1. `admin/handler.go:1475` `monitorH.RegisterMonitorRoutes(mux, h.admin)`
//      ⇒ 包 `admin.AdminMiddleware`（`admin/auth.go:85-138`）**只认证、不检 role**
//      （`admin/users.go:307-309` 的注释把这件事写成显式设计）；
//   2. `handleModelHistory`（`admin/credential_monitor.go:1502-1545`）自己也不检 role；
//   3. ★★ 租户键是**手写内联**的（`:1532-1535`）
//      ```go
//      tenantID := ""
//      if IsTenantAdmin(r) { tenantID = GetTenantID(r) }
//      ```
//      ⇒ 与 `EffectiveTenantIDAll` **语义相同但不叫那个名字**
//      ⇒ ★★ **grep `EffectiveTenantID` 找不到这一处**，这是批 113 那个坑的变体；
//   4. ⇒ 非 tenant_admin 一律空串 ⇒ 两段 SQL 的 `AND ($4 = '' OR …)` 放行 ⇒ **全租户**。
//
// ⚠️⚠️ **抽屉隐藏 ≠ 路由拦截**：`src/hyper-wiring.ts:34-45` 的 `beforeEach` 只查登录态，
//   `meta.requiresRole` 全仓无人读 ⇒ 深链照样打开 ⇒ 视图内自己再兜一层。
//   ⚠️ 这里是**兜底不是修复**：后端对越权返 **200 + 全租户数据**，根本没有 403。
//
// ★★★★★ 本族最要紧的五件事（文件头共 14 条）：
//   1. ★★★★★ **十个键全部恒在，其中六个可为裸 `null`** —— `ModelHistoryEvent`
//      一个 `omitempty` 都没有 ⇒ ★★★ `'probe_status' in ev` **永远为真**，
//      判「有没有值」必须判 `!== null`。桌面类型把这六个声明成可选，**比现实宽松**。
//   2. ★★★★★ **auto 与 manual 的 null 分布恰好互补**：auto 行恒 2 个 null
//      （actor / reason），manual 行恒 5 个 null（五个 probe 字段）
//      ⇒ 这是本族最强的自验判据来源。
//   3. ★★★★ **`reason` 的空被折叠成 `null`，而 `actor` 的空串原样保留**
//      ⇒ **`actor` 可以是空串，`reason` 不可能是空串**。
//   4. ★★★★★ **`ORDER BY ts DESC` 无 tiebreak** ⇒ 只能断言「非升序」，
//      **不能**断言严格降序。
//   5. ★★★★★ **scan 失败被 `continue` 静默跳过，响应里查不到**
//      （`slog.Warn` 有写，但**不在响应里**）⇒ 页面必须说清「这批数据可能不完整」。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchModelHistory,
  modelHistoryRoleIsAdminTier,
  modelHistoryTenantScopeLabel,
  modelHistoryParamsAreAcceptable,
  modelHistoryCountMatchesEvents,
  modelHistoryEventMatchesItsSource,
  modelHistoryAutoEventKindIsKnown,
  modelHistoryManualEventKindIsKnown,
  modelHistoryEventsAreNonAscending,
  MODEL_HISTORY_DEFAULT_LIMIT,
  MODEL_HISTORY_LIMIT_MIN,
  MODEL_HISTORY_LIMIT_MAX,
  MODEL_HISTORY_SOURCES,
  MODEL_HISTORY_AUTO_EVENTS,
  MODEL_HISTORY_MANUAL_EVENTS,
  type ModelHistoryResponse,
  type ModelHistoryEvent,
} from '@/api/modelHistory'

useHyperPage({ title: () => t('mh.title') })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════

const role = computed(() => auth.role)
const allowed = computed(() => modelHistoryRoleIsAdminTier(role.value))
const scopeLabel = computed(() => modelHistoryTenantScopeLabel(role.value))

// ══ 查询条件 ══════════════════════════════════════════════════════════════

const credentialIdRaw = ref('')
const rawModelName = ref('')
const limitRaw = ref(String(MODEL_HISTORY_DEFAULT_LIMIT))

/** ★ 空串 ⇒ `NaN`（不是 0）—— 页面据此显示「参数不可用」而不是「凭据 ID 是 0」。 */
const credentialId = computed(() => {
  const raw = credentialIdRaw.value.trim()
  return raw === '' ? Number.NaN : Number(raw)
})

const limit = computed(() => {
  const raw = limitRaw.value.trim()
  return raw === '' ? Number.NaN : Number(raw)
})

const paramsOk = computed(() =>
  modelHistoryParamsAreAcceptable({
    credentialId: credentialId.value,
    rawModelName: rawModelName.value,
    limit: limit.value,
  }),
)

const data = ref<ModelHistoryResponse | null>(null)
const historyError = ref<string | null>(null)
const loading = ref(false)

async function load(): Promise<void> {
  // ★ 兜底：不允许访问时**连请求都不发**
  if (!allowed.value) return
  loading.value = true
  historyError.value = null
  try {
    data.value = await fetchModelHistory({
      credentialId: credentialId.value,
      rawModelName: rawModelName.value,
      limit: limit.value,
    })
  } catch (err) {
    data.value = null
    historyError.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 判读 ══════════════════════════════════════════════════════════════════

/** ★★ (14) `count === events.length` —— 后端直接 `len(events)`。 */
const countMismatch = computed(() => (data.value ? !modelHistoryCountMatchesEvents(data.value) : false))

/** ★★ (6) 只能断言「非升序」，不能断言严格降序。 */
const notNonAscending = computed(() =>
  data.value ? !modelHistoryEventsAreNonAscending(data.value.events) : false,
)

/** ★★ 回显的两个键恒等于查询条件（后端原样写回）。 */
const credentialEchoBad = computed(() => (data.value ? data.value.credential_id !== credentialId.value : false))
const modelEchoBad = computed(() => (data.value ? data.value.raw_model_name !== rawModelName.value : false))

/** ★ (4) 的反面：形状破了的事件下标 —— auto 行少了那两个 null，或 manual 行少了那五个。 */
function shapeBroken(e: ModelHistoryEvent): boolean {
  return !modelHistoryEventMatchesItsSource(e)
}

/** ★ (5) `event` 取值随 `source` 变；两段判据合起来覆盖两个已知源。 */
function kindUnknown(e: ModelHistoryEvent): boolean {
  return !(modelHistoryAutoEventKindIsKnown(e) || modelHistoryManualEventKindIsKnown(e))
}

/**
 * ★★★ (2) 六个指针键**键恒在、可为裸 `null`** ⇒ 一律显示「未提供」。
 *   ★ 绝不能显示「键不存在」—— 键永远在；也**不能**把 `null` 显示成空串，
 *   那与 `actor` 真的是 `''` 的情形混为一谈（见 (3)）。
 */
function nullableText(v: string | null): string {
  return v === null ? t('mh.field.null') : v === '' ? t('mh.field.emptyString') : v
}

function nullableNumber(v: number | null): string {
  return v === null ? t('mh.field.null') : String(v)
}

/** ★ (4) 这一行里恒为 null 的那些键 —— 页面显式列出来，不让人误以为「没这一项」。 */
function nullFilledKeys(e: ModelHistoryEvent): string[] {
  if (e.source === 'auto') {
    const out: string[] = []
    if (e.actor === null) out.push('actor')
    if (e.reason === null) out.push('reason')
    return out
  }
  const out2: string[] = []
  for (const k of ['triggered_by', 'probe_status', 'http_status', 'error_code', 'error_message'] as const) {
    if (e[k] === null) out2.push(k)
  }
  return out2
}

function eventIndex(e: ModelHistoryEvent, i: number): string {
  return `${e.ts}#${i}`
}

onBeforeUnmount(() => {
  data.value = null
  historyError.value = null
})
</script>

<template>
  <div class="mh">
    <!-- ══ 权限兜底段 ══ -->
    <section v-if="!allowed" class="mh__sec" data-testid="mh-nope">
      <header class="mh__head">
        <h2 class="mh__h">{{ t('mh.title') }}</h2>
      </header>
      <!-- ★★ 这里没有 403 可显示 —— 后端对越权返 200 + 全租户数据。 -->
      <p class="mh__msg mh__msg--err" data-testid="mh-nope-msg">{{ t('mh.nope') }}</p>
      <p class="mh__msg" data-testid="mh-nope-scope">{{ t('mh.scope.note', { scope: scopeLabel }) }}</p>
      <p class="mh__msg" data-testid="mh-scope-legend">{{ t('mh.scope.legend') }}</p>
    </section>

    <template v-else>
      <!-- ══ 查询段 ══ -->
      <section class="mh__sec">
        <header class="mh__head">
          <h2 class="mh__h">{{ t('mh.query.title') }}</h2>
          <button
            type="button"
            class="mh__btn"
            :disabled="loading || !paramsOk"
            data-testid="mh-load"
            @click="load"
          >
            {{ data ? t('mh.reload') : t('mh.load') }}
          </button>
        </header>

        <div class="mh__row">
          <label class="mh__label">
            {{ t('mh.query.credentialId') }}
            <input
              v-model="credentialIdRaw"
              type="text"
              inputmode="numeric"
              class="mh__input"
              data-testid="mh-credentialId"
            />
          </label>
          <label class="mh__label">
            {{ t('mh.query.rawModelName') }}
            <input v-model="rawModelName" type="text" class="mh__input" data-testid="mh-rawModel" />
          </label>
        </div>

        <div class="mh__row">
          <label class="mh__label">
            {{ t('mh.query.limit', { min: MODEL_HISTORY_LIMIT_MIN, max: MODEL_HISTORY_LIMIT_MAX, def: MODEL_HISTORY_DEFAULT_LIMIT }) }}
            <input v-model="limitRaw" type="text" inputmode="numeric" class="mh__input" data-testid="mh-limit" />
          </label>
        </div>

        <p class="mh__msg" data-testid="mh-param-note">{{ t('mh.query.note') }}</p>

        <p v-if="!paramsOk" class="mh__msg mh__msg--err" data-testid="mh-params-bad">
          {{ t('mh.query.paramsBad', { min: MODEL_HISTORY_LIMIT_MIN, max: MODEL_HISTORY_LIMIT_MAX }) }}
        </p>

        <!-- ★★ 口径据实说明；这不是门，后端仍可能返回跨租户数据 -->
        <p class="mh__msg" data-testid="mh-scope-note">{{ t('mh.scope.note', { scope: scopeLabel }) }}</p>
        <p class="mh__msg" data-testid="mh-scope-legend">{{ t('mh.scope.legend') }}</p>

        <p v-if="historyError" class="mh__msg mh__msg--err" data-testid="mh-error">{{ historyError }}</p>
        <p v-if="loading" class="mh__msg">{{ t('common.loading') }}</p>
      </section>

      <!-- ══ 响应段 ══ -->
      <template v-if="data">
        <section class="mh__sec">
          <header class="mh__head">
            <h2 class="mh__h">{{ t('mh.meta.title') }}</h2>
          </header>

          <dl class="mh__kv">
            <div class="mh__kv-row">
              <dt>{{ t('mh.query.credentialId') }}</dt>
              <dd data-testid="mh-s-credential_id">{{ data.credential_id }}</dd>
            </div>
            <div class="mh__kv-row">
              <dt>{{ t('mh.query.rawModelName') }}</dt>
              <dd data-testid="mh-s-raw_model_name">{{ data.raw_model_name }}</dd>
            </div>
            <div class="mh__kv-row">
              <dt>{{ t('mh.col.count') }}</dt>
              <dd>
                <span data-testid="mh-s-count">{{ data.count }}</span>
                <span class="mh__muted" data-testid="mh-s-events-len">{{ t('mh.meta.lenNote') }}</span>
              </dd>
            </div>
          </dl>

          <p v-if="credentialEchoBad" class="mh__msg mh__msg--err" data-testid="mh-credential-echo-bad">
            {{ t('mh.meta.echoBad') }}
          </p>
          <p v-if="modelEchoBad" class="mh__msg mh__msg--err" data-testid="mh-model-echo-bad">
            {{ t('mh.meta.echoBad') }}
          </p>
          <p v-if="countMismatch" class="mh__msg mh__msg--err" data-testid="mh-count-mismatch">
            {{ t('mh.meta.countMismatch') }}
          </p>

          <!-- ★★ 恒等式：页面显式给出 count 与实际条数，让「扫失败的行查不到」这件事可见 -->
          <p class="mh__msg" data-testid="mh-count-relation">
            {{ t('mh.meta.countRelation', { count: data.count, len: data.events.length }) }}
          </p>

          <!-- ★★★★ (7) scan 失败被 continue 静默跳过，响应里**没有**这个字段 -->
          <p class="mh__msg mh__msg--warn" data-testid="mh-incomplete-note">
            {{ t('mh.meta.incomplete') }}
          </p>
          <!-- ★★ (6) ORDER BY ts DESC **无 tiebreak** -->
          <p class="mh__msg" data-testid="mh-order-note">{{ t('mh.meta.orderNote') }}</p>
          <p v-if="notNonAscending" class="mh__msg mh__msg--err" data-testid="mh-order-bad">
            {{ t('mh.meta.orderBad') }}
          </p>

          <p class="mh__msg" data-testid="mh-source-set">
            {{ t('mh.meta.sourceSet', { list: MODEL_HISTORY_SOURCES.join(' / ') }) }}
          </p>
          <p class="mh__msg" data-testid="mh-event-set">
            {{ t('mh.meta.eventSet', { auto: MODEL_HISTORY_AUTO_EVENTS.join(' / '), manual: MODEL_HISTORY_MANUAL_EVENTS.join(' / ') }) }}
          </p>
        </section>

        <section class="mh__sec">
          <header class="mh__head">
            <h2 class="mh__h">{{ t('mh.events.title') }}</h2>
          </header>

          <!-- ★ (14) events 是 make([]…, 0) ⇒ 空时是 [] 不是 null，可以确定说「没有事件」 -->
          <p v-if="data.events.length === 0" class="mh__msg" data-testid="mh-events-empty">
            {{ t('mh.events.empty') }}
          </p>

          <div v-else class="mh__col-head">
            <span>{{ t('mh.col.ts') }}</span>
            <span>{{ t('mh.col.source') }}</span>
          </div>

          <article
            v-for="(e, i) in data.events"
            :key="eventIndex(e, i)"
            class="mh__card"
            :data-testid="'mh-e-' + i"
            :data-source="e.source"
          >
            <div class="mh__row">
              <span class="mh__ts" :data-testid="'mh-e-' + i + '-ts'">{{ e.ts }}</span>
              <span class="mh__badge" :data-testid="'mh-e-' + i + '-source'">{{ e.source }}</span>
              <span class="mh__badge" :data-testid="'mh-e-' + i + '-event'">{{ e.event }}</span>
            </div>

            <!-- ★ (4) 的反面：两段形状都不满足就是破了 -->
            <p v-if="shapeBroken(e)" class="mh__msg mh__msg--err" :data-testid="'mh-e-' + i + '-shape-bad'">
              {{ t('mh.events.shapeBad') }}
            </p>
            <p v-if="kindUnknown(e)" class="mh__msg mh__msg--err" :data-testid="'mh-e-' + i + '-kind-bad'">
              {{ t('mh.events.kindBad') }}
            </p>

            <!-- ★★★ 这一行里恒为 null 的键：显式列出来，不让人以为「没这一项」 -->
            <p class="mh__msg" :data-testid="'mh-e-' + i + '-nulls'">
              {{ t('mh.events.nullsFilled', { keys: nullFilledKeys(e).join(' / ') || '—' }) }}
            </p>

            <dl class="mh__kv">
              <!-- ★★ (2) 键恒在，可为裸 null ⇒ 一律显示「未提供」而不是「键不存在」 -->
              <div class="mh__kv-row">
                <dt>triggered_by</dt>
                <dd :data-testid="'mh-e-' + i + '-triggered_by'">{{ nullableText(e.triggered_by) }}</dd>
              </div>
              <div class="mh__kv-row">
                <dt>probe_status</dt>
                <dd :data-testid="'mh-e-' + i + '-probe_status'">{{ nullableText(e.probe_status) }}</dd>
              </div>
              <div class="mh__kv-row">
                <dt>http_status</dt>
                <dd :data-testid="'mh-e-' + i + '-http_status'">{{ nullableNumber(e.http_status) }}</dd>
              </div>
              <div class="mh__kv-row">
                <dt>error_code</dt>
                <dd :data-testid="'mh-e-' + i + '-error_code'">{{ nullableText(e.error_code) }}</dd>
              </div>
              <div class="mh__kv-row">
                <dt>error_message</dt>
                <dd :data-testid="'mh-e-' + i + '-error_message'">{{ nullableText(e.error_message) }}</dd>
              </div>
              <!-- ★★ (3) actor 可以是空串，reason 不可能是空串 ⇒ 两种情况必须显示得不一样 -->
              <div class="mh__kv-row">
                <dt>actor</dt>
                <dd :data-testid="'mh-e-' + i + '-actor'">{{ nullableText(e.actor) }}</dd>
              </div>
              <div class="mh__kv-row">
                <dt>reason</dt>
                <dd :data-testid="'mh-e-' + i + '-reason'">{{ nullableText(e.reason) }}</dd>
              </div>
            </dl>
          </article>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.mh {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.mh__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.mh__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.mh__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.mh__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.mh__input {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.mh__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 160px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.mh__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.mh__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.mh__msg--err {
  color: var(--app-danger);
}

.mh__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.mh__muted {
  margin-left: var(--app-space-2);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.mh__kv {
  margin: 0;
}

.mh__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.mh__kv-row dt {
  color: var(--app-text-muted);
}

.mh__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.mh__col-head {
  display: flex;
  justify-content: space-between;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
  padding-bottom: 2px;
}

.mh__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.mh__ts {
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}

.mh__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>
