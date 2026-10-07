<script setup lang="ts">
// MaasOrdersView — MaaS 订单列表（**superAdmin 档**）。
//
// GET /api/admin/maas/orders?limit=N
//
// ⚠️⚠️⚠️ 本页最容易渲染错的四件事（详见 api/maas.ts 文件头 坑 11~18）：
//
// (1) ★★★★★★ **这条端点没有分页**，只有 `limit`。
//     后端 `ListOrders(ctx, "", limit)` 只有三个参数，SQL 是
//     `ORDER BY bo.created_at DESC LIMIT $1` —— **没有 OFFSET、没有游标**。
//     ⇒ 想看更老的订单，唯一办法是**调大 limit**，没有「下一页」按钮这回事。
// (2) ★★★★★ 列表**恒定**没有 `payment_hint` / `stub_mode`
//     （`enrichOrderPaymentHint` 只在 `GetOrder` 里调，`ListOrders` 那一圈没有）
//     ⇒ 页面必须说「这是端点差异，去详情页看」，**不能**说「这单没有支付信息」。
// (3) ★★★★ `amount_cents` 单位是**分**，显示要换算成元。
// (4) ★★★ 列表**跨全部租户**（handler 传 `tenantID = ""`，SQL 无租户过滤）
//     ⇒ 看到别人的单子不是越权，是这个端点的设计。
//
// ★ 写操作本页一律不碰（含 `POST /orders/{id}/confirm`）。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum, relativeTime } from '@/utils/format'
import {
  fetchMaasOrders,
  maasAmountYuan,
  maasListLacksPaymentHint,
  maasNameOrphaned,
  maasOrdersMaybeMore,
  MAAS_ORDERS_LIMIT_DEFAULT,
  MAAS_ORDERS_LIMIT_MAX,
  MAAS_ORDER_TYPES,
  MAAS_ORDER_STATUSES,
  MAAS_PAYMENT_CHANNELS,
  type MaasOrder,
} from '@/api/maas'

useHyperPage({ title: () => t('mo.title') })

const router = useRouter()

const data = ref<MaasOrder[] | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)
const keyword = ref('')

// ★ 可选值只在 1..100 内 ⇒ 前端**永远发不出**会被后端改写成 20 的值。
const limitChoices = [MAAS_ORDERS_LIMIT_DEFAULT, 50, MAAS_ORDERS_LIMIT_MAX] as const
const limit = ref<number>(MAAS_ORDERS_LIMIT_DEFAULT)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    const r = await fetchMaasOrders({ limit: limit.value })
    data.value = r.items
  } catch (e) {
    // ★★ 抛错**不许**退化成「没有订单」：503 是「MaaS 没开/没接库」。
    data.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

const rows = computed<MaasOrder[]>(() => {
  const all = data.value ?? []
  const q = keyword.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(
    (o) =>
      o.order_no.toLowerCase().includes(q) ||
      o.tenant_id.toLowerCase().includes(q) ||
      o.status.toLowerCase().includes(q) ||
      o.order_type.toLowerCase().includes(q) ||
      (o.plan_name ?? '').toLowerCase().includes(q) ||
      (o.package_name ?? '').toLowerCase().includes(q),
  )
})

/** ★ 响应没有 total ⇒ 「还有没有更多」只能靠「这页排满了」近似。 */
const maybeMore = computed(() => (data.value ? maasOrdersMaybeMore(data.value, limit.value) : false))

/** ★★ 列表恒无 enrich 字段 ⇒ 全局提示一次，不必每行都吵。 */
const allLackPaymentHint = computed(
  () => (data.value ?? []).every((o) => maasListLacksPaymentHint(o)) && (data.value ?? []).length > 0,
)

/** ★★ 跨租户：只要出现多于一个租户就要说破。 */
const tenantCount = computed(() => new Set((data.value ?? []).map((o) => o.tenant_id)).size)

function statusTone(s: string): 'success' | 'warning' | 'danger' | 'info' | 'muted' {
  switch (s) {
    case 'paid':
      return 'success'
    case 'pending':
      return 'warning'
    case 'cancelled':
      return 'danger'
    case 'expired':
      return 'muted'
    default:
      // ★ 枚举外的值不猜：后端只有 4 个，出现别的就是库里脏数据
      return 'info'
  }
}

function isKnownEnum(list: readonly string[], v: string): boolean {
  return (list as readonly string[]).includes(v)
}

function planLabel(o: MaasOrder): string {
  if (maasNameOrphaned(o.plan_id, o.plan_name)) return t('mo.orphanName')
  return o.plan_name || '—'
}

function packageLabel(o: MaasOrder): string {
  if (maasNameOrphaned(o.package_id, o.package_name)) return t('mo.orphanName')
  return o.package_name || '—'
}

function openDetail(o: MaasOrder): void {
  void router.push({ path: `/maas-orders/${o.id}` })
}

void load()

watch(limit, () => {
  void load()
})

onBeforeUnmount(() => {
  data.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="mo">
    <!-- ══════ 错误 ══════ -->
    <section v-if="error" class="mo__panel">
      <p class="mo__msg mo__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mo__note mo__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mo__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 列表 ══════ -->
    <section v-if="data" class="mo__panel">
      <span class="mo__panel-title">{{ t('mo.orders') }}</span>

      <!-- ★★ 跨全部租户 -->
      <p class="mo__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mo.crossTenantNote', { n: tenantCount }) }}</span>
      </p>

      <!-- ★★ 这条端点没有翻页：只有 limit -->
      <p class="mo__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mo.noPagingNote', { n: data.length }) }}</span>
      </p>

      <!-- limit 选择器（全部在 1..100 内 ⇒ 不会被后端改写成 20） -->
      <label class="mo__label" for="mo-limit">{{ t('mo.limit') }}</label>
      <div id="mo-limit" class="mo__seg" role="group" :aria-label="t('mo.limit')">
        <button
          v-for="n in limitChoices"
          :key="n"
          type="button"
          class="mo__seg-btn"
          :class="{ 'mo__seg-btn--on': limit === n }"
          :aria-pressed="limit === n"
          @click="limit = n"
        >
          {{ n }}
        </button>
      </div>

      <label class="mo__label" for="mo-kw">{{ t('mo.search') }}</label>
      <input
        id="mo-kw"
        v-model="keyword"
        class="mo__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('mo.searchPlaceholder')"
      />

      <p v-if="!rows.length" class="mo__msg">{{ t('mo.empty') }}</p>

      <!-- ★★ 排满了 ⇒ 可能还有更老的，但**没有下一页按钮**，只能调大 limit -->
      <p v-if="maybeMore" class="mo__note mo__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mo.maybeMore', { n: limit }) }}</span>
      </p>

      <ul v-if="rows.length" class="mo__list">
        <li v-for="o in rows" :key="o.id" class="mo__item">
          <button type="button" class="mo__open" @click="openDetail(o)">
            <div class="mo__item-head">
              <span class="mo__title">{{ o.order_no }}</span>
              <!-- ★ 状态是四态枚举；键一定存在（非 omitempty） -->
              <span class="mo__badge">
                <StatusDot :tone="statusTone(o.status)" />
                <span class="mo__badge-t">
                  {{ isKnownEnum(MAAS_ORDER_STATUSES, o.status) ? t('mo.status_' + o.status) : o.status }}
                </span>
              </span>
            </div>

            <p class="mo__meta">
              {{ t('mo.tenant') }}: {{ o.tenant_id }}
              <span class="mo__sep">·</span>
              <!-- ★ order_type 只有两个值；库里出现第三个就是脏数据，原样显示 -->
              {{ isKnownEnum(MAAS_ORDER_TYPES, o.order_type) ? t('mo.type_' + o.order_type) : o.order_type }}
              <span class="mo__sep">·</span>
              {{ t('mo.channel') }}:
              {{
                isKnownEnum(MAAS_PAYMENT_CHANNELS, o.payment_channel)
                  ? t('mo.channel_' + o.payment_channel)
                  : o.payment_channel
              }}
            </p>

            <!-- ★★ 金额单位是分 -->
            <p class="mo__amount">
              <span class="mo__amount-v">¥{{ fmtNum(maasAmountYuan(o), 2) }}</span>
              <span class="mo__amount-u">{{ t('mo.credits') }}: {{ fmtInt(o.credits) }}</span>
            </p>

            <p class="mo__meta">
              {{ t('mo.created') }}: {{ relativeTime(o.created_at) }}
              <span v-if="o.paid_at" class="mo__sep">·</span>
              <span v-if="o.paid_at">{{ t('mo.paidAt') }}: {{ relativeTime(o.paid_at) }}</span>
            </p>

            <p class="mo__meta mo__name">
              <template v-if="o.order_type === 'topup'">
                {{ t('mo.package') }}: <span class="mo__name-v">{{ packageLabel(o) }}</span>
              </template>
              <template v-else>
                {{ t('mo.plan') }}: <span class="mo__name-v">{{ planLabel(o) }}</span>
              </template>
              <span v-if="maasNameOrphaned(o.plan_id, o.plan_name) || maasNameOrphaned(o.package_id, o.package_name)" class="mo__tag mo__tag--warn">
                {{ t('mo.orphanTag') }}
              </span>
            </p>

            <!-- ★★ 这不是「没支付信息」，是列表端点不返回 -->
            <p v-if="maasListLacksPaymentHint(o)" class="mo__note">
              <AppIcon name="key" :size="13" />
              <span>{{ t('mo.noPaymentHintNote') }}</span>
            </p>
          </button>
        </li>
      </ul>

      <!-- ★★ 全局一次提示（每行都写会太吵） -->
      <p v-if="allLackPaymentHint" class="mo__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mo.listLacksEnrichNote') }}</span>
      </p>
    </section>
  </div>
</template>

<style scoped>
.mo__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mo__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mo__label { display: block; font-size: 12px; color: var(--app-text-muted); margin-bottom: 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.mo__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }
.mo__seg { display: flex; gap: 6px; margin-bottom: 10px; }
.mo__seg-btn { flex: 1; min-height: 48px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-border); background: transparent; color: inherit; }
.mo__seg-btn--on { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }
.mo__open { display: block; width: 100%; text-align: left; background: transparent;
  border: 0; border-top: 1px solid var(--app-border); padding: 10px 0; color: inherit; font: inherit;
  min-height: 48px; }

.mo__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mo__msg--err { color: var(--app-danger); }
.mo__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mo__note--warn { color: var(--app-warning); }

.mo__list { list-style: none; margin: 0; padding: 0; }
.mo__item { padding: 0; }
.mo__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mo__title { font-size: 14px; font-weight: 600; }
.mo__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.mo__badge-t { color: var(--app-text-secondary); }
.mo__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.mo__sep { margin: 0 4px; opacity: 0.5; }
.mo__amount { display: flex; align-items: baseline; gap: 10px; margin: 6px 0 0; }
.mo__amount-v { font-size: 17px; font-weight: 700; }
.mo__amount-u { font-size: 12px; color: var(--app-text-muted); }
.mo__tag { font-size: 10px; padding: 1px 5px; border-radius: 4px; background: var(--app-surface-muted); margin-left: 4px; }
.mo__tag--warn { background: var(--app-warning); color: var(--app-on-warning); }
</style>