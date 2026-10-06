<script setup lang="ts">
// RoutingAuditView — 路由覆盖规则的变更审计（/routing-audit）。
//
// 数据源：GET /api/admin/routing/overrides/audit
//
// ⚠️★ **superAdmin 档**：handler.go:1381 `RegisterAutoRouteRoutes(mux, h.superAdmin)`，
//   auth.go:353-357 对非 super_admin 直接 403。
//   ⇒ 导航项必须 `requiresRole: 'super_admin'`（见 appNav.ts）。
//   这也是排障线里唯一一条只有超管能看的——符合直觉：审计「谁改了配置」
//   本身就是管理动作，普通租户管理员看不到别人的操作。
//
// ⚠️ 参数越界是 400（days 1..90 / limit 1..1000），不是 clamp ——
//   与 admin/audit_operations.go:95-103 的静默 clamp 语义**相反**，两处不能互抄。
//   这里在发出前夹住，并把 90 天的上限显式告诉用户。
//
// 本端点**无分页**（只有 limit 上限 + days 窗口），所以是一次性拉取，
// 不套 ContinuousListController。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchRoutingAudit,
  describeAuditChange,
  AUDIT_ACTION_TONE,
  ROUTING_AUDIT_ACTIONS,
  ROUTING_AUDIT_MAX_DAYS,
  ROUTING_AUDIT_DEFAULT_DAYS,
  ROUTING_AUDIT_DEFAULT_LIMIT,
  type RoutingAuditAction,
  type RoutingAuditEntry,
} from '@/api/routingAudit'

useHyperPage({ title: () => t('routingAudit.title') })

const action = ref<RoutingAuditAction | ''>('')
const days = ref(ROUTING_AUDIT_DEFAULT_DAYS)
const entries = ref<RoutingAuditEntry[]>([])
const echo = ref<{ action: string; actor: string; override_id: string; days: string } | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const expanded = ref<number | null>(null)

/** 后端硬上限 90 天，超期选项不渲染（选了会吃 400）。 */
const DAY_OPTIONS = [1, 7, 30, 90].filter((d) => d <= ROUTING_AUDIT_MAX_DAYS)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const resp = await fetchRoutingAudit({
      limit: ROUTING_AUDIT_DEFAULT_LIMIT,
      days: days.value,
      ...(action.value ? { action: action.value } : {}),
    })
    // 后端保证 0 行时是 [] 不是 null（routing_overrides.go:406-408），
    // 这里仍兜一层 —— 契约变更是真的会发生的。
    entries.value = Array.isArray(resp.entries) ? resp.entries : []
    echo.value = resp.filter ?? null
  } catch (err) {
    entries.value = []
    echo.value = null
    error.value = (err as Error)?.message ?? null
  } finally {
    loading.value = false
  }
}
void load()

onBeforeUnmount(() => {
  // 无定时器要清；置空防止闭包持有已卸载组件的引用
  entries.value = []
  echo.value = null
})

async function onActionChange(a: RoutingAuditAction | ''): Promise<void> {
  if (action.value === a) return
  action.value = a
  expanded.value = null
  await load()
}
async function onDaysChange(d: number): Promise<void> {
  if (days.value === d) return
  days.value = d
  expanded.value = null
  await load()
}

const count = computed(() => entries.value.length)

function toneOf(e: RoutingAuditEntry): 'success' | 'warning' | 'danger' {
  return AUDIT_ACTION_TONE[e.action] ?? 'warning'
}

const ACTION_KEY: Record<string, string> = {
  insert: 'routingAudit.actionInsert',
  update: 'routingAudit.actionUpdate',
  delete: 'routingAudit.actionDelete',
}

function actionText(e: RoutingAuditEntry): string {
  return t(ACTION_KEY[e.action] ?? 'routingAudit.actionUpdate')
}

function changeText(e: RoutingAuditEntry): string | null {
  return describeAuditChange(e)
}

function toggle(id: number): void {
  expanded.value = expanded.value === id ? null : id
}
</script>

<template>
  <div class="view-root raudit">
    <div class="raudit__row" role="tablist">
      <button
        type="button"
        class="raudit__chip"
        :class="{ 'raudit__chip--on': action === '' }"
        :aria-selected="action === ''"
        @click="onActionChange('')"
      >
        {{ t('routingAudit.actionAll') }}
      </button>
      <button
        v-for="a in ROUTING_AUDIT_ACTIONS"
        :key="a"
        type="button"
        class="raudit__chip"
        :class="{ 'raudit__chip--on': action === a }"
        :aria-selected="action === a"
        @click="onActionChange(a)"
      >
        {{ t(`routingAudit.action${a.charAt(0).toUpperCase()}${a.slice(1)}`) }}
      </button>
    </div>

    <div class="raudit__row" role="tablist">
      <button
        v-for="d in DAY_OPTIONS"
        :key="d"
        type="button"
        class="raudit__chip raudit__chip--sm"
        :class="{ 'raudit__chip--on': days === d }"
        :aria-selected="days === d"
        @click="onDaysChange(d)"
      >
        {{ t('routingAudit.days', { n: d }) }}
      </button>
    </div>

    <p class="raudit__hint">
      <AppIcon name="alert" :size="14" />
      <span>{{ t('routingAudit.daysMaxed') }}</span>
    </p>

    <p v-if="loading" class="raudit__msg">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="raudit__msg raudit__msg--err">{{ error }}</p>
    <p v-else-if="count === 0" class="raudit__msg">{{ t('routingAudit.empty') }}</p>
    <p v-else class="raudit__summary">{{ t('routingAudit.total', { n: count }) }}</p>

    <ul v-if="!loading && !error" class="raudit__list">
      <li v-for="e in entries" :key="e.id" class="raudit__item">
        <button type="button" class="raudit__item-head" @click="toggle(e.id)">
          <div class="card-row">
            <span class="raudit__item-actor">
              <span class="badge" :class="`badge--${toneOf(e)}`">{{ actionText(e) }}</span>
              <span class="raudit__item-name">{{ e.actor || t('routingAudit.noActor') }}</span>
            </span>
            <span class="raudit__item-time">{{ relativeTime(e.ts) }}</span>
          </div>
          <!-- ★ 变更内容缺失时显示明确的「没有内容」，不拼出「从 到 」这类残句 -->
          <p v-if="changeText(e)" class="raudit__item-change">{{ changeText(e) }}</p>
          <p v-else class="raudit__item-change raudit__item-change--empty">
            {{ t('routingAudit.detailMissing') }}
          </p>
          <p v-if="e.reason" class="raudit__item-reason">{{ t('routingAudit.reason') }}: {{ e.reason }}</p>
        </button>

        <div v-if="expanded === e.id" class="raudit__detail">
          <div v-if="e.override_id != null" class="card-field">
            <span>{{ t('routingAudit.overrideId') }}</span>
            <span class="card-field__value num">{{ e.override_id }}</span>
          </div>
          <div v-if="e.expires_at" class="card-field">
            <span>{{ t('routingAudit.expiresAt') }}</span>
            <span class="card-field__value">{{ e.expires_at }}</span>
          </div>
          <div v-if="e.old_expires_at" class="card-field">
            <span>{{ t('routingAudit.oldExpiresAt') }}</span>
            <span class="card-field__value">{{ e.old_expires_at }}</span>
          </div>
        </div>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.raudit {
  padding: var(--app-space-3);
}
.raudit__row {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-bottom: var(--app-space-2);
}
.raudit__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.raudit__chip--sm {
  font-size: 13px;
  padding: 0 14px;
}
.raudit__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.raudit__hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.raudit__msg {
  padding: var(--app-space-4) 0;
  color: var(--app-text-muted);
  font-size: 14px;
  text-align: center;
}
.raudit__msg--err {
  color: var(--app-danger);
}
.raudit__summary {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
}
.raudit__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.raudit__item {
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  margin-bottom: var(--app-space-2);
  overflow: hidden;
}
.raudit__item-head {
  display: block;
  width: 100%;
  padding: var(--app-space-3);
  text-align: left;
  background: none;
  border: none;
}
.raudit__item-actor {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.raudit__item-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.raudit__item-time {
  margin-left: auto;
  font-size: 12px;
  color: var(--app-text-muted);
  white-space: nowrap;
}
.raudit__item-change {
  margin: 6px 0 0;
  font-size: 13px;
  color: var(--app-text-secondary);
  word-break: break-word;
}
.raudit__item-change--empty {
  color: var(--app-text-muted);
  font-style: italic;
}
.raudit__item-reason {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-muted);
}
.raudit__detail {
  padding: 0 var(--app-space-3) var(--app-space-3);
}
</style>
