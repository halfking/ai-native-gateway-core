<script setup lang="ts">
// RequestActionsView — 请求动作时间线（第 118 批把第 74 批建的 API 模块接进 UI）。
//
// GET /api/admin/requests/{id}/actions
//
// 它是 SSE 动作流的 **REST 回退**：刷新页面或请求已结束时，没有这个端点就没有回放
// （`admin/request_actions.go:16-18` 自陈）。数据源是 `llmgw:live:actions` 这条
// 有界 LIST（`LPUSH` + `LTRIM 5000`）。
//
// ── 权限 ────────────────────────────────────────────────────────────────────
// ★★★★★★ `requiresRole: 'super_admin'` —— 归**第三类**（服务端**没有租户维度**），
//   与批 115 `sliding-window`、批 117 `opsOverview` 同处置。
//   四处 + 一处**对照**（本批逐字核过）：
//   ① 注册处 `admin/handler.go:1045` 的 `admin(...)` = `AdminMiddleware`
//      （`admin/auth.go:85-140`，**只认证不检 role**）；
//   ② `admin/request_actions.go` 全文**没有** `AuthContext` / `claims.Role` / `IsTenantAdmin`；
//   ③ 唯一的过滤是 `:62` 的 `ev.RequestID != requestID` 精确比较，而 `request_id`
//      来自 **URL 路径段**（`:35`），由 `middleware/requestid_mw.go:83-89` 生成
//      —— **16 字节 crypto/rand 的 hex**，全局唯一、**不含租户信息**；
//   ④ ★★ **对照**（决定性）：读**同一个** store 的姊妹端点 `/api/admin/live-stream`
//      在 hub 内部**算了租户作用域**（`live_stream_sse.go:2263-2268`）并逐帧过滤
//      （`:1875-1882`，非超管客户端只在 `env.Request.TenantID == c.tenantID` 时收帧）。
//      ⇒ 同一份数据、同一个注册包装，**一边过滤一边不过滤** ⇒ 这是**后端不一致**。
// ⇒ ⚠️ 前端严于后端是**兜底不是修复**：真修复在服务端（要么给本端点加租户条件，
//   要么把它并进 SSE 那条已有的作用域逻辑）。
//
// ★★★★ 本族最要紧的四件事（源文件头共 8 条，见 `api/requestActions.ts`）：
//   1. ★★★★★ **每行的键集不可穷举**：`flattenActionEvent` 把 `detail` 的每个键
//      **摊平到顶层**（`live_stream_lifecycle.go:107-114`，且**不覆盖**已有键）
//      ⇒ 解包器**只能**校验 4 个恒在键，绝不能 `requireKeys` 全量。
//   2. ★★★★★ `detail` 键**永远不出现**（`:108` 已 delete）；`credential_label`
//      在本端点**也永远不会出现**（调用时 `labels` 传的是 `nil`）—— 那是 SSE 才有的字段。
//   3. ★★★★ `action` 是**开放字符串**：后端 `decodeStoredAction` 只判 `!= ""`，
//      不校验枚举 ⇒ **未知取值不是异常**，UI 要能显示而不是拒绝。
//   4. ★★★★ 这是**短期回放不是历史**：`LTRIM 5000` ⇒ 更老的事件已被丢弃，
//      而响应里**没有任何字段**说明丢过 ⇒ 页面必须把这句说给用户。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchRequestActions,
  requestActionsRoleIsAllowed,
  requestActionsUnavailableReason,
  requestActionsIsMissingId,
  requestActionsMatchesRequest,
  requestActionsRowsEchoRequestId,
  requestActionsCountAgrees,
  requestActionsHasDetailKey,
  requestActionsHasCredentialLabel,
  requestActionsAscending,
  requestActionsSeqUnique,
  actionExtraKeys,
  actionIsKnown,
  GO_ZERO_TIME,
  type RequestActionsResponse,
  type RequestActionEntry,
} from '@/api/requestActions'

useHyperPage({ title: () => (allowed.value ? t('ra.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 兜的是「不发请求」，不是数据访问 —— 后端这一族**没有门**。

const role = computed(() => auth.role)
const allowed = computed(() => requestActionsRoleIsAllowed(role.value))

// ══ 取数 ══════════════════════════════════════════════════════════════════

const requestIdRaw = ref('')
const requestId = computed(() => requestIdRaw.value.trim())

const data = ref<RequestActionsResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

/** ★★ `null`（没取到）与「取到了但是空」必须分开显示，不能都当「无」。 */
const hasData = computed(() => data.value !== null)
const actions = computed(() => (data.value ? data.value.actions : []))
// ★ `v-if="hasData"` 不会让 `vue-tsc` 收窄 `data` ⇒ 模板里**不要**直接摸 `data.x`，
//   一律走这两个受守卫的 computed（批 117 在同一个区域踩过一次 null 解引用）。
const echoedId = computed(() => (data.value ? data.value.request_id : ''))
const actionCount = computed(() => (data.value ? data.value.count : 0))

/** ★★ 路径段为空时后端 400（`request_actions.go:36-38`）⇒ 本地挡，不发请求。 */
const paramsOk = computed(() => requestId.value !== '')

function describe(err: unknown): string {
  const msg = (err as Error)?.message || String(err)
  const status = Number((err as { status?: number }).status ?? 0)
  // ★ 502 是本仓少见的网关错误码（Redis 读失败），必须显式容纳，
  //   否则会被当成「未知错误」而丢掉它「上游 Redis 挂了」这层含义。
  const reason = requestActionsUnavailableReason(status, msg)
  if (reason === 'not_wired') return t('ra.err.notWired')
  if (reason === 'store_unavailable') return t('ra.err.storeUnavailable')
  if (requestActionsIsMissingId(status, msg)) return t('ra.err.missingId')
  return msg
}

async function load(): Promise<void> {
  // ★ 兜底：不允许访问时**连请求都不发**；路径段为空也不发（后端会 400）。
  if (!allowed.value || !paramsOk.value) return
  loading.value = true
  error.value = null
  try {
    data.value = await fetchRequestActions(requestId.value)
  } catch (err) {
    data.value = null
    error.value = describe(err)
  } finally {
    loading.value = false
  }
}

// ══ 自检信号 ══════════════════════════════════════════════════════════════
// ★ 每一条都能被变异打掉；「该出现」的面与红线分开写。

/** ★ 回显对不对：顶层 id 必须等于我请求的那个（后端原样回显，不 trim 不规范化）。 */
const echoBad = computed(() =>
  data.value && !requestActionsMatchesRequest(data.value, requestId.value),
)
/** ★★★★ 逐行的 `request_id` 也必须回显成同一个 —— 与上面是**两半**，都要查。 */
const rowEchoBad = computed(() => (data.value ? !requestActionsRowsEchoRequestId(data.value) : false))
/** ★ `count` 是服务端算的 `len(actions)`（`:80`）⇒ 不等就说明载荷被换过。 */
const countBad = computed(() => (data.value ? !requestActionsCountAgrees(data.value) : false))
/** ★★★ 见 1：`detail` 键出现就是契约漂了（后端 `:108` 已 delete）。 */
const detailKeyBad = computed(() => (data.value ? requestActionsHasDetailKey(data.value) : false))
/** ★★★ 见 2：`credential_label` 只在 SSE 那条路注入，本端点永远不该有。 */
const credentialLabelBad = computed(() =>
  data.value ? requestActionsHasCredentialLabel(data.value) : false,
)
/** ★★ 后端按 `(ts, seq)` 排过一次（`:73-75`）⇒ 升序不成立说明载荷被换过。 */
const orderBad = computed(() => (data.value ? !requestActionsAscending(data.value) : false))
/** ★★ 后端按 `seq` 去重（`:65-68`）⇒ 不唯一说明去重没生效。 */
const seqBad = computed(() => (data.value ? !requestActionsSeqUnique(data.value) : false))

// ── 「该出现」的那一面（不能只钉红线）─────────────────────────────────────

/** ★★★ 见 3：未知 `action` **不是异常**，要显示出来而不是拒收。 */
const unknownActions = computed(() =>
  actions.value.filter((a) => !actionIsKnown(a)).length,
)
/** ★★ 见 4：零值 `ts` 会排到最前，顺序会看着反常 —— 要单独说。 */
const zeroTsCount = computed(() => actions.value.filter((a) => a.ts === GO_ZERO_TIME).length)
/** ★★ 摊平上来的额外键（不在四个恒在键、也不在已知条件键里）。 */
const extraKeys = computed(() => {
  const s = new Set<string>()
  for (const a of actions.value) for (const k of actionExtraKeys(a)) s.add(k)
  return [...s].sort()
})

function seqOf(a: RequestActionEntry): string {
  return String(a.seq)
}
</script>

<template>
  <div class="ra">
    <!-- ★★ 深链兜底：后端这一族**没有门**，所以是前端在**发请求之前**就挡住。 -->
    <section v-if="!allowed" class="ra__sec" data-testid="ra-nope">
      <header class="ra__head">
        <h2 class="ra__h">{{ t('ra.title') }}</h2>
      </header>
      <p class="ra__msg ra__msg--err" data-testid="ra-nope-msg">{{ t('ra.nope.msg') }}</p>
      <p class="ra__msg" data-testid="ra-nope-why">{{ t('ra.nope.why') }}</p>
    </section>

    <template v-else>
      <!-- ══ 全局操作 ══ -->
      <section class="ra__sec">
        <header class="ra__head">
          <h2 class="ra__h">{{ t('ra.load.title') }}</h2>
          <button
            type="button"
            class="ra__btn"
            data-testid="ra-load"
            :disabled="loading || !paramsOk"
            @click="load"
          >
            {{ loading ? t('common.loading') : t('ra.load.btn') }}
          </button>
        </header>

        <label class="ra__label">
          {{ t('ra.field.requestId') }}
          <input
            v-model="requestIdRaw"
            type="text"
            class="ra__input"
            data-testid="ra-request-id"
            @keyup.enter="load"
          />
        </label>

        <p class="ra__msg" data-testid="ra-scope-note">{{ t('ra.scope.note') }}</p>
        <p class="ra__msg" data-testid="ra-replay-note">{{ t('ra.scope.replay') }}</p>
        <p v-if="!paramsOk" class="ra__msg ra__msg--err" data-testid="ra-params-bad">
          {{ t('ra.scope.paramsBad') }}
        </p>

        <p v-if="error" class="ra__msg ra__msg--err" data-testid="ra-error">{{ error }}</p>

        <p v-if="!hasData && !error && !loading" class="ra__msg" data-testid="ra-empty">
          {{ t('ra.scope.empty') }}
        </p>
      </section>

      <!-- ══ 自检段（响应级不变式，放这里而不是塞进某一行里）═════════════ -->
      <section v-if="hasData" class="ra__sec">
        <header class="ra__head">
          <h2 class="ra__h">{{ t('ra.check.title') }}</h2>
          <span class="ra__badge" data-testid="ra-count">{{ actionCount }}</span>
        </header>

        <div class="ra__row">
          <span>{{ t('ra.field.echo') }}</span>
          <span class="ra__mono" data-testid="ra-echo">{{ echoedId }}</span>
        </div>

        <p v-if="echoBad" class="ra__msg ra__msg--err" data-testid="ra-echo-bad">
          {{ t('ra.check.echoBad') }}
        </p>
        <p v-if="rowEchoBad" class="ra__msg ra__msg--err" data-testid="ra-row-echo-bad">
          {{ t('ra.check.rowEchoBad') }}
        </p>
        <p v-if="countBad" class="ra__msg ra__msg--err" data-testid="ra-count-bad">
          {{ t('ra.check.countBad') }}
        </p>
        <p v-if="detailKeyBad" class="ra__msg ra__msg--err" data-testid="ra-detail-key-bad">
          {{ t('ra.check.detailBad') }}
        </p>
        <p v-if="credentialLabelBad" class="ra__msg ra__msg--err" data-testid="ra-credential-label-bad">
          {{ t('ra.check.credentialLabelBad') }}
        </p>
        <p v-if="orderBad" class="ra__msg ra__msg--err" data-testid="ra-order-bad">
          {{ t('ra.check.orderBad') }}
        </p>
        <p v-if="seqBad" class="ra__msg ra__msg--err" data-testid="ra-seq-bad">
          {{ t('ra.check.seqBad') }}
        </p>

        <!-- ★★★ 「该出现」的面：这三行不许被红线吃掉。 -->
        <p class="ra__msg" data-testid="ra-unknown-actions">
          {{ t('ra.check.unknownActions', { n: unknownActions }) }}
        </p>
        <p v-if="zeroTsCount" class="ra__msg" data-testid="ra-zero-ts">
          {{ t('ra.check.zeroTs', { n: zeroTsCount }) }}
        </p>
        <p class="ra__msg" data-testid="ra-extra-keys">
          {{ t('ra.check.extraKeys', { list: extraKeys.length ? extraKeys.join(' / ') : t('ra.check.none') }) }}
        </p>
      </section>

      <!-- ══ 时间线 ══ -->
      <section v-if="hasData" class="ra__sec">
        <header class="ra__head">
          <h2 class="ra__h">{{ t('ra.timeline.title') }}</h2>
          <span class="ra__badge" data-testid="ra-actions-count">{{ actions.length }}</span>
        </header>

        <p v-if="!actions.length" class="ra__msg" data-testid="ra-actions-empty">
          {{ t('ra.timeline.empty') }}
        </p>

        <ul v-else class="ra__list">
          <li
            v-for="(a, i) in actions"
            :key="`${a.seq}-${i}`"
            class="ra__card"
            :data-testid="`ra-row-${seqOf(a)}`"
          >
            <div class="ra__row">
              <span class="ra__strong">{{ a.action }}</span>
              <!-- ★★★ 未知取值**照样显示**（后端只判非空，不校验枚举）。 -->
              <span v-if="!actionIsKnown(a)" class="ra__tag" data-testid="ra-unknown-tag">
                {{ t('ra.timeline.unknownTag') }}
              </span>
              <span class="ra__badge">#{{ a.seq }}</span>
            </div>

            <div v-if="'model' in a" class="ra__list-row">
              <span>{{ t('ra.field.model') }}</span>
              <span class="ra__mono">{{ a.model }}</span>
            </div>
            <div v-if="'credential_id' in a" class="ra__list-row">
              <span>{{ t('ra.field.credentialId') }}</span>
              <span class="ra__mono">{{ a.credential_id }}</span>
            </div>
            <div v-if="'error_kind' in a" class="ra__list-row">
              <span>{{ t('ra.field.errorKind') }}</span>
              <span class="ra__mono">{{ a.error_kind }}</span>
            </div>
            <div v-if="'retry_seq' in a" class="ra__list-row">
              <span>{{ t('ra.field.retrySeq') }}</span>
              <span class="ra__mono">{{ a.retry_seq }}</span>
            </div>

            <!-- ★★ 摊平上来的额外键：必须显示，否则用户看不到 detail 里的上下文。 -->
            <p v-if="actionExtraKeys(a).length" class="ra__ts" :data-testid="`ra-extra-${seqOf(a)}`">
              {{ actionExtraKeys(a).join(' / ') }}
            </p>
            <p class="ra__ts">{{ a.ts }}</p>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.ra {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.ra__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.ra__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.ra__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件一律 ≥48 CSS px。 */
.ra__btn,
.ra__input {
  min-height: 48px;
}

.ra__btn {
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.ra__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.ra__input {
  width: 100%;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.ra__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.ra__msg--err {
  color: var(--app-danger);
}

.ra__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.ra__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.ra__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
  justify-content: space-between;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.ra__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.ra__mono {
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.ra__strong {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text);
}

.ra__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}

/* ★ 未知动作标记用 --app-primary —— 本仓**没有** --app-accent。 */
.ra__tag {
  font-size: 0.6875rem;
  font-weight: 600;
  color: var(--app-primary);
}

.ra__ts {
  margin: 0;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
</style>