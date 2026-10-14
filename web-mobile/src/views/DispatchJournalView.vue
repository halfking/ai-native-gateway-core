<script setup lang="ts">
// DispatchJournalView — 单请求调度轨迹快照（2026-10-14，第一百一十二批）。
//
// GET /api/admin/dispatch/journal/{tenant}/{request_id}
//
// 权限：★★ **admin 档**（不是 superAdmin！）—— 可执行注册处
//   `cmd/gateway/main.go:7143-7144`：
//   ```go
//   journalSnapshotAPI := admin.NewJournalSnapshotAPI(journalSnapshotStore)
//   journalSnapshotAPI.RegisterRoutes(mux, requestJourneyWrapAdmin)   // newWrapAdmin → AdminMiddleware
//   ```
//   ⇒ `admin/journal_handlers.go:30-35` ⇒ 抽屉席**不设** `requiresRole`
//     （tenant_admin 也够用 ⇒ **不得**进 `AppDrawer.spec.ts` 的 superAdmin 白名单）。
//   ⚠️ 这与批 109/110/111 连续三批的 superAdmin 相反 ⇒ 别照抄那一套。
//
// ★★★★★★ 本族是本仓**第一个真正做租户鉴权的读端点** —— 权限是「角色 × 路径里的租户」
//   **二维**的（`admin/journal_handlers.go:59-67`）：
//   ```go
//   privileged := auth.Role == "super_admin" || auth.Role == "admin_key"
//   if !privileged && auth.Role != "tenant_admin" { 404 }
//   if !privileged && callerTenant != tenantID     { 404 }   // 租户键取自 JWT，不是 URL
//   ```
//   ⇒ 与批 75/76/77/104/106/109/110 的「读路径不隔离」**完全相反**：
//     那些是「superAdmin/admin 档 + 不过滤租户」，本族是「admin 档 + handler 内过滤」。
//   ⇒ ⇒ 所以本页**也不需要**任何段落级门控，但理由不同：不是「档位挡住了跨租户」
//     而是「**后端自己按调用方租户挡了**」。
//   ★ 下拉里的租户框只是**便利**（从 JWT 预填），**不是安全门** —— 后端仍会 404。
//
// ★★★★★ 本族最要紧的五件事（详见 api/dispatchJournal.ts 文件头 15 条）：
//   1. ★★★★★ **`truncated === false` 不等于「轨迹完整」** —— 截断是**两段式**的，
//      而只有第二段被报告：第一段是每请求的环形缓冲（上限 128，`journal.go:137-140`，
//      满了从**头部**丢最旧的），**这个丢弃没有任何标记**。
//   2. ★★★★★ **`entries` 为空是**二义**的**（`dispatchJournalEmptyEntriesAreAmbiguous`）：
//      「这个请求没有重试/切换」与「快照被 LRU 淘汰 / 进程重启冲掉了」返回**同一个 200**。
//   3. ★★★★ **`404` 有七种成因、文案完全相同**（认证缺失 / 角色不对 / 跨租户 /
//      快照不存在 / 路径与快照不符 / consumer 三字段任一为空 / consumer 第二道闸）
//      ⇒ ★★ 本族**根本没有 403 这条路**（连普通用户都是 404，不是 403）。
//   4. ★★★★ **12 个可选键都是「值类型 + 无指针 + omitempty」**
//      ⇒ ★★ **值为 0 与「不存在」在响应里不可区分**（与批 110 的指针族方向相反）。
//   5. ★★★★ 快照是**进程内 LRU**（`NewInMemoryJournalStore(10000)`），重启即全失；
//      且 **`ConsumeSnapshot` 会 `moveToFrontLocked` ⇒ 读操作有副作用**（续命）。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchDispatchJournal,
  dispatchJournalParamsAreAcceptable,
  dispatchJournalTruncatedFillsWindow,
  dispatchJournalTruncatedCountMatchesFlag,
  dispatchJournalLeadingEntriesWereDropped,
  dispatchJournalEndsWithTerminal,
  dispatchJournalSeqIsContiguous,
  dispatchJournalSeqStartsAtOne,
  dispatchJournalVersionMatchesLastSeq,
  dispatchJournalAttemptIsNonDecreasing,
  dispatchJournalCountsAreCumulative,
  dispatchJournalEntryIsTerminal,
  dispatchJournalEmptyEntriesAreAmbiguous,
  dispatchJournalOptionalNumberIsPresent,
  DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES,
  DISPATCH_JOURNAL_RING_CAPACITY,
  DISPATCH_JOURNAL_TERMINAL_ACTIONS,
  type DispatchJournalSnapshot,
  type DispatchJournalEntry,
} from '@/api/dispatchJournal'

useHyperPage({ title: () => t('dj.title') })

const auth = useAuthStore()

// ══ 查询条件 ════════════════════════════════════════════════════════════

/**
 * ★ 租户框从 JWT **预填**（便利，不是安全门）—— 后端仍会按调用方租户 404。
 *   判据里钉住「换角色/换租户后渲染不变」，因为本视图**不该**有任何角色分支。
 */
const tenantId = ref<string>(auth.userInfo?.tenant_id ?? '')
const requestId = ref('')

/** ★★★ 两段都合法才值得发 —— 否则必然 400 `invalid journal path`（`:96-117`）。 */
const paramsOk = computed(() => dispatchJournalParamsAreAcceptable(tenantId.value, requestId.value))

const snap = ref<DispatchJournalSnapshot | null>(null)
const journalError = ref<string | null>(null)
const loading = ref(false)

async function load(): Promise<void> {
  loading.value = true
  journalError.value = null
  try {
    snap.value = await fetchDispatchJournal({ tenantId: tenantId.value, requestId: requestId.value })
  } catch (err) {
    snap.value = null
    journalError.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 八条可自验不变式 ═════════════════════════════════════════════════════
// ★★ 全是告警类判据 ⇒ **每条都必须配正控**（判「正常时不得误报」），
//   详见 .spec.ts 的 ③ 组。

const truncatedBad = computed(() => (snap.value ? !dispatchJournalTruncatedFillsWindow(snap.value) : false))
const truncatedCountBad = computed(() =>
  snap.value ? !dispatchJournalTruncatedCountMatchesFlag(snap.value) : false,
)
/** ★★ 第一段（环形）的丢弃是**隐形的**：从 seq 起点不是 1 就能看出来。 */
const leadingDropped = computed(() => (snap.value ? dispatchJournalLeadingEntriesWereDropped(snap.value) : false))
const terminalBad = computed(() => (snap.value ? !dispatchJournalEndsWithTerminal(snap.value) : false))
const seqContiguousBad = computed(() => (snap.value ? !dispatchJournalSeqIsContiguous(snap.value) : false))
const seqStartsBad = computed(() => (snap.value ? !dispatchJournalSeqStartsAtOne(snap.value) : false))
const versionBad = computed(() => (snap.value ? !dispatchJournalVersionMatchesLastSeq(snap.value) : false))
const attemptBad = computed(() => (snap.value ? !dispatchJournalAttemptIsNonDecreasing(snap.value) : false))
const countsBad = computed(() => (snap.value ? !dispatchJournalCountsAreCumulative(snap.value) : false))

// ══ 判读 ════════════════════════════════════════════════════════════════

/** ★★★★ 空 `entries` 是**二义**的，不是「这个请求很干净」。 */
const entriesAmbiguous = computed(() => (snap.value ? dispatchJournalEmptyEntriesAreAmbiguous(snap.value) : false))

/** ★★ 响应里的 `tenant_id` / `request_id` 恒等于路径里那两段（后端 `:83-86` 校验过）。 */
const tenantEchoMatches = computed(() => (snap.value ? snap.value.tenant_id === tenantId.value.trim() : true))
const requestEchoMatches = computed(() => (snap.value ? snap.value.request_id === requestId.value.trim() : true))

/**
 * ★★ 值类型 + `omitempty` ⇒ **值为 0 与「键不存在」不可区分**。
 *   ⇒ 页面只能说「未提供（键缺失或值为 0）」，**不能**说「是 0」也不能说「没有」。
 */
function optionalNumberText(e: DispatchJournalEntry, key: string): string {
  return dispatchJournalOptionalNumberIsPresent(e, key) ? String(e[key as 'http_status']) : t('dj.row.optionalGone')
}

function actionText(e: DispatchJournalEntry): string {
  return dispatchJournalEntryIsTerminal(e) ? `${e.action} ${t('dj.row.terminal')}` : e.action
}

onBeforeUnmount(() => {
  snap.value = null
  journalError.value = null
})
</script>

<template>
  <div class="dj">
    <!-- ══ 查询段 ══ -->
    <section class="dj__sec">
      <header class="dj__head">
        <h2 class="dj__h">{{ t('dj.query.title') }}</h2>
        <button
          type="button"
          class="dj__btn"
          :disabled="loading || !paramsOk"
          data-testid="dj-load"
          @click="load"
        >
          {{ snap ? t('dj.reload') : t('dj.load') }}
        </button>
      </header>

      <div class="dj__row">
        <label class="dj__label">
          {{ t('dj.query.tenant') }}
          <input v-model="tenantId" type="text" class="dj__input" data-testid="dj-tenant" />
        </label>
        <label class="dj__label">
          {{ t('dj.query.requestId') }}
          <input v-model="requestId" type="text" class="dj__input" data-testid="dj-request" />
        </label>
      </div>

      <!-- ★★★ 两段都必须合法：含 `/` `\` NUL 或 `.` `..` 必然 400 -->
      <p v-if="!paramsOk && (tenantId.trim() !== '' || requestId.trim() !== '')" class="dj__msg dj__msg--err" data-testid="dj-params-bad">
        {{ t('dj.query.paramsBad') }}
      </p>

      <!-- ★★ 权限是「角色 × 路径租户」二维的，越权返 **404 而不是 403** -->
      <p class="dj__msg" data-testid="dj-permission-note">
        {{ t('dj.query.permissionNote') }}
      </p>

      <p v-if="journalError" class="dj__msg dj__msg--err" data-testid="dj-error">{{ journalError }}</p>
      <p v-if="loading" class="dj__msg">{{ t('common.loading') }}</p>
    </section>

    <!-- ══ 快照段 ══ -->
    <template v-if="snap">
      <section class="dj__sec">
        <header class="dj__head">
          <h2 class="dj__h">{{ t('dj.snap.title') }}</h2>
        </header>

        <p class="dj__msg" data-testid="dj-snap-keys">{{ t('dj.snap.keys', { tenant: snap.tenant_id, request: snap.request_id }) }}</p>
        <p v-if="!tenantEchoMatches" class="dj__msg dj__msg--err" data-testid="dj-tenant-echo-bad">
          {{ t('dj.snap.echoBad') }}
        </p>
        <p v-if="!requestEchoMatches" class="dj__msg dj__msg--err" data-testid="dj-request-echo-bad">
          {{ t('dj.snap.echoBad') }}
        </p>

        <dl class="dj__kv">
          <div class="dj__kv-row">
            <dt>{{ t('dj.col.entries') }}</dt>
            <dd :data-testid="'dj-s-entries'">{{ snap.entries.length }}</dd>
          </div>
          <div class="dj__kv-row">
            <dt>{{ t('dj.col.truncated') }}</dt>
            <dd :data-testid="'dj-s-truncated'">{{ snap.truncated ? t('dj.snap.truncatedYes') : t('dj.snap.truncatedNo') }}</dd>
          </div>
          <div class="dj__kv-row">
            <dt>{{ t('dj.col.truncatedCount') }}</dt>
            <dd>
              <span :data-testid="'dj-s-truncated_count'">{{ snap.truncated_count }}</span>
              <span v-if="truncatedCountBad" class="dj__danger" data-testid="dj-truncatedcount-bad">
                ★ {{ t('dj.snap.truncatedCountBad') }}
              </span>
            </dd>
          </div>
          <div class="dj__kv-row">
            <dt>{{ t('dj.col.version') }}</dt>
            <dd>
              <span :data-testid="'dj-s-version'">{{ snap.snapshot_version }}</span>
              <span v-if="versionBad" class="dj__danger" data-testid="dj-version-bad">
                ★ {{ t('dj.snap.versionBad') }}
              </span>
            </dd>
          </div>
        </dl>

        <!-- ★★★ 两段式截断：truncated 只覆盖第二段，第一段（环形 128）隐形 -->
        <p class="dj__msg" data-testid="dj-two-stage">
          {{ t('dj.snap.twoStage', { ring: DISPATCH_JOURNAL_RING_CAPACITY, window: DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES }) }}
        </p>
        <!-- ★★ 终态动作是**封闭三值**，这里显式列出来，让「末条是终态」这条不变式可核对 -->
        <p class="dj__msg" data-testid="dj-terminal-set">
          {{ t('dj.snap.terminalSet', { actions: DISPATCH_JOURNAL_TERMINAL_ACTIONS.join(' / ') }) }}
        </p>
        <p v-if="leadingDropped" class="dj__msg dj__msg--warn" data-testid="dj-leading-dropped">
          {{ t('dj.snap.leadingDropped') }}
        </p>
        <p v-if="truncatedBad" class="dj__msg dj__msg--err" data-testid="dj-truncated-bad">
          {{ t('dj.snap.truncatedBad', { n: DISPATCH_JOURNAL_SNAPSHOT_MAX_ENTRIES }) }}
        </p>

        <!-- ★ 八条可自验不变式的违例提示 -->
        <p v-if="seqStartsBad" class="dj__msg dj__msg--err" data-testid="dj-seqstart-bad">
          {{ t('dj.snap.seqStartBad') }}
        </p>
        <p v-if="seqContiguousBad" class="dj__msg dj__msg--err" data-testid="dj-seq-bad">
          {{ t('dj.snap.seqBad') }}
        </p>
        <p v-if="terminalBad" class="dj__msg dj__msg--err" data-testid="dj-terminal-bad">
          {{ t('dj.snap.terminalBad') }}
        </p>
        <p v-if="attemptBad" class="dj__msg dj__msg--err" data-testid="dj-attempt-bad">
          {{ t('dj.snap.attemptBad') }}
        </p>
        <p v-if="countsBad" class="dj__msg dj__msg--err" data-testid="dj-counts-bad">
          {{ t('dj.snap.countsBad') }}
        </p>

        <!-- ★★★★ 空 entries 是二义的，不是「这个请求很干净」 -->
        <p v-if="entriesAmbiguous" class="dj__msg dj__msg--warn" data-testid="dj-empty-ambiguous">
          {{ t('dj.snap.emptyAmbiguous') }}
        </p>

        <div v-if="snap.entries.length > 0" class="dj__col-head">
          <span>{{ t('dj.col.action') }}</span>
          <span>{{ t('dj.col.attempt') }}</span>
          <span>{{ t('dj.col.counts') }}</span>
        </div>

        <article
          v-for="e in snap.entries"
          :key="e.seq"
          class="dj__card"
          :data-testid="'dj-e-' + e.seq"
        >
          <div class="dj__row">
            <span class="dj__seq">{{ e.seq }}</span>
            <span class="dj__action" :data-testid="'dj-e-' + e.seq + '-action'">{{ actionText(e) }}</span>
            <span class="dj__badge" :data-testid="'dj-e-' + e.seq + '-attempt'">{{ e.attempt }}</span>
          </div>
          <p class="dj__msg">{{ e.at }}</p>
          <dl class="dj__kv">
            <!-- ★★ counts 五键恒在（无 omitempty），非递减 -->
            <div class="dj__kv-row">
              <dt>{{ t('dj.counts.retries') }}</dt><dd>{{ e.counts.retries }}</dd>
            </div>
            <div class="dj__kv-row">
              <dt>{{ t('dj.counts.nodeSwitches') }}</dt><dd>{{ e.counts.node_switches }}</dd>
            </div>
            <div class="dj__kv-row">
              <dt>{{ t('dj.counts.modelSwitches') }}</dt><dd>{{ e.counts.model_switches }}</dd>
            </div>
            <div class="dj__kv-row">
              <dt>{{ t('dj.counts.capacityWaits') }}</dt><dd>{{ e.counts.capacity_waits }}</dd>
            </div>
            <div class="dj__kv-row">
              <dt>{{ t('dj.counts.scheduledWaits') }}</dt><dd>{{ e.counts.scheduled_waits }}</dd>
            </div>
            <div v-if="e.model !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.model') }}</dt><dd>{{ e.model }}</dd>
            </div>
            <div v-if="e.vendor !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.vendor') }}</dt><dd>{{ e.vendor }}</dd>
            </div>
            <div v-if="e.error_kind !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.errorKind') }}</dt><dd>{{ e.error_kind }}</dd>
            </div>
            <!-- ★★★ 值为 0 与「键缺失」不可区分 ⇒ 一律说「未提供」 -->
            <div class="dj__kv-row">
              <dt>{{ t('dj.col.httpStatus') }}</dt>
              <dd :data-testid="'dj-e-' + e.seq + '-http_status'">{{ optionalNumberText(e, 'http_status') }}</dd>
            </div>
            <div class="dj__kv-row">
              <dt>{{ t('dj.col.credentialId') }}</dt>
              <dd :data-testid="'dj-e-' + e.seq + '-credential_id'">{{ optionalNumberText(e, 'credential_id') }}</dd>
            </div>
            <div class="dj__kv-row">
              <dt>{{ t('dj.col.providerId') }}</dt>
              <dd :data-testid="'dj-e-' + e.seq + '-provider_id'">{{ optionalNumberText(e, 'provider_id') }}</dd>
            </div>
          </dl>
          <!-- ★ 切换类字段：只在该条目真的是「切换」时出现 -->
          <dl v-if="e.from_model !== undefined || e.to_model !== undefined" class="dj__kv">
            <div v-if="e.from_model !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.fromModel') }}</dt><dd>{{ e.from_model }}</dd>
            </div>
            <div v-if="e.to_model !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.toModel') }}</dt><dd>{{ e.to_model }}</dd>
            </div>
            <div v-if="e.from_credential_id !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.fromCredential') }}</dt><dd>{{ optionalNumberText(e, 'from_credential_id') }}</dd>
            </div>
            <div v-if="e.to_credential_id !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.toCredential') }}</dt><dd>{{ optionalNumberText(e, 'to_credential_id') }}</dd>
            </div>
            <div v-if="e.from_provider_id !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.fromProvider') }}</dt><dd>{{ optionalNumberText(e, 'from_provider_id') }}</dd>
            </div>
            <div v-if="e.to_provider_id !== undefined" class="dj__kv-row">
              <dt>{{ t('dj.col.toProvider') }}</dt><dd>{{ optionalNumberText(e, 'to_provider_id') }}</dd>
            </div>
          </dl>
        </article>
      </section>
    </template>
  </div>
</template>

<style scoped>
.dj {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.dj__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.dj__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.dj__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.dj__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.dj__input {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.dj__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 140px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.dj__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.dj__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.dj__msg--err {
  color: var(--app-danger);
}

.dj__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.dj__danger {
  color: var(--app-danger);
}

.dj__kv {
  margin: 0;
}

.dj__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.dj__kv-row dt {
  color: var(--app-text-muted);
}

.dj__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.dj__col-head {
  display: flex;
  justify-content: space-between;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
  padding-bottom: 2px;
}

.dj__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.dj__seq {
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}

.dj__action {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text);
}

.dj__badge {
  margin-left: auto;
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-variant-numeric: tabular-nums;
}
</style>