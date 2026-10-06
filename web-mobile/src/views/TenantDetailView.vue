<script setup lang="ts">
// TenantDetailView — 租户详情：基本信息 + 用户 + 密钥 + 用量统计（**superAdmin 档**）。
//
// GET /api/admin/tenants/{code}
// GET /api/admin/tenants/{code}/users
// GET /api/admin/tenants/{code}/keys
// GET /api/admin/tenants/{code}/stats?days=
//
// ⚠️ 四条全是**裸结构**（users/keys 是裸数组，get/stats 是裸对象）。
//
// 六个最容易渲染错的语义（详见 api/tenants.ts 坑 1~11）：
//
// (1) ★★★★★★ 列表的 7 天用量**可能不可信**（1.5s 独立预算 + 只 slog）
//     ⇒ 明说是「读数不可信」，不是「这段时间没用量」。
// (2) ★★★★ **详情页的聚合比列表页更不可信**：`getTenant` 五个计数全是
//     `_ = h.db.QueryRow(...)` ⇒ **连一行日志都不留**地吞掉错误。
// (3) ★★★★ stats 会返回 **504**（`retry with a smaller days window`）
//     ⇒ 这是「查询超时、调小窗口重试」，**不是**「查不到」。
// (4) ★★★★ 成本与积分**来自两张不同的表**：requests/tokens/cost/unique*
//     ← `usage_ledger_with_current_month`；credits/in-out tokens/cache/latency
//     ← `request_logs_hot ∪ request_logs`。对不上是可能的，不是 bug。
// (5) ★★★★ `daily` 的日切按 **Asia/Shanghai**，而对账页用 **UTC**
//     —— **有意分叉**，跨零点的差异不是数据错。
// (6) ★★★ `userInfo.last_login_at` 无 omitempty（键在值为 null = 从未登录），
//     而 `tenantKeyInfo` 的 4 个指针**带** omitempty（键可不存在）。
//
// ★ 写操作一律不碰。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum, fmtTime, relativeTime } from '@/utils/format'
import {
  fetchTenant,
  fetchTenantUsers,
  fetchTenantKeys,
  fetchTenantStats,
  tenantStatsDaysClamped,
  tenantUsageMayBeDegraded,
  tenantUserNeverLoggedIn,
  tenantUsersNeedingPasswordChange,
  tenantUsersDisabled,
  tenantKeyNeverExpires,
  tenantKeyHasAlias,
  tenantKeyHasOwner,
  tenantDailyIsComplete,
  TENANT_STATS_DAYS_DEFAULT,
  TENANT_STATS_DAYS_MAX,
  TENANT_STATS_DAY_TZ,
  type TenantInfo,
  type TenantUser,
  type TenantKey,
  type TenantStats,
} from '@/api/tenants'

useHyperPage({ title: () => t('tnd.title') })

const route = useRoute()
const router = useRouter()

const code = ref(typeof route.params.code === 'string' ? route.params.code : '')
const days = ref<number>(TENANT_STATS_DAYS_DEFAULT)

const tenant = ref<TenantInfo | null>(null)
const users = ref<TenantUser[] | null>(null)
const keys = ref<TenantKey[] | null>(null)
const stats = ref<TenantStats | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'timeout' | 'notfound' | 'unconfigured' | 'other'>('none')
const loading = ref(false)

/** ★ 1 / 7 / 30 / 90 / 365 —— 上界 365 是后端 clamp 点。 */
const dayChoices = [1, 7, 30, 90, TENANT_STATS_DAYS_MAX] as const

const effDays = computed(() => tenantStatsDaysClamped(days.value))
const needPwdCount = computed(() => (users.value ? tenantUsersNeedingPasswordChange(users.value) : 0))
const disabledCount = computed(() => (users.value ? tenantUsersDisabled(users.value) : 0))
const neverLoggedIn = computed(() => (users.value ?? []).filter(tenantUserNeverLoggedIn).length)

/** ★★ 详情页的聚合是 `_ =` 吞错来的，比列表页更不可信。 */
const detailAggShaky = computed(() => (tenant.value ? tenantUsageMayBeDegraded(tenant.value) : false))

async function load(c: string): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    const [t1, u, k, s] = await Promise.all([
      fetchTenant(c),
      fetchTenantUsers(c),
      fetchTenantKeys(c),
      fetchTenantStats(c, { days: days.value }),
    ])
    tenant.value = t1
    users.value = u
    keys.value = k
    stats.value = s
  } catch (e) {
    tenant.value = null
    users.value = null
    keys.value = null
    stats.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    // ★★★ 504 是「查询超时、调小窗口重试」—— 必须与「查不到」分开
    if (/timed out|smaller days|504/i.test(msg)) errorKind.value = 'timeout'
    else if (/database not configured/i.test(msg)) errorKind.value = 'unconfigured'
    else if (/tenant not found|404/i.test(msg)) errorKind.value = 'notfound'
    else errorKind.value = 'other'
  } finally {
    loading.value = false
  }
}

function reload(): void {
  const c = code.value.trim()
  if (!c) {
    tenant.value = null
    users.value = null
    keys.value = null
    stats.value = null
    error.value = t('tnd.needCode')
    errorKind.value = 'other'
    return
  }
  void load(c)
}

function goBack(): void {
  void router.push('/tenants')
}

void (async () => {
  reload()
})()

watch(
  () => route.params.code,
  () => {
    code.value = typeof route.params.code === 'string' ? route.params.code : ''
    reload()
  },
)

watch(days, () => {
  const c = code.value.trim()
  if (c) void load(c)
})

onBeforeUnmount(() => {
  tenant.value = null
  users.value = null
  keys.value = null
  stats.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="tnd">
    <!-- ══════ 错误 ══════ -->
    <section v-if="error" class="tnd__panel">
      <p class="tnd__msg tnd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <!-- ★★ 504 单列：提示「调小 days 重试」，不要说成「查不到」 -->
      <p v-if="errorKind === 'timeout'" class="tnd__note tnd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('tnd.timeoutNote') }}</span>
      </p>
      <p v-else-if="errorKind === 'unconfigured'" class="tnd__note tnd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
      <p v-else-if="errorKind === 'notfound'" class="tnd__note tnd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('tnd.notFoundNote') }}</span>
      </p>
    </section>

    <p v-if="loading" class="tnd__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 租户条 ══════ -->
    <section v-if="tenant" class="tnd__panel">
      <div class="tnd__item-head">
        <span class="tnd__title">{{ tenant.name }}</span>
        <span class="tnd__badge">
          <StatusDot :tone="tenant.status === 'active' ? 'success' : 'muted'" />
          <span class="tnd__badge-t">{{ tenant.status }}</span>
        </span>
      </div>
      <p class="tnd__meta">{{ tenant.code }}</p>
      <p v-if="tenant.contact_email" class="tnd__meta">{{ tenant.contact_email }}</p>
      <p class="tnd__meta">{{ t('tnd.created') }}: {{ fmtTime(tenant.created_at) }}</p>

      <div class="tnd__grid">
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.users') }}</span>
          <span class="tnd__cell-v">{{ tenant.user_count ?? 0 }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.keys') }}</span>
          <span class="tnd__cell-v">{{ tenant.api_key_count ?? 0 }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.requests7d') }}</span>
          <span class="tnd__cell-v">{{ tenant.requests_7d ?? 0 }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.credits7d') }}</span>
          <span class="tnd__cell-v">{{ tenant.credits_7d ?? 0 }}</span>
        </span>
      </div>

      <!-- ★★★ 详情页的聚合是 `_ =` 吞错来的，比列表页更不可信 -->
      <p v-if="detailAggShaky" class="tnd__note tnd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('tnd.detailAggNote') }}</span>
      </p>
    </section>

    <!-- ══════ 统计 ══════ -->
    <section v-if="stats" class="tnd__panel">
      <span class="tnd__panel-title">{{ t('tnd.stats') }}</span>

      <label class="tnd__label">{{ t('tnd.days') }}</label>
      <div class="tnd__seg" role="group" :aria-label="t('tnd.days')">
        <button
          v-for="n in dayChoices"
          :key="n"
          type="button"
          class="tnd__seg-btn"
          :class="{ 'tnd__seg-btn--on': days === n }"
          :aria-pressed="days === n"
          @click="days = n"
        >
          {{ n }}
        </button>
      </div>

      <div class="tnd__grid">
        <span class="tnd__cell">
          <!-- ★ 来自 usage_ledger -->
          <span class="tnd__cell-l">{{ t('tnd.totalRequests') }}</span>
          <span class="tnd__cell-v">{{ fmtInt(stats.total_requests) }}</span>
        </span>
        <span class="tnd__cell">
          <!-- ★ 来自 usage_ledger -->
          <span class="tnd__cell-l">{{ t('tnd.totalCost') }}</span>
          <span class="tnd__cell-v">${{ fmtNum(stats.total_cost_usd, 4) }}</span>
        </span>
        <span class="tnd__cell">
          <!-- ★ 来自 logsTable（另一张表） -->
          <span class="tnd__cell-l">{{ t('tnd.totalCredits') }}</span>
          <span class="tnd__cell-v">{{ fmtInt(stats.total_credits) }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.uniqueKeys') }}</span>
          <span class="tnd__cell-v">{{ fmtInt(stats.unique_keys) }}</span>
        </span>
      </div>

      <!-- ★★★★ 成本与积分来自两张不同的表 -->
      <p class="tnd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('tnd.twoSourcesNote') }}</span>
      </p>
      <!-- ★★ 日切时区：对账页用 UTC，这里用 Asia/Shanghai —— 有意分叉 -->
      <p class="tnd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('tnd.tzNote', { tz: TENANT_STATS_DAY_TZ }) }}</span>
      </p>
      <p class="tnd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('tnd.echoDaysNote', { n: effDays, d: stats.days }) }}</span>
      </p>

      <!-- ★★ daily 条数少于 days ⇒ 后端降级过，不能画折线 -->
      <p v-if="!tenantDailyIsComplete(stats)" class="tnd__note tnd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('tnd.dailyIncomplete', { got: stats.daily.length, want: stats.days }) }}</span>
      </p>
      <p v-else class="tnd__note">
        <AppIcon name="check" :size="13" />
        <span>{{ t('tnd.dailyComplete', { n: stats.daily.length }) }}</span>
      </p>

      <!-- R49-E2：横向滚动包裹（UI规范 10 §4.6.26）——五列表在 375px 视口
           必然溢出撑破容器；沿 web 侧收敛先例（TenantModelPolicyPanel）的包裹样式。 -->
      <div style="overflow-x:auto">
        <table class="tnd__table">
          <thead>
            <tr>
              <th>{{ t('tnd.date') }}</th>
              <th>{{ t('tnd.requests') }}</th>
              <th>{{ t('tnd.success') }}</th>
              <th>{{ t('tnd.errors') }}</th>
              <th>{{ t('tnd.credits') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in stats.daily" :key="d.date">
              <td class="tnd__td-l">{{ d.date }}</td>
              <td class="tnd__td-v">{{ fmtInt(d.requests) }}</td>
              <td class="tnd__td-v">{{ fmtInt(d.success) }}</td>
              <td class="tnd__td-v" :class="{ 'tnd__td-err': d.errors > 0 }">{{ fmtInt(d.errors) }}</td>
              <td class="tnd__td-v">{{ fmtInt(d.credits) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="!stats.by_model.length" class="tnd__msg">{{ t('tnd.emptyModels') }}</p>
    </section>

    <!-- ══════ 用户 ══════ -->
    <section v-if="users" class="tnd__panel">
      <span class="tnd__panel-title">{{ t('tnd.users') }}</span>
      <div class="tnd__grid">
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.needPwd') }}</span>
          <span class="tnd__cell-v">{{ needPwdCount }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.disabled') }}</span>
          <span class="tnd__cell-v">{{ disabledCount }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.neverLoggedIn') }}</span>
          <span class="tnd__cell-v">{{ neverLoggedIn }}</span>
        </span>
        <span class="tnd__cell">
          <span class="tnd__cell-l">{{ t('tnd.total') }}</span>
          <span class="tnd__cell-v">{{ users.length }}</span>
        </span>
      </div>
      <p v-if="!users.length" class="tnd__msg">{{ t('tnd.emptyUsers') }}</p>
      <ul v-else class="tnd__list">
        <li v-for="u in users" :key="u.id" class="tnd__item">
          <div class="tnd__item-head">
            <span class="tnd__title">{{ u.display_name || u.username }}</span>
            <span class="tnd__badge">
              <StatusDot :tone="u.enabled ? 'success' : 'muted'" />
              <span class="tnd__badge-t">{{ u.role }}</span>
            </span>
          </div>
          <p class="tnd__meta">{{ u.username }} · {{ u.email }}</p>
          <!-- ★★★ last_login_at 无 omitempty：键在值为 null = 从未登录 -->
          <p v-if="tenantUserNeverLoggedIn(u)" class="tnd__note tnd__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('tnd.neverLoggedNote') }}</span>
          </p>
          <p v-else class="tnd__meta">{{ t('tnd.lastLogin') }}: {{ relativeTime(u.last_login_at) }}</p>
          <p v-if="u.must_change_password" class="tnd__note tnd__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('tnd.mustChangePwd') }}</span>
          </p>
        </li>
      </ul>
    </section>

    <!-- ══════ 密钥 ══════ -->
    <section v-if="keys" class="tnd__panel">
      <span class="tnd__panel-title">{{ t('tnd.keys') }}</span>
      <p class="tnd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('tnd.keyOrderNote') }}</span>
      </p>
      <p v-if="!keys.length" class="tnd__msg">{{ t('tnd.emptyKeys') }}</p>
      <ul v-else class="tnd__list">
        <li v-for="k in keys" :key="k.id" class="tnd__item">
          <div class="tnd__item-head">
            <span class="tnd__title">{{ k.key_prefix }}</span>
            <span class="tnd__badge">
              <StatusDot :tone="k.enabled ? 'success' : 'muted'" />
              <span class="tnd__badge-t">{{ k.status }}</span>
            </span>
          </div>
          <p class="tnd__meta">
            {{ fmtInt(k.total_requests) }} {{ t('tnd.requests') }}
            <span class="tnd__sep">·</span>
            ${{ fmtNum(k.total_cost_usd, 4) }}
          </p>
          <!-- ★ 这几个指针带 omitempty：键不存在 ≠ 没查到 -->
          <p class="tnd__meta">
            {{ t('tnd.alias') }}: {{ tenantKeyHasAlias(k) ? k.key_alias : t('tnd.keyAbsent') }}
            <span class="tnd__sep">·</span>
            {{ t('tnd.owner') }}: {{ tenantKeyHasOwner(k) ? k.owner_user : t('tnd.keyAbsent') }}
          </p>
          <p v-if="tenantKeyNeverExpires(k)" class="tnd__meta">{{ t('tnd.neverExpires') }}</p>
          <p v-else class="tnd__meta">{{ t('tnd.expiresAt') }}: {{ fmtTime(k.expires_at) }}</p>
        </li>
      </ul>
    </section>

    <div class="tnd__actions">
      <button type="button" class="tnd__btn" @click="goBack">{{ t('tnd.backToList') }}</button>
      <button type="button" class="tnd__btn tnd__btn--primary" @click="reload">{{ t('common.refresh') }}</button>
    </div>

    <p class="tnd__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('tnd.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.tnd__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.tnd__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.tnd__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.tnd__seg { display: flex; gap: 6px; margin-bottom: 8px; }
.tnd__seg-btn { flex: 1; min-height: 48px; font-size: 13px; border-radius: 8px;
  border: 1px solid var(--border, #ddd); background: transparent; color: inherit; }
.tnd__seg-btn--on { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }
.tnd__actions { display: flex; gap: 8px; margin-bottom: 12px; }
.tnd__btn { flex: 1; min-height: 48px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--border, #ddd); background: transparent; color: inherit; }
.tnd__btn--primary { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }

.tnd__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.tnd__msg--err { color: var(--app-danger); }
.tnd__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.tnd__note--warn { color: var(--app-warning); }
.tnd__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.tnd__cell { display: flex; flex-direction: column; }
.tnd__cell-l { font-size: 11px; color: var(--app-text-muted); }
.tnd__cell-v { font-size: 14px; font-weight: 600; }

.tnd__list { list-style: none; margin: 0; padding: 0; }
.tnd__item { padding: 10px 0; border-top: 1px solid var(--border, #eee); }
.tnd__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.tnd__title { font-size: 14px; font-weight: 600; }
.tnd__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.tnd__badge-t { color: var(--app-text-secondary); }
.tnd__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.tnd__sep { margin: 0 4px; opacity: 0.5; }

.tnd__table { width: 100%; border-collapse: collapse; margin-top: 8px; font-size: 12px; }
.tnd__table th { text-align: left; font-weight: 500; color: var(--app-text-muted); font-size: 11px;
  border-bottom: 1px solid var(--border, #eee); padding: 4px 2px; }
.tnd__table td { padding: 4px 2px; vertical-align: middle; }
.tnd__td-l { color: var(--app-text-secondary); white-space: nowrap; }
.tnd__td-v { font-weight: 600; text-align: right; }
.tnd__td-err { color: var(--app-danger); }
</style>