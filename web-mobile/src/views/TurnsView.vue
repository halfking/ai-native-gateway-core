<script setup lang="ts">
// TurnsView — 会话 / 轮次维度（/turns）。
//
// 数据源：
//   GET /api/admin/turns/sessions      会话列表（**游标**分页）
//   GET /api/admin/sessions/{id}/turns  单会话轮次树（展开时按需拉）
// 鉴权：两条都 AdminMiddleware（handler.go:1187 / :1275）⇒ tenant_admin 可用。
//
// 它在排障链上的位置：会话 =「一次对话」。用户报「结果不对」时，
// 一轮里可能有 3 次尝试、2 次压缩、1 次注入拦截——这些只有这一层看得到。
// 每条 turn 的 request_id 直接链到 /journey/:id，接到上一轮的链路详情。
//
// ⚠️ 游标分页怎么套进 ContinuousListController：
//   controller 传进来的 `page` 是页号，而本端点要的是**不透明游标**。
//   做法是自维护 `page → 该页返回的 next_cursor` 映射：第 1 页无游标，
//   第 N 页用第 **N-1** 页的 next_cursor。
//   ⚠️ 读 `get(page)` 而不是 `get(page - 1)` 是本轮真犯过的错（已修）——
//     那样第 2 页会读一个还不存在的条目 ⇒ 从不发游标 ⇒ 后端每页都返回
//     第 1 页，去重后列表永远不增长。判据：TurnsView.spec 的游标用例。
//   refresh（loadFirst）时额外 clear() 是**防御性**措施：实测当前流程下
//   非承重（重置后的第 1 页必覆写 cursors[1]），保留是为了分页中途失败等边界。

import { onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage, ContinuousListController } from '@/hyper'
import HyperList from '@/components/common/HyperList.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchTurnsSessions,
  fetchSessionTurns,
  latencyOf,
  costNumber,
  TURNS_SESSIONS_MAX_LIMIT,
  type TurnsSessionGroup,
  type SessionTurnTreeItem,
} from '@/api/turnsSessions'

useHyperPage({ title: () => t('turns.title') })
const router = useRouter()

const PAGE_SIZE = 20

const search = ref('')
const expanded = ref<string | null>(null)
const turnsBySession = ref<Record<string, SessionTurnTreeItem[]>>({})
const turnsLoading = ref<string | null>(null)
const turnsError = ref<string | null>(null)

/** page → cursor。刷新时清空（见文件头）。 */
const cursors = new Map<number, string>()

const controller = new ContinuousListController<TurnsSessionGroup>({
  fetchPage: async (page) => {
    // ★★ 游标要取**上一页**返回的那个。写成 cursors.get(page) 会永远拿到
    //   undefined（本页的游标此刻还不存在）⇒ 从不发送游标 ⇒ 后端每页都返回
    //   第 1 页的 20 条 ⇒ 按 stableKey 去重后列表再也不增长，用户无限滚。
    //   判据：TurnsView.spec 的「翻到第 2 页后改搜索」用例（变异实测转红）。
    const cursor = page <= 1 ? undefined : cursors.get(page - 1)
    const q = search.value.trim()
    const resp = await fetchTurnsSessions({
      limit: Math.min(PAGE_SIZE, TURNS_SESSIONS_MAX_LIMIT),
      ...(cursor ? { cursor } : {}),
      ...(q ? { search: q } : {}),
    })
    // 记下本页的 next_cursor，供**下一页**用
    if (resp.next_cursor) cursors.set(page, resp.next_cursor)
    else cursors.delete(page)
    return {
      items: resp.items ?? [],
      // has_more 是后端给的权威信号；没有游标就一定到底了
      total: resp.has_more && resp.next_cursor ? undefined : (resp.items ?? []).length,
    }
  },
  stableKey: (s) => `sess-${s.session_id}`,
  scopeKey: 'turns-sessions',
})

// 换搜索条件是服务端参数 ⇒ 必须重取，且旧游标作废
let debounceTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput(ev: Event): void {
  const v = (ev.target as HTMLInputElement).value
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    search.value = v
    cursors.clear()
    controller.loadFirst('requery')
  }, 300)
}
onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer)
  // ★ 卸载时必须 dispose：不 abort 的话在途请求会打到已销毁的 controller 上
  //   （六页共用的 HyperList 坑，见 §11.25）
  controller.dispose()
})

/**
 * 暴露 controller 供测试驱动翻页。
 *
 * 为什么必须暴露：jsdom 里 IntersectionObserver 不触发，HyperList 的 sentinel
 * 永远不预载 ⇒ **测试里永远走不到第 2 页**。于是「换筛选后第 2 页是否用了
 * 旧游标」这个最危险的场景无法验证 —— 判据量的是一个恒真的条件。
 * （变异实测：把 `cursors.clear()` 删掉，第一版的第 1 页断言仍然全绿。）
 */
defineExpose({ controller })

async function toggleTurns(s: TurnsSessionGroup): Promise<void> {
  if (expanded.value === s.session_id) {
    expanded.value = null
    return
  }
  expanded.value = s.session_id
  if (turnsBySession.value[s.session_id]) return
  turnsLoading.value = s.session_id
  turnsError.value = null
  try {
    const resp = await fetchSessionTurns(s.session_id, { limit: 50 })
    turnsBySession.value = { ...turnsBySession.value, [s.session_id]: resp.turns ?? [] }
  } catch (err) {
    turnsError.value = (err as Error)?.message ?? null
  } finally {
    turnsLoading.value = null
  }
}

function sessionTone(s: TurnsSessionGroup): 'success' | 'warning' | 'danger' | 'muted' {
  if (s.error_count > 0) return 'danger'
  if (s.failover_count > 0) return 'warning'
  if (s.status && s.status !== 'active' && s.status !== 'open') return 'muted'
  return 'success'
}

function costText(s: TurnsSessionGroup): string | null {
  const n = costNumber(s.total_cost_usd)
  return n == null ? null : t('turns.cost', { v: n.toFixed(4) })
}

function openJourney(requestId?: string): void {
  if (!requestId) return
  void router.push({ path: `/journey/${encodeURIComponent(requestId)}` })
}

/**
 * ★ 延迟未知时显示占位符，绝不显示 0ms。
 *
 * 本页渲染的是 turns **树**（SessionTurnTreeItem，字段名 `latency`，可空），
 * 不是 sessions 列表里的 TurnGroupItem（字段名 `latency_ms`）。
 * 两者键名不同 —— latencyOf 已统一收口，这里显式注释以防下一个人「简化」成
 * 直接读 turn.latency_ms，那会永远拿到 undefined。
 */
function latencyText(turn: SessionTurnTreeItem): string {
  const ms = latencyOf(turn)
  return ms == null ? t('turns.latencyUnknown') : `${ms}ms`
}

</script>

<template>
  <div class="view-root turns">
    <div class="turns__search">
      <AppIcon name="search" :size="18" />
      <input
        type="search"
        :placeholder="t('turns.searchPlaceholder')"
        :aria-label="t('common.search')"
        @input="onSearchInput"
      />
    </div>

    <HyperList
      :controller="controller"
      :item-key="(s: TurnsSessionGroup) => `sess-${s.session_id}`"
      :on-refresh="async () => { cursors.clear(); controller.loadFirst('requery') }"
      :empty-hint="search ? t('turns.emptyFiltered') : t('turns.empty')"
    >
      <template #item="{ item: s }">
        <div class="data-card sess-card">
          <button type="button" class="sess-card__head" @click="toggleTurns(s)">
            <div class="card-row">
              <span class="sess-card__title">
                <StatusDot :tone="sessionTone(s)" />
                {{ s.title || s.topic || s.session_id }}
              </span>
              <span class="badge" :class="`badge--${sessionTone(s) === 'muted' ? 'muted' : sessionTone(s)}`">
                {{ s.status === 'closed' ? t('turns.statusClosed') : t('turns.statusOpen') }}
              </span>
            </div>
            <div class="sess-card__fields">
              <span class="sess-card__field">{{ relativeTime(s.updated_at) }}</span>
              <span class="sess-card__field">{{ t('turns.total', { n: s.total_turns ?? 0 }) }}</span>
              <span v-if="s.total_tokens != null" class="sess-card__field">
                {{ t('turns.tokens', { n: s.total_tokens }) }}
              </span>
              <span v-if="costText(s)" class="sess-card__field">{{ costText(s) }}</span>
            </div>
            <div class="sess-card__marks">
              <span v-if="s.failover_count > 0" class="sess-card__mark sess-card__mark--warn">
                {{ t('turns.failovers', { n: s.failover_count }) }}
              </span>
              <span v-if="s.error_count > 0" class="sess-card__mark sess-card__mark--err">
                {{ t('turns.errors', { n: s.error_count }) }}
              </span>
            </div>
          </button>

          <div v-if="expanded === s.session_id" class="sess-card__turns">
            <p v-if="turnsLoading === s.session_id" class="sess-card__msg">{{ t('common.loading') }}</p>
            <p v-else-if="turnsError" class="sess-card__msg sess-card__msg--err">{{ turnsError }}</p>
            <p v-else-if="(turnsBySession[s.session_id] ?? []).length === 0" class="sess-card__msg">
              {{ t('turns.noTurns') }}
            </p>
            <ul v-else class="turn-list">
              <li v-for="tn in (turnsBySession[s.session_id] ?? [])" :key="tn.turn_number" class="turn">
                <div class="turn__head">
                  <span class="turn__no">#{{ tn.turn_number }}</span>
                  <span class="turn__model">{{ tn.model || '—' }}</span>
                  <span class="turn__latency">{{ latencyText(tn) }}</span>
                </div>
                <p class="turn__status">
                  {{ tn.status }}
                  <template v-if="tn.body_status === 'unavailable'"> · body {{ tn.body_status }}</template>
                </p>
                <div v-if="tn.child_requests && tn.child_requests.length > 0" class="turn__children">
                  <button
                    v-for="c in tn.child_requests"
                    :key="c.request_id"
                    type="button"
                    class="turn__child"
                    @click="openJourney(c.request_id)"
                  >
                    <span class="turn__child-type">{{ c.request_type }}</span>
                    <span class="turn__child-id">{{ c.request_id }}</span>
                  </button>
                </div>
                <button
                  v-if="tn.request_id"
                  type="button"
                  class="turn__journey"
                  @click="openJourney(tn.request_id)"
                >
                  {{ t('turns.openJourney') }}
                </button>
              </li>
            </ul>
          </div>
        </div>
      </template>
    </HyperList>
  </div>
</template>

<style scoped>
.turns {
  padding: var(--app-space-3) var(--app-space-3) 0;
}
.turns__search {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  padding: 0 var(--app-space-3);
  min-height: 48px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-3);
}
.turns__search input {
  flex: 1;
  min-width: 0;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  /* 高度由外层 48px 容器决定；不自己设 min-height ——
     写了 46px 会被 R1 门判为新增触控控件 < 48px（见 .logs__search input 同款）。 */
  height: 100%;
  align-self: stretch;
}
.turns__search input:focus {
  outline: none;
}
.sess-card {
  padding: 0;
}
.sess-card__head {
  display: block;
  width: 100%;
  padding: var(--app-space-3);
  text-align: left;
  background: none;
  border: none;
}
.sess-card__title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sess-card__fields {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 6px;
}
.sess-card__field {
  font-size: 12px;
  color: var(--app-text-muted);
}
.sess-card__marks {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 6px;
}
.sess-card__mark {
  font-size: 12px;
}
.sess-card__mark--warn {
  color: var(--app-warning);
}
.sess-card__mark--err {
  color: var(--app-danger);
}
.sess-card__turns {
  padding: 0 var(--app-space-3) var(--app-space-3);
}
.sess-card__msg {
  margin: 0;
  padding: var(--app-space-3) 0;
  font-size: 13px;
  color: var(--app-text-muted);
  text-align: center;
}
.sess-card__msg--err {
  color: var(--app-danger);
}
.turn-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.turn {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.turn__head {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
}
.turn__no {
  font-size: 12px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
.turn__model {
  font-size: 14px;
  color: var(--app-text);
}
.turn__latency {
  margin-left: auto;
  font-size: 12px;
  color: var(--app-text-secondary);
  font-variant-numeric: tabular-nums;
}
.turn__status {
  margin: 3px 0 0;
  font-size: 12px;
  color: var(--app-text-muted);
}
.turn__children {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 6px;
}
.turn__child {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 48px;
  padding: 0 10px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  font-size: 11px;
  color: var(--app-text-secondary);
  max-width: 100%;
}
.turn__child-type {
  font-weight: 600;
}
.turn__child-id {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.turn__journey {
  min-height: 48px;
  margin-top: 6px;
  padding: 0 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-primary);
  font-size: 13px;
}
</style>
