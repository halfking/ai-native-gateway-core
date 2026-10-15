<script setup lang="ts">
// StorageMigrationStateView — 附件存储目录迁移进度（2026-10-15，第一百二十五批）。
//
// GET /api/admin/storage/migration-state
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★ `requiresRole: 'admin'` —— **不是** super_admin，也**不是**不设。
//
//   注册 `admin/handler.go:1132`：`admin(h.handleMigrationState)`
//   而 `admin` 是 `admin/handler.go:925` 的**局部别名** `admin := h.admin`
//   ⇒ `h.admin` = `AdminMiddleware`（`admin/auth.go:85` 起）——
//     ★★ 全文**无任何 `claims.Role` 比对** ⇒ **没有角色下限**
//   ⇒ 「admin 档」在注册行里的真实含义是「**已认证**」，不是「管理员」。
//
//   ★★ **两条独立理由，缺一不可**：
//   （甲）**不需要 super_admin**：`admin/storage_migration.go` 整个文件
//        **`grep -c tenant` = 0** ⇒ 载荷是本机单例的迁移进度，
//        不含任何租户的数据 ⇒ 按批 119 的边界**不过度限制**。
//   （乙）**但需要 admin**：`AdminMiddleware` 无角色下限，且
//        `pkg/identity` 的 `supportedRole` **接受 `"user"`**
//        ⇒ 任何已认证的普通用户都能调到这个端点，
//        而「本机附件目录正从 A 搬到 B」**没理由给普通用户看**。
//
//   ⚠️⚠️ ★★★★ **与第一百二十四批结论相反，而且只隔一行**：
//     `:1129`（`storage/config`）是 `h.superAdmin` ⇒ **服务端是真门**；
//     `:1132`（本族）是 `admin(...)`      ⇒ **服务端没有门**。
//   ⇒ ★★★ 前端这一层在批 124 是**体验兜底**，在**本族是唯一的门**。
//     前端严于后端是**兜底不是修复**。
//
// ══ 本页只读 ══════════════════════════════════════════════════════════════
// ★ 本模块只导出一个 GET；**启动/取消迁移都不在本族**（上游是
//   `PUT /api/admin/storage/config`，那是 `h.superAdmin` 档的写端点，
//   而那个模块第一百二十四批已明确「移动端只读」）⇒ 本页**一条写控件都没有**。
import { computed, onUnmounted, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchStorageMigrationState,
  effectiveMigrationRun,
  migrationStateIsEmpty,
  shouldPollMigration,
  migrationRunIsTerminal,
  migrationRunHasErrorDetail,
  migrationFileProgress,
  migrationByteProgress,
  migrationStateRoleNeedsAdminGate,
  migrationStateCarriesNoTenantDimension,
  MIGRATION_POLL_INTERVAL_MS,
  MIGRATION_POLL_MAX,
  type MigrationStateResponse,
} from '@/api/storageMigrationState'

useHyperPage({ title: () => t('sms.title') })

const auth = useAuthStore()

// ══ 权限兜底（★ 本族它是唯一的门，不是体验兜底）═══════════════════════════

const role = computed(() => auth.role)
const allowed = computed(() => !migrationStateRoleNeedsAdminGate(role.value))

// ══ 取数 ══════════════════════════════════════════════════════════════════

const state = ref<MigrationStateResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  // ★ 双重保险：抽屉席/路由已经挡了一道，这里再挡一道是为了
  //   **深链直达**（壳层 `meta.requiresRole` 全仓无人消费，见批 116 待办）。
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    state.value = await fetchStorageMigrationState()
  } catch (err) {
    // ★ 轮询途中的失败**不清空** `state`：否则一次网络抖动就会把
    //   「正在迁移」抹成空态，用户以为迁移凭空消失了。
    //   只有**首次**取数失败才报错。
    if (state.value === null) error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 派生 ══════════════════════════════════════════════════════════════════

const isEmpty = computed(() => state.value !== null && migrationStateIsEmpty(state.value))
/** ★★ 取「当前有效的那个」：`running` 优先，与后端 `getMigration()` 同序。 */
const run = computed(() => (state.value ? effectiveMigrationRun(state.value) : null))
const isRunning = computed(() => run.value !== null && run.value.status === 'running')
const isTerminal = computed(() => (run.value ? migrationRunIsTerminal(run.value) : false))
const failed = computed(() => run.value !== null && run.value.status === 'failed')

/**
 * ★★ 把「不过度限制到 super_admin」的**依据**显示在页面上
 * （沿用批 123 的做法）：本页之所以不是 superAdmin 席，
 * 是因为载荷里**没有任何租户的数据** ⇒ 哪天这句话不成立了，该重判档位。
 */
const noTenantDimension = computed(() =>
  state.value !== null ? migrationStateCarriesNoTenantDimension(state.value) : false,
)

/**
 * ★★★ 进度比**必须处理除零**（模块头 (9)）：
 * 空目录迁移是「成功但零进度」的合法形状（`files_total === 0` 终态 `succeeded`）
 * ⇒ `0/0 = NaN` 喂给进度条会渲染成空白或 `NaN%`。
 */
const filePct = computed(() => (run.value ? Math.round(migrationFileProgress(run.value) * 100) : 0))
const bytePct = computed(() => (run.value ? Math.round(migrationByteProgress(run.value) * 100) : 0))

/** ★★ 失败的结构化细节是否存在；`false` **不代表没失败**，只代表细节只在 `message` 里。 */
const hasErrorDetail = computed(() => (run.value ? migrationRunHasErrorDetail(run.value) : false))

/**
 * ★★★★ 「旧目录是否已清理」的唯一权威答案是 `old_dir_purged`。
 *
 * ⇒ ★★★ **不能**用 `files_deleted === files_total` 代替：
 *   空目录 + 迁移期间有新写入 ⇒ 跳过删除 ⇒
 *   `files_total === 0`、`files_deleted === 0`、`old_dir_purged === false`
 *   ⇒ **`0 === 0` 成立但目录没删。**
 */
const purged = computed(() => run.value?.old_dir_purged === true)

// ══ 轮询 ══════════════════════════════════════════════════════════════════

const pollCount = ref(0)
const pollTimedOut = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

function stopPolling(): void {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
}

function startPolling(): void {
  if (timer !== null) return
  pollCount.value = 0
  pollTimedOut.value = false
  timer = setInterval(() => {
    // ★★ 上限是**协议缺口的补丁**，不是装饰：迁移 goroutine 卡死时
    //   `status` 仍是 `running`、`shouldPollMigration` 永远为真
    //   ⇒ 没有上限就会变成手机**永远转圈 + 永久后台请求**。
    if (pollCount.value >= MIGRATION_POLL_MAX) {
      stopPolling()
      pollTimedOut.value = true
      return
    }
    pollCount.value += 1
    void load()
  }, MIGRATION_POLL_INTERVAL_MS)
}

/**
 * ★ 是否该继续轮询 —— 三种「必须停」的情形（模块头轮询策略）：
 * ① 从未迁移过（两槽位都空）⇒ 否则**永远转圈**；
 * ② 终态（`succeeded` / `failed`）⇒ 重试循环不会把 `latest` 变回 `running`；
 * ③ 轮询次数到上限。
 */
const polling = computed(() =>
  state.value !== null && shouldPollMigration(state.value) && !pollTimedOut.value,
)

async function refresh(): Promise<void> {
  await load()
  if (shouldPollMigration(state.value ?? { running: null, latest: null })) startPolling()
  else stopPolling()
}

onUnmounted(stopPolling)
</script>

<template>
  <div class="sms">
    <!-- ★★ 深链兜底：本族它是**唯一的门**（服务端没有角色下限）。 -->
    <section v-if="!allowed" class="sms__sec" data-testid="sms-nope">
      <header class="sms__head">
        <h2 class="sms__h">{{ t('sms.title') }}</h2>
      </header>
      <p class="sms__msg sms__msg--err" data-testid="sms-nope-msg">{{ t('sms.nope.msg') }}</p>
      <p class="sms__msg" data-testid="sms-nope-why">{{ t('sms.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="sms__sec">
        <header class="sms__head">
          <h2 class="sms__h">{{ t('sms.load.title') }}</h2>
          <button type="button" class="sms__btn" data-testid="sms-load" :disabled="loading" @click="refresh">
            {{ loading ? t('common.loading') : t('sms.load.btn') }}
          </button>
        </header>
        <p class="sms__msg" data-testid="sms-readonly-note">{{ t('sms.readonly.note') }}</p>
        <!-- ★ 说明「这一层是唯一的门」，别让人以为后端也在挡。 -->
        <p class="sms__msg sms__msg--warn" data-testid="sms-gate-note">{{ t('sms.gate.note') }}</p>
        <p v-if="error" class="sms__msg sms__msg--err" data-testid="sms-error">{{ error }}</p>
      </section>

      <section v-if="state" class="sms__sec">
        <header class="sms__head">
          <h2 class="sms__h">{{ t('sms.state.title') }}</h2>
          <span v-if="isRunning" class="sms__badge" data-testid="sms-running">{{ t('sms.status.running') }}</span>
          <span v-if="isTerminal" class="sms__badge" data-testid="sms-terminal">{{ t('sms.status.terminal') }}</span>
          <span v-if="polling" class="sms__badge sms__badge--warn" data-testid="sms-polling">{{ t('sms.polling.on') }}</span>
        </header>

        <!-- ★ 把「不过度限制」的依据摆出来：载荷里没有租户维度。 -->
        <p v-if="noTenantDimension" class="sms__msg" data-testid="sms-no-tenant">{{ t('sms.tenant.none') }}</p>

        <!-- ★ 空态：一个「从未有过迁移」的形状，两槽位都是 null。 -->
        <p v-if="isEmpty" class="sms__msg" data-testid="sms-empty">{{ t('sms.empty') }}</p>

        <!-- ★ 轮询上限被打满：服务端无法表达「卡住」，只能我们自己停。 -->
        <p v-if="pollTimedOut" class="sms__msg sms__msg--err" data-testid="sms-poll-timeout">{{ t('sms.polling.timeout') }}</p>

        <template v-if="run">
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.status') }}</span>
            <span class="sms__v" data-testid="sms-status">{{ run.status }}</span>
          </div>
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.runId') }}</span>
            <span class="sms__v sms__mono" data-testid="sms-run-id">{{ run.run_id }}</span>
          </div>
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.from') }}</span>
            <span class="sms__v sms__mono" data-testid="sms-from">{{ run.from_dir }}</span>
          </div>
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.to') }}</span>
            <span class="sms__v sms__mono" data-testid="sms-to">{{ run.to_dir }}</span>
          </div>
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.startedAt') }}</span>
            <span class="sms__v sms__mono" data-testid="sms-started">{{ run.started_at }}</span>
          </div>

          <!-- ★ 进度条：除零已在上游处理，这里只会拿到 0..100 的整数。 -->
          <div class="sms__prog">
            <div class="sms__prow">
              <span class="sms__k">{{ t('sms.field.fileProgress') }}</span>
              <div class="sms__track"><div class="sms__bar" :style="{ width: filePct + '%' }" /></div>
              <span class="sms__pct" data-testid="sms-file-pct">{{ filePct }}%</span>
              <span class="sms__mono" data-testid="sms-file-counts">{{ run.files_copied }} / {{ run.files_total }}</span>
            </div>
            <div class="sms__prow">
              <span class="sms__k">{{ t('sms.field.byteProgress') }}</span>
              <div class="sms__track"><div class="sms__bar" :style="{ width: bytePct + '%' }" /></div>
              <span class="sms__pct" data-testid="sms-byte-pct">{{ bytePct }}%</span>
              <span class="sms__mono" data-testid="sms-byte-counts">{{ run.bytes_copied }} / {{ run.bytes_total }}</span>
            </div>
          </div>

          <!-- ★ `files_deleted` 不作权威，只展示并说明它不作数。 -->
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.purge') }}</span>
            <span class="sms__v" data-testid="sms-purged">{{ purged ? t('sms.purge.yes') : t('sms.purge.no') }}</span>
          </div>
          <p class="sms__msg" data-testid="sms-purge-note">{{ t('sms.purge.note') }}</p>
          <div class="sms__kv">
            <span class="sms__k">{{ t('sms.field.filesDeleted') }}</span>
            <span class="sms__v sms__mono" data-testid="sms-files-deleted">{{ run.files_deleted }}</span>
          </div>

          <!-- ★ 失败细节：`errors` 可能整个键都不存在。 -->
          <section v-if="failed" class="sms__sub" data-testid="sms-failed">
            <h3 class="sms__h3">{{ t('sms.failed.title') }}</h3>
            <p class="sms__msg" data-testid="sms-failed-why">{{ t('sms.failed.note') }}</p>
            <p v-if="run.message" class="sms__msg sms__mono" data-testid="sms-message">{{ run.message }}</p>
            <ul v-if="hasErrorDetail" class="sms__list" data-testid="sms-errors">
              <li v-for="(e, i) in run.errors" :key="i" class="sms__mono">{{ e }}</li>
            </ul>
          </section>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.sms {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px;
}
.sms__sec {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  background: var(--app-surface, #fff);
  border-radius: 10px;
}
.sms__head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.sms__h {
  font-size: 16px;
  font-weight: 600;
  flex: 1;
}
.sms__h3 {
  font-size: 14px;
  font-weight: 600;
}
.sms__btn {
  min-height: 48px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--app-primary, #1677ff);
  background: var(--app-primary, #1677ff);
  color: #fff;
  font-size: 14px;
}
.sms__badge {
  padding: 4px 8px;
  border-radius: 10px;
  background: var(--app-primary, #1677ff);
  color: #fff;
  font-size: 12px;
}
.sms__badge--warn {
  background: #d48806;
}
.sms__msg {
  font-size: 13px;
  line-height: 1.5;
  color: var(--app-text-secondary, #666);
}
.sms__msg--warn {
  color: #d48806;
}
.sms__msg--err {
  color: #cf1322;
}
.sms__kv {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.sms__k {
  min-width: 96px;
  color: var(--app-text-secondary, #666);
}
.sms__v {
  flex: 1;
  word-break: break-all;
}
.sms__mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.sms__prog {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sms__prow {
  display: flex;
  align-items: center;
  gap: 8px;
}
.sms__track {
  flex: 1;
  height: 8px;
  border-radius: 4px;
  background: rgba(0, 0, 0, 0.08);
  overflow: hidden;
}
.sms__bar {
  height: 100%;
  background: var(--app-primary, #1677ff);
}
.sms__pct {
  min-width: 44px;
  text-align: right;
  font-size: 12px;
}
.sms__sub {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border-radius: 8px;
  background: rgba(207, 19, 34, 0.06);
}
.sms__list {
  margin: 0;
  padding-left: 20px;
  font-size: 12px;
}
</style>
