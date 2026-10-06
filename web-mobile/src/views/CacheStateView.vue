<script setup lang="ts">
// CacheStateView — Redis 可用性缓存快照（/api/admin/probe/cache-state，**admin 档**）。
//
// 它回答的是一个**具体且高频**的问题：
//   「探测说这个凭据是健康的，路由为什么没选它？」
// 答案就在这份缓存里 —— `state`（探测判定）与 `available`（是否参与路由）
// 是**两个独立字段**，两者不一致就是根因。
//
// ⚠️★★★ 四个后端语义（详见 api/probeTimelineCache.ts 文件头）：
//
// (1) ★★ **503 的两种来源都表示「读不到」，不是「空」。**
//     · `availability reader not wired` ⇒ 这个部署没接 Redis 读取器
//     · `redis client unavailable`      ⇒ 接了但客户端类型不对
//     ⇒ 页面**绝不能**把 503 显示成「缓存里什么都没有」——
//       那会让运维去查「凭据是不是没被探测」，而问题在部署配置。
//
// (2) ★★ **枚举被 `ScanKeys` 截断在 4096 个 key**，响应里**没有**截断标记
//     （bg/model_availability_reader.go:150-153 的注释：
//     「Cap admin enumeration to keep the endpoint cheap」）。
//     ⇒ `count === 4096` 只能说「至少这么多」。
//
// (3) ★ `credential_id` 的非法值被后端**静默忽略**（= 不过滤，返回全量）。
//     ⇒ 客户端只在正整数时发；不发就明说「未筛选」。
//
// (4) ★★ **`format=prom` 会把同一个 URL 变成 `text/plain`**。
//     本页永远不发 `format`（写进注释，免得下一个人「顺手加个导出按钮」）。
//
// (5) ★ `raw_model_name` 的 **Go 字段名是 `RawModel`** —— 与 JSON 键不同名
//     （`CacheStateEntry:1937`）。按 Go 名访问会得到 undefined 且不报错。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchCacheState,
  cacheStateAtCap,
  classifyCacheStateError,
  cacheStateTone,
  cacheStateKeyOf,
  cacheStateContradiction,
  CACHE_STATE_KEY_CAP,
  type CacheStateResponse,
  type CacheStateFailure,
} from '@/api/probeTimelineCache'

useHyperPage({ title: () => t('cache.title') })

const credId = ref('')
const model = ref('')
const data = ref<CacheStateResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
/** ★ 503 的分类：未接线 / redis 不可用，两者文案不同。 */
const failure = ref<CacheStateFailure | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  failure.value = null
  // ★ 非法凭据 id **本地拦下**：发出去后端会静默忽略并返回**全量**，
  //   客户端会以为「筛了这个凭据」。见文件头 (3)。
  //   ⚠️ 守卫必须是 `^[1-9]\d*$` 而不是 `^\d+$`：后者放行 "0"，
  //   而 0 在 fetchCacheState 的 `> 0` 守卫里会被丢掉 ⇒ 结果同样是「以为筛了、
  //   实际拿到全量」，正是这段代码要防的那件事（文案也写着「必须是正整数」）。
  const raw = credId.value.trim()
  if (raw !== '' && !/^[1-9]\d*$/.test(raw)) {
    error.value = t('cache.badCredId')
    data.value = null
    loading.value = false
    loaded.value = true
    return
  }
  try {
    data.value = await fetchCacheState({
      credentialId: raw === '' ? undefined : Number(raw),
      model: model.value.trim() || undefined,
    })
  } catch (err) {
    data.value = null
    const f = classifyCacheStateError(err)
    // ★ 403 走普通错误，不归到「读不到」那两类里
    if ((err as { status?: number })?.status === 403) {
      error.value = t('cache.errForbidden')
    } else if (f.kind !== 'other') {
      failure.value = f
    } else {
      error.value = f.message || t('common.error')
    }
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

function onSearch(): void {
  void load()
}

function onClear(): void {
  credId.value = ''
  model.value = ''
  void load()
}

const entries = computed(() => data.value?.entries ?? [])
const atCap = computed(() => cacheStateAtCap(data.value))
const count = computed(() => data.value?.count ?? 0)

/** ★ 「读不到」态：与「读到了但空」分开渲染。 */
const isUnavailable = computed(() => failure.value !== null)
const isEmpty = computed(
  () => loaded.value && !loading.value && !isUnavailable.value && !error.value && entries.value.length === 0,
)

onBeforeUnmount(() => {
  data.value = null
  failure.value = null
  error.value = null
})
</script>

<template>
  <div class="view-root cs">
    <form class="cs__form" @submit.prevent="onSearch">
      <label class="cs__field">
        <span>{{ t('cache.credId') }}</span>
        <input
          v-model="credId"
          class="cs__input"
          inputmode="numeric"
          :placeholder="t('cache.credIdPlaceholder')"
          autocomplete="off"
        />
      </label>
      <label class="cs__field">
        <span>{{ t('cache.model') }}</span>
        <input
          v-model="model"
          class="cs__input"
          :placeholder="t('cache.modelPlaceholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
        />
      </label>
      <div class="cs__actions">
        <button type="submit" class="cs__btn cs__btn--go">{{ t('cache.search') }}</button>
        <button v-if="credId || model" type="button" class="cs__btn" @click="onClear">{{ t('common.clearFilters') }}</button>
      </div>
    </form>

    <p v-if="error" class="cs__msg cs__msg--err">{{ error }}</p>

    <!-- ★★★ 「读不到」与「空」是两件事，绝不能合并显示 -->
    <section v-if="isUnavailable" class="cs__unavail">
      <div class="cs__unavail-head">
        <StatusDot tone="warning" />
        <span class="cs__unavail-title">
          {{ failure?.kind === 'not_wired' ? t('cache.notWired') : t('cache.redisUnavailable') }}
        </span>
      </div>
      <p class="cs__unavail-hint">
        {{ failure?.kind === 'not_wired' ? t('cache.notWiredHint') : t('cache.redisUnavailableHint') }}
      </p>
    </section>

    <p v-if="loading" class="cs__msg">{{ t('common.loading') }}</p>
    <p v-else-if="isEmpty" class="cs__msg">{{ t('cache.empty') }}</p>

    <!-- ★★ 撞 4096 ⇒ 只能说「可能被截断」 -->
    <p v-if="atCap" class="cs__note cs__note--warn">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('cache.truncated', { n: count, cap: CACHE_STATE_KEY_CAP }) }}</span>
    </p>
    <p v-else-if="count" class="cs__count">{{ t('cache.count', { n: count }) }}</p>

    <ul v-if="entries.length" class="cs__list">
      <li
        v-for="e in entries"
        :key="e.credential_id + '|' + e.raw_model_name"
        class="cs__item"
        :class="{ 'cs__item--contra': cacheStateContradiction(e) }"
      >
        <div class="cs__item-head">
          <StatusDot :tone="cacheStateTone(e)" />
          <span class="cs__item-model">{{ e.raw_model_name }}</span>
          <span class="badge" :class="`badge--${cacheStateTone(e)}`">{{ t(cacheStateKeyOf(e)) }}</span>
        </div>
        <p class="cs__item-sub">
          {{ t('cache.credId') }} {{ e.credential_id }}
          <span v-if="e.source" class="cs__item-source">{{ e.source }}</span>
        </p>

        <!-- ★ state 与 available 是两个独立字段，必须并排显示 ——
             两者不一致正是「模型健康却没被选中」的根因。 -->
        <p class="cs__avail" :class="{ 'cs__avail--no': !e.available }">
          <span class="cs__avail-label">{{ t('cache.routable') }}</span>
          <span class="cs__avail-value">{{ e.available ? t('cache.yesLabel') : t('cache.noLabel') }}</span>
        </p>

        <p v-if="cacheStateContradiction(e)" class="cs__contra">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('cache.contradiction') }}</span>
        </p>

        <div class="cs__kv">
          <span class="cs__kv-item">
            <span class="cs__kv-l">{{ t('cache.okStreak') }}</span>
            <span class="cs__kv-v">{{ fmtInt(e.consecutive_successes) }}</span>
          </span>
          <span class="cs__kv-item">
            <span class="cs__kv-l">{{ t('cache.failStreak') }}</span>
            <span class="cs__kv-v" :class="{ 'cs__bad': e.consecutive_failures > 0 }">
              {{ fmtInt(e.consecutive_failures) }}
            </span>
          </span>
        </div>
        <p v-if="e.updated_at" class="cs__meta">{{ t('cache.updatedAt', { t: relativeTime(e.updated_at) }) }}</p>
        <p v-if="e.next_retry_at" class="cs__meta">{{ t('cache.nextRetry', { t: relativeTime(e.next_retry_at) }) }}</p>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.cs {
  padding: var(--app-space-3);
}
.cs__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}
.cs__field {
  display: block;
}
.cs__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.cs__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.cs__actions {
  display: flex;
  gap: var(--app-space-2);
}
.cs__btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.cs__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.cs__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.cs__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.cs__unavail {
  border: 1px solid var(--app-warning);
  border-left-width: 3px;
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.cs__unavail-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.cs__unavail-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.cs__unavail-hint {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-secondary);
  line-height: 1.5;
}
.cs__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.cs__note--warn {
  color: var(--app-warning);
}
.cs__count {
  margin: var(--app-space-2) 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.cs__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.cs__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.cs__item--contra {
  border-left: 3px solid var(--app-warning);
}
.cs__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.cs__item-model {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-word;
  min-width: 0;
}
.cs__item-sub {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.cs__item-source {
  margin-left: 4px;
  padding: 0 5px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
}
.cs__avail {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
}
.cs__avail-label {
  font-size: 12px;
  color: var(--app-text-secondary);
}
.cs__avail-value {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-success);
}
.cs__avail--no .cs__avail-value {
  color: var(--app-danger);
}
.cs__contra {
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
.cs__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.cs__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.cs__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.cs__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.cs__bad {
  color: var(--app-danger);
}
.cs__meta {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
</style>
