<script setup lang="ts">
// DataLifecycleStorageView — 存储总览 + 表级大小（2026-10-14，第一百二十三批）。
//
// GET /api/admin/data-lifecycle/storage
// GET /api/admin/data-lifecycle/storage/tables?limit=
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★ `requiresRole: 'admin'` —— **不是** super_admin，也**不是**不设。
//
//   注册 `admin/handler.go:1034` / `:1035` 两行都是 `admin(...)`（批153 收口 :1009/:1010 后，12h-audit 20261017 轮再漂 25 行），
//   而 `admin` 是 `handler.go:925` 的**局部别名** `admin := h.admin`
//   ⇒ `h.admin` = `AdminMiddleware` —— ★ **没有任何角色下限**
//   ⇒ 「admin 档」在注册行里的真实含义是「**已认证**」，不是「管理员」。
//
//   ★★ **两条独立理由，缺一不可**：
//   （甲）**不需要 super_admin**：载荷是**纯平台基础设施事实**
//        （`data_lifecycle_storage.go` **整个文件零 `tenant` 命中**）——
//        全库字节数、本机磁盘、日志目录。**不含任何租户的数据**
//        ⇒ 按批 119 的边界（过度限制的正当性来自「载荷里有没有别人的东西」）
//        ⇒ 没有理由挡到 super_admin。
//   （乙）**但需要 admin**：`AdminMiddleware` 无角色下限，
//        且 `pkg/identity` 的 `supportedRole` **接受 `"user"`**
//        ⇒ 任何已认证的普通用户都能调这两个端点，
//        而「本机磁盘已用 92%」「日志目录超过 5GB」**没理由给普通用户看**
//        ⇒ 客户端这一层要补的是**角色下限**，不是数据隔离。
//
//   ⚠️ 前端严于后端是**兜底不是修复**。
//   ★★ 同族 `:1013-:1019` 的 vacuum / vacuum-full / reindex（六个）
//      全是 `h.superAdmin` 且全是**写操作**（锁表）⇒ 本页**一条都不碰**。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchStorageOverview,
  fetchStorageTableSizesChecked,
  storageOverviewRoleNeedsAdminGate,
  storageOverviewCarriesNoTenantDimension,
  storageDatabaseQueryFailed,
  storageDatabaseBlockIsAllZero,
  STORAGE_TABLES_LIMIT_FALLBACK,
  type StorageOverview,
  type StorageTableSizes,
} from '@/api/dataLifecycleStorage'

useHyperPage({ title: () => (allowed.value ? t('dls.title') : t('common.pageDenied')) })

const auth = useAuthStore()

// ══ 权限兜底 ══════════════════════════════════════════════════════════════
const role = computed(() => auth.role)
const allowed = computed(() => !storageOverviewRoleNeedsAdminGate(role.value))

// ══ 取数 ══════════════════════════════════════════════════════════════════

const overview = ref<StorageOverview | null>(null)
const tables = ref<StorageTableSizes | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    overview.value = await fetchStorageOverview()
    tables.value = await fetchStorageTableSizesChecked({ limit: STORAGE_TABLES_LIMIT_FALLBACK })
  } catch (err) {
    overview.value = null
    tables.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 自检红线 ══════════════════════════════════════════════════════════════
// ★ 本族最要紧的一条：**失败会留下「全零整块」，而不是缺键**（模块 (1)(2)）。

/**
 * ★★★★★ `database_human === ''` ⇔ 数据库大小查询**失败**（不是「大小为零」）。
 * 成功路径必然来自 `pg_size_pretty`，非空。
 */
const dbFailed = computed(() => {
  if (!overview.value) return false
  // ★★ 两条合起来才是「失败」的完整形状（模块 (1)）：
  //   判据说「database_human 为空」，而 `server_version` 被 omitempty 省掉、
  //   其余八键全 0 —— 两者同时成立才叫「整块塌成零」。
  return storageDatabaseQueryFailed(overview.value) && storageDatabaseBlockIsAllZero(overview.value.database)
})
/**
 * ★★★★ `free_human` **恒为 `""`** —— 是「测不到」，不是「空闲 0 字节」（模块 (2)）。
 * ⇒ ★ 只在**真的为空串**时才提示；它恒为空，所以这一条**常态可见**是契约的正确表达。
 */
const freeUnmeasurable = computed(() => overview.value?.database.free_human === '')
/** ★★★★ columnar 用 `note` 说明原因，与 database 的表达**不同构**（模块 (1)）。 */
const columnarNote = computed(() => overview.value?.columnar.note ?? null)
/** ★★★ `warnings` 是**横向诊断**，不只是错误列表（磁盘 ≥90%、DB 超磁盘 5 倍…）。 */
const warnings = computed(() => overview.value?.warnings ?? [])
/** ★★★★ 载荷里**没有租户维度** —— 这是「不过度限制」的依据本身。 */
const noTenantDimension = computed(() => storageOverviewCarriesNoTenantDimension(overview.value))

// ══ 「该出现」的一面 ══════════════════════════════════════════════════════
const dbOk = computed(() => (overview.value ? !storageDatabaseQueryFailed(overview.value) : false))
const tableRows = computed(() => tables.value?.tables ?? [])
</script>

<template>
  <div class="dls">
    <!-- ★★ 深链兜底：服务端没有角色下限，这一层补的就是它。 -->
    <section v-if="!allowed" class="dls__sec" data-testid="dls-nope">
      <header class="dls__head">
        <h2 class="dls__h">{{ t('dls.title') }}</h2>
      </header>
      <p class="dls__msg dls__msg--err" data-testid="dls-nope-msg">{{ t('dls.nope.msg') }}</p>
      <p class="dls__msg" data-testid="dls-nope-why">{{ t('dls.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="dls__sec">
        <header class="dls__head">
          <h2 class="dls__h">{{ t('dls.load.title') }}</h2>
          <button type="button" class="dls__btn" data-testid="dls-load" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('dls.load.btn') }}
          </button>
        </header>
        <p class="dls__msg" data-testid="dls-scope-note">{{ t('dls.scope.note') }}</p>

        <p v-if="error" class="dls__msg dls__msg--err" data-testid="dls-error">{{ error }}</p>
      </section>

      <template v-if="overview">
        <!-- ══ 警告 ══ -->
        <section v-if="warnings.length" class="dls__sec" data-testid="dls-warn-sec">
          <header class="dls__head">
            <h2 class="dls__h">{{ t('dls.warn.title') }}</h2>
            <span class="dls__badge" data-testid="dls-warn-count">{{ warnings.length }}</span>
          </header>
          <ul class="dls__ul">
            <li v-for="(wmsg, i) in warnings" :key="i" class="dls__li" data-testid="dls-warn">
              {{ wmsg }}
            </li>
          </ul>
        </section>

        <!-- ══ 数据库 ══ -->
        <section class="dls__sec">
          <header class="dls__head">
            <h2 class="dls__h">{{ t('dls.db.title') }}</h2>
            <span v-if="dbOk" class="dls__badge" data-testid="dls-db-ok">{{ t('dls.db.ok') }}</span>
          </header>

          <p v-if="dbFailed" class="dls__msg dls__msg--err" data-testid="dls-db-failed">
            {{ t('dls.check.dbFailed') }}
          </p>
          <p v-if="freeUnmeasurable" class="dls__msg" data-testid="dls-free-note">
            {{ t('dls.check.freeUnmeasurable') }}
          </p>
          <p v-if="noTenantDimension" class="dls__msg" data-testid="dls-no-tenant">
            {{ t('dls.check.noTenant') }}
          </p>

          <dl class="dls__dl">
            <div class="dls__kv">
              <dt>{{ t('dls.field.total') }}</dt>
              <dd class="dls__mono" data-testid="dls-db-total">{{ overview.database.total_human }}</dd>
            </div>
            <div class="dls__kv">
              <dt>{{ t('dls.field.tables') }}</dt>
              <dd class="dls__mono" data-testid="dls-db-tables">{{ overview.database.tables_bytes }}</dd>
            </div>
            <div class="dls__kv">
              <dt>{{ t('dls.field.indexes') }}</dt>
              <dd class="dls__mono" data-testid="dls-db-indexes">{{ overview.database.indexes_bytes }}</dd>
            </div>
            <!-- ★ free_human 恒为空串是**契约**，不是缺陷 ⇒ 渲染占位符。 -->
            <div class="dls__kv">
              <dt>{{ t('dls.field.free') }}</dt>
              <dd class="dls__mono" data-testid="dls-db-free">{{ overview.database.free_human || '—' }}</dd>
            </div>
          </dl>

          <p v-if="columnarNote" class="dls__msg dls__msg--warn" data-testid="dls-columnar-note">
            {{ columnarNote }}
          </p>
        </section>

        <!-- ══ 表级大小 ══ -->
        <section v-if="tableRows.length" class="dls__sec">
          <header class="dls__head">
            <h2 class="dls__h">{{ t('dls.tables.title') }}</h2>
            <span class="dls__badge" data-testid="dls-table-count">{{ tableRows.length }}</span>
          </header>
          <p class="dls__msg" data-testid="dls-tables-limit">
            {{ t('dls.tables.limit', { n: STORAGE_TABLES_LIMIT_FALLBACK }) }}
          </p>
          <ul class="dls__ul">
            <li
              v-for="r in tableRows"
              :key="r.schema + '.' + r.table"
              class="dls__li"
              :data-testid="`dls-table-${r.schema}-${r.table}`"
            >
              <span class="dls__strong">{{ r.schema }}.{{ r.table }}</span>
              <span class="dls__mono">{{ r.total_human }}</span>
              <!-- ★ toast_human：桌面端漏了这个字段，本模块逐字带上。 -->
              <span v-if="r.toast_human" class="dls__tag" data-testid="dls-toast">
                {{ r.toast_human }}
              </span>
            </li>
          </ul>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.dls {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.dls__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.dls__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.dls__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件 ≥48 CSS px。 */
.dls__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.dls__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.dls__msg--err {
  color: var(--app-danger);
}

.dls__msg--warn {
  color: var(--app-primary);
}

.dls__ul {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}

.dls__li {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 6px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
}

.dls__dl {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  margin: 0;
}

.dls__kv {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 4px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
}

.dls__kv dt {
  margin: 0;
}

.dls__mono {
  margin: 0;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.dls__strong {
  color: var(--app-text);
  font-weight: 600;
}

.dls__tag {
  font-size: 0.625rem;
  color: var(--app-text-muted);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  padding: 0 4px;
}

.dls__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>
