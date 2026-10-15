<script setup lang="ts">
// V1DataHorizonView — 「v1 读源族是否已停更」的全局告示（2026-10-14，第一百一十九批）。
//
// GET /api/admin/v1-data-horizon
//
// 三态：主键 `v1_data_horizon` **恒在**，值是「七键对象」或**裸 `null`**。
// · `null` ⇒ live（写门明确开着 ⇒ 数据是新的 ⇒ 后端**刻意不告示**）
// · 七键对象且 `frozen: true` ⇒ 明确读到停更
// · 七键对象且 `unknown: true` ⇒ **第三态**：读点没给出可信答案（配置键没有显式取值）
//
// ── 权限 ────────────────────────────────────────────────────────────────────
// ★★★★★ 权限档：**admin 档，抽屉席不设 `requiresRole`**。
//   注册处 `admin/handler.go:1109` 的 `admin(...)` = `AdminMiddleware`
//   （`admin/auth.go:85-140`，**只认证不检 role**）⇒ tenant_admin 后端**能**读。
//
// ★★★★★★ 第一百一十九批把这条结论**升级成明确规则**，因为它与批 115/117/118 的处置
//   **相反**，而那几批并没有错：
//     **过度限制的正当性来自「载荷里有别人的东西」，不是来自「没有租户维度」本身。**
//   本族确实没有租户维度（它本来就不谈租户），但载荷是**平台级全局事实** ——
//   `_request_logs` 写门是否停更，七个键里**没有一丁点租户相关**。
//   ⇒ ★★★ 而且**必须**让 tenant_admin 看到：`silence` 那句话自己写着
//     「接口照常 200、结果集非空、字段齐全，接口没报错不代表数据是新的」——
//     这份告示是**读懂自己数据的必要前提**。把它挡在 tenant_admin 之外，
//     等于让他们把停更数据当成新鲜数据。
//   ⇒ 这就是**第三类（过度限制）的边界**。
//
// ★★★★ 本族最要紧的四件事（源文件头共 11 条，见 `api/v1DataHorizon.ts`）：
//   1. ★★★★★ **主键恒在，值有「对象」与「裸 `null`」两种形态** —— 不是主键消失。
//      ⇒ 判据必须是「值为 null ⇒ 未停更」，**不能**是「键存在 ⇒ 有告示」。
//   2. ★★★★★ 后端**刻意不产出**「有对象但 frozen=false 且 unknown=false」
//      ⇒ 「有对象 ⇒ 两个标志**恰好一个**为 true」是**可自验的强不变量**。
//   3. ★★★★★ `frozen: false` 现在**有合法实例**（就是 unknown 那一档）
//      ⇒ 判「已停更」**不能**只读 `frozen`。
//   4. ★★★★ `notice.source` 是**表名** `"_request_logs"`，**不是**配置来源；
//      判状态只能用 `frozen` / `unknown`，**不能**用 `effect` / `silence` 文案。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchV1DataHorizon,
  v1HorizonIsNotFrozen,
  v1HorizonIsFrozen,
  v1HorizonIsUnknown,
  v1HorizonExactlyOneFlag,
  v1HorizonAffectsAreKnown,
  v1HorizonAffectsCountIs,
  V1_HORIZON_AFFECTS,
  V1_HORIZON_GATE_KEY,
  V1_HORIZON_READ_SOURCE,
  type V1DataHorizonResponse,
} from '@/api/v1DataHorizon'

useHyperPage({ title: () => t('v1h.title') })

const data = ref<V1DataHorizonResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

/** ★★ `null`（没取到）与「取到了、值为 null（live）」是**两件事**，不能都当「无」。 */
const hasData = computed(() => data.value !== null)
const notice = computed(() => (data.value ? data.value.v1_data_horizon : undefined))

/** ★★★ 三态互斥且必居其一（见 2）；本页只用这三个判据，不自己拼条件。 */
const state = computed<'live' | 'frozen' | 'unknown' | 'unknown-shape'>(() => {
  if (!data.value) return 'unknown-shape'
  if (v1HorizonIsNotFrozen(data.value)) return 'live'
  if (v1HorizonIsFrozen(data.value)) return 'frozen'
  if (v1HorizonIsUnknown(data.value)) return 'unknown'
  // ★ 有对象但三个判据都不成立 ⇒ 形状不对（后端不产出这种，见 2）。
  return 'unknown-shape'
})

// ══ 自检红线（每一条都能被变异打掉）═════════════════════════════════════

/** ★★★★★ 见 2：有对象 ⇒ `frozen` 与 `unknown` **恰好一个**为 true。 */
const flagBad = computed(() => (data.value ? !v1HorizonExactlyOneFlag(data.value) : false))
/** ★★★ `affects` 恒是那三值（两个分支字面量完全相同）。 */
const affectsBad = computed(() =>
  notice.value ? !v1HorizonAffectsAreKnown(notice.value) : false,
)
/** ★★★ `affects` 恒是**三个**（后端两个分支都写满）。 */
const affectsCountBad = computed(() =>
  notice.value ? !v1HorizonAffectsCountIs(notice.value, V1_HORIZON_AFFECTS.length) : false,
)
/** ★★★ 本批修掉的契约错：`source` 是表名，带 `_` 前缀；不是配置来源。 */
const sourceBad = computed(() => (notice.value ? notice.value.source !== V1_HORIZON_READ_SOURCE : false))
const gateKeyBad = computed(() => (notice.value ? notice.value.gate_key !== V1_HORIZON_GATE_KEY : false))

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchV1DataHorizon()
  } catch (err) {
    data.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="v1h">
    <!-- ══ 全局操作 ══ -->
    <section class="v1h__sec">
      <header class="v1h__head">
        <h2 class="v1h__h">{{ t('v1h.title') }}</h2>
        <button type="button" class="v1h__btn" data-testid="v1h-load" :disabled="loading" @click="load">
          {{ loading ? t('common.loading') : t('v1h.load.btn') }}
        </button>
      </header>
      <p class="v1h__msg" data-testid="v1h-scope-note">{{ t('v1h.scope.note') }}</p>
      <p class="v1h__msg" data-testid="v1h-header-note">{{ t('v1h.scope.headerNote') }}</p>

      <p v-if="error" class="v1h__msg v1h__msg--err" data-testid="v1h-error">{{ error }}</p>
      <p v-if="!hasData && !error && !loading" class="v1h__msg" data-testid="v1h-empty">
        {{ t('v1h.scope.empty') }}
      </p>
    </section>

    <!-- ══ 状态卡 ══ -->
    <section v-if="hasData" class="v1h__sec">
      <header class="v1h__head">
        <h2 class="v1h__h">{{ t('v1h.state.title') }}</h2>
        <span class="v1h__badge" data-testid="v1h-state">{{ t(`v1h.state.${state}`) }}</span>
      </header>

      <!-- ★★★ 三态各有**独立**文案；unknown 那套刻意不说「已停更」也不说「仍在写入」。 -->
      <p
        class="v1h__msg"
        :class="{ 'v1h__msg--warn': state !== 'live' }"
        data-testid="v1h-state-note"
      >
        {{ t(`v1h.state.note.${state}`) }}
      </p>

      <template v-if="notice">
        <ul class="v1h__list">
          <li class="v1h__card" data-testid="v1h-detail">
            <div class="v1h__list-row">
              <span>{{ t('v1h.field.source') }}</span>
              <span class="v1h__mono" data-testid="v1h-source">{{ notice.source }}</span>
            </div>
            <div class="v1h__list-row">
              <span>{{ t('v1h.field.gateKey') }}</span>
              <span class="v1h__mono" data-testid="v1h-gate-key">{{ notice.gate_key }}</span>
            </div>
            <div class="v1h__list-row">
              <span>{{ t('v1h.field.affects') }}</span>
              <span class="v1h__mono">{{ notice.affects.join(' / ') }}</span>
            </div>
          </li>
        </ul>

        <!-- ★★★ `effect` / `silence` 是**给人读的文案**，只用于展示，不参与判状态。 -->
        <p class="v1h__msg" data-testid="v1h-effect">{{ notice.effect }}</p>
        <p class="v1h__msg v1h__msg--warn" data-testid="v1h-silence">{{ notice.silence }}</p>
      </template>

      <!-- ══ 自检红线：响应级不变式，**不**嵌在上面的 v-if 里 ══ -->
      <p v-if="flagBad" class="v1h__msg v1h__msg--err" data-testid="v1h-flag-bad">
        {{ t('v1h.check.flagBad') }}
      </p>
      <p v-if="affectsBad" class="v1h__msg v1h__msg--err" data-testid="v1h-affects-bad">
        {{ t('v1h.check.affectsBad') }}
      </p>
      <p v-if="affectsCountBad" class="v1h__msg v1h__msg--err" data-testid="v1h-affects-count-bad">
        {{ t('v1h.check.affectsCountBad') }}
      </p>
      <p v-if="sourceBad" class="v1h__msg v1h__msg--err" data-testid="v1h-source-bad">
        {{ t('v1h.check.sourceBad') }}
      </p>
      <p v-if="gateKeyBad" class="v1h__msg v1h__msg--err" data-testid="v1h-gate-key-bad">
        {{ t('v1h.check.gateKeyBad') }}
      </p>
      <p v-if="state === 'unknown-shape'" class="v1h__msg v1h__msg--err" data-testid="v1h-shape-bad">
        {{ t('v1h.check.shapeBad') }}
      </p>

      <!-- ★ 「该出现」的一面：live 那一档也要说清「为什么没有告示」。 -->
      <p class="v1h__msg" data-testid="v1h-envelope">
        {{ t('v1h.check.envelope', { state: t(`v1h.state.${state}`) }) }}
      </p>
    </section>
  </div>
</template>

<style scoped>
.v1h {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.v1h__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.v1h__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.v1h__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件 ≥48 CSS px。 */
.v1h__btn {
  min-height: 48px;
}

.v1h__btn {
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.v1h__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.v1h__msg--warn {
  color: var(--app-warning);
}

.v1h__msg--err {
  color: var(--app-danger);
}

.v1h__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.v1h__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.v1h__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.v1h__mono {
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.v1h__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>