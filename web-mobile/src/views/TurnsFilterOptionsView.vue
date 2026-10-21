<script setup lang="ts">
// TurnsFilterOptionsView — 轮次列表页的筛选框可选值（2026-10-15，第一百二十七批）。
//
// GET /api/admin/turns/sessions/filter-options
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★★ `requiresRole: 'admin'` —— 而**不是**「admin 档所以不设」。
//
//   注册 `admin/handler.go:1349`：`admin(h.handleTurnsFilterOptions)`
//   而 `admin` 是 `admin/handler.go:962` 的**局部别名** `admin := h.admin`
//   ⇒ `h.admin` = `AdminMiddleware`（`admin/auth.go:86` 起）
//     ⇒ ★★ **全文无任何 `claims.Role` 比对** ⇒ **服务端没有角色下限**
//   ⇒ 「admin 档」在注册行里的真实含义是「**已认证**」，不是「管理员」。
//
//   ★★ **两条独立理由，缺一不可**：
//   （甲）**需要 admin**：载荷是**租户级聚合清单** —— 8 个维度是「这个租户
//        **所有人**用过的项目/任务/负责人/客户端/标签/模型/供应商/状态码」
//        ⇒ 一个只该看**自己**请求的普通用户，没理由看到整个租户的
//          负责人名单与客户端清单。
//   （乙）**不过度限制到 super_admin**：载荷是**租户内**的 —— handler 对每个
//        维度都拼了租户条件（ss 臂 `turns_filter_options.go:125`
//        `tenantWhere = " AND ss.tenant_id = $1"`；turns 臂 `:199`
//        `h.turnMergedSource(tenantID)`，由它内部拼 ` AND tenant_id = $1`）
//        ⇒ 没有「别人的东西」⇒ 按批 119 的边界**不过度限制**。
//
//   ⚠️ 与本模块头原写的「admin 档 ⇒ 抽屉席不设 requiresRole」**正好相反**
//     —— 那正是第一百二十一/一百二十二/一百二十三批连续返工的那句话。
//   ⇒ **服务端这一层是唯一的门**；前端严于后端是**兜底不是修复**。
//
// ══ 本页只读 ══════════════════════════════════════════════════════════════
// ★ 本模块只导出一个 GET。真正的筛选**由** `turnsSessions` 列表端点执行
//   （`/api/admin/turns/sessions`，`handler.go:1347`），本页只负责**取候选值**。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchTurnsFilterOptions,
  turnsFilterOptionsRoleNeedsAdminGate,
  turnsFilterOptionsIsTenantScoped,
  turnsFilterOptionsIsEmpty,
  turnsFilterOptionsHasAnyValue,
  turnsFilterOptionIsTruncated,
  statusCodeFilterValue,
  TURNS_FILTER_OPTION_KEYS,
  TURNS_FILTER_OPTION_LIMIT,
  TURNS_TURN_DIMENSION_WINDOW_DAYS,
  TURNS_CLIENT_VALUE_SOURCES,
  type TurnsFilterOptions,
} from '@/api/turnsFilterOptions'

useHyperPage({ title: () => (allowed.value ? t('tfo.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底（★ 本族它是唯一的门，不是体验兜底）═══════════════════════════

const role = computed(() => auth.role)
const allowed = computed(() => !turnsFilterOptionsRoleNeedsAdminGate(role.value))

// ══ 取数 ══════════════════════════════════════════════════════════════════

const options = ref<TurnsFilterOptions | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  // ★ 双重保险：抽屉席/路由已挡一道，这里再挡是为了**深链直达**
  //   （壳层 `meta.requiresRole` 全仓无人消费，见批 116 待办）。
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    options.value = await fetchTurnsFilterOptions()
  } catch (err) {
    options.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 派生 ══════════════════════════════════════════════════════════════════

const isEmpty = computed(() => options.value !== null && turnsFilterOptionsIsEmpty(options.value))
const hasAny = computed(() => options.value !== null && turnsFilterOptionsHasAnyValue(options.value))

/**
 * ★★ 把 8 个后端键映射成「维度卡片」时**显式列出**，不用 `TURNS_FILTER_OPTION_KEYS`
 *   循环 —— 因为每个维度的**说明文案**不一样，而其中三个维度的口径**不一样**：
 *   ★ 只有 `models` / `providers` / `status_codes` 有 30 天窗口，
 *     其余五个是「**全历史**里最近活跃的」（模块头 (3)）。
 * ⇒ **把五个无窗口的维度说成「近 30 天」就是撒谎** ⇒ 页面上必须分开写。
 */
interface Dim {
  key: (typeof TURNS_FILTER_OPTION_KEYS)[number]
  labelKey: string
  /** ★ 该维度是否真的受 30 天窗口约束。 */
  windowed: boolean
  noteKey: string
}

const DIMS: readonly Dim[] = [
  { key: 'models', labelKey: 'tfo.dim.models', windowed: true, noteKey: 'tfo.note.windowed' },
  { key: 'providers', labelKey: 'tfo.dim.providers', windowed: true, noteKey: 'tfo.note.windowed' },
  { key: 'status_codes', labelKey: 'tfo.dim.statusCodes', windowed: true, noteKey: 'tfo.note.windowed' },
  { key: 'projects', labelKey: 'tfo.dim.projects', windowed: false, noteKey: 'tfo.note.allTime' },
  { key: 'tasks', labelKey: 'tfo.dim.tasks', windowed: false, noteKey: 'tfo.note.allTime' },
  { key: 'owners', labelKey: 'tfo.dim.owners', windowed: false, noteKey: 'tfo.note.allTime' },
  { key: 'clients', labelKey: 'tfo.dim.clients', windowed: false, noteKey: 'tfo.note.clients' },
  { key: 'tags', labelKey: 'tfo.dim.tags', windowed: false, noteKey: 'tfo.note.tags' },
]

function valuesOf(dim: Dim): readonly string[] {
  return options.value ? (options.value[dim.key] as readonly string[]) : []
}

/** ★ (6) `LIMIT 20` ⇒ 正好 20 时**也算**可能截断（`>=` 不是 `>`）。 */
function truncated(dim: Dim): boolean {
  return turnsFilterOptionIsTruncated(valuesOf(dim))
}

/**
 * ★★ (7) `status_codes` 的元素是 **`integer::text`**（`"200"`），**不是数字**。
 *
 * ⇒ 桌面把参数类型写成 `number`（`turns.ts:164`）而把**字符串**绑进下拉
 *   ⇒ **桌面的类型是错的**（只因 `String(params.status_code)` 才没出事）。
 *
 * ⚠️⚠️ ★★★ **初版这里有一个 `statusCodeLabel(v)` 帮助函数，本批把它删了。**
 *   变异 S14 把渲染分支换成 `v-if="false"`（退回原样输出）时**全绿** ——
 *   因为 `statusCodeFilterValue` 只接受 `/^-?\d+$/`，合法值转成 `Number` 再
 *   `String()` 回去**与原串逐字相同** ⇒ ★★ **它是一个恒等函数，也就是一个假门。**
 *   ⇒ 按第一百二十六批的纪律：**恒定返回（恒等）的函数就是假门，删掉而不是补判据。**
 *   ⇒ 真正有牙齿的是两处：**原样显示数字串**（④-1）与 **`badStatusCodes` 标出坏值**（④-2）。
 */

/** ★★ `status_codes` 里无法转成整数的项 —— 页面必须把它们单独标出来而不是悄悄丢掉。 */
const badStatusCodes = computed(() =>
  options.value ? options.value.status_codes.filter((v) => statusCodeFilterValue(v) === null) : [],
)

/** ★ (2) 空维度是 `[]` 不是 `null` —— 「有键但没值」与「没有这个键」是两回事。 */
const emptyDims = computed(() => DIMS.filter((d) => valuesOf(d).length === 0))

/** ★★ 把「不该渲染的那把下拉」明说在页面上，而不是悄悄省略（模块头 (1)）。 */
const noApiKeys = true
const noTenantKeys = computed(() => options.value !== null && turnsFilterOptionsIsTenantScoped(options.value))
const windowDays = TURNS_TURN_DIMENSION_WINDOW_DAYS
const clientSources = TURNS_CLIENT_VALUE_SOURCES.join(' ∪ ')
</script>

<template>
  <div class="tfo">
    <!-- ★★ 深链兜底：本族它是**唯一的门**（服务端没有角色下限）。 -->
    <section v-if="!allowed" class="tfo__sec" data-testid="tfo-nope">
      <header class="tfo__head">
        <h2 class="tfo__h">{{ t('tfo.title') }}</h2>
      </header>
      <p class="tfo__msg tfo__msg--err" data-testid="tfo-nope-msg">{{ t('tfo.nope.msg') }}</p>
      <p class="tfo__msg" data-testid="tfo-nope-why">{{ t('tfo.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="tfo__sec">
        <header class="tfo__head">
          <h2 class="tfo__h">{{ t('tfo.load.title') }}</h2>
          <button type="button" class="tfo__btn" data-testid="tfo-load" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('tfo.load.btn') }}
          </button>
        </header>
        <p class="tfo__msg" data-testid="tfo-readonly-note">{{ t('tfo.readonly.note') }}</p>
        <!-- ★ 说明「这一层是唯一的门」，别让人以为后端也在挡。 -->
        <p class="tfo__msg tfo__msg--warn" data-testid="tfo-gate-note">{{ t('tfo.gate.note') }}</p>
        <p v-if="error" class="tfo__msg tfo__msg--err" data-testid="tfo-error">{{ error }}</p>
      </section>

      <section v-if="options" class="tfo__sec">
        <header class="tfo__head">
          <h2 class="tfo__h">{{ t('tfo.state.title') }}</h2>
          <span v-if="hasAny && !isEmpty" class="tfo__badge" data-testid="tfo-nonempty">{{ t('tfo.badge.nonEmpty') }}</span>
        </header>

        <p v-if="isEmpty" class="tfo__msg" data-testid="tfo-empty">{{ t('tfo.empty') }}</p>
        <p v-if="emptyDims.length > 0" class="tfo__msg" data-testid="tfo-empty-dims">
          {{ t('tfo.emptyDims', { list: emptyDims.map((d) => t(d.labelKey)).join(' / ') }) }}
        </p>
        <!-- ★★ 把「不该有的那把下拉」摆出来（模块头 (1)：桌面那把永远空）。 -->
        <p v-if="noApiKeys" class="tfo__msg tfo__msg--warn" data-testid="tfo-no-apikeys">{{ t('tfo.noApiKeys') }}</p>
        <!-- ★ 把「不过度限制」的依据摆出来：载荷是租户内的。 -->
        <p v-if="noTenantKeys" class="tfo__msg" data-testid="tfo-no-tenant">{{ t('tfo.tenant.scoped') }}</p>
        <!-- ★★ (3) 文件头注释说「近 30 天」，但只有 3 个维度有那个窗口。 -->
        <p class="tfo__msg" data-testid="tfo-window-note">
          {{ t('tfo.windowNote', { days: windowDays }) }}
        </p>
        <!-- ★ (9) clients 混着两种来源，不能假定它只是客户端 ID。 -->
        <p class="tfo__msg" data-testid="tfo-clients-note">{{ t('tfo.clientsNote', { sources: clientSources }) }}</p>

        <section
          v-for="d in DIMS"
          :key="d.key"
          class="tfo__dim"
          :data-testid="'tfo-dim-' + d.key"
        >
          <header class="tfo__dhead">
            <h3 class="tfo__h3">{{ t(d.labelKey) }}</h3>
            <span v-if="truncated(d)" class="tfo__badge tfo__badge--warn" :data-testid="'tfo-cut-' + d.key">
              {{ t('tfo.truncated', { n: TURNS_FILTER_OPTION_LIMIT }) }}
            </span>
          </header>
          <!-- ★★ 说明必须真的带上窗口天数：`tfo.note.windowed` 里有 `{n}` 占位符，
               不传参就会在页面上显示成字面的 `{n}`。 -->
          <p class="tfo__msg" :data-testid="'tfo-note-' + d.key">{{ t(d.noteKey, { n: windowDays }) }}</p>
          <ul v-if="valuesOf(d).length > 0" class="tfo__list" :data-testid="'tfo-list-' + d.key">
            <li
              v-for="(v, i) in valuesOf(d)"
              :key="i"
              class="tfo__item"
              :data-testid="'tfo-item-' + d.key + '-' + i"
            >
              {{ v }}
            </li>
          </ul>
          <p v-else class="tfo__msg" :data-testid="'tfo-none-' + d.key">{{ t('tfo.dim.empty') }}</p>
        </section>

        <p v-if="badStatusCodes.length > 0" class="tfo__msg tfo__msg--err" data-testid="tfo-bad-status">
          {{ t('tfo.badStatus', { list: badStatusCodes.join(' / ') }) }}
        </p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.tfo {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px;
}
.tfo__sec {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  background: var(--app-surface, #fff);
  border-radius: 10px;
}
.tfo__head,
.tfo__dhead {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.tfo__h {
  font-size: 16px;
  font-weight: 600;
  flex: 1;
}
.tfo__h3 {
  font-size: 14px;
  font-weight: 600;
  flex: 1;
}
.tfo__btn {
  min-height: 48px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--app-primary, #1677ff);
  background: var(--app-primary, #1677ff);
  color: #fff;
  font-size: 14px;
}
.tfo__badge {
  padding: 4px 8px;
  border-radius: 10px;
  background: var(--app-primary, #1677ff);
  color: #fff;
  font-size: 12px;
}
.tfo__badge--warn {
  background: #d48806;
}
.tfo__msg {
  font-size: 13px;
  line-height: 1.5;
  color: var(--app-text-secondary, #666);
}
.tfo__msg--warn {
  color: #d48806;
}
.tfo__msg--err {
  color: #cf1322;
}
.tfo__dim {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border-radius: 8px;
  background: rgba(0, 0, 0, 0.03);
}
.tfo__list {
  margin: 0;
  padding-left: 20px;
  font-size: 13px;
}
.tfo__item {
  word-break: break-all;
}
</style>
