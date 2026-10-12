<script setup lang="ts">
// MaasCatalogView — MaaS 租户/客户面：**目录与价目**（**admin 档**）。
//
// GET /api/maas/settings + /api/maas/models + /api/maas/plans + /api/maas/topup-packages
//
// ⚠️⚠️⚠️ 这四条端点在 `/api/maas/` 下，鉴权是 `h.admin(...)`（**不是** superAdmin），
// 与 `maas.ts` 前两段的 `/api/admin/maas/**` 是**同族不同档** ——
// 接线时抽屉席**不能**设 requiresRole（设了 tenant_admin 会被 403 挡在门外）。
//
// 四个最容易渲染错的语义（详见 api/maas.ts 坑 19~24）：
//
// (1) ★★★★★★ `/api/maas/settings` **只有 3 个键**（后端自陈「Tenants see
//     conversion knobs only, not internal cost data」）⇒ 租户**看不到**折扣、
//     也看不到 `base_credits_per_1m_in`。本页据此说明「这里的基价是旧字段」。
// (2) ★★★★★ `/api/maas/models` 的行是 **`ModelRateRow`（12 键）**，
//     与 admin 档那个 30 键的 `AdminModelRateRow` **是两个结构**：
//     **只有 4 维** —— **没有** image / audio / video。
//     ⇒ 本页只画 4 维，并显式说明另外三维**不在这条端点上**（不是免费）。
// (3) ★★★★ 模态是「盖章」还是「按名字猜」在响应里**分不出来**
//     （`ModelRateRow` 没有 modality_source 字段）⇒ 本页**不说**「配置的模态」。
// (4) ★★★ plans / topup-packages 只列 `enabled = TRUE`；
//     且 `jsonSlice` 保证空清单是 `[]` 而**不是** null。
//
// ★ 写操作本页一律不碰（settings PUT、model-rates 增删改、orders confirm…）。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchMaasPublicSettings,
  fetchMaasPublicModels,
  fetchMaasPublicPlans,
  fetchMaasPublicTopupPackages,
  maasCatalogPriceYuan,
  maasUnitPriceFenPerCredit,
  maasPublicModelLacksMultiDims,
  maasPublicDimValue,
  MAAS_PUBLIC_DIMS,
  type MaasPublicSettings,
  type MaasPublicModel,
  type MaasPlan,
  type MaasTopupPackage,
} from '@/api/maas'

useHyperPage({ title: () => t('mp.title') })

// ★★★ 维表与取值都从 API 模块取（`MAAS_PUBLIC_DIMS` 只有 4 个值）——
//   这样 i18n 动态键判据能 import **同一个常量**，不手抄后缀。
//   响应里**没有** image / audio / video 的键：不是 0，是**根本不存在**
//   （`ModelRateRow` 只有 12 个键，见 maas/service.go:536）。

const settings = ref<MaasPublicSettings | null>(null)
const models = ref<MaasPublicModel[] | null>(null)
const plans = ref<MaasPlan[] | null>(null)
const topups = ref<MaasTopupPackage[] | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)
const keyword = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    // ★ 四条并发；任一形状不符都抛错 ⇒ 整个页面进错误态，**不许**局部静默
    const [s, m, p, tp] = await Promise.all([
      fetchMaasPublicSettings(),
      fetchMaasPublicModels(),
      fetchMaasPublicPlans(),
      fetchMaasPublicTopupPackages(),
    ])
    settings.value = s
    models.value = m.items
    plans.value = p.items
    topups.value = tp.items
  } catch (e) {
    settings.value = null
    models.value = null
    plans.value = null
    topups.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

const modelRows = computed<MaasPublicModel[]>(() => {
  const all = models.value ?? []
  const q = keyword.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(
    (m) =>
      m.canonical_name.toLowerCase().includes(q) ||
      m.display_name.toLowerCase().includes(q) ||
      m.vendor.toLowerCase().includes(q),
  )
})

/** ★★★ 这条端点**只有 4 维**：另外三维的键在响应里根本不存在。 */
const allLackMultiDims = computed(
  () => (models.value ?? []).length > 0 && (models.value ?? []).every((m) => maasPublicModelLacksMultiDims(m)),
)

/** ★ 三块：换算旋钮 / 套餐 / 充值包。缺哪块都算坏。 */
const ready = computed(() => settings.value !== null && models.value !== null)

void load()

onBeforeUnmount(() => {
  settings.value = null
  models.value = null
  plans.value = null
  topups.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="mp">
    <!-- ══════ 错误 ══════ -->
    <section v-if="error" class="mp__panel">
      <p class="mp__msg mp__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mp__note mp__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mp__msg">{{ t('common.loading') }}</p>

    <template v-if="ready">
      <!-- ══════ 换算旋钮（只有 3 个键） ══════ -->
      <section class="mp__panel">
        <span class="mp__panel-title">{{ t('mp.conversion') }}</span>
        <div class="mp__grid">
          <span class="mp__cell">
            <span class="mp__cell-l">{{ t('mp.centsPerCredit') }}</span>
            <span class="mp__cell-v">{{ fmtNum(settings!.cents_per_credit, 4) }}</span>
          </span>
          <span class="mp__cell">
            <span class="mp__cell-l">{{ t('mp.currency') }}</span>
            <span class="mp__cell-v">{{ settings!.currency_display || t('maas.noValue') }}</span>
          </span>
          <span class="mp__cell">
            <!-- ★★ 这是**旧字段**；租户面拿不到 base_credits_per_1m_in -->
            <span class="mp__cell-l">{{ t('mp.legacyBase') }}</span>
            <span class="mp__cell-v">{{ fmtInt(settings!.base_credits_per_1m) }}</span>
          </span>
        </div>

        <!-- ★★★ 租户看不到成本数据，必须说破，否则像「没配」 -->
        <p class="mp__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mp.settingsOnlyThree') }}</span>
        </p>
      </section>

      <!-- ══════ 套餐 ══════ -->
      <section class="mp__panel">
        <span class="mp__panel-title">{{ t('mp.plans') }}</span>
        <p class="mp__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mp.enabledOnlyNote') }}</span>
        </p>
        <p v-if="!plans!.length" class="mp__msg">{{ t('mp.emptyPlans') }}</p>
        <ul v-else class="mp__list">
          <li v-for="p in plans!" :key="p.code" class="mp__item">
            <div class="mp__item-head">
              <span class="mp__title">{{ p.name }}</span>
              <span class="mp__price">¥{{ fmtNum(maasCatalogPriceYuan(p.price_cents), 2) }}</span>
            </div>
            <p class="mp__meta">
              {{ t('mp.code') }}: {{ p.code }}
              <span class="mp__sep">·</span>
              {{ t('mp.tier') }}: {{ p.tier }}
              <span class="mp__sep">·</span>
              {{ t('mp.credits') }}: {{ fmtInt(p.monthly_credits) }}
            </p>
            <p v-if="maasUnitPriceFenPerCredit(p.price_cents, p.monthly_credits) !== null" class="mp__unit">
              {{ t('mp.unitPrice', { v: fmtNum(maasUnitPriceFenPerCredit(p.price_cents, p.monthly_credits)!, 4) }) }}
            </p>
          </li>
        </ul>
      </section>

      <!-- ══════ 充值包 ══════ -->
      <section class="mp__panel">
        <span class="mp__panel-title">{{ t('mp.topups') }}</span>
        <p class="mp__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mp.enabledOnlyNote') }}</span>
        </p>
        <p v-if="!topups!.length" class="mp__msg">{{ t('mp.emptyTopups') }}</p>
        <ul v-else class="mp__list">
          <li v-for="tp in topups!" :key="tp.code" class="mp__item">
            <div class="mp__item-head">
              <span class="mp__title">{{ tp.name }}</span>
              <span class="mp__price">¥{{ fmtNum(maasCatalogPriceYuan(tp.price_cents), 2) }}</span>
            </div>
            <p class="mp__meta">
              {{ t('mp.code') }}: {{ tp.code }}
              <span class="mp__sep">·</span>
              {{ t('mp.tier') }}: {{ tp.tier }}
              <span class="mp__sep">·</span>
              {{ t('mp.credits') }}: {{ fmtInt(tp.credits_amount) }}
            </p>
            <p v-if="maasUnitPriceFenPerCredit(tp.price_cents, tp.credits_amount) !== null" class="mp__unit">
              {{ t('mp.unitPrice', { v: fmtNum(maasUnitPriceFenPerCredit(tp.price_cents, tp.credits_amount)!, 4) }) }}
            </p>
          </li>
        </ul>
      </section>

      <!-- ══════ 模型价目 ══════ -->
      <section class="mp__panel">
        <span class="mp__panel-title">{{ t('mp.models') }}</span>

        <!-- ★★★★★ 这条端点只有 4 维 -->
        <p v-if="allLackMultiDims" class="mp__note mp__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mp.onlyFourDims') }}</span>
        </p>

        <!-- ★★★ 模态的「盖章/猜测」在响应里分不出来 -->
        <p class="mp__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mp.modalityNotVerifiable') }}</span>
        </p>

        <label class="mp__label" for="mp-kw">{{ t('mp.search') }}</label>
        <input
          id="mp-kw"
          v-model="keyword"
          class="mp__input"
          type="search"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          :placeholder="t('mp.searchPlaceholder')"
        />

        <p v-if="!modelRows.length" class="mp__msg">{{ t('mp.emptyModels') }}</p>

        <ul v-if="modelRows.length" class="mp__list">
          <li v-for="m in modelRows" :key="m.canonical_name" class="mp__item">
            <div class="mp__item-head">
              <span class="mp__title">{{ m.display_name }}</span>
              <span class="mp__id">{{ m.canonical_name }}</span>
            </div>
            <p class="mp__meta">
              {{ t('mp.vendor') }}: {{ m.vendor }}
              <span class="mp__sep">·</span>
              {{ t('mp.modality') }}: {{ m.modality }}
              <template v-if="m.context_window !== undefined">
                <span class="mp__sep">·</span>
                {{ t('mp.contextWindow') }}: {{ fmtInt(m.context_window) }}
              </template>
            </p>
            <table class="mp__table">
              <thead>
                <tr>
                  <th>{{ t('mp.dim') }}</th>
                  <th>{{ t('mp.creditsPer1M') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="d in MAAS_PUBLIC_DIMS" :key="d">
                  <td class="mp__td-l">{{ t('mp.dim_' + d) }}</td>
                  <td class="mp__td-v">{{ fmtInt(maasPublicDimValue(m, d)) }}</td>
                </tr>
              </tbody>
            </table>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.mp__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mp__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mp__label { display: block; font-size: 12px; color: var(--app-text-muted); margin-bottom: 4px; }
/* ★ R1：新增交互控件 ≥48 CSS px */
.mp__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }

.mp__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mp__msg--err { color: var(--app-danger); }
.mp__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mp__note--warn { color: var(--app-warning); }
.mp__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
.mp__cell { display: flex; flex-direction: column; }
.mp__cell-l { font-size: 11px; color: var(--app-text-muted); }
.mp__cell-v { font-size: 14px; font-weight: 600; }

.mp__list { list-style: none; margin: 0; padding: 0; }
.mp__item { padding: 10px 0; border-top: 1px solid var(--app-border); }
.mp__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mp__title { font-size: 14px; font-weight: 600; }
.mp__id { font-size: 12px; color: var(--app-text-secondary); }
.mp__price { font-size: 15px; font-weight: 700; }
.mp__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.mp__sep { margin: 0 4px; opacity: 0.5; }
.mp__unit { font-size: 11px; color: var(--app-text-muted); margin: 4px 0 0; }

.mp__table { width: 100%; border-collapse: collapse; margin-top: 8px; font-size: 12px; }
.mp__table th { text-align: left; font-weight: 500; color: var(--app-text-muted); font-size: 11px;
  border-bottom: 1px solid var(--app-border); padding: 4px 2px; }
.mp__table td { padding: 4px 2px; vertical-align: middle; }
.mp__td-l { color: var(--app-text-secondary); white-space: nowrap; }
.mp__td-v { font-weight: 600; text-align: right; }
</style>
