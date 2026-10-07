<script setup lang="ts">
// MaasRatesView — MaaS 每个 canonical 模型的**生效**积分价（**superAdmin 档**）。
//
// GET /api/admin/maas/model-rates
//
// ⚠️⚠️⚠️⚠️ 本页最要紧的一件事：响应里 7 个 `credits_per_1m_*` **不是「库里存的值」**，
// 而是后端 `globalEffective` + `effectiveModelRates` **算出来的生效价**。
// 不复刻那两个函数就没有任何办法回答「这个数字从哪来」，所以本页逐维标注来源。
//
// 四条最容易渲染错的语义（详见 api/maas.ts 文件头）：
//
// (1) ★★★★★ `manual_X = true` **不保证**用自定义值：
//     `pick()` 是三条件与运算 `manual && val != nil && *val > 0`
//     ⇒ 没存值 / 存的是 0 或负数 ⇒ **照样回落到全局**。
//     本页在「已开手动但没生效」时明确说破。
// (2) ★★★★★ `global_discount = 0` 的含义是「**不打折**」，不是「全免」
//     （`normalizeDiscount`：`d <= 0 || d > 1 ⇒ 1`）；且 `applyDiscount` 是 **ceil**。
// (3) ★★★★ `custom_credits_per_1m_*` 是 stored 的**原样拷贝**，
//     ⇒ 完全可能「生效价 = 全局、custom = 300」——因为 manual=false。
//     本页把这种「改了但没启用」显式标出来，否则看起来像数据自相矛盾。
// (4) ★★ `vendor` 的末级兜底是字面量「其他」；只列 **active** 模型；**没有分页**。
//
// ★ 写操作本页一律不碰（PUT/POST/DELETE、batch、plans、orders…）。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchMaasModelRates,
  maasDimUsesCustom,
  maasDimHasDormantCustom,
  maasRowHasNoRateRecord,
  maasEffectiveDiscount,
  maasGlobalBaseIn,
  maasEffectiveSource,
  MAAS_RATE_DIMS,
  MAAS_EFFECTIVE_KEYS,
  MAAS_MANUAL_KEYS,
  MAAS_VENDOR_FALLBACK,
  MAAS_MODALITY_FALLBACK,
  type MaasModelRatesResponse,
  type MaasModelRateRow,
  type MaasRateDim,
} from '@/api/maas'

useHyperPage({ title: () => t('maas.title') })

const data = ref<MaasModelRatesResponse | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)
const keyword = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    data.value = await fetchMaasModelRates()
  } catch (e) {
    // ★★ 抛错**不许**退化成「没配过价」：503 是「MaaS 没开/没接库」。
    data.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

const rows = computed<MaasModelRateRow[]>(() => {
  const all = data.value?.items ?? []
  const q = keyword.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(
    (r) =>
      r.canonical_name.toLowerCase().includes(q) ||
      r.display_name.toLowerCase().includes(q) ||
      r.vendor.toLowerCase().includes(q),
  )
})

/** ★★ 只列 active 模型（SQL 的 WHERE）⇒ 清单「看着不完整」不代表库里没有。 */
const activeOnlyCount = computed(() => rows.value.length)

// ★ 键表是 `as const` 的常量映射；`noUncheckedIndexedAccess` 开着 ⇒ 显式兜底
function dimEffective(r: MaasModelRateRow, d: MaasRateDim): number {
  return (r as unknown as Record<string, number | undefined>)[MAAS_EFFECTIVE_KEYS[d]] ?? 0
}

function dimManual(r: MaasModelRateRow, d: MaasRateDim): boolean {
  return (r as unknown as Record<string, boolean | undefined>)[MAAS_MANUAL_KEYS[d]] === true
}

/** ★★★ 逐维来源：自定义 / 全局（配置来的）/ 全局（硬编码 10000）。 */
function dimSource(r: MaasModelRateRow, d: MaasRateDim): 'custom' | 'global_configured' | 'global_hardcoded' {
  if (!data.value) return 'global_configured'
  return maasEffectiveSource(r, data.value.settings, d)
}

const discount = computed(() => (data.value ? maasEffectiveDiscount(data.value.settings) : 1))
const baseInInfo = computed(() =>
  data.value
    ? maasGlobalBaseIn(data.value.settings)
    : { value: 0, source: 'configured' as const },
)

/** ★★ `global_discount` 配 0 的人以为「全免」，实际是「不打折」——本页点破。 */
const discountLooksFree = computed(() => {
  const raw = data.value?.settings.global_discount
  return typeof raw === 'number' && (raw <= 0 || raw > 1)
})

function vendorIsFallback(r: MaasModelRateRow): boolean {
  return r.vendor === MAAS_VENDOR_FALLBACK
}

function modalityIsFallback(r: MaasModelRateRow): boolean {
  return r.modality === MAAS_MODALITY_FALLBACK
}

void load()

onBeforeUnmount(() => {
  data.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="view-root ms">
    <!-- ══════ 全局基价与折扣 ══════ -->
    <section v-if="data" class="ms__panel">
      <span class="ms__panel-title">{{ t('maas.globalRates') }}</span>

      <div class="ms__grid">
        <span class="ms__cell">
          <span class="ms__cell-l">{{ t('maas.baseIn') }}</span>
          <span class="ms__cell-v">{{ fmtInt(baseInInfo.value) }}</span>
        </span>
        <span class="ms__cell">
          <span class="ms__cell-l">{{ t('maas.discount') }}</span>
          <span class="ms__cell-v">{{ fmtNum(discount * 100, 1) }}%</span>
        </span>
        <span class="ms__cell">
          <span class="ms__cell-l">{{ t('maas.centsPerCredit') }}</span>
          <span class="ms__cell-v">{{ fmtNum(data.settings.cents_per_credit, 4) }}</span>
        </span>
        <span class="ms__cell">
          <span class="ms__cell-l">{{ t('maas.currency') }}</span>
          <span class="ms__cell-v">{{ data.settings.currency_display || t('maas.noValue') }}</span>
        </span>
      </div>

      <!-- ★★ 硬编码 10000 不是「没配」 -->
      <p v-if="baseInInfo.source === 'hardcoded'" class="ms__note ms__note--warn">
        <AppIcon name="alert" :size="14" />
        <span>{{ t('maas.hardcodedNote') }}</span>
      </p>
      <p v-else-if="baseInInfo.source === 'configured_legacy'" class="ms__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('maas.legacyBaseNote') }}</span>
      </p>

      <!-- ★★★ 折扣 0 / >1 被归一成「不打折」 -->
      <p v-if="discountLooksFree" class="ms__note ms__note--warn">
        <AppIcon name="alert" :size="14" />
        <span>{{ t('maas.discountTrap') }}</span>
      </p>
      <p v-else-if="discount !== 1" class="ms__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('maas.discountNote', { d: fmtNum(discount * 100, 1) }) }}</span>
      </p>

      <!-- ★★ 折扣只作用于全局价 -->
      <p class="ms__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('maas.discountScopeNote') }}</span>
      </p>
    </section>

    <!-- ══════ 错误 ══════ -->
    <section v-if="error" class="ms__panel">
      <p class="ms__msg ms__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="ms__note ms__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="ms__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 列表 ══════ -->
    <section v-if="data" class="ms__panel">
      <span class="ms__panel-title">{{ t('maas.models') }}</span>

      <!-- ★ 没有分页：SQL 无 LIMIT ⇒ 「共 N 个」只能数当前返回的条数 -->
      <p class="ms__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('maas.noPagingNote', { n: activeOnlyCount }) }}</span>
      </p>

      <!-- ★★ 只列 active 模型 -->
      <p class="ms__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('maas.activeOnlyNote') }}</span>
      </p>

      <label class="ms__label" for="ms-kw">{{ t('maas.search') }}</label>
      <input
        id="ms-kw"
        v-model="keyword"
        class="ms__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('maas.searchPlaceholder')"
      />

      <p v-if="!rows.length" class="ms__msg">{{ t('maas.empty') }}</p>

      <ul v-if="rows.length" class="ms__list">
        <li v-for="r in rows" :key="r.canonical_id" class="ms__item">
          <div class="ms__item-head">
            <span class="ms__title">{{ r.display_name }}</span>
            <span class="ms__id">{{ r.canonical_name }}</span>
          </div>

          <p class="ms__meta">
            {{ t('maas.vendor') }}: {{ r.vendor }}
            <span v-if="vendorIsFallback(r)" class="ms__tag ms__tag--warn">{{ t('maas.vendorFallbackTag') }}</span>
            ·
            {{ t('maas.modality') }}: {{ r.modality }}
            <span v-if="modalityIsFallback(r)" class="ms__tag">{{ t('maas.modalityFallbackTag') }}</span>
          </p>

          <!-- ★★ updated_at = null ⇒ model_credit_rates 里没有这一行 -->
          <p v-if="maasRowHasNoRateRecord(r)" class="ms__note ms__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('maas.noRateRow') }}</span>
          </p>

          <!-- ★★★ 逐维：生效价 + 手动标记 + 来源，三者一起看才不出错 -->
          <table class="ms__table">
            <thead>
              <tr>
                <th>{{ t('maas.dim') }}</th>
                <th>{{ t('maas.effective') }}</th>
                <th>{{ t('maas.manual') }}</th>
                <th>{{ t('maas.source') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in MAAS_RATE_DIMS" :key="d.key">
                <td class="ms__td-l">{{ t('maas.dim_' + d.key) }}</td>
                <td class="ms__td-v">{{ fmtInt(dimEffective(r, d.key)) }}</td>
                <td>
                  <!-- ★ manual 标记与「是否真在用」是**两件事** -->
                  <StatusDot :tone="maasDimUsesCustom(r, d.key) ? 'success' : dimManual(r, d.key) ? 'danger' : 'muted'" />
                  <span class="ms__td-s">
                    {{ maasDimUsesCustom(r, d.key) ? t('maas.usingCustom') : dimManual(r, d.key) ? t('maas.manualNotEffective') : t('maas.notManual') }}
                  </span>
                </td>
                <td class="ms__td-s" :class="'ms__src--' + dimSource(r, d.key)">
                  {{ t('maas.src_' + dimSource(r, d.key)) }}
                </td>
              </tr>
            </tbody>
          </table>

          <!-- ★★★ 「改了但没启用」显式标出，否则看起来像自相矛盾 -->
          <p
            v-for="d in MAAS_RATE_DIMS.filter((x) => maasDimHasDormantCustom(r, x.key))"
            :key="'dormant-' + d.key"
            class="ms__note ms__note--warn"
          >
            <AppIcon name="alert" :size="13" />
            <span>{{ t('maas.dormantCustom', { dim: t('maas.dim_' + d.key) }) }}</span>
          </p>

          <p v-if="r.is_custom" class="ms__note">
            <AppIcon name="key" :size="13" />
            <span>{{ t('maas.isCustomNote') }}</span>
          </p>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.ms__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.ms__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.ms__label { display: block; font-size: 12px; color: var(--app-text-muted); margin-bottom: 4px; }
/* ★ R1：新增交互控件 ≥48 CSS px */
.ms__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }

.ms__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.ms__msg--err { color: var(--app-danger); }
.ms__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.ms__note--warn { color: var(--app-warning); }
.ms__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
.ms__cell { display: flex; flex-direction: column; }
.ms__cell-l { font-size: 11px; color: var(--app-text-muted); }
.ms__cell-v { font-size: 14px; font-weight: 600; }

.ms__list { list-style: none; margin: 0; padding: 0; }
.ms__item { padding: 10px 0; border-top: 1px solid var(--border, #eee); }
.ms__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.ms__title { font-size: 14px; font-weight: 600; }
.ms__id { font-size: 12px; color: var(--app-text-secondary); }
.ms__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.ms__tag { font-size: 10px; padding: 1px 5px; border-radius: 4px; background: var(--bg-2, #eee); }
.ms__tag--warn { background: var(--app-warning); color: #fff; }

.ms__table { width: 100%; border-collapse: collapse; margin-top: 8px; font-size: 12px; }
.ms__table th { text-align: left; font-weight: 500; color: var(--app-text-muted); font-size: 11px;
  border-bottom: 1px solid var(--border, #eee); padding: 4px 2px; }
.ms__table td { padding: 4px 2px; vertical-align: middle; }
.ms__td-l { color: var(--app-text-secondary); white-space: nowrap; }
.ms__td-v { font-weight: 600; text-align: right; }
.ms__td-s { font-size: 11px; }
.ms__src--custom { color: var(--success, #2e7d32); }
.ms__src--global_configured { color: var(--app-warning); }
.ms__src--global_hardcoded { color: var(--app-danger); }
</style>
