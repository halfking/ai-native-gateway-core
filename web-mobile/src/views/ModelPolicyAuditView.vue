<script setup lang="ts">
// ModelPolicyAuditView — 租户模型策略审计（**superAdmin 档**，只读）。
//
// GET /api/admin/tenants/{code}/model-policies/audit?limit=
//
// ★★★★★★ 这张表**没有「更早一页」**：后端是 `ORDER BY ts DESC LIMIT $2`，
//     **没有 OFFSET、没有游标** ⇒ 只能调**小** limit 拿最近 N 条。
//     页面刻意不放任何翻页控件。
//
// ★★★★ limit **越界是回落默认值 100，不是 clamp 到 500**
//     （`if n,err := Atoi(s); err==nil && n>0 && n<=500 { limit=n }`，否则保持 100）
//     ⇒ `0`/`-1`/`501`/`abc` 都得 100。**这是本仓第九种限幅语义**，
//     与 MaaS 的 `ClampUsageLimit`（>50⇒50）方向相反。
//
// ★★★★★★ **空列表不等于「没有变更过」**：读路径 `withTenantTx` 只
//     `SET LOCAL app.current_tenant` 就查，**不校验租户存在**
//     ⇒ 租户码拼错 ⇒ **200 + `{audit: [], count: 0}`**。
//     同子树的 list 会 404 `tenant not found`。两者对同一个拼错的码**表现不同**。
//
// ★★★ 动作是数据库触发器写的（`tenant_model_policies_audit_fn`），不是
//     `h.writeAuditLog`。三条由此而来的事实页面必须说破：
//     (a) `actor` 兜底是 `'system'` ⇒ **无署名**，不是「某个叫 system 的账号」；
//     (b) `delete` 那行写的是 `OLD.reason`（删除**前**的理由），
//         而 insert/update/undelete 写 `NEW.reason`
//         ⇒ 同一条策略的「理由」在审计里会前后不一致，**这是设计不是数据错**；
//     (c) UPDATE 只在 `deleted_at`/`reason`/`canonical_name` 变化时才落行
//         ⇒ 「改成同样的值」的 PATCH **不留任何审计痕迹**。
//
// ★ 动作集合是**闭合的**：表上有 `CHECK (action = ANY (['insert','update','delete','undelete']))`。
//   注意第一个是 `insert`，不是 `create`。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchTenantModelPolicyAudit,
  modelPolicyAuditLimitEffective,
  modelPolicyAuditLimitFellBack,
  modelPolicyAuditHasPolicyRef,
  type TenantModelPolicyAudit,
} from '@/api/modelPolicies'

useHyperPage({ title: () => t('mpolAudit.title') })

const route = useRoute()

const tenantCode = ref(typeof route.query.tenant === 'string' ? route.query.tenant : '')
/** ★ 用户选的值；后端可能回落，页面要显示**实际生效**的那个。 */
const limit = ref(100)

const audit = ref<TenantModelPolicyAudit | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)

const code = computed(() => tenantCode.value.trim())
/** ★★ 后端实际会用的 limit（越界回落 100，不是 clamp）。 */
const effectiveLimit = computed(() => modelPolicyAuditLimitEffective(limit.value))
const fellBack = computed(() => modelPolicyAuditLimitFellBack(limit.value))
const shownCount = computed(() => (audit.value ? audit.value.audit.length : 0))
const orphanCount = computed(() =>
  audit.value ? audit.value.audit.filter((a) => !modelPolicyAuditHasPolicyRef(a)).length : 0,
)

async function load(): Promise<void> {
  if (!code.value) {
    audit.value = null
    return
  }
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    audit.value = await fetchTenantModelPolicyAudit(code.value, { limit: limit.value })
  } catch (e) {
    // ★★ 抛错不许退化成空审计 —— 空审计和「租户码打错了」长得一模一样
    audit.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

function actionTone(a: string): 'success' | 'warning' | 'danger' | 'muted' {
  switch (a) {
    case 'insert':
      return 'success'
    case 'update':
      return 'muted'
    case 'delete':
      return 'danger'
    case 'undelete':
      return 'warning'
    default:
      // ★ CHECK 约束保证不会出现别的动作；这里只是防御
      return 'muted'
  }
}

void load()

// ★ limit 是**服务端参数**，不是本地过滤 ⇒ 必须重新取数。
watch(limit, () => {
  void load()
})

onBeforeUnmount(() => {
  audit.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="mpa">
    <section class="mpa__panel">
      <span class="mpa__panel-title">{{ t('mpolAudit.tenantCode') }}</span>
      <input
        id="mpa-code"
        v-model="tenantCode"
        class="mpa__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('mpolAudit.tenantCodePlaceholder')"
        @keyup.enter="load"
      />

      <label class="mpa__label" for="mpa-limit">{{ t('mpolAudit.limit') }}</label>
      <div id="mpa-limit" class="mpa__seg" role="group" :aria-label="t('mpolAudit.limit')">
        <button
          v-for="n in [25, 100, 500]"
          :key="n"
          type="button"
          class="mpa__seg-btn"
          :class="{ 'mpa__seg-btn--on': limit === n }"
          :aria-pressed="limit === n"
          @click="limit = n"
        >
          {{ n }}
        </button>
      </div>

      <!-- ★★ 越界回落：页面必须显示**实际生效**的那个，而不是用户选的那个 -->
      <p v-if="fellBack" class="mpa__note mpa__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpolAudit.limitFellBack', { n: effectiveLimit }) }}</span>
      </p>
      <p v-else class="mpa__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mpolAudit.effectiveLimit', { n: effectiveLimit }) }}</span>
      </p>

      <!-- ★★★★★ 没有「更早一页」：ORDER BY ts DESC LIMIT n，无 OFFSET 无游标 -->
      <p class="mpa__note mpa__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpolAudit.noOlderPageNote') }}</span>
      </p>
    </section>

    <section v-if="error" class="mpa__panel">
      <p class="mpa__msg mpa__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mpa__note mpa__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mpolAudit.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mpa__msg">{{ t('common.loading') }}</p>

    <template v-if="audit">
      <section class="mpa__panel">
        <div class="mpa__item-head">
          <span class="mpa__panel-title">{{ t('mpolAudit.rows') }}</span>
          <span class="mpa__badge">
            <span class="mpa__badge-t">{{ t('mpolAudit.shownCount', { n: shownCount }) }}</span>
            <span v-if="orphanCount > 0" class="mpa__badge-t mpa__badge-t--warn">
              {{ t('mpolAudit.orphanCount', { n: orphanCount }) }}
            </span>
          </span>
        </div>

        <!-- ★★★★ 空审计不能当成「这个租户没有变更过」：租户码拼错也是 200 + 空 -->
        <p v-if="!audit.audit.length" class="mpa__note mpa__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mpolAudit.emptyAmbiguousNote') }}</span>
        </p>

        <p class="mpa__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mpolAudit.triggerNote') }}</span>
        </p>

        <ul v-if="audit.audit.length" class="mpa__list">
          <li v-for="r in audit.audit" :key="r.id" class="mpa__item">
            <div class="mpa__item-head">
              <span class="mpa__title">{{ r.action }}</span>
              <span class="mpa__badge">
                <StatusDot :tone="actionTone(r.action)" />
                <span class="mpa__badge-t">{{ relativeTime(r.ts) }}</span>
              </span>
            </div>

            <p class="mpa__meta">{{ r.canonical_name }}</p>
            <p class="mpa__meta">{{ t('mpolAudit.reason') }}: {{ r.reason || '—' }}</p>
            <p class="mpa__meta">
              {{ t('mpolAudit.policyRef') }}:
              <!-- ★ policy_id 带 omitempty：键不存在 = 这行没挂到具体策略 -->
              {{ modelPolicyAuditHasPolicyRef(r) ? '#' + r.policy_id : t('mpolAudit.noPolicyRef') }}
            </p>
            <!-- ★★ actor === 'system' 是**没设 GUC** 的兜底，不是账号名 -->
            <p class="mpa__meta">
              {{ t('mpolAudit.actor') }}:
              <span v-if="r.actor === 'system'">{{ t('mpolAudit.actorSystem') }}</span>
              <span v-else>{{ r.actor }}</span>
            </p>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.mpa__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mpa__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mpa__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.mpa__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }
.mpa__seg { display: flex; gap: 6px; margin-bottom: 8px; }
.mpa__seg-btn { flex: 1; min-height: 48px; font-size: 13px; border-radius: 8px;
  border: 1px solid var(--border, #ddd); background: transparent; color: inherit; }
.mpa__seg-btn--on { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }

.mpa__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mpa__msg--err { color: var(--app-danger); }
.mpa__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mpa__note--warn { color: var(--app-warning); }
.mpa__list { list-style: none; margin: 0; padding: 0; }
.mpa__item { padding: 10px 0; border-top: 1px solid var(--border, #eee); }
.mpa__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mpa__title { font-size: 14px; font-weight: 600; }
.mpa__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.mpa__badge-t { color: var(--app-text-secondary); }
.mpa__badge-t--warn { color: var(--app-warning); }
.mpa__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
</style>