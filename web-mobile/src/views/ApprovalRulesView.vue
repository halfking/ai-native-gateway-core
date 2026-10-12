<script setup lang="ts">
// ApprovalRulesView — 租户审批人与审批规则（**admin 档**，只读）。
//
// GET /api/admin/tenant-approval-config/{code}/approvers
// GET /api/admin/tenant-approval-config/{code}/approval-rules
//
// ★ 通知渠道**不在本页**：它只存在于 `approval-config` 响应的 `channels` 里，
//   没有独立端点 ⇒ 画在 ApprovalConfigView（那边已经取了 config）。
//   （曾在本页留过一个 `channels` ref，但它从未被赋值 ⇒ 那个面板是死代码。）
//
// ★★★★★★ 头号陷阱：这两个端点的 SQL **都带 `AND enabled = true`**
//   ⇒ **被停用的审批人 / 规则根本不在这两个列表里**。
//   ⇒ 而且它们读的是 `approval_approvers` / `approval_rules` **表**，
//     而 `stats.*_count` 读的是 `approval_configs.config` 那个 **JSONB 列**（含停用的）
//     ⇒ **两个数据源**，条数可以不一致，**这不是数据错**。
//
// ★★★★ 第二个陷阱：空时序列化成 **`null`**，不是 `[]`
//   （`store.go:390` 是 `var approvers []Approver`，nil 切片）
//   ⇒ 页面**必须**把 null 渲染成「0 条」，不能当错误、也不能崩。
//
// ★★ 第三个：邮箱 / 手机是 `COALESCE(…, '')` 再被 `omitempty` 省掉
//   ⇒ **键整个不存在** ⇒ 页面要显示「未填」而不是空白。
//
// ★★ 排序方向相反：approvers `priority ASC`（数小优先）、rules `priority DESC`（数大优先）。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import {
  fetchApprovalApprovers,
  fetchApprovalRules,
  approverHasEmail,
  approverHasPhone,
  ruleHasNoConditions,
  type ApproverList,
  type ApprovalRuleList,
} from '@/api/approvalConfig'

useHyperPage({ title: () => t('acfgRules.title') })

const route = useRoute()

const tenantCode = ref(typeof route.query.tenant === 'string' ? route.query.tenant : '')

const approvers = ref<ApproverList | null>(null)
const rules = ref<ApprovalRuleList | null>(null)

const error = ref<string | null>(null)
const errorKind = ref<'none' | 'notregistered' | 'wrongpath' | 'other'>('none')
const loading = ref(false)

const code = computed(() => tenantCode.value.trim())

/** ★ `null` 与 `[]` 都归一化成数组，模板不判空。 */
const approverRows = computed(() => approvers.value?.approvers ?? [])
const ruleRows = computed(() => rules.value?.rules ?? [])

function classifyError(msg: string): 'none' | 'notregistered' | 'wrongpath' | 'other' {
  if (/404 page not found|page not found/i.test(msg)) return 'notregistered'
  if (/unknown sub-resource/i.test(msg)) return 'wrongpath'
  return 'other'
}

async function load(): Promise<void> {
  if (!code.value) {
    approvers.value = null
    rules.value = null
    return
  }
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    const [a, r] = await Promise.all([
      fetchApprovalApprovers(code.value),
      fetchApprovalRules(code.value),
    ])
    approvers.value = a
    rules.value = r
  } catch (e) {
    // ★★ 抛错不许退化成空清单 —— 空清单和「真的没人」长得一模一样
    approvers.value = null
    rules.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = classifyError(msg)
  } finally {
    loading.value = false
  }
}

void load()

watch(tenantCode, () => {
  void load()
})

onBeforeUnmount(() => {
  approvers.value = null
  rules.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="ar">
    <section class="ar__panel">
      <span class="ar__panel-title">{{ t('acfgRules.tenantCode') }}</span>
      <input
        id="ar-code"
        v-model="tenantCode"
        class="ar__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('acfgRules.tenantCodePlaceholder')"
        @keyup.enter="load"
      />
    </section>

    <section v-if="error" class="ar__panel">
      <p class="ar__msg ar__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'notregistered'" class="ar__note ar__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('acfg.notRegisteredNote') }}</span>
      </p>
      <p v-else-if="errorKind === 'wrongpath'" class="ar__note ar__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('acfg.wrongPathNote') }}</span>
      </p>
    </section>

    <p v-if="loading" class="ar__msg">{{ t('common.loading') }}</p>

    <!-- ★★★★ 后端把空列表序列化成 null，本页把它渲染成 0 条而不是崩溃 -->
    <template v-if="approvers">
      <section class="ar__panel">
        <div class="ar__item-head">
          <span class="ar__panel-title">{{ t('acfgRules.approversTitle') }}</span>
          <span class="ar__badge">
            <span class="ar__badge-t">{{ t('acfgRules.shownCount', { n: approverRows.length }) }}</span>
          </span>
        </div>

        <!-- ★★★★★ 这一列只回 enabled 行，停用的人不在这儿 -->
        <p class="ar__note ar__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('acfgRules.enabledOnlyNote') }}</span>
        </p>
        <p class="ar__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('acfgRules.priorityNote') }}</span>
        </p>

        <p v-if="!approverRows.length" class="ar__msg">{{ t('acfgRules.noApprovers') }}</p>

        <ul v-if="approverRows.length" class="ar__list">
          <li v-for="a in approverRows" :key="a.user_id" class="ar__item">
            <div class="ar__item-head">
              <span class="ar__title">{{ a.name }}</span>
              <span class="ar__badge">
                <span class="ar__badge-t">{{ a.role }}</span>
              </span>
            </div>
            <p class="ar__meta">{{ a.user_id }} · {{ t('acfgRules.priority') }} {{ a.priority }}</p>
            <!-- ★★ 键不存在 = 空串被 omitempty 省掉 ⇒ 显示「未填」不留空白 -->
            <p class="ar__meta">
              {{ t('acfgRules.email') }}:
              {{ approverHasEmail(a) ? a.email : t('acfgRules.notFilled') }}
            </p>
            <p class="ar__meta">
              {{ t('acfgRules.phone') }}:
              {{ approverHasPhone(a) ? a.phone : t('acfgRules.notFilled') }}
            </p>
          </li>
        </ul>
      </section>
    </template>

    <template v-if="rules">
      <section class="ar__panel">
        <div class="ar__item-head">
          <span class="ar__panel-title">{{ t('acfgRules.rulesTitle') }}</span>
          <span class="ar__badge">
            <span class="ar__badge-t">{{ t('acfgRules.shownCount', { n: ruleRows.length }) }}</span>
          </span>
        </div>

        <p class="ar__note ar__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('acfgRules.enabledOnlyNote') }}</span>
        </p>
        <!-- ★★ 规则优先级方向与审批人**相反** -->
        <p class="ar__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('acfgRules.rulePriorityNote') }}</span>
        </p>

        <p v-if="!ruleRows.length" class="ar__msg">{{ t('acfgRules.noRules') }}</p>

        <ul v-if="ruleRows.length" class="ar__list">
          <li v-for="r in ruleRows" :key="r.name" class="ar__item">
            <div class="ar__item-head">
              <span class="ar__title">{{ r.name }}</span>
              <span class="ar__badge">
                <span class="ar__badge-t">{{ t('acfgRules.priority') }} {{ r.priority }}</span>
              </span>
            </div>
            <p class="ar__meta">
              {{ r.action.type }} · {{ r.action.risk_level }}
            </p>
            <p class="ar__meta">{{ r.action.reason || '—' }}</p>

            <!-- ★ conditions 为 null 时是一条条件都没有，不是「条件未知」 -->
            <p v-if="ruleHasNoConditions(r)" class="ar__note ar__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('acfgRules.noConditions') }}</span>
            </p>
            <ul v-else class="ar__conds">
              <li v-for="(c, i) in r.conditions" :key="i" class="ar__cond">
                {{ c.field }} {{ c.operator }} {{ c.value }}
              </li>
            </ul>
          </li>
        </ul>
      </section>
    </template>

    <p class="ar__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('acfg.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.ar__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.ar__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.ar__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.ar__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }

.ar__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.ar__msg--err { color: var(--app-danger); }
.ar__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.ar__note--warn { color: var(--app-warning); }
.ar__list { list-style: none; margin: 0; padding: 0; }
.ar__item { padding: 10px 0; border-top: 1px solid var(--app-border); }
.ar__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.ar__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.ar__badge-t { color: var(--app-text-secondary); }
.ar__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
.ar__conds { list-style: none; margin: 4px 0 0; padding: 0; }
.ar__cond { font-size: 12px; color: var(--app-text-secondary); padding: 2px 0; }
</style>