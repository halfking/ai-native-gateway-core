<script setup lang="ts">
// StorageAndLogConfigView — 存储配置 + 日志轮转配置（2026-10-14，第一百二十四批）。
//
// GET /api/admin/storage/config   （admin/handler.go:1129 h.superAdmin）
// GET /api/admin/logs/config      （admin/handler.go:1135 h.superAdmin）
//
// ══ 权限 ══════════════════════════════════════════════════════════════════
// ★★★★★ `requiresRole: 'super_admin'` —— 而且**服务端是真门**（第二类）。
//
//   `h.superAdmin` → `SuperAdminMiddleware`（`admin/auth.go:376`）逐字：
//     `if claims.Role != "super_admin" { 403 "super_admin role required for this endpoint" }`
//   ⇒ ★★ 越权请求**会被服务端挡下来** ⇒ 视图内这一层兜的是**体验**
//     （不发请求、不显示一屏 403），**不是**安全边界。
//
//   ⚠️ 与批 122 / 123 两族**性质不同**：那两族服务端**根本没有**角色下限
//   （`AdminMiddleware` 只认证），前端那一层是唯一的门；本族不是。
//
//   ★★ 权限必须**逐行**读：`:1129`（superAdmin）与 `:1132`（admin，
//      storage/migration-state）**相邻两行却是两种档位**
//   ⇒ **绝不能按 `/api/admin/storage/` 前缀推权限**。
//
//   ★★ 本模块**只读**：桌面端有 PUT（`web/src/api/tuning.ts:802` / `:934`），
//   而本模块**只导出两个 GET** ⇒ 移动端**不提供任何写入入口**。
import { computed, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchStorageConfig,
  fetchLogConfig,
  logConfigPathMatchesRuntime,
  STORAGE_MASKED_KEYS,
  STORAGE_TYPES,
  CONFIG_SOURCES,
  type StorageConfigResponse,
  type LogConfigResponse,
} from '@/api/storageAndLogConfig'

useHyperPage({ title: () => t('slc.title') })

const auth = useAuthStore()

// ══ 权限兜底（体验层，服务端才是那道门）══════════════════════════════════

const role = computed(() => auth.role)
const allowed = computed(() => role.value === 'super_admin')

// ══ 取数 ══════════════════════════════════════════════════════════════════

const storage = ref<StorageConfigResponse | null>(null)
const logs = ref<LogConfigResponse | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  // ⚠️ ★★ 当前接线下这一句**不可达**：`slc-load` 按钮在 `<template v-else>` 里，
  //   而 `v-else` 的条件就是 `allowed` ⇒ 非超管根本拿不到能点它的按钮。
  // ⇒ 所以「非超管不发请求」真正的保证是**结构**那条（按钮不存在），
  //   而 ⑤-1 钉的也正是它（`expect(hasId(w, 'slc-load')).toBe(false)`）。
  // ★★ 保留这句是**为将来**：若哪天加上 `onMounted(load)` 自动取数，
  //   没有它就会在深链打开时白发一轮请求（服务端必 403）。
  // ⇒ 变异台 S5（摘掉这句）判 NO_EFFECT **不是判据没牙**，是它本来就够不着。
  if (!allowed.value) return
  loading.value = true
  error.value = null
  try {
    storage.value = await fetchStorageConfig()
    logs.value = await fetchLogConfig()
  } catch (err) {
    storage.value = null
    logs.value = null
    error.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ══ 自检红线 ══════════════════════════════════════════════════════════════

/**
 * ★★★★★ `config_source` 只描述**一个键**的来源，不是整张表单的来源。
 *
 * ⚠️ 这里**刻意不做成一个布尔判据**：那会是一个**恒真的假门**——
 *   看起来像一道闸，实际什么都不判断（第一百二十一批删 `settingsRollbackNeedsSuperAdmin()`
 *   正是这个理由）。正确载体是**页面上的静态说明**，它无条件显示。
 *
 * ★★ 初版这里写过 `const showSourceMisuseNote = computed(() => true)`，
 *   那是**同一个毛病的视图侧版本**——恒定返回的 computed 依旧是假门，
 *   只是它躲在我自己写的那条判据视线之外（③-2 只扫 API 模块）。
 *   ⇒ 已删，提醒直接挂在下面的 `sourceMisuseNoteShown` 上。
 */
/** ★★★★ 源值必须落在三值域内（`default` / `env` / `db`）。 */
const sourceUnknown = computed(() =>
  storage.value ? !(CONFIG_SOURCES as readonly string[]).includes(storage.value.config_source) : false,
)
/**
 * ★★★★ `file_path` 是**配置期望值**、`log_file` 是**运行时真值**
 * ⇒ DB 覆盖了但尚未热加载时两者必然不等 ⇒ 提示「等热加载」。
 */
const logPathPendingReload = computed(() =>
  logs.value ? !logConfigPathMatchesRuntime(logs.value) : false,
)
/** ★★★ 掩码键的「存在」不等于「拿到了明文」。 */
const maskedPresent = computed(() =>
  storage.value
    ? STORAGE_MASKED_KEYS.filter((k) => storage.value![k] !== undefined).length
    : 0,
)
/** ★★★★ `storage_type` 取值域**不是** `STORAGE_TYPES`**（可被任意字符串覆盖）。 */
const storageTypeKnown = computed(() =>
  storage.value ? (STORAGE_TYPES as readonly string[]).includes(storage.value.storage_type) : false,
)
const needsRestart = computed(() => Boolean(storage.value?.needs_restart))

// ══ 「该出现」的一面 ══════════════════════════════════════════════════════
const logPathSynced = computed(() => (logs.value ? logConfigPathMatchesRuntime(logs.value) : false))
const STORAGE_TYPE_LIST = [...STORAGE_TYPES]
/** ★ 「config_source 只描述一个键」这条**静态提醒**的可见性：取到数就显示，不参与任何安全判断。 */
const sourceMisuseNoteShown = computed(() => Boolean(storage.value))
</script>

<template>
  <div class="slc">
    <!-- ★★ 深链兜底：服务端是真门，这里只是不让他看到一屏 403。 -->
    <section v-if="!allowed" class="slc__sec" data-testid="slc-nope">
      <header class="slc__head">
        <h2 class="slc__h">{{ t('slc.title') }}</h2>
      </header>
      <p class="slc__msg slc__msg--err" data-testid="slc-nope-msg">{{ t('slc.nope.msg') }}</p>
      <p class="slc__msg" data-testid="slc-nope-why">{{ t('slc.nope.why') }}</p>
    </section>

    <template v-else>
      <section class="slc__sec">
        <header class="slc__head">
          <h2 class="slc__h">{{ t('slc.load.title') }}</h2>
          <button type="button" class="slc__btn" data-testid="slc-load" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('slc.load.btn') }}
          </button>
        </header>
        <p class="slc__msg" data-testid="slc-readonly-note">{{ t('slc.readonly.note') }}</p>

        <p v-if="error" class="slc__msg slc__msg--err" data-testid="slc-error">{{ error }}</p>
      </section>

      <!-- ══ 存储配置 ══ -->
      <section v-if="storage" class="slc__sec">
        <header class="slc__head">
          <h2 class="slc__h">{{ t('slc.storage.title') }}</h2>
          <span class="slc__badge" data-testid="slc-storage-type">{{ storage.storage_type }}</span>
        </header>

        <p v-if="sourceUnknown" class="slc__msg slc__msg--err" data-testid="slc-source-unknown">
          {{ t('slc.check.sourceUnknown') }}
        </p>
        <!-- ★ 无条件显示：这不是「检测到违规」，而是一条**永远成立的使用提醒**。 -->
        <p v-if="sourceMisuseNoteShown" class="slc__msg slc__msg--warn" data-testid="slc-source-misused">
          {{ t('slc.check.sourceMisused') }}
        </p>
        <p v-if="!storageTypeKnown" class="slc__msg slc__msg--err" data-testid="slc-type-unknown">
          {{ t('slc.check.typeUnknown') }}
        </p>
        <p v-if="needsRestart" class="slc__msg slc__msg--err" data-testid="slc-needs-restart">
          {{ t('slc.check.needsRestart') }}
        </p>

        <dl class="slc__dl">
          <div class="slc__kv">
            <dt>{{ t('slc.field.storageType') }}</dt>
            <dd class="slc__mono" data-testid="slc-type">{{ storage.storage_type }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.effectiveDir') }}</dt>
            <dd class="slc__mono" data-testid="slc-effective-dir">{{ storage.effective_dir }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.configSource') }}</dt>
            <dd class="slc__mono" data-testid="slc-config-source">{{ storage.config_source }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.downloadPrefix') }}</dt>
            <dd class="slc__mono" data-testid="slc-download-prefix">
              {{ storage.download_url_prefix }}
            </dd>
          </div>
          <!-- ★★ 掩码键：只报「有多少个存在」，绝不回显值。 -->
          <div v-if="maskedPresent" class="slc__kv">
            <dt>{{ t('slc.field.masked') }}</dt>
            <dd class="slc__mono" data-testid="slc-masked-count">
              {{ t('slc.masked.count', { n: maskedPresent }) }}
            </dd>
          </div>
        </dl>

        <p class="slc__msg" data-testid="slc-types-note">
          {{ t('slc.storage.types', { list: STORAGE_TYPE_LIST.join(' / ') }) }}
        </p>
      </section>

      <!-- ══ 日志轮转 ══ -->
      <section v-if="logs" class="slc__sec">
        <header class="slc__head">
          <h2 class="slc__h">{{ t('slc.logs.title') }}</h2>
          <span v-if="logPathSynced" class="slc__badge" data-testid="slc-logs-synced">
            {{ t('slc.logs.synced') }}
          </span>
        </header>

        <p v-if="logPathPendingReload" class="slc__msg slc__msg--err" data-testid="slc-logs-pending">
          {{ t('slc.check.logPendingReload') }}
        </p>

        <dl class="slc__dl">
          <div class="slc__kv">
            <dt>{{ t('slc.field.filePath') }}</dt>
            <dd class="slc__mono" data-testid="slc-file-path">{{ logs.file_path }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.logFile') }}</dt>
            <dd class="slc__mono" data-testid="slc-log-file">{{ logs.log_file }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.maxSize') }}</dt>
            <dd class="slc__mono" data-testid="slc-max-size">{{ logs.max_size_mb }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.maxBackups') }}</dt>
            <dd class="slc__mono" data-testid="slc-max-backups">{{ logs.max_backups }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.deleteDays') }}</dt>
            <dd class="slc__mono" data-testid="slc-delete-days">{{ logs.delete_days }}</dd>
          </div>
          <div class="slc__kv">
            <dt>{{ t('slc.field.enabledSource') }}</dt>
            <dd class="slc__mono" data-testid="slc-enabled-source">{{ logs.enabled_source }}</dd>
          </div>
        </dl>
      </section>
    </template>
  </div>
</template>

<style scoped>
.slc {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.slc__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.slc__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.slc__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

/* ★ R1：新增触控控件 ≥48 CSS px。 */
.slc__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.slc__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.slc__msg--err {
  color: var(--app-danger);
}

.slc__msg--warn {
  color: var(--app-primary);
}

.slc__dl {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  margin: 0;
}

.slc__kv {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 4px 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
  border-bottom: 1px solid var(--app-border);
}

.slc__kv dt {
  margin: 0;
}

.slc__mono {
  margin: 0;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
  text-align: right;
  word-break: break-all;
}

.slc__badge {
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--app-primary);
  font-variant-numeric: tabular-nums;
}
</style>
