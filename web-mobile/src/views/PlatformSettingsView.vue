<script setup lang="ts">
// PlatformSettingsView — 平台设置清单 / 单键详情 / 变更历史（2026-10-14，第一百二十一批）。
//
// GET /api/admin/settings
// GET /api/admin/settings/{key}
// GET /api/admin/settings/{key}/history
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★★★ 权限档**第三类（过度限制）⇒ `requiresRole: 'super_admin'`**。
//   ★★★ 而本族原先登记的是**「admin 档，不设 requiresRole」——那条已被订正**，
//     订正链条见 `api/platformSettings.ts` 的 (1)。这里复述结论与其**逐字**依据：
//
//   注册处 `admin/settings.go:24` 是
//     `mux.HandleFunc("/api/admin/settings/", h.admin(h.settingsRouter))`
//   ★★ 它包的是**一个会再分发到四个 handler 的 router**，不是叶子 handler
//     ⇒ ★★★ **`h.admin` 与 URL 前缀都推不出权限**，必须逐个子端点读：
//
//   | 子端点 | 分发 | role 检查 |
//   |---|---|---|
//   | GET {key}        | :104 | ★★ **零** |
//   | PUT {key}        | :106 | ★ `settings.go:236` 仅当 DangerLevel >= Dangerous |
//   | GET {key}/history| :108 | ★★ **零** |
//   | POST {key}/rollback | :110 | ★★★ **零**（连那道 DangerLevel 门都没有）|
//
//   `h.admin` = `AdminMiddleware`（`handler.go:894` → `auth.go:85`）**只认证不检 role**
//   ⇒ 所以「三个 GET 后端零 role 门」是对的。
//   ★★★★★ **错的是推论**：「所以抽屉席不设 requiresRole」。
//
//   ⇒ 为什么**必须**过度限制（两条**独立**的理由，都不是「有没有 tenant 条件」）：
//     ① **载荷里就有别人的身份数据**：history 传 `tid=""`（跨全部租户），
//        而 `operator_user` / `operator_role` 是**恒在键** ⇒ tenant_admin 能读到
//        **别的操作者**的用户名与角色（`client_ip` 另见模块 (8)，可选键）。
//     ② **写路径提权**：rollback 无 DangerLevel 门 ⇒ 能 PUT 不了的档能直接 rollback。
//
//   ⚠️ **前端严于后端是兜底，不是修复。** 真正堵住上面两条的是服务端该补的那道门；
//     本页只保证「不该看的人看不到、也不发请求」。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSettingsList,
  fetchSettingHistory,
  settingsListMayBeIncomplete,
  settingsValueIsNullIffTenantScope,
  settingsValueIsNullForTenantScope,
  settingsSourceIsEmptyForTenantScope,
  settingsAuditCarriesOperatorIdentity,
  settingsHistoryIsCrossTenant,
  settingsHistoryIsNewestFirst,
  settingsHistoryIsWithinLimit,
  settingsWriteNeedsSuperAdmin,
  settingsDangerLevelName,
  SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD,
  type SettingsListResponse,
  type SettingsListItem,
  type SettingsHistoryResponse,
} from '@/api/platformSettings'

useHyperPage({ title: () => t('ps.title') })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
// ★★★ 本族这一层**不是体验层**，比批 116/120 那两族更重：
//   批 116/120 的服务端中间件是**真门**（越权请求被服务端 403 挡住），
//   而这里服务端对三个 GET **零 role 门** —— tenant_admin 只要过 `h.admin` 就读得到
//   跨租户的操作者身份。⇒ **前端这一层此刻是唯一挡在 tenant_admin 与
//   别人身份数据之间的东西**（直到后端补上门）。
//   ⇒ 这也是「深链可绕过壳层」（meta.requiresRole 全仓无人消费）在本族格外危险的原因。

const role = computed(() => auth.role)
const allowed = computed(() => role.value === 'super_admin')

// ══ 取数 ══════════════════════════════════════════════════════════════════

const list = ref<SettingsListResponse | null>(null)
const selectedKey = ref<string | null>(null)
const history = ref<SettingsHistoryResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const historyError = ref<string | null>(null)

const items = computed<SettingsListItem[]>(() => list.value?.items ?? [])

async function load(): Promise<void> {
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    list.value = await fetchSettingsList()
  } catch (err) {
    list.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

async function openHistory(key: string): Promise<void> {
  if (!allowed.value) return
  selectedKey.value = key
  history.value = null
  historyError.value = null
  try {
    history.value = await fetchSettingHistory({ key })
  } catch (err) {
    history.value = null
    historyError.value = (err as Error)?.message || String(err)
  }
}

// ══ 自检红线 ══════════════════════════════════════════════════════════════
// ★ 每一条都能被变异打掉；每一条的**反向**（合规载荷不报）都有阴性对照。

/** ★★★★ `value === null` ⟺ tenant 作用域（模块 (4)）⇒ 违反即载荷形状被换过。 */
const scopeMismatch = computed(() => items.value.filter((i) => !settingsValueIsNullIffTenantScope(i)))
/** ★★ tenant 作用域的 `source` 必是空串（模块 (4)）。 */
const sourceMismatch = computed(() =>
  items.value.filter((i) => settingsValueIsNullForTenantScope(i) && !settingsSourceIsEmptyForTenantScope(i)),
)
/** ★★★ 列表长度可能小于注册表规模（模块 (15)）⇒ 提示，不算错。 */
const maybeIncomplete = computed(() =>
  list.value ? settingsListMayBeIncomplete(items.value.length, items.value.length + 1) : false,
)
/** ★★★★ danger_level ≥ 2 的档在 PUT 上要 super_admin ⇒ UI 必须标出来。 */
const gatedCount = computed(
  () => items.value.filter((i) => settingsWriteNeedsSuperAdmin(i.danger_level)).length,
)
/** ★★★★★ 审计项恒带操作者身份 ⇒ 这一页是「谁改过平台设置」的全局名录。 */
const operatorIdentityShown = computed(() =>
  (history.value?.items ?? []).filter((e) => settingsAuditCarriesOperatorIdentity(e)).length,
)
/** ★★★★★ history **跨租户** ⇒ 这不是「我的租户的变更史」。 */
const historyCrossTenant = computed(() => (history.value ? settingsHistoryIsCrossTenant(history.value) : false))
/** ★★★ 排序是 `ORDER BY created_at DESC LIMIT 50` 无 tiebreak ⇒ 只能断言非升序。 */
const historyOrderBad = computed(() =>
  history.value?.items != null ? !settingsHistoryIsNewestFirst(history.value) : false,
)
/** ★★★ `limit` 写死 50 ⇒ 超出即载荷被换过。 */
const historyLimitBad = computed(() =>
  history.value?.items != null ? !settingsHistoryIsWithinLimit(history.value) : false,
)
</script>

<template>
  <div class="ps">
    <!-- ★★ 深链兜底：不是后端 403（这里后端**根本没有** role 门），
         而是前端在**发请求之前**就挡住。 -->
    <section v-if="!allowed" class="ps__sec" data-testid="ps-nope">
      <header class="ps__head">
        <h2 class="ps__h">{{ t('ps.title') }}</h2>
      </header>
      <p class="ps__msg ps__msg--err" data-testid="ps-nope-msg">{{ t('ps.nope.msg') }}</p>
      <p class="ps__msg" data-testid="ps-nope-why">{{ t('ps.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="ps__sec">
        <header class="ps__head">
          <h2 class="ps__h">{{ t('ps.load.title') }}</h2>
          <button type="button" class="ps__btn" data-testid="ps-load" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('ps.load.btn') }}
          </button>
        </header>
        <p class="ps__msg" data-testid="ps-scope-note">{{ t('ps.scope.note') }}</p>
        <p class="ps__msg" data-testid="ps-scope-warn">{{ t('ps.scope.warn') }}</p>

        <p v-if="error" class="ps__msg ps__msg--err" data-testid="ps-error">{{ error }}</p>
      </section>

      <!-- ══ 清单 ══ -->
      <section v-if="items.length" class="ps__sec">
        <header class="ps__head">
          <h2 class="ps__h">{{ t('ps.list.title') }}</h2>
          <span class="ps__badge" data-testid="ps-count">{{ items.length }}</span>
        </header>

        <p v-if="scopeMismatch.length" class="ps__msg ps__msg--err" data-testid="ps-scope-bad">
          {{ t('ps.check.scopeBad', { n: scopeMismatch.length }) }}
        </p>
        <p v-if="sourceMismatch.length" class="ps__msg ps__msg--err" data-testid="ps-source-bad">
          {{ t('ps.check.sourceBad', { n: sourceMismatch.length }) }}
        </p>
        <p v-if="maybeIncomplete" class="ps__msg" data-testid="ps-maybe-incomplete">
          {{ t('ps.check.maybeIncomplete') }}
        </p>

        <!-- ★★★ 「该出现」的那一面：危险档的计数不能被红线吃掉。 -->
        <p class="ps__msg" data-testid="ps-gated">{{ t('ps.check.gated', { n: gatedCount }) }}</p>

        <ul class="ps__list">
          <li
            v-for="i in items"
            :key="i.key"
            class="ps__card"
            :data-testid="`ps-item-${i.key}`"
          >
            <div class="ps__row">
              <span class="ps__strong">{{ i.key }}</span>
              <span
                class="ps__badge"
                :data-testid="`ps-danger-${i.key}`"
                :data-danger="i.danger_level >= SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD ? 'yes' : 'no'"
              >
                {{ settingsDangerLevelName(i.danger_level) }}
              </span>
            </div>
            <div class="ps__kv">
              <span>{{ t('ps.field.scope') }}</span>
              <span class="ps__mono" :data-testid="`ps-scope-${i.key}`">{{ i.scope }}</span>
            </div>
            <div class="ps__kv">
              <span>{{ t('ps.field.source') }}</span>
              <span class="ps__mono" :data-testid="`ps-src-${i.key}`">{{ i.source || '—' }}</span>
            </div>
            <button
              type="button"
              class="ps__btn ps__btn--sm"
              :data-testid="`ps-history-${i.key}`"
              @click="openHistory(i.key)"
            >
              {{ t('ps.list.historyBtn') }}
            </button>
          </li>
        </ul>
      </section>

      <!-- ══ 变更历史 ══ -->
      <section v-if="selectedKey" class="ps__sec" data-testid="ps-history-sec">
        <header class="ps__head">
          <h2 class="ps__h">{{ t('ps.history.title', { key: selectedKey }) }}</h2>
        </header>

        <p v-if="historyError" class="ps__msg ps__msg--err" data-testid="ps-history-error">
          {{ historyError }}
        </p>

        <template v-else-if="history">
          <p v-if="historyOrderBad" class="ps__msg ps__msg--err" data-testid="ps-history-order-bad">
            {{ t('ps.check.historyOrderBad') }}
          </p>
          <p v-if="historyLimitBad" class="ps__msg ps__msg--err" data-testid="ps-history-limit-bad">
            {{ t('ps.check.historyLimitBad') }}
          </p>
          <p v-if="historyCrossTenant" class="ps__msg ps__msg--err" data-testid="ps-history-cross-tenant">
            {{ t('ps.check.historyCrossTenant') }}
          </p>

          <p class="ps__msg" data-testid="ps-history-operators">
            {{ t('ps.check.operators', { n: operatorIdentityShown }) }}
          </p>

          <p v-if="!history.items" class="ps__msg" data-testid="ps-history-empty">
            {{ t('ps.history.empty') }}
          </p>
          <ul v-else class="ps__list">
            <li
              v-for="(e, idx) in history.items"
              :key="`${e.setting_key}-${e.created_at}-${idx}`"
              class="ps__card"
              data-testid="ps-history-item"
            >
              <div class="ps__row">
                <span class="ps__strong">{{ e.action }}</span>
                <span class="ps__badge">{{ e.created_at }}</span>
              </div>
              <!-- ★★★★★ 这三项就是「载荷里有别人的东西」的具体所指。 -->
              <div class="ps__kv">
                <span>{{ t('ps.field.operator') }}</span>
                <span class="ps__mono" data-testid="ps-history-operator">{{ e.operator_user }}</span>
              </div>
              <div class="ps__kv">
                <span>{{ t('ps.field.role') }}</span>
                <span class="ps__mono" data-testid="ps-history-role">{{ e.operator_role }}</span>
              </div>
            </li>
          </ul>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.ps {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.ps__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.ps__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.ps__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件 ≥48 CSS px（两处按钮共用这一条）。 */
.ps__btn {
  min-height: 48px;
}

.ps__btn {
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.ps__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.ps__msg--err {
  color: var(--app-danger);
}

.ps__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.ps__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.ps__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
  justify-content: space-between;
}

.ps__kv {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.ps__mono {
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.ps__strong {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text);
}

.ps__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>
