<script setup lang="ts">
// ModelIQView — 模型智商（Model IQ）：模型目录智商 + 节点智商 + 单节点历史
// （2026-10-14，第一百一十六批）。
//
// GET /api/admin/model-iq/catalog        → 每个 canonical 模型的标定智商 + 节点聚合
// GET /api/admin/model-iq/node-latest    → 各凭据节点的最新智商（可按 provider/canonical 过滤）
// GET /api/admin/model-iq/history        → 单个凭据+模型的历史测试点
//
// ⚠️ `POST /api/admin/model-iq/trigger` **不接**：它有外部副作用（真调
//   `modelQualityBackend`、真花 token）。本页三条**全是只读**。
//
// ── 权限 ────────────────────────────────────────────────────────────────────
// ★★★★ 权限档：**`requiresRole: 'super_admin'`**。
//   注册处 `admin/handler.go:1492-1495` **四条全部**是 `h.superAdmin(...)`。
//   逐字读过 `SuperAdminMiddleware`（`admin/auth.go:376-409`）：
//   ```go
//   if claims.Role != "super_admin" {
//       writeError(w, http.StatusForbidden, "super_admin role required for this endpoint")
//   ```
//   ⇒ 与批 113/114 的 `admin(...)` **形态相反**：那两条走 `AdminMiddleware`，
//     **只认证不检 role**（`users.go:307-309` 自陈是显式设计），
//     所以本仓才被迫推出 `requiresRole: 'admin'` 那一档。
//     本族走 `SuperAdminMiddleware` ⇒ role 是**逐字比对**出来的，
//     抽屉席设 `requiresRole` 即是安全意义上的完整处置。
//
//   ★★★ 与批 109 `FormatAnomaliesView.vue`「不需要任何段落级门控」那句的**关系**
//   （本批逐字核过 `hyper-wiring.ts` 的 `beforeEach`，两条都要说清）：
//   - **安全上**批 109 是对的：服务端中间件是真门，前端兜不兜都不改变「谁能看到数据」。
//   - **体验上**它留了个坑：`beforeEach` 只查 `to.meta.requiresAuth` 与
//     `auth.isAuthenticated`，**`meta.requiresRole` 全仓无人消费**
//     ⇒ 深链照样能打开页面 ⇒ tenant_admin 会看见一个**必然每个请求都 403** 的壳。
//   ⇒ 所以本页照批 113/114/115 加一层兜底。⚠️ 但要说清它兜的是**不发请求**，
//     不是数据访问 —— 那一层在服务端。
//   ⚠️ 前端严于后端是兜底不是修复。
//
// ★★★★★★ **本批的功能增量**：桌面端 `web/src/api/model-iq.ts` 导出了 4 个函数，
//   但全仓 grep 调用点只有 `ModelOfferExtrasPanel.vue` 用了
//   `getModelIQHistory` + `triggerModelIQTest`；
//   **`getCatalogIQ` 与 `getNodeIQLatest` 零调用方**（连绕过封装的直接 URL 调用都没有）
//   ⇒ 本页把 **catalog 与 node-latest 也做进 UI** ⇒ 移动端比桌面端多覆盖两个读端点。
//   ⇒ 桌面那侧把它们称为 "Backs the provider model list" 的是**注释**，不是事实。
//
// ★★★★★ 本族最要紧的五件事（文件头共 15 条，见 `api/modelIQ.ts`）：
//   1. ★★★★★ **三个端点的响应都是顶层裸数组**（不是 `{data:…}` 信封），
//      且 `out := []T{}` ⇒ **无匹配是 `[]` 不是 null** ⇒ 「空」有唯一形态。
//   2. ★★★★★ `catalog` 的 10 键里 **4 个可为裸 `null`**（`*float64` 无 omitempty）
//      ⇒ 页面显示 `—`，**不能**当 0 参与运算。
//   3. ★★★★★ **`node_count` 与三个聚合互为充要**：`node_count===0` ⇒
//      `node_avg/max/min` 全 null；`>0` ⇒ 全非 null。
//      ⇒ 本页把这条做成**可见的自检信号**，而不是默默相信。
//   4. ★★★★ `status` 可由 `accuracy`/`stability` **反推**（写入侧 `dbstorage.go:80-87`
//      算出来的）⇒ history 每行都能自校验；`stability` 被 COALESCE 成 0，
//      所以 NULL-stability 的行**看起来像 failed** —— 这是**二义**不是 bug。
//   5. ★★★★ **两个端点的 `tested_at` 不是同一种串**：node-latest 是 SQL
//      `to_char(…秒级…)`（恒 20 字符、恒无小数点、**可为 null**），
//      history 是 Go `time.Time` 的 RFC3339Nano（带亚秒、**恒不为 null**）。
//      ⇒ 本页 node 段**不显示秒以下**，也不因为 history 精确就以为同源。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchModelIQCatalog,
  fetchModelIQNodeLatest,
  fetchModelIQHistory,
  modelIQRoleIsAllowed,
  modelIQParamsAreAcceptable,
  modelIQBadRequestKind,
  modelIQForbiddenMessage,
  modelIQDbNotConfiguredMessage,
  modelIQCatalogAggregatesMatchCount,
  modelIQCatalogHasStandardOrNodes,
  modelIQCatalogIsNonIncreasing,
  modelIQCatalogStandardIqIsNull,
  modelIQNodeHasNoProvider,
  modelIQNodeTestedAtIsNull,
  modelIQNodeTestedAtIsSecondPrecision,
  modelIQNodeLatestIsNonIncreasing,
  modelIQHistoryIsNonIncreasing,
  modelIQHistoryStatusMatchesMetrics,
  modelIQStatusIsKnown,
  modelIQHistoryLimitIsAccepted,
  modelIQHistoryEffectiveLimit,
  modelIQHistoryStabilityIsZero,
  MODEL_IQ_HISTORY_DEFAULT_LIMIT,
  MODEL_IQ_HISTORY_MAX_LIMIT,
  type ModelIQCatalogRow,
  type ModelIQNodeLatest,
  type ModelIQHistoryPoint,
} from '@/api/modelIQ'

useHyperPage({ title: () => t('miq.title') })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 兜的是「不发请求」，不是数据访问 —— 真正的门在服务端中间件。

const role = computed(() => auth.role)
const allowed = computed(() => modelIQRoleIsAllowed(role.value))

// ══ 段①：模型智商目录（catalog） ═══════════════════════════════════════════
// ★ 无参数端点 ⇒ 进页面即可读。

const catalog = ref<ModelIQCatalogRow[]>([])
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)

/** ★★ 四个可空键显示成同一个占位符，且**不参与任何运算**。见 (2)。 */
const DASH = '—'

/** ★ (2) `standard_iq` 可为 null（即使有节点）—— 这是**正常形态**，不是异常。 */
function num(v: number | null): string {
  return v === null ? DASH : String(v)
}

/** ★★★ (3) `node_count` 与三个聚合互为充要；不成立就是**响应对不上 SQL 语义**。 */
const catalogBad = computed(() => catalog.value.filter((r) => !modelIQCatalogAggregatesMatchCount(r)))
/** ★★★ (3) WHERE 保证 `standard_iq` 与「有节点」至少一个成立。 */
const catalogEmptyBad = computed(() => catalog.value.filter((r) => !modelIQCatalogHasStandardOrNodes(r)))
/** ★★★★ 排序键是 COALESCE 后的值；不成立说明响应不是按那列排的。 */
const catalogOrderBad = computed(() => !modelIQCatalogIsNonIncreasing(catalog.value))
const catalogStandardNullCount = computed(
  () => catalog.value.filter((r) => modelIQCatalogStandardIqIsNull(r)).length,
)

// ══ 段②：节点智商（node-latest） ═══════════════════════════════════════════
// ★ 两个过滤参数**都是可选的**，且非法值会被后端 400（与 history 的 limit 相反）。

const providerIdRaw = ref('')
const canonicalIdRaw = ref('')

function parseOptional(v: string): number | undefined {
  const s = v.trim()
  if (s === '') return undefined
  const n = Number(s)
  return Number.isFinite(n) ? n : Number.NaN
}

const providerId = computed(() => parseOptional(providerIdRaw.value))
const canonicalId = computed(() => parseOptional(canonicalIdRaw.value))

/** ★★ 这两个参数非法会 **400**，与 history 的 limit「静默回落」正相反 ⇒ 本地挡。 */
const filterOk = computed(() => {
  const p = providerId.value
  const c = canonicalId.value
  if (p !== undefined && !(Number.isFinite(p) && p > 0)) return false
  if (c !== undefined && !(Number.isFinite(c) && c > 0)) return false
  return true
})

const nodes = ref<ModelIQNodeLatest[]>([])
const nodesLoading = ref(false)
const nodesError = ref<string | null>(null)

const nodesNoProvider = computed(() => nodes.value.filter((r) => modelIQNodeHasNoProvider(r)))
/** ★★ `tested_at` 为 null = 从未测过；这与「有值但精度不对」是**两件事**。 */
const nodesNeverTested = computed(() => nodes.value.filter((r) => modelIQNodeTestedAtIsNull(r)))
/** ★★★★★ 见 (15)：`to_char` 那一路恒 20 字符、恒无小数点。 */
const nodesBadPrecision = computed(() =>
  nodes.value.filter((r) => !modelIQNodeTestedAtIsNull(r) && !modelIQNodeTestedAtIsSecondPrecision(r)),
)
const nodesOrderBad = computed(() => !modelIQNodeLatestIsNonIncreasing(nodes.value))

// ══ 段③：单节点历史（history） ═════════════════════════════════════════════
// ★★ 两个参数**都是必填**；`limit` 非法**不报错**而是静默回落 50。见 (7)。

const credentialIdRaw = ref('')
const rawModelName = ref('')
const limitRaw = ref(String(MODEL_IQ_HISTORY_DEFAULT_LIMIT))

const credentialId = computed(() => {
  const s = credentialIdRaw.value.trim()
  return s === '' ? Number.NaN : Number(s)
})
const limit = computed(() => {
  const s = limitRaw.value.trim()
  return s === '' ? Number.NaN : Number(s)
})

const historyParamsOk = computed(() =>
  modelIQParamsAreAcceptable({
    credentialId: credentialId.value,
    rawModelName: rawModelName.value.trim(),
  }),
)

/** ★★★ (7) 非法 limit **不报错**，客户端无从得知是否被采纳 ⇒ 显式显示生效值。 */
const limitAccepted = computed(() => modelIQHistoryLimitIsAccepted(limit.value))
const effectiveLimit = computed(() => modelIQHistoryEffectiveLimit(limit.value))
const LIMIT_CHOICES = [MODEL_IQ_HISTORY_DEFAULT_LIMIT, 100, MODEL_IQ_HISTORY_MAX_LIMIT] as const

const history = ref<ModelIQHistoryPoint[]>([])
const historyLoading = ref(false)
const historyError = ref<string | null>(null)

/** ★★★★ (4) `status` 与 accuracy/stability 自洽（写入侧同源算出）⇒ 可自校验。 */
const historyStatusBad = computed(() => history.value.filter((p) => !modelIQHistoryStatusMatchesMetrics(p)))
/** ★★ 三值域之外的 status 要单独标出来，不能当正常渲染。 */
const historyStatusUnknown = computed(() => history.value.filter((p) => !modelIQStatusIsKnown(p)))
/** ★★ `stability===0` 二义（COALESCE 的 NULL 还是真 0）—— 只说「是 0」，不断言必 failed。 */
const historyZeroStability = computed(() => history.value.filter((p) => modelIQHistoryStabilityIsZero(p)))
const historyOrderBad = computed(() => !modelIQHistoryIsNonIncreasing(history.value))

/** ★★ 选中的节点（点行填参用）。★ 选中态用 `--app-primary`（本仓无 `--app-accent`）。 */
const pickedKey = ref<string | null>(null)

function nodeKey(n: ModelIQNodeLatest): string {
  return `${n.credential_id}::${n.raw_model_name}`
}

/**
 * ★★ 点节点行 → 填进历史查询并直接发。
 *
 * ⚠️ 这里**刻意不在模板内联回调里写 `xxx.value`**：`<script setup>` 的模板
 *   表达式中顶层 ref 会**自动解包**，写 `.value` 运行时就是 `undefined.value`
 *   （批 114 在 `NodesView.vue` 上踩过一次，204 个测试文件全绿、只有 `vue-tsc` 拦得住）。
 *   ⇒ 一律搬进 `<script setup>` 的函数里。
 */
function pickNode(n: ModelIQNodeLatest): void {
  pickedKey.value = nodeKey(n)
  credentialIdRaw.value = String(n.credential_id)
  rawModelName.value = n.raw_model_name
  void loadHistory()
}

// ══ 取数 ══════════════════════════════════════════════════════════════════

function describe(err: unknown): string {
  const msg = (err as Error)?.message || String(err)
  // ★★★ 403 只有一种来源（三个 handler 自己都不检 role）⇒ 可以直接归因。
  if (modelIQForbiddenMessage(msg)) return t('miq.err.forbidden')
  if (modelIQDbNotConfiguredMessage(msg)) return t('miq.err.dbNotConfigured')
  const kind = modelIQBadRequestKind(msg)
  if (kind !== 'unknown') return t(`miq.err.${kind}`)
  return msg
}

async function loadCatalog(): Promise<void> {
  if (!allowed.value) return
  catalogLoading.value = true
  catalogError.value = null
  try {
    catalog.value = await fetchModelIQCatalog()
  } catch (err) {
    catalog.value = []
    catalogError.value = describe(err)
  } finally {
    catalogLoading.value = false
  }
}

async function loadNodes(): Promise<void> {
  if (!allowed.value || !filterOk.value) return
  nodesLoading.value = true
  nodesError.value = null
  try {
    nodes.value = await fetchModelIQNodeLatest({
      providerId: providerId.value,
      canonicalId: canonicalId.value,
    })
  } catch (err) {
    nodes.value = []
    nodesError.value = describe(err)
  } finally {
    nodesLoading.value = false
  }
}

async function loadHistory(): Promise<void> {
  // ★ 兜底：不允许访问时**连请求都不发**；参数不合法也不发（history 会 400）。
  if (!allowed.value || !historyParamsOk.value) return
  historyLoading.value = true
  historyError.value = null
  try {
    history.value = await fetchModelIQHistory({
      credentialId: credentialId.value,
      rawModelName: rawModelName.value.trim(),
      limit: limit.value,
    })
  } catch (err) {
    history.value = []
    historyError.value = describe(err)
  } finally {
    historyLoading.value = false
  }
}

async function loadAll(): Promise<void> {
  await Promise.all([loadCatalog(), loadNodes()])
}
</script>

<template>
  <div class="miq">
    <!-- ★★ 深链兜底：不是后端 403，而是前端在**发请求之前**就挡住。 -->
    <section v-if="!allowed" class="miq__sec" data-testid="miq-nope">
      <header class="miq__head">
        <h2 class="miq__h">{{ t('miq.title') }}</h2>
      </header>
      <p class="miq__msg miq__msg--err" data-testid="miq-nope-msg">{{ t('miq.nope.msg') }}</p>
      <p class="miq__msg" data-testid="miq-nope-why">{{ t('miq.nope.why') }}</p>
    </section>

    <template v-else>
      <!-- ══ 全局操作 ══ -->
      <section class="miq__sec">
        <header class="miq__head">
          <h2 class="miq__h">{{ t('miq.load.title') }}</h2>
          <button
            type="button"
            class="miq__btn"
            data-testid="miq-load-all"
            :disabled="catalogLoading || nodesLoading"
            @click="loadAll"
          >
            {{ catalogLoading || nodesLoading ? t('common.loading') : t('miq.load.btn') }}
          </button>
        </header>
        <p class="miq__msg" data-testid="miq-scope-note">{{ t('miq.scope.note') }}</p>
      </section>

      <!-- ══ 段①：模型智商目录 ══ -->
      <section class="miq__sec">
        <header class="miq__head">
          <h2 class="miq__h">{{ t('miq.catalog.title') }}</h2>
          <span class="miq__badge" data-testid="miq-catalog-count">{{ catalog.length }}</span>
        </header>

        <p v-if="catalogError" class="miq__msg miq__msg--err" data-testid="miq-catalog-error">
          {{ catalogError }}
        </p>
        <p v-else-if="catalogLoading" class="miq__msg">{{ t('common.loading') }}</p>

        <template v-if="!catalogError">
          <p class="miq__msg" data-testid="miq-catalog-empty-note">{{ t('miq.catalog.emptyNote') }}</p>

          <!-- ★ 自检信号：(3) 聚合与 node_count 互为充要。 -->
          <p v-if="catalogBad.length" class="miq__msg miq__msg--err" data-testid="miq-catalog-bad">
            {{ t('miq.catalog.aggMismatch', { n: catalogBad.length }) }}
          </p>
          <p v-if="catalogEmptyBad.length" class="miq__msg miq__msg--err" data-testid="miq-catalog-empty-bad">
            {{ t('miq.catalog.bothEmpty', { n: catalogEmptyBad.length }) }}
          </p>
          <p v-if="catalogOrderBad" class="miq__msg miq__msg--err" data-testid="miq-catalog-order-bad">
            {{ t('miq.catalog.orderBad') }}
          </p>
          <!-- ★ 「该出现的内容」要单独钉，不能只靠否定式断言。 -->
          <p class="miq__msg" data-testid="miq-catalog-standard-null">
            {{ t('miq.catalog.standardNull', { n: catalogStandardNullCount }) }}
          </p>

          <p v-if="!catalogLoading && !catalog.length" class="miq__msg" data-testid="miq-catalog-empty">
            {{ t('miq.catalog.empty') }}
          </p>

          <ul v-else class="miq__list">
            <li v-for="row in catalog" :key="row.canonical_id" class="miq__card" :data-testid="`miq-catalog-row-${row.canonical_id}`">
              <div class="miq__row">
                <span class="miq__strong">{{ row.display_name || row.canonical_name }}</span>
                <span class="miq__badge">{{ num(row.standard_iq) }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.canonical') }}</span>
                <span class="miq__mono">{{ row.canonical_name }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.family') }}</span>
                <span class="miq__mono">{{ row.family }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.nodes') }}</span>
                <span class="miq__mono">{{ row.node_count }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.nodeAvg') }}</span>
                <span class="miq__mono">{{ num(row.node_avg_iq) }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.nodeRange') }}</span>
                <span class="miq__mono">{{ num(row.min_node_iq) }} / {{ num(row.max_node_iq) }}</span>
              </div>
              <p class="miq__ts">{{ t('miq.field.source') }}: {{ row.standard_iq_source }}</p>
            </li>
          </ul>
        </template>
      </section>

      <!-- ══ 段②：节点智商 ══ -->
      <section class="miq__sec">
        <header class="miq__head">
          <h2 class="miq__h">{{ t('miq.nodes.title') }}</h2>
          <button
            type="button"
            class="miq__btn"
            data-testid="miq-nodes-load"
            :disabled="nodesLoading"
            @click="loadNodes"
          >
            {{ nodesLoading ? t('common.loading') : t('miq.nodes.btn') }}
          </button>
        </header>

        <div class="miq__row">
          <label class="miq__label">
            {{ t('miq.field.providerId') }}
            <input
              v-model="providerIdRaw"
              type="text"
              inputmode="numeric"
              class="miq__input"
              data-testid="miq-provider-id"
            />
          </label>
          <label class="miq__label">
            {{ t('miq.field.canonicalId') }}
            <input
              v-model="canonicalIdRaw"
              type="text"
              inputmode="numeric"
              class="miq__input"
              data-testid="miq-canonical-id"
            />
          </label>
        </div>

        <p class="miq__msg" data-testid="miq-nodes-filter-note">{{ t('miq.nodes.filterNote') }}</p>
        <p v-if="!filterOk" class="miq__msg miq__msg--err" data-testid="miq-nodes-filter-bad">
          {{ t('miq.nodes.filterBad') }}
        </p>

        <p v-if="nodesError" class="miq__msg miq__msg--err" data-testid="miq-nodes-error">{{ nodesError }}</p>

        <template v-if="!nodesError">
          <p v-if="nodesNoProvider.length" class="miq__msg" data-testid="miq-nodes-no-provider">
            {{ t('miq.nodes.noProvider', { n: nodesNoProvider.length }) }}
          </p>
          <p v-if="nodesNeverTested.length" class="miq__msg" data-testid="miq-nodes-never-tested">
            {{ t('miq.nodes.neverTested', { n: nodesNeverTested.length }) }}
          </p>
          <!-- ★★★★★ 见 (15)：只有秒精度是**正常**形态，带小数点反而说明格式变了。 -->
          <p v-if="nodesBadPrecision.length" class="miq__msg miq__msg--err" data-testid="miq-nodes-bad-precision">
            {{ t('miq.nodes.badPrecision', { n: nodesBadPrecision.length }) }}
          </p>
          <p v-if="nodesOrderBad" class="miq__msg miq__msg--err" data-testid="miq-nodes-order-bad">
            {{ t('miq.nodes.orderBad') }}
          </p>

          <p v-if="!nodesLoading && !nodes.length" class="miq__msg" data-testid="miq-nodes-empty">
            {{ t('miq.nodes.empty') }}
          </p>

          <ul v-else class="miq__list">
            <li v-for="n in nodes" :key="nodeKey(n)" :data-testid="`miq-node-row-${n.credential_id}`">
              <button
                type="button"
                class="miq__pick"
                :class="{ 'miq__pick--on': pickedKey === nodeKey(n) }"
                :data-testid="`miq-node-pick-${n.credential_id}`"
                @click="pickNode(n)"
              >
                <div class="miq__row">
                  <span class="miq__strong">{{ n.credential_label || `#${n.credential_id}` }}</span>
                  <span class="miq__badge">{{ n.overall_score }}</span>
                </div>
                <div class="miq__list-row">
                  <span>{{ t('miq.field.model') }}</span>
                  <span class="miq__mono">{{ n.raw_model_name }}</span>
                </div>
                <div class="miq__list-row">
                  <span>{{ t('miq.field.provider') }}</span>
                  <span class="miq__mono">{{ n.provider_name || t('miq.nodes.unbound') }}</span>
                </div>
                <div class="miq__list-row">
                  <span>{{ t('miq.field.canonical') }}</span>
                  <span class="miq__mono">{{ n.canonical_name || DASH }}</span>
                </div>
                <div class="miq__list-row">
                  <span>{{ t('miq.field.range') }}</span>
                  <span class="miq__mono">{{ n.min_score }} / {{ n.avg_score }} / {{ n.max_score }}</span>
                </div>
                <div class="miq__list-row">
                  <span>{{ t('miq.field.samples') }}</span>
                  <span class="miq__mono">{{ n.sample_count }}</span>
                </div>
                <p class="miq__ts">
                  {{ t('miq.field.grade') }}: {{ n.grade || DASH }} ·
                  {{ t('miq.field.testedAt') }}:
                  {{ modelIQNodeTestedAtIsNull(n) ? t('miq.nodes.never') : n.tested_at }}
                </p>
              </button>
            </li>
          </ul>
        </template>
      </section>

      <!-- ══ 段③：单节点历史 ══ -->
      <section class="miq__sec">
        <header class="miq__head">
          <h2 class="miq__h">{{ t('miq.history.title') }}</h2>
          <button
            type="button"
            class="miq__btn"
            data-testid="miq-history-load"
            :disabled="historyLoading || !historyParamsOk"
            @click="loadHistory"
          >
            {{ historyLoading ? t('common.loading') : t('miq.history.btn') }}
          </button>
        </header>

        <div class="miq__row">
          <label class="miq__label">
            {{ t('miq.field.credentialId') }}
            <input
              v-model="credentialIdRaw"
              type="text"
              inputmode="numeric"
              class="miq__input"
              data-testid="miq-credential-id"
            />
          </label>
          <label class="miq__label">
            {{ t('miq.field.model') }}
            <input v-model="rawModelName" type="text" class="miq__input" data-testid="miq-raw-model" />
          </label>
        </div>

        <div class="miq__row">
          <button
            v-for="c in LIMIT_CHOICES"
            :key="c"
            type="button"
            class="miq__chip"
            :class="{ 'miq__chip--on': limitAccepted && limit === c }"
            :data-testid="`miq-limit-${c}`"
            @click="limitRaw = String(c)"
          >
            {{ c }}
          </button>
        </div>

        <p class="miq__msg" data-testid="miq-history-param-note">{{ t('miq.history.paramNote') }}</p>
        <!-- ★★★ (7) 非法 limit 静默回落，**不报错** ⇒ 页面必须自己说清生效值。 -->
        <p class="miq__msg" data-testid="miq-history-effective-limit">
          {{ t('miq.history.effective', { n: effectiveLimit }) }}
        </p>
        <p v-if="!historyParamsOk" class="miq__msg miq__msg--err" data-testid="miq-history-params-bad">
          {{ t('miq.history.paramsBad') }}
        </p>

        <p v-if="historyError" class="miq__msg miq__msg--err" data-testid="miq-history-error">{{ historyError }}</p>

        <template v-if="!historyError">
          <p v-if="historyStatusBad.length" class="miq__msg miq__msg--err" data-testid="miq-history-status-bad">
            {{ t('miq.history.statusMismatch', { n: historyStatusBad.length }) }}
          </p>
          <p v-if="historyStatusUnknown.length" class="miq__msg miq__msg--err" data-testid="miq-history-status-unknown">
            {{ t('miq.history.statusUnknown', { n: historyStatusUnknown.length }) }}
          </p>
          <p v-if="historyZeroStability.length" class="miq__msg" data-testid="miq-history-zero-stability">
            {{ t('miq.history.zeroStability', { n: historyZeroStability.length }) }}
          </p>
          <p v-if="historyOrderBad" class="miq__msg miq__msg--err" data-testid="miq-history-order-bad">
            {{ t('miq.history.orderBad') }}
          </p>

          <p v-if="!historyLoading && !history.length" class="miq__msg" data-testid="miq-history-empty">
            {{ t('miq.history.empty') }}
          </p>

          <ul v-else class="miq__list">
            <li
              v-for="(p, i) in history"
              :key="`${p.tested_at}-${i}`"
              class="miq__card"
              :data-testid="`miq-history-row-${i}`"
            >
              <div class="miq__row">
                <span class="miq__strong">{{ p.overall_score }}</span>
                <span class="miq__badge">{{ p.status }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.accuracy') }}</span>
                <span class="miq__mono">{{ p.accuracy }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.stability') }}</span>
                <span class="miq__mono">{{ p.stability }}</span>
              </div>
              <div class="miq__list-row">
                <span>{{ t('miq.field.latency') }}</span>
                <span class="miq__mono">{{ p.latency_p95 }}</span>
              </div>
              <p class="miq__ts">
                {{ t('miq.field.grade') }}: {{ p.grade || DASH }} ·
                {{ t('miq.field.probe') }}: {{ p.probe_kind }} ·
                {{ t('miq.field.trigger') }}: {{ p.trigger_kind }}
              </p>
              <p class="miq__ts">{{ p.tested_at }}</p>
            </li>
          </ul>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.miq {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.miq__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.miq__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.miq__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件一律 ≥48 CSS px。 */
.miq__btn,
.miq__input,
.miq__chip,
.miq__pick {
  min-height: 48px;
}

.miq__btn {
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.miq__input {
  width: 100%;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.miq__chip {
  padding: 0 14px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

/* ★ 选中态用 --app-primary —— 本仓**没有** --app-accent。 */
.miq__chip--on {
  color: var(--app-primary);
  border-color: var(--app-primary);
}

.miq__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 160px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.miq__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
  justify-content: space-between;
}

.miq__pick {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  width: 100%;
  padding: var(--app-space-3);
  text-align: left;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.miq__pick--on {
  border-color: var(--app-primary);
}

.miq__pick--on .miq__strong {
  color: var(--app-primary);
}

.miq__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.miq__msg--err {
  color: var(--app-danger);
}

.miq__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.miq__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.miq__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.miq__mono {
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.miq__strong {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text);
}

.miq__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}

.miq__ts {
  margin: 0;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
</style>