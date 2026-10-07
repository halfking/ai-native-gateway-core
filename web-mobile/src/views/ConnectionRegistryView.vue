<script setup lang="ts">
// ConnectionRegistryView — 流式连接注册台（live + 注销审计）（2026-10-08，第一百零七批）。
//
// GET /api/admin/connection-registry            → 手写 4 键 map
// GET /api/admin/connection-registry/{request_id} → 裸 snapshot（非信封）
//
// 权限：**admin 档**（`admin/handler.go:1024-1025` 注册是 `admin(...)`），
//   tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//   ⚠️ 桌面把这条路由标成 `requiresSuper: true`（`web/src/router.ts:284`）/
//      `super: true`（`web/src/config/appNav.ts:195`）—— **前端比后端严**，
//      按纪律以可执行注册为准。
//   ★ 数据源是**进程内**状态（`domains/streaming.ConnectionRegistry`），
//     不是数据库 ⇒ 本族**没有跨租户查询**，也**没有**租户隔离问题。
//     （对比批 104/106 那两个真正跨租户的族。）
//
// ★★★★ 本页最要紧的七件事（详见 api/connectionRegistry.ts 文件头）：
//   1. ★★★★★★ **`live` 段里可能出现 `closed: true` 的行** —— 一个真实竞态窗口：
//      `WriteFrame` 超时分支（`connection_registry.go:417-441`）是
//      **先 `entry.closed = true`、放开 entry 锁，再从 map 摘除**
//      ⇒ 窗口内 `List()` 会返回一行 `closed=true` 却出现在 `live` 段里。
//      ⇒ 本页对它**显式标注**「正在注销」，且**不把它搬去 closed 段**
//      （它还没进环形缓冲，搬走就是凭空造出「已归档」）。
//   2. ★★★★ **同一份载荷里两段的顺序保证相反**：`live` 是 map 遍历序（无保证），
//      `closed` 是 newest-first（稳定）⇒ 本页对 live **客户端排序**。
//   3. ★★★★ **`live: []` 会真的出现**（`List()` 用 `make(..., 0, ...)`），
//      而 **`closed: []` 后端永不产生**（`null` 或非空数组）
//      ⇒ 两段的空态语义不同，文案也不同。
//   4. ★★★★ **「已暴露满 50 行」≠「更早的已丢弃」**：环形缓冲 256、端点只给 50
//      ⇒ 50 行时缓冲区里**至少还有另外 206 条**没暴露。措辞按这个口径。
//   5. ★★★★ **`Lookup` 只查活跃表** ⇒ `closed` **段**的行按 id 查**必然 404**
//      ⇒ 本页对 closed 段**不发**详情请求，直接用列表里那份快照。
//      ⚠️ 但 `live` 段里 `closed=true` 的竞态行**仍查得到**（它还在活跃表里）
//      ⇒ 判定口径是「**行来自哪一段**」，**不是** `closed` 字段。
//   6. ★★★ **404 与「id 不存在」后端返回同一个响应**（都是
//      `{"error":{"detail":"request not registered"}}`）⇒ 客户端**分不出**这两种
//      ⇒ 本页只能说「查不到这一条」，用**注释承担契约**。
//   7. ★★★ **503 是「注册台没装配」，不是「空列表」**
//      （message 是 `connection registry not wired`）⇒ 两种失败必须分开渲染。
//
// ★ 本页与桌面的两处**刻意不同**（不是漏抄）：
//   · 桌面 `searchByRequestId` 把命中结果**塞回 live 表**，并按 `snap.closed` 分流；
//     而 `closed=true` 既可能在 live 段也可能在 closed 段（见 (1)(5)）
//     ⇒ 按 `closed` 分流会把 live 段的竞态行塞错段。本页改成**独立结果卡片**，不动列表。
//   · 桌面渲染「距最后一帧 Ns」，依赖 1s tick 的墙上时钟 ⇒ 断言会变成时间炸弹。
//     本页只显示**绝对时间戳**，不做相对时间。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchConnectionRegistryList,
  fetchConnectionByRequestId,
  registryClosedIsAbsent,
  registryClosedRows,
  registryClosedPossiblyTruncated,
  registryLiveSortedByRegisteredAt,
  registryUtilization,
  snapshotClientType,
  snapshotCloseReason,
  snapshotIsClosed,
  snapshotNeverWroteFrame,
  snapshotProtocol,
  snapshotTenantId,
  CLOSED_HISTORY_LIMIT,
  type ConnectionRegistryListResponse,
  type ConnectionSnapshot,
} from '@/api/connectionRegistry'

useHyperPage({ title: () => t('cr.title') })

const router = useRouter()

const list = ref<ConnectionRegistryListResponse | null>(null)
const errorMsg = ref<string | null>(null)
/** ★ 503 = 没装配；与「装配了但没有连接」是两种不同的失败。 */
const notWired = ref(false)
const loading = ref(false)

const searchId = ref('')
const searchError = ref<string | null>(null)
const searchLoading = ref(false)
/** ★★ 命中结果**独立展示**，不塞回列表（见头部「刻意不同」第一条）。 */
const lookupHit = ref<ConnectionSnapshot | null>(null)

const filterText = ref('')

// ══ 状态行 ══════════════════════════════════════════════════════════════

/** ★★ `capacity` 恒 ≥ 4096（构造期兜底）⇒ 水位恒有意义，不会出现「算不出」。 */
const watermark = computed(() => (list.value ? registryUtilization(list.value) : null))

/** ★★★★ `live` 段里 `closed: true` 的行数 —— 竞态窗口的可见化。 */
const liveClosingRows = computed(() =>
  (list.value?.live ?? []).filter((s) => snapshotIsClosed(s)).length,
)

/** ★★★★ `live` 客户端排序（后端是 map 遍历序，无保证）。 */
const liveSorted = computed(() =>
  list.value ? registryLiveSortedByRegisteredAt(list.value) : [],
)

/** ★ `closed` 段恒为数组（解包器把 `null` 之外的东西都校过）。 */
const closedRows = computed(() => (list.value ? registryClosedRows(list.value) : []))

/** ★★★★ 满 50 行 ⇒ **可能还有更早的没被端点暴露**（不是「已丢弃」）。 */
const closedMaybeMore = computed(() =>
  list.value ? registryClosedPossiblyTruncated(list.value) : false,
)

/** ★★ `closed` 的空态是 `null`（「从无注销记录」），与 `live` 的 `[]` 语义不同。 */
const closedAbsent = computed(() => (list.value ? registryClosedIsAbsent(list.value) : false))

// ══ 过滤 ════════════════════════════════════════════════════════════════

function matches(s: ConnectionSnapshot): boolean {
  const q = filterText.value.trim().toLowerCase()
  if (!q) return true
  return [s.request_id, s.protocol, s.client_type, s.tenant_id].some((v) =>
    String(v ?? '').toLowerCase().includes(q),
  )
}

const liveVisible = computed(() => liveSorted.value.filter(matches))
const closedVisible = computed(() => closedRows.value.filter(matches))

// ══ 取数 ════════════════════════════════════════════════════════════════

async function load(): Promise<void> {
  loading.value = true
  errorMsg.value = null
  try {
    list.value = await fetchConnectionRegistryList()
    // ★ 失败过就复位：503 与「空列表」不能共用一种呈现。
    notWired.value = false
  } catch (err) {
    list.value = null
    // ★ 客户端拿不到 HTTP 状态码时，靠 message 区分「没装配」与其它失败。
    //   这是**降级判定**，不是契约 —— 契约在 `registryNotWired(status)` 那一侧。
    notWired.value = String((err as Error)?.message ?? '').includes('connection registry not wired')
    errorMsg.value = notWired.value ? null : (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

async function lookup(): Promise<void> {
  const id = searchId.value.trim()
  if (!id) return
  searchError.value = null
  lookupHit.value = null
  searchLoading.value = true
  try {
    lookupHit.value = await fetchConnectionByRequestId(id)
  } catch (err) {
    // ★★★ 见头部 (6)：404 与「id 不存在」「这行已注销」三者**后端同形**，
    //   客户端分不出 ⇒ 只能说「查不到」，**不能**断言是哪一种。
    lookupHit.value = null
    searchError.value = (err as Error)?.message || String(err)
  } finally {
    searchLoading.value = false
  }
}

function clearLookup(): void {
  lookupHit.value = null
  searchError.value = null
  searchId.value = ''
}

// ══ 呈现辅助 ════════════════════════════════════════════════════════════

function protocolOf(s: ConnectionSnapshot): string {
  return snapshotProtocol(s) ?? t('cr.no.protocol')
}

function clientOf(s: ConnectionSnapshot): string {
  return snapshotClientType(s) ?? t('cr.no.client')
}

function tenantOf(s: ConnectionSnapshot): string {
  return snapshotTenantId(s) ?? t('cr.no.tenant')
}

function reasonOf(s: ConnectionSnapshot): string {
  return snapshotCloseReason(s) ?? t('cr.no.reason')
}

/** ★★ `frames_written` 是 `uint64` **无 omitempty** ⇒ 键恒在，0 是**真的 0 帧**。 */
function framesOf(s: ConnectionSnapshot): string {
  return `${snapshotOf(s, 'frames')} ${t('cr.unit.frames')}`
}

function bytesOf(s: ConnectionSnapshot): string {
  return `${snapshotOf(s, 'bytes')} ${t('cr.unit.bytes')}`
}

function snapshotOf(s: ConnectionSnapshot, kind: 'frames' | 'bytes'): string {
  const v = kind === 'frames' ? s.frames_written : s.bytes_written
  const n = Number(v)
  return Number.isFinite(n) ? String(n) : t('cr.no.number')
}

/**
 * ★★ `last_frame_at` 是 `time.Time` 无 omitempty ⇒ **键恒在**，
 *   但「从未写过帧」时它是 Go 零值时间 `"0001-01-01T00:00:00Z"`。
 *   ⇒ 不能把它当有效时间点渲染（也不能渲染成「1970 年」）。
 */
function lastFrameOf(s: ConnectionSnapshot): string {
  if (snapshotNeverWroteFrame(s)) return t('cr.lastFrame.never')
  return s.last_frame_at
}

function openJourney(requestId: string): void {
  void router.push({ name: 'journey-detail', params: { id: requestId } })
}

onBeforeUnmount(() => {
  list.value = null
  lookupHit.value = null
  errorMsg.value = null
  searchError.value = null
})
</script>

<template>
  <div class="cr">
    <!-- ══ 状态行 ══ -->
    <section class="cr__sec">
      <header class="cr__head">
        <h2 class="cr__h">{{ t('cr.title') }}</h2>
        <button type="button" class="cr__btn" :disabled="loading" @click="load">
          {{ list ? t('cr.reload') : t('cr.load') }}
        </button>
      </header>
      <p class="cr__sub">{{ t('cr.subtitle') }}</p>
      <!-- ★ 503：没装配。这**不是**「空列表」。 -->
      <p v-if="notWired" class="cr__msg cr__msg--warn">{{ t('cr.notWired') }}</p>
      <p v-if="errorMsg" class="cr__msg cr__msg--err">{{ errorMsg }}</p>
      <p v-if="loading" class="cr__msg">{{ t('common.loading') }}</p>
      <template v-if="list">
        <p class="cr__msg" data-testid="cr-watermark">
          {{ t('cr.watermark', { n: list.live_count, cap: list.capacity }) }}
          <template v-if="watermark !== null">（{{ Math.round(watermark * 1000) / 10 }}%）</template>
        </p>
        <!-- ★★★★ 竞态窗口的可见化：live 段里出现 closed 行不是 bug，是并发窗口 -->
        <p v-if="liveClosingRows > 0" class="cr__msg cr__msg--warn" data-testid="cr-race">
          {{ t('cr.live.closing', { n: liveClosingRows }) }}
        </p>
      </template>
    </section>

    <!-- ══ 按 request_id 查 ══ -->
    <section class="cr__sec">
      <header class="cr__head">
        <h2 class="cr__h">{{ t('cr.search.title') }}</h2>
      </header>
      <div class="cr__row">
        <input
          v-model="searchId"
          type="search"
          class="cr__input"
          :placeholder="t('cr.search.placeholder')"
          data-testid="cr-search-input"
          @keydown.enter.prevent="lookup"
        />
        <button
          type="button"
          class="cr__btn"
          :disabled="searchLoading || !searchId.trim()"
          data-testid="cr-search-btn"
          @click="lookup"
        >
          {{ t('cr.search.btn') }}
        </button>
        <button
          v-if="lookupHit || searchError"
          type="button"
          class="cr__btn cr__btn--ghost"
          data-testid="cr-search-clear"
          @click="clearLookup"
        >
          {{ t('cr.search.clear') }}
        </button>
      </div>
      <!-- ★★★ 404 与「id 不存在」「这行已注销」后端同形 ⇒ 只能说「查不到」 -->
      <p v-if="searchError" class="cr__msg cr__msg--err" data-testid="cr-search-error">
        {{ t('cr.search.notFound') }}
      </p>
      <p v-if="searchLoading" class="cr__msg">{{ t('common.loading') }}</p>
      <article v-if="lookupHit" class="cr__card" data-testid="cr-hit">
        <div class="cr__row">
          <span class="cr__rid">{{ lookupHit.request_id }}</span>
          <span
            v-if="lookupHit.closed"
            class="cr__badge cr__badge--warn"
            data-testid="cr-hit-closing"
          >
            {{ t('cr.badge.closing') }}
          </span>
        </div>
        <dl class="cr__kv">
          <div class="cr__kv-row">
            <dt>{{ t('cr.col.protocol') }}</dt><dd>{{ protocolOf(lookupHit) }}</dd>
          </div>
          <div class="cr__kv-row">
            <dt>{{ t('cr.col.client') }}</dt><dd>{{ clientOf(lookupHit) }}</dd>
          </div>
          <div class="cr__kv-row">
            <dt>{{ t('cr.col.tenant') }}</dt><dd>{{ tenantOf(lookupHit) }}</dd>
          </div>
          <div class="cr__kv-row">
            <dt>{{ t('cr.col.frames') }}</dt><dd>{{ framesOf(lookupHit) }}</dd>
          </div>
          <div class="cr__kv-row">
            <dt>{{ t('cr.col.bytes') }}</dt><dd>{{ bytesOf(lookupHit) }}</dd>
          </div>
          <div class="cr__kv-row">
            <dt>{{ t('cr.col.lastFrame') }}</dt><dd>{{ lastFrameOf(lookupHit) }}</dd>
          </div>
          <div v-if="lookupHit.closed" class="cr__kv-row">
            <dt>{{ t('cr.col.closeReason') }}</dt><dd>{{ reasonOf(lookupHit) }}</dd>
          </div>
        </dl>
        <button
          type="button"
          class="cr__btn cr__btn--ghost"
          :data-testid="'cr-hit-journey-' + lookupHit.request_id"
          @click="openJourney(lookupHit.request_id)"
        >
          {{ t('cr.journey') }}
        </button>
      </article>
      <!-- ★★★ closed 段的行按 id 查**必然 404**（Lookup 只查活跃表）⇒ 明说 -->
      <p class="cr__msg" data-testid="cr-closed-lookup-note">{{ t('cr.search.closedNote') }}</p>
    </section>

    <!-- ══ 过滤 ══ -->
    <section class="cr__sec">
      <input
        v-model="filterText"
        type="search"
        class="cr__input"
        :placeholder="t('cr.filter.placeholder')"
        data-testid="cr-filter-input"
      />
      <p v-if="filterText.trim()" class="cr__msg" data-testid="cr-filter-count">
        {{ t('cr.filter.count', { n: liveVisible.length + closedVisible.length }) }}
      </p>
    </section>

    <!-- ══ live 段 ══ -->
    <section class="cr__sec">
      <header class="cr__head">
        <h2 class="cr__h">{{ t('cr.live.title') }}</h2>
        <span class="cr__count">{{ liveVisible.length }}</span>
      </header>
      <p v-if="!list" class="cr__msg">{{ t('common.loading') }}</p>
      <template v-else>
        <!-- ★ `live: []` 是**正常形态**（`List()` 恒返回非 nil 切片） -->
        <p v-if="liveVisible.length === 0" class="cr__msg" data-testid="cr-live-empty">
          {{ t('cr.live.empty') }}
        </p>
        <article
          v-for="s in liveVisible"
          :key="'live-' + s.request_id"
          class="cr__card"
          :data-testid="'cr-live-row-' + s.request_id"
        >
          <div class="cr__row">
            <span class="cr__rid">{{ s.request_id }}</span>
            <!-- ★★★★ 竞态窗口：这一行已被标记 closed，但仍在活跃表里（还能按 id 查到） -->
            <span
              v-if="snapshotIsClosed(s)"
              class="cr__badge cr__badge--warn"
              :data-testid="'cr-closing-' + s.request_id"
            >
              {{ t('cr.badge.closing') }}
            </span>
          </div>
          <dl class="cr__kv">
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.protocol') }}</dt><dd>{{ protocolOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.client') }}</dt><dd>{{ clientOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.tenant') }}</dt><dd>{{ tenantOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.frames') }}</dt><dd>{{ framesOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.bytes') }}</dt><dd>{{ bytesOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.registered') }}</dt><dd>{{ s.registered_at }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.lastFrame') }}</dt><dd>{{ lastFrameOf(s) }}</dd>
            </div>
          </dl>
          <button
            type="button"
            class="cr__btn cr__btn--ghost"
            :data-testid="'cr-live-journey-' + s.request_id"
            @click="openJourney(s.request_id)"
          >
            {{ t('cr.journey') }}
          </button>
        </article>
      </template>
    </section>

    <!-- ══ closed 段 ══ -->
    <section class="cr__sec">
      <header class="cr__head">
        <h2 class="cr__h">{{ t('cr.closed.title') }}</h2>
        <span class="cr__count">{{ closedVisible.length }}</span>
      </header>
      <p v-if="!list" class="cr__msg">{{ t('common.loading') }}</p>
      <template v-else>
        <!-- ★ `null`（从无注销记录）与「非空数组」是两种不同的空态语义 -->
        <p v-if="closedAbsent" class="cr__msg" data-testid="cr-closed-absent">
          {{ t('cr.closed.absent') }}
        </p>
        <!-- ★★★★ 措辞：不是「更早的已丢弃」，是「还有更早的没被端点暴露」 -->
        <p v-if="closedMaybeMore" class="cr__msg cr__msg--warn" data-testid="cr-closed-more">
          {{ t('cr.closed.moreUnrevealed', { limit: CLOSED_HISTORY_LIMIT }) }}
        </p>
        <p v-if="closedVisible.length === 0 && !closedAbsent" class="cr__msg" data-testid="cr-closed-empty">
          {{ t('cr.closed.empty') }}
        </p>
        <article
          v-for="s in closedVisible"
          :key="'closed-' + s.request_id"
          class="cr__card cr__card--closed"
          :data-testid="'cr-closed-row-' + s.request_id"
        >
          <div class="cr__row">
            <span class="cr__rid">{{ s.request_id }}</span>
            <span class="cr__badge">{{ reasonOf(s) }}</span>
          </div>
          <dl class="cr__kv">
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.protocol') }}</dt><dd>{{ protocolOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.frames') }}</dt><dd>{{ framesOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.bytes') }}</dt><dd>{{ bytesOf(s) }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.registered') }}</dt><dd>{{ s.registered_at }}</dd>
            </div>
            <div class="cr__kv-row">
              <dt>{{ t('cr.col.lastFrame') }}</dt><dd>{{ lastFrameOf(s) }}</dd>
            </div>
          </dl>
          <button
            type="button"
            class="cr__btn cr__btn--ghost"
            :data-testid="'cr-closed-journey-' + s.request_id"
            @click="openJourney(s.request_id)"
          >
            {{ t('cr.journey') }}
          </button>
        </article>
      </template>
    </section>
  </div>
</template>

<style scoped>
.cr {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.cr__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.cr__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.cr__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.cr__sub {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.cr__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.cr__btn--ghost {
  align-self: flex-start;
  color: var(--app-text-secondary);
}

.cr__input {
  min-height: 48px;
  flex: 1 1 160px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.cr__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.cr__count {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.cr__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.cr__msg--err {
  color: var(--app-danger);
}

.cr__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.cr__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.cr__card--closed {
  background: var(--app-surface-muted);
}

.cr__rid {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}

.cr__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
}

.cr__badge--warn {
  color: var(--app-warning, var(--app-danger));
}

.cr__kv {
  margin: 0;
}

.cr__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.cr__kv-row dt {
  color: var(--app-text-muted);
}

.cr__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}
</style>
