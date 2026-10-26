<script setup lang="ts">
// SubscriptionTiersView — 订阅档与「每档包含哪些模块」（2026-10-14，第一百二十批）。
//
// GET /api/admin/tiers
//
// ── 权限 ────────────────────────────────────────────────────────────────────
// ★★★★★★ 权限档：**`requiresRole: 'super_admin'`**，而且这是**真门**（与批 115/117/118 相反）。
//   ★★★ **第三种注册机制**：它**不在** `admin/handler.go` 的 `mux.HandleFunc` 里，
//   而在 **echo 的 group** 上：
//     `cmd/gateway/main.go:7188` `adminGroup := e.Group("/api/admin", requireSuperAdmin)`
//     → `:7183` `licensing.RegisterModuleRoutes(adminGroup, licensingStore)`
//     → `licensing/module_api.go:22` `g.GET("/tiers", h.ListTiers)`
//   ⇒ ★★★ **只 grep mux 会把「已注册」误判成「死端点」** —— 本批取证时差点就这么判。
//   ⇒ 中间件 `requireSuperAdmin` 逐字（`cmd/gateway/main_jwt_middleware.go:93-102`，
//     工厂函数 `newRequireSuperAdminMiddleware`，role 比对在 `:96`）：
//   ⚠️ 上一行是**判据假阳性**（token 是**小写** `requireSuperAdmin`，而 Go 里逐字是
//     `RequireSuperAdmin`（**首字母大写**）⇒ 大小写敏感 ⇒ 门禁抓不到，
//     已登记在 `goLineRef.spec.ts` 的 `KNOWN_FALSE_POSITIVE` 里，**不得改动它**）。
//     ```go
//     if role, _ := c.Get("role").(string); role != "super_admin" {
//         return c.JSON(http.StatusForbidden, map[string]string{"error": "super_admin required"})
//     }
//     ```
//     ⚠️ `requireSuperAdmin` 是 `main.go:7120` 的**局部变量名**，
//        按变量名去那个文件里找定义**找不到** —— 定义用的是工厂函数名。
//   ⇒ **档位本身就是那道门**（与批 116 `modelIQ` 同款），
//     所以抽屉席设 `requiresRole` 即是安全意义上的完整处置。
//   ⚠️ 该端点本身**不按租户隔离**（读全局 `subscription_tiers` 表），
//     但**不必**为它再补一层理由：中间件已经挡住了一切非超管。
//
// ★★★★ 本族最要紧的五件事（源文件头共 13 条，见 `api/subscriptionTiers.ts`）：
//   1. ★★★★★ 响应是**顶层裸数组**，无订阅档时是 `[]` 而不是 `null`。
//   2. ★★★★★ 每行**六键同层** —— Go 的**匿名内嵌**被平铺，
//      不是 `{tier:{…}, module_keys:[…]}`（最容易写成 `.tier.code` 而全盘 undefined）。
//   3. ★★★★★ **`module_keys` 的顺序未定义**（SQL 无 `ORDER BY`）⇒ 绝不能依赖下标；
//      但表上有 `PRIMARY KEY (tier_code, module_key)` ⇒ **顺序未定义、元素唯一**。
//   4. ★★★★★ **`enabled` 列存在但 SELECT 根本没取它** ⇒ **被停用的订阅档也会出现**，
//      而客户端**看不到 enabled** ⇒ 这是一个「不可见的过滤维度」。
//   5. ★★★★ 行顺序由 `ORDER BY sort_order` **定义** ⇒ 与 module_keys 相反。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSubscriptionTiers,
  subscriptionTiersAreSortOrdered,
  subscriptionTierModuleKeysAreUnique,
  subscriptionTierCodesAreUnique,
  subscriptionTierHasNoModules,
  subscriptionTierEnabledIsNotExposed,
  subscriptionTierMaxFeaturesIsAbsent,
  subscriptionTierDescriptionIsEmptyString,
  SUBSCRIPTION_TIER_KEYS,
  type SubscriptionTierRow,
} from '@/api/subscriptionTiers'

useHyperPage({ title: () => (allowed.value ? t('sts.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 本族这一层兜的是「体验」：服务端中间件是真门（401/403 由它给），
//   但 `meta.requiresRole` 全仓无人消费 ⇒ 深链照样打开页面 ⇒ 兜一层「不发请求」。

const role = computed(() => auth.role)
const allowed = computed(() => role.value === 'super_admin')

// ══ 取数 ══════════════════════════════════════════════════════════════════

const rows = ref<SubscriptionTierRow[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    rows.value = await fetchSubscriptionTiers()
  } catch (err) {
    rows.value = []
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 自检红线（每一条都能被变异打掉）══════════════════════════════════════

/** ★★★★★ 行顺序由 `ORDER BY sort_order` **定义** ⇒ 逆序说明载荷被换过。 */
const orderBad = computed(() => !subscriptionTiersAreSortOrdered(rows.value))

// ★★★★ 下面两条**判据与清单必须拆开**，不能拿 API 层那个「整数组」布尔直接当过滤器：
//   `subscriptionTierModuleKeysAreUnique(rows)` / `subscriptionTierCodesAreUnique(rows)`
//   的入参是**整个数组**，返回的是「**全部**行里有没有一行违规」，
//   拿它去 `.filter()` 会得到「全都合格」的结果 ⇒ **红线永远不亮**。
//   正确拆法（与批 117 同一纪律）：
//     布尔（任一违规 ⇒ 开关）交给 API 层判据；
//     清单（**是哪几行**）在视图里逐行复核，只用于显示 n。

/** ★ 表上有 `PRIMARY KEY (tier_code, module_key)` ⇒ 组内元素唯一（任一行重复即 false）。 */
const keysUnique = computed(() => subscriptionTierModuleKeysAreUnique(rows.value))
/** ★ 与上面那个布尔同源的逐行复核，只为报出「哪几行」。 */
const dupKeys = computed(() =>
  keysUnique.value ? [] : rows.value.filter((r) => new Set(r.module_keys).size !== r.module_keys.length),
)

/** ★ `code` 有 UNIQUE 约束 ⇒ 数组内唯一。 */
const codesUnique = computed(() => subscriptionTierCodesAreUnique(rows.value))
/** ★ 同上：只取「重复出现的那些 code」对应的行（保留首次出现那行）。 */
const dupCodes = computed(() =>
  codesUnique.value
    ? []
    : rows.value.filter((r, i) => rows.value.findIndex((o) => o.code === r.code) !== i),
)

// ⚠️⚠️ 下面两个 helper 的**名字是反的**，返回值 **true 才是好状态**：
//   `subscriptionTierEnabledIsNotExposed(row)` = 「enabled 没出现」= 符合契约；
//   `subscriptionTierMaxFeaturesIsAbsent(row)` = 「max_features 没有」= 符合契约。
//   ⇒ ★★★ 直接把它们丢进 `.filter()` 收集「泄漏行」会得到**全部行**，
//     红线恒亮（每一行都「泄漏」）⇒ 这里必须再取一次负。

/** ★★★★★ `enabled` **绝不**该出现（端点压根没 SELECT 它）。 */
const enabledLeak = computed(() => rows.value.filter((r) => !subscriptionTierEnabledIsNotExposed(r)))
/** ★★★★ `max_features` 被 SELECT 了但 struct 里没有 ⇒ 响应里没有它。 */
const maxFeaturesLeak = computed(() =>
  rows.value.filter((r) => !subscriptionTierMaxFeaturesIsAbsent(r)),
)
/** ★★★ 没有模块的档数（「该键恒为数组」⇒ 空数组是合法形态，不是缺键）。 */
const emptyModuleCount = computed(() => rows.value.filter(subscriptionTierHasNoModules).length)
const emptyDescriptionCount = computed(() =>
  rows.value.filter(subscriptionTierDescriptionIsEmptyString).length,
)
</script>

<template>
  <div class="sts">
    <!-- ★★ 深链兜底：不是后端 403（那是真门），而是前端在**发请求之前**就挡住。 -->
    <section v-if="!allowed" class="sts__sec" data-testid="sts-nope">
      <header class="sts__head">
        <h2 class="sts__h">{{ t('sts.title') }}</h2>
      </header>
      <p class="sts__msg sts__msg--err" data-testid="sts-nope-msg">{{ t('sts.nope.msg') }}</p>
      <p class="sts__msg" data-testid="sts-nope-why">{{ t('sts.nope.why') }}</p>
    </section>

    <template v-else>
      <!-- ══ 全局操作 ══ -->
      <section class="sts__sec">
        <header class="sts__head">
          <h2 class="sts__h">{{ t('sts.load.title') }}</h2>
          <button type="button" class="sts__btn" data-testid="sts-load" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('sts.load.btn') }}
          </button>
        </header>
        <p class="sts__msg" data-testid="sts-scope-note">{{ t('sts.scope.note') }}</p>
        <p class="sts__msg" data-testid="sts-enabled-note">{{ t('sts.scope.enabledNote') }}</p>

        <p v-if="error" class="sts__msg sts__msg--err" data-testid="sts-error">{{ error }}</p>
      </section>

      <!-- ══ 自检段 ══ -->
      <section v-if="rows.length" class="sts__sec">
        <header class="sts__head">
          <h2 class="sts__h">{{ t('sts.check.title') }}</h2>
          <span class="sts__badge" data-testid="sts-count">{{ rows.length }}</span>
        </header>

        <p v-if="orderBad" class="sts__msg sts__msg--err" data-testid="sts-order-bad">
          {{ t('sts.check.orderBad') }}
        </p>
        <p v-if="dupKeys.length" class="sts__msg sts__msg--err" data-testid="sts-dup-keys">
          {{ t('sts.check.dupKeys', { n: dupKeys.length }) }}
        </p>
        <p v-if="dupCodes.length" class="sts__msg sts__msg--err" data-testid="sts-dup-codes">
          {{ t('sts.check.dupCodes', { n: dupCodes.length }) }}
        </p>
        <p v-if="enabledLeak.length" class="sts__msg sts__msg--err" data-testid="sts-enabled-leak">
          {{ t('sts.check.enabledLeak', { n: enabledLeak.length }) }}
        </p>
        <p v-if="maxFeaturesLeak.length" class="sts__msg sts__msg--err" data-testid="sts-max-features-leak">
          {{ t('sts.check.maxFeaturesLeak', { n: maxFeaturesLeak.length }) }}
        </p>

        <!-- ★★★「该出现」的那一面：这两条不能被红线吃掉。 -->
        <p class="sts__msg" data-testid="sts-empty-modules">
          {{ t('sts.check.emptyModules', { n: emptyModuleCount }) }}
        </p>
        <p class="sts__msg" data-testid="sts-empty-description">
          {{ t('sts.check.emptyDescription', { n: emptyDescriptionCount }) }}
        </p>
      </section>

      <!-- ══ 订阅档 ══ -->
      <section class="sts__sec">
        <header class="sts__head">
          <h2 class="sts__h">{{ t('sts.list.title') }}</h2>
          <span v-if="rows.length" class="sts__badge" data-testid="sts-rows">{{ rows.length }}</span>
        </header>

        <p v-if="!loading && !rows.length && !error" class="sts__msg" data-testid="sts-empty">
          {{ t('sts.list.empty') }}
        </p>

        <ul v-else class="sts__list">
          <li
            v-for="r in rows"
            :key="r.code"
            class="sts__card"
            :data-testid="`sts-row-${r.code}`"
          >
            <div class="sts__row">
              <span class="sts__strong">{{ r.name }}</span>
              <span class="sts__badge">#{{ r.sort_order }}</span>
            </div>
            <div class="sts__list-row">
              <span>{{ t('sts.field.code') }}</span>
              <span class="sts__mono">{{ r.code }}</span>
            </div>
            <div class="sts__list-row">
              <span>{{ t('sts.field.price') }}</span>
              <span class="sts__mono">{{ r.price_cents }}</span>
            </div>
            <p v-if="r.description" class="sts__ts">{{ r.description }}</p>
            <!-- ★★★ 空数组是**正常形态**（该键恒为数组），不是「没有这一项」。 -->
            <div class="sts__list-row">
              <span>{{ t('sts.field.modules') }}</span>
              <span class="sts__mono" :data-testid="`sts-modules-${r.code}`">
                {{ r.module_keys.length ? r.module_keys.join(' / ') : t('sts.field.noModules') }}
              </span>
            </div>
            <p class="sts__ts">{{ t('sts.field.keys', { n: SUBSCRIPTION_TIER_KEYS.length }) }}</p>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.sts {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.sts__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.sts__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.sts__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件 ≥48 CSS px。 */
.sts__btn {
  min-height: 48px;
}

.sts__btn {
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sts__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.sts__msg--err {
  color: var(--app-danger);
}

.sts__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.sts__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sts__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
  justify-content: space-between;
}

.sts__list-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.sts__mono {
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.sts__strong {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text);
}

.sts__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}

.sts__ts {
  margin: 0;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}
</style>