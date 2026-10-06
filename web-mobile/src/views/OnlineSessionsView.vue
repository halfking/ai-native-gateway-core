<script setup lang="ts">
// OnlineSessionsView — 当前活跃会话（/api/admin/sessions/online，**admin 档**）。
//
// 它答的是「**现在**谁还在请求、用的哪个模型、最后一次结果如何」，
// 与同族另外两页分工明确：
//   /sessions/list     历史清单 + 逐轮明细
//   本页               实时活跃面（游标分页，不落历史）
//
// ⚠️★★★ 四个后端语义（详见 api/sessions.ts 文件头）：
//
// (1) ★★★ **superAdmin / legacy 角色不加租户过滤。**
//     SQL 是 `if !IsSuperAdminOrLegacy(r) { … AND rl.tenant_id = $1 }`
//     ⇒ 同一屏数据在两种角色下含义完全不同（自己的租户 vs 全平台）。
//     ⇒ 页首**按角色**显示当前作用域，不能只写一句中性说明。
//
// (2) ★★ `limit` 被 **静默 clamp 到 100**（不是 400，也不是回落默认值）
//     ⇒ 客户端只发 1..100；发 500 会被悄悄改成 100。
//
// (3) ★★ 分页用 `LIMIT limit+1` 多取一条判定 `has_more`
//     ⇒ **没有**「静默截断」问题，`has_more` 是精确信号。
//
// (4) ★★ `last_latency_ms` / `last_provider_id` 可空（源列 NULL ⇒ 键缺失），
//     `last_model` 是 `COALESCE(...,'')` ⇒ 空串意味着源列 NULL。
//     ⇒ 缺失显示「—」，空模型显示「未知模型」，不显示 0ms。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { useAuthStore } from '@/stores/auth'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchOnlineSessions,
  onlineHasMore,
  ONLINE_LIMIT_MAX,
  type OnlineSession,
  type OnlineSessionsResponse,
} from '@/api/sessions'

useHyperPage({ title: () => t('online.title') })

const auth = useAuthStore()
const PAGE = ONLINE_LIMIT_MAX

const rows = ref<OnlineSession[]>([])
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

/** ★ superAdmin/legacy 不加租户过滤 ⇒ 页面必须说清当前作用域。 */
const isSuperAdmin = computed(() => auth.role === 'super_admin')

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const r: OnlineSessionsResponse = await fetchOnlineSessions({ limit: PAGE })
    rows.value = r.sessions ?? []
    nextCursor.value = r.next_cursor ?? null
    hasMore.value = onlineHasMore(r)
  } catch (err) {
    rows.value = []
    nextCursor.value = null
    hasMore.value = false
    const st = (err as { status?: number })?.status
    if (st === 401 || st === 403) error.value = t('online.errForbidden')
    else error.value = (err as Error)?.message || t('common.error')
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

async function loadMore(): Promise<void> {
  // ★ 游标**不透明**：只用后端回传的 next_cursor，不自己拼。
  if (!hasMore.value || !nextCursor.value || loadingMore.value) return
  loadingMore.value = true
  try {
    const r = await fetchOnlineSessions({ limit: PAGE, cursor: nextCursor.value })
    rows.value = [...rows.value, ...(r.sessions ?? [])]
    nextCursor.value = r.next_cursor ?? null
    hasMore.value = onlineHasMore(r)
  } catch (err) {
    error.value = (err as Error)?.message || t('common.error')
  } finally {
    loadingMore.value = false
  }
}

const isEmpty = computed(() => loaded.value && !loading.value && !error.value && rows.value.length === 0)

function toneOf(s: OnlineSession): 'success' | 'warning' | 'danger' | 'muted' {
  const st = (s.last_request_status ?? '').toLowerCase()
  if (st === 'ok' || st === 'success' || st === 'succeeded') return 'success'
  if (st === 'error' || st === 'failed' || st === 'failure') return 'danger'
  if (st !== '') return 'warning'
  return 'muted'
}

onBeforeUnmount(() => {
  rows.value = []
  nextCursor.value = null
  error.value = null
})
</script>

<template>
  <div class="view-root on">
    <!-- ★★ 作用域必须按角色说清 -->
    <section class="on__scope">
      <p class="on__scope-line">
        <AppIcon name="alert" :size="13" />
        <span>{{ isSuperAdmin ? t('online.scopeSuper') : t('online.scopeTenant') }}</span>
      </p>
    </section>

    <p v-if="error" class="on__msg on__msg--err">{{ error }}</p>
    <p v-if="loading" class="on__msg">{{ t('common.loading') }}</p>
    <p v-else-if="isEmpty" class="on__msg">{{ t('online.empty') }}</p>
    <p v-else class="on__count">{{ t('online.count', { n: rows.length }) }}</p>

    <ul v-if="rows.length" class="on__list">
      <li v-for="s in rows" :key="s.session_id" class="on__item">
        <div class="on__item-head">
          <StatusDot :tone="toneOf(s)" />
          <span class="on__sid">{{ s.session_id }}</span>
        </div>
        <p v-if="s.title" class="on__title">{{ s.title }}</p>

        <div class="on__kv">
          <span class="on__kv-item">
            <span class="on__kv-l">{{ t('online.model') }}</span>
            <!-- ★ 空串意味着源列 NULL ⇒ 说「未知模型」而不是留白 -->
            <span class="on__kv-v">{{ s.last_model || t('online.unknownModel') }}</span>
          </span>
          <span class="on__kv-item">
            <span class="on__kv-l">{{ t('online.provider') }}</span>
            <!-- ★ 键缺失 = NULL，不是 0 -->
            <span class="on__kv-v">{{ s.last_provider_id === undefined ? '—' : `#${s.last_provider_id}` }}</span>
          </span>
          <span class="on__kv-item">
            <span class="on__kv-l">{{ t('online.latency') }}</span>
            <span class="on__kv-v">{{ s.last_latency_ms === undefined ? '—' : `${s.last_latency_ms}ms` }}</span>
          </span>
        </div>

        <!-- ★★ freshness 是这个端点自带的权威位，stale 要显式标出 -->
        <p v-if="s.freshness" class="on__fresh" :class="{ 'on__fresh--stale': s.freshness.stale }">
          <span>{{ t('online.freshness', { ms: s.freshness.freshness_ms, src: s.freshness.data_source }) }}</span>
          <span v-if="s.freshness.stale" class="on__stale">{{ t('online.stale') }}</span>
        </p>

        <p v-if="s.last_active_at" class="on__meta">{{ t('online.lastActive', { t: relativeTime(s.last_active_at) }) }}</p>
      </li>
    </ul>

    <button v-if="hasMore" type="button" class="on__more" :disabled="loadingMore" @click="loadMore">
      {{ loadingMore ? t('common.loading') : t('online.loadMore') }}
    </button>
  </div>
</template>

<style scoped>
.on {
  padding: var(--app-space-3);
}
.on__scope {
  border: 1px solid var(--app-warning);
  border-left-width: 3px;
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-2) var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.on__scope-line {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0;
  color: var(--app-text-secondary);
  font-size: 11px;
  line-height: 1.5;
}
.on__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.on__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.on__count {
  margin: var(--app-space-2) 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.on__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.on__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.on__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.on__sid {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-all;
  min-width: 0;
}
.on__title {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  word-break: break-word;
}
.on__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.on__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.on__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.on__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.on__fresh {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.on__fresh--stale {
  color: var(--app-warning);
}
.on__stale {
  padding: 1px 5px;
  border-radius: var(--app-radius-pill);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-weight: 600;
}
.on__meta {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.on__more {
  width: 100%;
  min-height: 48px;
  margin-top: var(--app-space-2);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
</style>