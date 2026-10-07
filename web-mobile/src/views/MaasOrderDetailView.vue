<script setup lang="ts">
// MaasOrderDetailView — MaaS 订单详情（**superAdmin 档**）。
//
// GET /api/admin/maas/orders/{id}
//
// ⚠️⚠️⚠️ 与列表页对着看，三处最容易搞反（详见 api/maas.ts 文件头）：
//
// (1) ★★★★★★ 响应是**裸对象**（`writeJSON(w, 200, order)`），
//     **没有** `items` 也没有任何包装键 ⇒ 与列表的 `{items:[…]}` 形状**不同**。
// (2) ★★★★★ 出错时**一律 404**（`writeError(w, 404, "order not found")`），
//     不区分「订单不存在」与「查询失败」⇒ 404 **不许**只说「订单不存在」。
// (3) ★★★★★ `payment_hint` / `stub_mode` **只有这里才有**
//     （`enrichOrderPaymentHint` 只在 `GetOrder` 里调）。
//     ⇒ 列表页显示「没有支付信息」是**端点差异**，本页才是真值。
//
// ★ 本页只读，`POST /orders/{id}/confirm` 不碰。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum, fmtTime, relativeTime } from '@/utils/format'
import {
  fetchMaasOrder,
  maasAmountYuan,
  maasNameOrphaned,
  MAAS_ORDER_TYPES,
  MAAS_ORDER_STATUSES,
  MAAS_PAYMENT_CHANNELS,
  type MaasOrder,
} from '@/api/maas'

useHyperPage({ title: () => t('mo.detailTitle') })

const route = useRoute()
const router = useRouter()

const idInput = ref(typeof route.params.id === 'string' ? route.params.id : '')
const detail = ref<MaasOrder | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'badid' | 'notfound' | 'unconfigured' | 'other'>('none')
const loading = ref(false)

/**
 * ★★ 后端是 `strconv.ParseInt(parts[0], 10, 64)`（maas_handlers.go:653）
 * ⇒ 只吃**纯十进制数字**。
 * 而 `Number()` 会接受 `'1e3'` / `'7.0'` / `'0x10'` / `'+7'` 并解析成整数
 * ⇒ 直接 `Number()` 的话这些串**本地能过、后端必回 400 `invalid order id`**。
 * 所以这里必须先卡正则，再谈数值。
 */
const idNum = computed(() => {
  const raw = idInput.value.trim()
  if (!/^\d+$/.test(raw)) return null
  const n = Number(raw)
  return Number.isSafeInteger(n) && n > 0 ? n : null
})

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
      return 'info'
  }
}

function isKnownEnum(list: readonly string[], v: string): boolean {
  return (list as readonly string[]).includes(v)
}

async function load(id: number): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    detail.value = await fetchMaasOrder(id)
  } catch (e) {
    // ★★ 抛错不许退化成「没数据」
    detail.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    if (/database not configured/i.test(msg)) errorKind.value = 'unconfigured'
    // ★ 400 类（后端 `invalid order id`，或客户端自己的正整数守卫）都归 badid
    else if (/invalid order id|正整数/i.test(msg)) errorKind.value = 'badid'
    // ★★★ 后端把「不存在」与「查询失败」都写成 404 ⇒ 不能只判成 notfound
    else if (/not found|404/i.test(msg)) errorKind.value = 'notfound'
    else errorKind.value = 'other'
  } finally {
    loading.value = false
  }
}

function reload(): void {
  const n = idNum.value
  if (n === null) {
    detail.value = null
    error.value = t('mo.badId')
    errorKind.value = 'badid'
    return
  }
  void load(n)
}

function goBack(): void {
  void router.push('/maas-orders')
}

reload()

watch(
  () => route.params.id,
  () => {
    idInput.value = typeof route.params.id === 'string' ? route.params.id : ''
    reload()
  },
)

onBeforeUnmount(() => {
  detail.value = null
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
      <p v-if="errorKind === 'badid'" class="mo__note mo__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mo.badIdNote') }}</span>
      </p>
      <!-- ★★★ 404 身兼两职 -->
      <p v-else-if="errorKind === 'notfound'" class="mo__note mo__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mo.notFoundNote') }}</span>
      </p>
      <p v-else-if="errorKind === 'unconfigured'" class="mo__note mo__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mo__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 详情 ══════ -->
    <section v-if="detail" class="mo__panel">
      <div class="mo__item-head">
        <span class="mo__title">{{ detail.order_no }}</span>
        <span class="mo__badge">
          <StatusDot :tone="statusTone(detail.status)" />
          <span class="mo__badge-t">
            {{
              isKnownEnum(MAAS_ORDER_STATUSES, detail.status)
                ? t('mo.status_' + detail.status)
                : detail.status
            }}
          </span>
        </span>
      </div>

      <!-- ★★ 金额单位是分 -->
      <p class="mo__amount">
        <span class="mo__amount-v">¥{{ fmtNum(maasAmountYuan(detail), 2) }}</span>
        <span class="mo__amount-u">{{ t('mo.credits') }}: {{ fmtInt(detail.credits) }}</span>
      </p>

      <div class="mo__grid">
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.orderId') }}</span>
          <span class="mo__cell-v">{{ detail.id }}</span>
        </span>
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.tenant') }}</span>
          <span class="mo__cell-v">{{ detail.tenant_id || '—' }}</span>
        </span>
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.orderType') }}</span>
          <span class="mo__cell-v">
            {{
              isKnownEnum(MAAS_ORDER_TYPES, detail.order_type)
                ? t('mo.type_' + detail.order_type)
                : detail.order_type
            }}
          </span>
        </span>
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.channel') }}</span>
          <span class="mo__cell-v">
            {{
              isKnownEnum(MAAS_PAYMENT_CHANNELS, detail.payment_channel)
                ? t('mo.channel_' + detail.payment_channel)
                : detail.payment_channel
            }}
          </span>
        </span>
      </div>

      <!-- ★★ 孤儿：有 id 但名字是空串 = 关联行已被删 -->
      <template v-if="detail.order_type === 'topup'">
        <p class="mo__note" :class="{ 'mo__note--warn': maasNameOrphaned(detail.package_id, detail.package_name) }">
          <AppIcon :name="maasNameOrphaned(detail.package_id, detail.package_name) ? 'alert' : 'key'" :size="13" />
          <span>
            {{ t('mo.package') }}: {{ detail.package_name || '—' }}
            <template v-if="maasNameOrphaned(detail.package_id, detail.package_name)">
              （{{ t('mo.orphanTag') }} — {{ t('mo.orphanNote') }}）
            </template>
          </span>
        </p>
      </template>
      <template v-else>
        <p class="mo__note" :class="{ 'mo__note--warn': maasNameOrphaned(detail.plan_id, detail.plan_name) }">
          <AppIcon :name="maasNameOrphaned(detail.plan_id, detail.plan_name) ? 'alert' : 'key'" :size="13" />
          <span>
            {{ t('mo.plan') }}: {{ detail.plan_name || '—' }}
            <template v-if="maasNameOrphaned(detail.plan_id, detail.plan_name)">
              （{{ t('mo.orphanTag') }} — {{ t('mo.orphanNote') }}）
            </template>
          </span>
        </p>
      </template>

      <!-- ★★★ 这一段只有详情页才有：列表端点不 enrich -->
      <p class="mo__panel-sub">{{ t('mo.paymentSection') }}</p>
      <p class="mo__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mo.enrichOnlyNote') }}</span>
      </p>
      <div class="mo__grid">
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.paymentHint') }}</span>
          <span class="mo__cell-v">{{ detail.payment_hint || t('maas.noValue') }}</span>
        </span>
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.stubMode') }}</span>
          <span class="mo__cell-v">
            <!-- ★ bool + omitempty：false 时**键整个不存在**，与「显式 false」不可区分 -->
            <StatusDot :tone="detail.stub_mode === true ? 'danger' : 'success'" />
            <span class="mo__cell-s">
              {{ detail.stub_mode === true ? t('mo.stubOn') : t('mo.stubOff') }}
            </span>
          </span>
        </span>
      </div>
      <p v-if="detail.stub_mode === undefined" class="mo__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mo.stubMissingNote') }}</span>
      </p>

      <p v-if="detail.qr_url" class="mo__note">
        <AppIcon name="expand" :size="13" />
        <span class="mo__break">{{ detail.qr_url }}</span>
      </p>
      <p v-if="detail.qr_payload" class="mo__note">
        <AppIcon name="key" :size="13" />
        <span class="mo__break">{{ detail.qr_payload }}</span>
      </p>

      <!-- ══════ 时间线 ══════ -->
      <p class="mo__panel-sub">{{ t('mo.timeline') }}</p>
      <div class="mo__grid">
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.created') }}</span>
          <span class="mo__cell-v">{{ fmtTime(detail.created_at) }}</span>
        </span>
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.updated') }}</span>
          <span class="mo__cell-v">{{ fmtTime(detail.updated_at) }}</span>
        </span>
        <span class="mo__cell">
          <!-- ★ paid_at 是指针 + omitempty ⇒ 未支付时键不存在 -->
          <span class="mo__cell-l">{{ t('mo.paidAt') }}</span>
          <span class="mo__cell-v">
            {{ detail.paid_at ? fmtTime(detail.paid_at) : t('mo.notPaid') }}
            <span v-if="detail.paid_at" class="mo__cell-s">({{ relativeTime(detail.paid_at) }})</span>
          </span>
        </span>
        <span class="mo__cell">
          <span class="mo__cell-l">{{ t('mo.expiresAt') }}</span>
          <span class="mo__cell-v">{{ fmtTime(detail.expires_at) }}</span>
        </span>
      </div>

      <p v-if="detail.note" class="mo__note">
        <AppIcon name="key" :size="13" />
        <span class="mo__break">{{ detail.note }}</span>
      </p>

      <!-- ★ 只读：本页不提供 confirm -->
      <p class="mo__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mo.readOnlyNote') }}</span>
      </p>
    </section>

    <div class="mo__actions">
      <button type="button" class="mo__btn" @click="goBack">{{ t('mo.backToList') }}</button>
      <button type="button" class="mo__btn mo__btn--primary" @click="reload">{{ t('common.refresh') }}</button>
    </div>
  </div>
</template>

<style scoped>
.mo__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mo__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mo__panel-sub { display: block; font-size: 13px; font-weight: 600; margin: 12px 0 4px; }

.mo__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mo__msg--err { color: var(--app-danger); }
.mo__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mo__note--warn { color: var(--app-warning); }
.mo__break { word-break: break-all; }

.mo__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
.mo__cell { display: flex; flex-direction: column; }
.mo__cell-l { font-size: 11px; color: var(--app-text-muted); }
.mo__cell-v { font-size: 14px; font-weight: 600; }
.mo__cell-s { font-size: 11px; font-weight: 400; color: var(--app-text-muted); margin-left: 4px; }

.mo__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mo__title { font-size: 15px; font-weight: 700; }
.mo__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.mo__badge-t { color: var(--app-text-secondary); }
.mo__amount { display: flex; align-items: baseline; gap: 10px; margin: 8px 0 0; }
.mo__amount-v { font-size: 22px; font-weight: 700; }
.mo__amount-u { font-size: 12px; color: var(--app-text-muted); }

/* ★ R1：新增交互控件 ≥48 CSS px */
.mo__actions { display: flex; gap: 8px; }
.mo__btn { flex: 1; min-height: 48px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-border); background: transparent; color: inherit; }
.mo__btn--primary { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }
</style>