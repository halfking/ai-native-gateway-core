<script setup lang="ts">
// DataHorizonView — 「流量数据地平线」告示（2026-10-15，第一百二十九批）。
//
// GET /api/admin/data-horizon
//
// ══════ 本族是**重写**，不是改名 ══════
// 前一代 `V1DataHorizonView` 盯着 v1 停写门（`storage.request_logs_write_enabled`），
// 那个前提在统计链全面 session 化之后已不成立 —— 后端把
// `admin/v1_freeze_notice.go` 整文件换成了 `admin/data_horizon.go`。
// ⇒ 判据从「单配置键」换成「**两个信号对齐**」：
//   `session_turns_hot` 的 MAX(ts) × hub 的 lastActivity。
//
// ══ 权限：抽屉席**不设** `requiresRole`（admin 档，且这是有理由的）══
//   注册 `admin/handler.go:1162`：`mux.HandleFunc("/api/admin/data-horizon", admin(h.handleDataHorizon))`
//   而 `admin := h.admin` 是 `admin/handler.go:962` 的局部别名，
//   `h.admin` = `AdminMiddleware`（`admin/auth.go:86` 起，**全文无 `claims.Role` 比对**）
//   ⇒ 「admin 档」在注册行里的真实含义是「**已认证**」。
//
//   ★★★ 但**不能**因此就放行所有角色 —— 也不能收窄到 `super_admin`：
//   （甲）载荷是**平台级全局事实**（「这台网关的 session_turns 有没有跟上」），
//        六键里没有一丁点租户相关 ⇒ 按批 119 的边界**不过度限制**；
//   （乙）**必须**让 tenant_admin 看到 —— `silence` 自己写着
//        「这类断流没有任何错误信号：统计接口照常返回、数字齐全。
//         接口没报错不代表数据是新的。」
//        把告示挡在 tenant_admin 之外，等于让他们把断流数据读成新鲜数据。
//   ⇒ 所以这一族是**深链兜底的典型反例**：它**不需要**权限兜底，
//     但**必须**把「读到没有告示 ≠ 页面坏了」说清楚。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchDataHorizon,
  dataHorizonIsLive,
  dataHorizonIsStale,
  dataHorizonIsUnknown,
  dataHorizonExactlyOneFlag,
  dataHorizonSourceIsSessionTurns,
  dataHorizonIsUnexpectedShape,
  DATA_HORIZON_SOURCE,
  DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES,
  DATA_HORIZON_LAG_TOLERANCE_MINUTES,
  type DataHorizonResponse,
} from '@/api/dataHorizon'

useHyperPage({ title: () => t('dh.title') })

const data = ref<DataHorizonResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

/** ★★ `null`（没取到）与「取到了、值为 null（数据新鲜或网关空闲）」是**两件事**。 */
const hasData = computed(() => data.value !== null)
const notice = computed(() => (data.value ? data.value.data_horizon : undefined))

/**
 * ★★★★ 三态互斥且必居其一（模块头 (2)）。
 *
 * ⚠️ 注意与前一代不同：第三档旧名 `unavailable`（「不可用」）、
 *   新名 `unknown`（「读不到」）。**这不是改名了事** ——
 *   前一代的 unavailable 说的是「配置值读不出来」，
 *   这一代的 unknown 说的是「**session 数据读不出来**」，
 *   而「读不到」是**正常状态而非故障**（模块头 (6)：空表 + 有流量 = stale）。
 * ⇒ 页面文案必须体现这个差别，不能沿用旧措辞。
 */
const state = computed<'live' | 'stale' | 'unknown' | 'unexpected-shape'>(() => {
  if (!data.value) return 'unexpected-shape'
  if (dataHorizonIsLive(data.value)) return 'live'
  if (dataHorizonIsStale(data.value)) return 'stale'
  if (dataHorizonIsUnknown(data.value)) return 'unknown'
  // ★ 有对象但两个 flag 都不是 true ⇒ 形状不对（后端不产出这种，见 (2)）。
  return 'unexpected-shape'
})

// ══ 自检红线（每一条都能被变异打掉）═════════════════════════════════════

/** ★★★★★ (2) 有对象 ⇒ `stale` 与 `unknown` **恰好一个**为 true。 */
const flagBad = computed(() =>
  data.value ? !dataHorizonExactlyOneFlag(data.value) : false,
)
/** ★★★ (9) `source` 恒为 `session_turns_hot`（被检查的数据源族）。 */
const sourceBad = computed(() =>
  notice.value ? !dataHorizonSourceIsSessionTurns(notice.value) : false,
)
/** ★★★ (2) 「有对象但两个 flag 都不是 true」这一档必须显式报出来。 */
const shapeBad = computed(() => (data.value ? dataHorizonIsUnexpectedShape(data.value) : false))

/** ★ (5) 两个时间窗常量：只展示，**不参与判定**（判定在后端）。 */
const recentWindowMin = DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES
const lagToleranceMin = DATA_HORIZON_LAG_TOLERANCE_MINUTES
const sourceFamily = DATA_HORIZON_SOURCE

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchDataHorizon()
  } catch (err) {
    data.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="dh">
    <!-- ══ 全局操作 ══ -->
    <section class="dh__sec">
      <header class="dh__head">
        <h2 class="dh__h">{{ t('dh.title') }}</h2>
        <button type="button" class="dh__btn" data-testid="dh-load" :disabled="loading" @click="load">
          {{ loading ? t('common.loading') : t('dh.load.btn') }}
        </button>
      </header>
      <p class="dh__msg" data-testid="dh-scope-note">{{ t('dh.scope.note') }}</p>
      <p class="dh__msg" data-testid="dh-header-note">{{ t('dh.scope.headerNote') }}</p>

      <p v-if="error" class="dh__msg dh__msg--err" data-testid="dh-error">{{ error }}</p>
      <p v-if="!hasData && !error && !loading" class="dh__msg" data-testid="dh-empty">
        {{ t('dh.scope.empty') }}
      </p>
    </section>

    <!-- ══ 状态卡 ══ -->
    <section v-if="hasData" class="dh__sec">
      <header class="dh__head">
        <h2 class="dh__h">{{ t('dh.state.title') }}</h2>
        <span class="dh__badge" data-testid="dh-state">{{ t(`dh.state.${state}`) }}</span>
      </header>

      <!-- ★★★ 三态各有**独立**文案；unknown 那套刻意不说「已断流」也不说「正常」。 -->
      <p
        class="dh__msg"
        :class="{ 'dh__msg--warn': state !== 'live' }"
        data-testid="dh-state-note"
      >
        {{ t(`dh.state.note.${state}`) }}
      </p>

      <template v-if="notice">
        <ul class="dh__list">
          <li class="dh__card" data-testid="dh-detail">
            <div class="dh__list-row">
              <span>{{ t('dh.field.source') }}</span>
              <span class="dh__mono" data-testid="dh-source">{{ notice.source }}</span>
            </div>
            <!-- ★★★ (5) 两个时间窗是本代新增的判据参数，写出来便于运维对账。 -->
            <div class="dh__list-row">
              <span>{{ t('dh.field.recentWindow') }}</span>
              <span class="dh__mono" data-testid="dh-recent-window">{{ recentWindowMin }} min</span>
            </div>
            <div class="dh__list-row">
              <span>{{ t('dh.field.lagTolerance') }}</span>
              <span class="dh__mono" data-testid="dh-lag-tolerance">{{ lagToleranceMin }} min</span>
            </div>
          </li>
        </ul>

        <!-- ★★★ `effect` / `silence` / `hint` 是**给人读的文案**，只展示，不参与判状态。 -->
        <p class="dh__msg" data-testid="dh-effect">{{ notice.effect }}</p>
        <p class="dh__msg dh__msg--warn" data-testid="dh-silence">{{ notice.silence }}</p>
        <p class="dh__msg" data-testid="dh-hint">{{ notice.hint }}</p>
      </template>

      <!-- ══ 自检红线：响应级不变式，**不**嵌在上面的 v-if 里 ══ -->
      <p v-if="flagBad" class="dh__msg dh__msg--err" data-testid="dh-flag-bad">
        {{ t('dh.check.flagBad') }}
      </p>
      <p v-if="shapeBad" class="dh__msg dh__msg--err" data-testid="dh-shape-bad">
        {{ t('dh.check.shapeBad') }}
      </p>
      <p v-if="sourceBad" class="dh__msg dh__msg--err" data-testid="dh-source-bad">
        {{ t('dh.check.sourceBad') }}
      </p>

      <!-- ★ 把「本页不参与判定」写出来：判定在后端，前端只是如实转述。 -->
      <p class="dh__msg" data-testid="dh-source-note">
        {{ t('dh.source.note', { source: sourceFamily }) }}
      </p>
    </section>
  </div>
</template>

<style scoped>
.dh {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px;
}
.dh__sec {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border-radius: 12px;
  background: var(--app-surface, #fff);
  border: 1px solid var(--app-border, #e5e7eb);
}
.dh__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.dh__h {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}
.dh__msg {
  font-size: 13px;
  line-height: 1.5;
  margin: 0;
  color: var(--app-text-secondary);
}
.dh__msg--err {
  color: var(--app-danger);
}
.dh__msg--warn {
  color: var(--app-warning);
}
.dh__badge {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--app-primary);
  color: #fff;
  white-space: nowrap;
}
/* ★ 触控目标 ≥48 CSS px（touch:check 的硬门）。 */
.dh__btn {
  min-height: 48px;
  min-width: 96px;
  padding: 0 16px;
  border-radius: 10px;
  border: 1px solid var(--app-border);
  background: var(--app-primary);
  color: #fff;
  font-size: 14px;
  cursor: pointer;
}
.dh__btn:disabled {
  opacity: 0.6;
  cursor: default;
}
.dh__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.dh__card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px;
  border-radius: 8px;
  background: var(--app-bg);
  border: 1px solid var(--app-border);
}
.dh__list-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
}
.dh__mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  word-break: break-all;
  text-align: right;
}
</style>
