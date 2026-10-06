<script setup lang="ts">
// ModelPoliciesView — 租户模型策略（**superAdmin 档**，只读）。
//
// GET  /api/admin/tenants/{code}/model-policies?include_deleted=
// POST /api/admin/tenants/{code}/model-policies/check
//
// ⚠️⚠️⚠️ 档位：后端文件头注释把 check 标成 `(admin)`，**那是过时的**。
//   `admin/handler.go:926-927` 两条注册都是 `h.superAdmin(h.handleTenants)`，
//   而 `handleTenantModelPolicies` 只有这一个调用点
//   ⇒ **整棵子树都是 superAdmin 硬门槛**，`SuperAdminMiddleware` 只认 JWT。
//   ★★★ 也就是说：**租户管理员管不了自己租户的模型策略**，必须 super_admin。
//
// ★★★★★★ check **完全不做租户隔离**：后端函数收了 `tenantCode`，
//   但 SQL 里只有 `WHERE lower(canonical_name)=lower($1)`，**tenantCode 一个字没用**。
//   查的是**全局** `models_canonical`。
//   ⇒ 不能把这个面板描述成「本租户可用的模型」。
//
// ★★★★ `exists:false` 有多种成因：真的没登记 / 名字对但被下线
//   （`COALESCE(status,'active')='active'` 这个条件）/ 大小写写法不同。
//   ⇒ 面板不许把它渲染成「拼错了」。
//
// ★★★★ `vendor` 字段后端**从不赋值** ⇒ 响应里永远没有这个键
//   ⇒ 厂商那一格必须显式说明「后端不提供」，不能留空白。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchTenantModelPolicies,
  checkTenantModelPolicy,
  modelPolicySoftDeleted,
  modelPolicyDeletedBy,
  modelPolicyDeletedCount,
  modelPolicyCheckIsUnknown,
  modelPolicyCheckHasFamily,
  type TenantModelPolicyList,
  type TenantModelPolicyCheck,
} from '@/api/modelPolicies'

useHyperPage({ title: () => t('mpol.title') })

const route = useRoute()

const tenantCode = ref(typeof route.query.tenant === 'string' ? route.query.tenant : '')
const includeDeleted = ref(false)

const list = ref<TenantModelPolicyList | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'notfound' | 'other'>('none')
const loading = ref(false)

const checkName = ref('')
const checkResult = ref<TenantModelPolicyCheck | null>(null)
const checkError = ref<string | null>(null)
const checking = ref(false)

const code = computed(() => tenantCode.value.trim())

/** ★ `count` 是 `len(policies)` 的派生值，恒等于长度 ⇒ **不能**当全库条数显示。 */
const shownCount = computed(() => (list.value ? list.value.policies.length : 0))
const deletedCount = computed(() => (list.value ? modelPolicyDeletedCount(list.value) : 0))

function classifyError(msg: string): 'none' | 'unconfigured' | 'notfound' | 'other' {
  if (/database not configured/i.test(msg)) return 'unconfigured'
  // ★ 只有 list 会用 404 报「租户不存在」；audit/check 静默返空
  if (/tenant not found/i.test(msg)) return 'notfound'
  return 'other'
}

async function load(): Promise<void> {
  if (!code.value) {
    list.value = null
    return
  }
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    list.value = await fetchTenantModelPolicies(code.value, { includeDeleted: includeDeleted.value })
  } catch (e) {
    // ★★ 抛错不许退化成空名单 —— 空名单和「这个租户真没策略」长得一模一样
    list.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = classifyError(msg)
  } finally {
    loading.value = false
  }
}

async function runCheck(): Promise<void> {
  const name = checkName.value.trim()
  if (!name || !code.value) return
  checking.value = true
  checkError.value = null
  checkResult.value = null
  try {
    checkResult.value = await checkTenantModelPolicy(code.value, name)
  } catch (e) {
    checkResult.value = null
    checkError.value = (e as Error)?.message || t('common.error')
  } finally {
    checking.value = false
  }
}

void load()

// ★ include_deleted 是**服务端筛选**，不是本地过滤 ⇒ 必须重新取数。
//   漏了这个 watch，分段控件就是死的：点了不发请求、列表也不变。
watch(includeDeleted, () => {
  void load()
})

onBeforeUnmount(() => {
  list.value = null
  error.value = null
  errorKind.value = 'none'
  checkResult.value = null
  checkError.value = null
})
</script>

<template>
  <div class="mp">
    <section class="mp__panel">
      <span class="mp__panel-title">{{ t('mpol.tenantCode') }}</span>
      <input
        id="mp-code"
        v-model="tenantCode"
        class="mp__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('mpol.tenantCodePlaceholder')"
        @keyup.enter="load"
      />

      <label class="mp__label" for="mp-del">{{ t('mpol.includeDeleted') }}</label>
      <div id="mp-del" class="mp__seg" role="group" :aria-label="t('mpol.includeDeleted')">
        <!-- ★ 两段写死：不用 `t(opt.k)` 那种动态键（i18n 门禁会报成未覆盖前缀） -->
        <button
          type="button"
          class="mp__seg-btn"
          :class="{ 'mp__seg-btn--on': !includeDeleted }"
          :aria-pressed="!includeDeleted"
          @click="includeDeleted = false"
        >
          {{ t('mpol.onlyActive') }}
        </button>
        <button
          type="button"
          class="mp__seg-btn"
          :class="{ 'mp__seg-btn--on': includeDeleted }"
          :aria-pressed="includeDeleted"
          @click="includeDeleted = true"
        >
          {{ t('mpol.withDeleted') }}
        </button>
      </div>

      <!-- ★★ 后端只认字面 "true"：`1` / `TRUE` / `yes` 一律当 false，且不报错 -->
      <p class="mp__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mpol.includeDeletedNote') }}</span>
      </p>
    </section>

    <section v-if="error" class="mp__panel">
      <p class="mp__msg mp__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mp__note mp__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpol.unconfigured') }}</span>
      </p>
      <!-- ★★ list 是三条端点里**唯一**会用 404 报「租户不存在」的 -->
      <p v-else-if="errorKind === 'notfound'" class="mp__note mp__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpol.notFoundNote') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mp__msg">{{ t('common.loading') }}</p>

    <template v-if="list">
      <section class="mp__panel">
        <div class="mp__item-head">
          <span class="mp__panel-title">{{ t('mpol.policies') }}</span>
          <span class="mp__badge">
            <span class="mp__badge-t">{{ t('mpol.shownCount', { n: shownCount }) }}</span>
            <span v-if="deletedCount > 0" class="mp__badge-t mp__badge-t--warn">
              {{ t('mpol.deletedCount', { n: deletedCount }) }}
            </span>
          </span>
        </div>

        <!-- ★★★ count 是 len(out) 的派生值，证明不了扫描没丢行 -->
        <p class="mp__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mpol.countNote') }}</span>
        </p>
        <p class="mp__note mp__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mpol.readOnlyNote') }}</span>
        </p>

        <p v-if="!list.policies.length" class="mp__msg">{{ t('mpol.empty') }}</p>

        <ul v-if="list.policies.length" class="mp__list">
          <li v-for="x in list.policies" :key="x.id" class="mp__item">
            <div class="mp__item-head">
              <span class="mp__title">{{ x.canonical_name }}</span>
              <span class="mp__badge">
                <StatusDot :tone="modelPolicySoftDeleted(x) ? 'danger' : 'success'" />
                <span class="mp__badge-t">
                  {{ modelPolicySoftDeleted(x) ? t('mpol.softDeleted') : t('mpol.active') }}
                </span>
              </span>
            </div>

            <p class="mp__meta">#{{ x.id }} · {{ t('mpol.by') }} {{ x.created_by }}</p>
            <p class="mp__meta">{{ t('mpol.reason') }}: {{ x.reason || '—' }}</p>
            <p class="mp__meta">{{ t('mpol.updated') }}: {{ relativeTime(x.updated_at) }}</p>

            <!-- ★★ 删除人：键不存在 = 无从得知（不是「系统删的」） -->
            <p v-if="modelPolicySoftDeleted(x)" class="mp__note mp__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('mpol.deletedBy', { who: modelPolicyDeletedBy(x) ?? t('mpol.unknownActor') }) }}</span>
            </p>
          </li>
        </ul>
      </section>
    </template>

    <section class="mp__panel">
      <span class="mp__panel-title">{{ t('mpol.checkTitle') }}</span>

      <!-- ★★★★★★ 这一栏查的是**全局**模型名录，与租户无关 -->
      <p class="mp__note mp__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpol.checkGlobalNote') }}</span>
      </p>

      <label class="mp__label" for="mp-check-name">{{ t('mpol.checkName') }}</label>
      <input
        id="mp-check-name"
        v-model="checkName"
        class="mp__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('mpol.checkNamePlaceholder')"
        @keyup.enter="runCheck"
      />

      <button type="button" class="mp__btn" :disabled="checking || !checkName.trim()" @click="runCheck">
        {{ checking ? t('common.loading') : t('mpol.checkRun') }}
      </button>

      <p v-if="checkError" class="mp__msg mp__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ checkError }}</span>
      </p>

      <div v-if="checkResult" class="mp__grid">
        <span class="mp__cell">
          <span class="mp__cell-l">{{ t('mpol.checkExists') }}</span>
          <span class="mp__cell-v">{{ checkResult.exists ? t('mpol.yes') : t('mpol.no') }}</span>
        </span>
        <span class="mp__cell">
          <span class="mp__cell-l">{{ t('mpol.checkModality') }}</span>
          <!-- ★ 恒有值：exists:false 时也是初值 "text"，不是「没有模态」 -->
          <span class="mp__cell-v">{{ checkResult.modality }}</span>
        </span>
        <span class="mp__cell">
          <span class="mp__cell-l">{{ t('mpol.checkFamily') }}</span>
          <!-- ★ 键缺失 ≠ 「无族」：mc.family IS NULL 也是键缺失 -->
          <span class="mp__cell-v">
            {{ modelPolicyCheckHasFamily(checkResult) ? checkResult.family : t('mpol.noFamilyKey') }}
          </span>
        </span>
        <span class="mp__cell">
          <span class="mp__cell-l">{{ t('mpol.checkVendor') }}</span>
          <!-- ★★ 后端永不提供厂商 ⇒ 必须显式说明，不许留空白 -->
          <span class="mp__cell-v">{{ t('mpol.vendorUnavailable') }}</span>
        </span>
      </div>

      <!-- ★★ exists:false 有多种成因，不许断言「拼错了」 -->
      <p v-if="checkResult && modelPolicyCheckIsUnknown(checkResult)" class="mp__note mp__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpol.checkUnknownNote') }}</span>
      </p>
    </section>
  </div>
</template>

<style scoped>
.mp__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mp__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mp__label { display: block; font-size: 12px; color: var(--text-3, #999); margin: 8px 0 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.mp__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }
.mp__seg { display: flex; gap: 6px; margin-bottom: 8px; }
.mp__seg-btn { flex: 1; min-height: 48px; font-size: 13px; border-radius: 8px;
  border: 1px solid var(--border, #ddd); background: transparent; color: inherit; }
.mp__seg-btn--on { border-color: var(--primary, #1976d2); color: var(--primary, #1976d2); font-weight: 600; }
.mp__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--primary, #1976d2); background: transparent; color: var(--primary, #1976d2); }
.mp__btn:disabled { opacity: 0.5; }

.mp__msg { font-size: 13px; color: var(--text-2, #666); padding: 8px 0; }
.mp__msg--err { color: var(--danger, #c0392b); }
.mp__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--text-2, #666); margin: 6px 0; }
.mp__note--warn { color: var(--warn, #b26a00); }
.mp__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 8px; }
.mp__cell { display: flex; flex-direction: column; }
.mp__cell-l { font-size: 11px; color: var(--text-3, #999); }
.mp__cell-v { font-size: 14px; font-weight: 600; }

.mp__list { list-style: none; margin: 0; padding: 0; }
.mp__item { padding: 10px 0; border-top: 1px solid var(--border, #eee); }
.mp__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mp__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.mp__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.mp__badge-t { color: var(--text-2, #666); }
.mp__badge-t--warn { color: var(--warn, #b26a00); }
.mp__meta { font-size: 12px; color: var(--text-2, #666); margin: 4px 0 0; word-break: break-all; }
</style>