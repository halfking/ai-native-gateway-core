<script setup lang="ts">
// RoutingAuditView.vue — P9.2: admin UI for routing override audit log.

import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { fmtDateTime24h } from '../i18n/useFormat'
import {
  getRoutingAudit,
  type RoutingAuditEntry,
} from '../api'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField, CardTone } from '../components/ui/CardList.vue'

const { t } = useI18n()

const entries = ref<RoutingAuditEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const filterAction = ref<'' | 'insert' | 'update' | 'delete'>('')
const filterActor = ref('')
const filterOverrideId = ref<number | null>(null)
const filterDays = ref(7)
const filterLimit = ref(200)

const expandedId = ref<number | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const r = await getRoutingAudit({
      action: filterAction.value,
      actor: filterActor.value || undefined,
      override_id: filterOverrideId.value ?? undefined,
      days: filterDays.value,
      limit: filterLimit.value,
    })
    // 防御：后端若吐 `null`（Go nil 切片）而不是 `[]`，模板的 `entries.length`
    // 会抛 TypeError 整页白屏。数组字段一律 ?? []，不让一个 null 决定整页生死。
    entries.value = Array.isArray(r.entries) ? r.entries : []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function actionClass(a: string): string {
  switch (a) {
    case 'insert': return 'action-insert'
    case 'update': return 'action-update'
    case 'delete': return 'action-delete'
    default: return ''
  }
}

function actionLabel(a: string): string {
  const key = `routingAudit.actions.${a}` as 'routingAudit.actions.insert'
  if (a === 'insert' || a === 'update' || a === 'delete') return t(key)
  return a
}

function shortModel(m?: string): string {
  if (!m) return '—'
  return m.length > 18 ? m.slice(0, 15) + '...' : m
}

const summary = computed(() => {
  const total = entries.value.length
  return {
    total,
    insert: entries.value.filter(e => e.action === 'insert').length,
    update: entries.value.filter(e => e.action === 'update').length,
    delete: entries.value.filter(e => e.action === 'delete').length,
  }
})

/**
 * ── H6 第十条切片（2026-10-06）：路由覆盖审计表接 compact 卡片形态 ────────────
 * 与切片七/八/九同源：`getRoutingAudit` 只有 `limit`（上限 1000）、**没有分页**，
 * 所以只改呈现形态，**不引入连续加载**。
 *
 * ## 三态归属：**第一种形态**（表内三态）—— 判据是「空态时那块东西在不在」
 *
 * 桌面是 `<p v-if="!loading && !entries.length" class="empty">` + `<table v-else>`。
 * 逐档推演（★ 我第一版把它读成了第四种形态，门禁当场把错读抓了出来）：
 *   ① 空态（!loading 且无行） → 出 `.empty`，**表壳被撤掉**
 *   ② 首载中（loading 且无行） → `v-if` 为假 ⇒ 走 `v-else` ⇒ **出空表壳**（表头在、无行）
 *   ③ 刷新中（loading 但有旧行）→ 表照旧，旧数据留在屏上
 * ⇒ 这正是「桌面三态在页面里、容器跟着 v-else 裁」的那一种：
 * 容器挂在 `v-else` 上，**两档共用页面自己的 `.empty` 与空表壳**，
 * **不传 `:empty` / `:loading`** —— 传了就是死代码，且会给桌面凭空加一个空态块。
 * ②这一档 compact 侧就是「0 张卡」的卡片列表：与桌面的空表壳同形，不是加载动画。
 *
 * ## title-key 必须唯一：不要照抄 AuditLogView 的 `title-key="action"`
 *
 * `CardList` 的 `:key` 取自 `titleKey`（`keyOf`）。`action` 只有 insert/update/delete
 * 三个取值 —— 拿它当键，同一列表里**所有行都是重复键**，Vue 会告警且复用整片 DOM。
 * 这里用后端主键 `id` 当 `titleKey`，显示文本交给 `titleFormat`：
 * **键用主键、脸用句柄**，两件事分开。
 *
 * ## 卡头为什么不是时间（`fmtDateTime24h` 的坑，2026-10-06 实测撞上）
 *
 * 我第一版拿时间当卡头，门禁立刻报「6 张卡的卡头全是 `10/5/2026`」。
 * 根因在共享 helper：`new Intl.DateTimeFormat(locale, { hour12: false })`
 * **没有任何日期/时间选项 ⇒ 按规范默认只输出「年/月/日」，根本没有时分**。
 * 函数名写着 `DateTime24h`，行为是 `DateOnly`。
 * 同一审计行 6 条全在同一天 ⇒ 卡头 6 个全同，compact 直接不可用。
 * **该 helper 有 27 个真实调用点、跨 10 个页面**（VibeCoding / UsersView /
 * RequestRegistry / UserDetailDrawer / …）⇒ 改它是**一次独立立项**，
 * 不塞进这条切片。已登记为存量缺陷（见审计台账）。
 * ⇒ 本切片只改自己的卡头选择：**覆盖 ID（`#42`）就是这一行的唯一句柄**，
 * 没有覆盖 ID 的行退回动作译名。同时把「覆盖」字段撤掉 —— 否则卡头与字段重复。
 *
 * `table-min-width="0px"`：本页 `.audit-table` 只有 `width:100%`、**没有** `min-width`
 * —— 传默认值 720px 会给窄内容凭空加一条横向滚动条。
 *
 * ## `#actions` 里放什么，以及为什么复用了一个死键
 *
 * 桌面这两样分处两地：「覆盖」格里的 `router-link`，和最后一列的展开钮。
 * 卡片没有列的概念，于是收进 `#actions`（≤2 个，符合 `CardList` 的动作上限）。
 * 链接标签用 `routingAudit.table.details` —— 该键 8 语言齐全但在页面上**从未被引用**
 * ⇒ 本次切片 **0 新增 i18n 键**。
 * ★ 这段说明原本写在模板的 `<!-- -->` 里，**搬到了这里**：
 *   硬编码中文计数器只跳过 trim 后以 `//` / `*` / `/*` 开头的整行，
 *   模板注释**会被计入**（实测这段 5 行贡献 17 个计数，HEAD 该文件是 0）。
 *   处置归属见审计台账：真硬编码才改实现、注释放错地方才搬位置、
 *   改计数器口径才动脚本（后者需拍板）。**没有把注释改写成英文去压计数。**
 */
function cardTitle(row: Record<string, unknown>): string {
  const id = row.override_id
  if (id != null) return `#${id}`
  return actionLabel(String(row.action ?? ''))
}

/** 动作 → 卡片 tone。与桌面 `.action-insert/.action-update/.action-delete` 配色同源。 */
function actionTone(a: string): CardTone {
  if (a === 'insert') return 'good'
  if (a === 'delete') return 'danger'
  return 'neutral'
}

/** 任务类型 / Profile / 模式：桌面是同一格里的三个标签，卡片合成一个字段。 */
function taskProfileModeText(row: Record<string, unknown>): string | null {
  const parts = [row.task_type, row.profile, row.mode]
    .map((x) => (x == null || String(x).trim() === '' ? '' : String(x).trim()))
    .filter((x) => x !== '')
  return parts.length ? parts.join(' · ') : null
}

/**
 * compact 卡片字段。**标签全部复用 `routingAudit.table.headers.*`** ——
 * 卡片字段与表头本来就是同一批信息，不另立词条（本次切片 0 新增 i18n 键）。
 *
 * 「覆盖」**不在这里**：它已经是卡头（`#42`），再列一遍就是同一行字出现两次。
 * 桌面上那格还是链接；卡片侧由 `#actions` 的「详情」链接承担同一意图。
 *
 * 动作做成 `badge` + 逐行 tone：桌面那层「新增绿 / 删除红」在卡片上不能只剩文字。
 */
const cardFields = computed<CardField[]>(() => [
  {
    key: 'action',
    label: t('routingAudit.table.headers.action'),
    type: 'badge',
    tone: (row) => actionTone(String(row.action ?? '')),
    format: (v) => actionLabel(String(v ?? '')) || null,
  },
  {
    key: 'ts',
    label: t('routingAudit.table.headers.when'),
    format: (v) => (v == null ? null : fmtDateTime24h(String(v))),
  },
  {
    key: 'task_type',
    label: t('routingAudit.table.headers.taskProfileMode'),
    format: (_v, row) => taskProfileModeText(row),
  },
  {
    key: 'model_chosen',
    label: t('routingAudit.table.headers.model'),
    format: (v) => shortModel(v == null ? undefined : String(v)),
  },
  {
    key: 'reason',
    label: t('routingAudit.table.headers.reason'),
    format: (v) => (v == null || v === '' ? null : String(v)),
  },
  {
    key: 'actor',
    label: t('routingAudit.table.headers.actor'),
    format: (v) => (v == null || v === '' ? null : String(v)),
  },
])

onMounted(load)
</script>

<template>
  <div class="audit-view">
    <h1>{{ t('routingAudit.title') }}</h1>
    <p class="subtitle">{{ t('routingAudit.subtitle') }}</p>

    <div v-if="entries.length > 0" class="summary-cards">
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.total') }}</div>
        <div class="summary-value">{{ summary.total }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.inserts') }}</div>
        <div class="summary-value" style="color: var(--success)">{{ summary.insert }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.updates') }}</div>
        <div class="summary-value" style="color: var(--accent)">{{ summary.update }}</div>
      </div>
      <div class="summary-card">
        <div class="summary-label">{{ t('routingAudit.summary.deletes') }}</div>
        <div class="summary-value" style="color: var(--danger)">{{ summary.delete }}</div>
      </div>
    </div>

    <section class="card">
      <div class="filter-bar">
        <label>{{ t('routingAudit.filter.action') }}:
          <select v-model="filterAction" @change="load">
            <option value="">{{ t('routingAudit.filter.all') }}</option>
            <option value="insert">insert</option>
            <option value="update">update</option>
            <option value="delete">delete</option>
          </select>
        </label>
        <label>{{ t('routingAudit.filter.actor') }}:
          <input v-model="filterActor" :placeholder="t('routingAudit.filter.actorPlaceholder')"
                 @keyup.enter="load" />
        </label>
        <label>{{ t('routingAudit.filter.overrideId') }}:
          <input v-model.number="filterOverrideId" type="number" min="1"
                 :placeholder="t('routingAudit.filter.overrideIdPlaceholder')" @keyup.enter="load" />
        </label>
        <label>{{ t('routingAudit.filter.window') }}:
          <select v-model.number="filterDays" @change="load">
            <option :value="1">{{ t('routingAudit.filter.days.d1') }}</option>
            <option :value="7">{{ t('routingAudit.filter.days.d7') }}</option>
            <option :value="30">{{ t('routingAudit.filter.days.d30') }}</option>
            <option :value="90">{{ t('routingAudit.filter.days.d90') }}</option>
          </select>
        </label>
        <label>{{ t('routingAudit.filter.limit') }}:
          <select v-model.number="filterLimit" @change="load">
            <option :value="50">{{ t('routingAudit.filter.limits.l50') }}</option>
            <option :value="200">{{ t('routingAudit.filter.limits.l200') }}</option>
            <option :value="500">{{ t('routingAudit.filter.limits.l500') }}</option>
            <option :value="1000">{{ t('routingAudit.filter.limits.l1000') }}</option>
          </select>
        </label>
        <button @click="load" :disabled="loading">
          {{ loading ? t('routingAudit.filter.loading') : t('routingAudit.filter.refresh') }}
        </button>
      </div>
      <p v-if="error" class="error">⚠️ {{ error }}</p>
    </section>

    <section class="card">
      <h2>{{ t('routingAudit.table.title', { n: entries.length }) }}</h2>
      <p v-if="!loading && entries.length === 0" class="empty">
        {{ t('routingAudit.table.empty') }}
      </p>

      <ResponsiveDataView
        v-else
        :rows="entries"
        title-key="id"
        :title-format="cardTitle"
        :fields="cardFields"
        table-min-width="0px"
      >
        <template #table>
      <table class="audit-table">
        <thead>
          <tr>
            <th>{{ t('routingAudit.table.headers.when') }}</th>
            <th>{{ t('routingAudit.table.headers.action') }}</th>
            <th>{{ t('routingAudit.table.headers.override') }}</th>
            <th>{{ t('routingAudit.table.headers.taskProfileMode') }}</th>
            <th>{{ t('routingAudit.table.headers.model') }}</th>
            <th>{{ t('routingAudit.table.headers.reason') }}</th>
            <th>{{ t('routingAudit.table.headers.actor') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="e in entries" :key="e.id">
            <tr :class="['audit-row', actionClass(e.action)]">
              <td class="mono">{{ fmtDateTime24h(e.ts) }}</td>
              <td>
                <span :class="['action-badge', actionClass(e.action)]">
                  {{ actionLabel(e.action) }}
                </span>
              </td>
              <td class="mono">
                <router-link v-if="e.override_id" :to="`/routing/overrides#${e.override_id}`">
                  #{{ e.override_id }}
                </router-link>
                <span v-else class="text-muted">—</span>
              </td>
              <td>
                <span v-if="e.task_type" class="tag tag-task">{{ e.task_type }}</span>
                <span v-if="e.profile" class="tag tag-profile">{{ e.profile }}</span>
                <span v-if="e.mode" :class="['tag', 'mode-' + e.mode]">{{ e.mode }}</span>
              </td>
              <td><span class="tag tag-model">{{ shortModel(e.model_chosen) }}</span></td>
              <td class="reason">{{ e.reason ?? '—' }}</td>
              <td><span class="actor">{{ e.actor ?? 'system' }}</span></td>
              <td>
                <button v-if="e.expires_at || e.old_expires_at"
                        @click="expandedId = expandedId === e.id ? null : e.id"
                        class="btn-expand">
                  {{ expandedId === e.id ? '−' : '+' }}
                </button>
              </td>
            </tr>
            <tr v-if="expandedId === e.id" class="expand-row">
              <td colspan="8">
                <div class="diff">
                  <div v-if="e.old_expires_at" class="diff-field">
                    <span class="diff-label">{{ t('routingAudit.expand.oldExpires') }}:</span>
                    <code>{{ e.old_expires_at }}</code>
                  </div>
                  <div v-if="e.expires_at" class="diff-field">
                    <span class="diff-label">{{ t('routingAudit.expand.newExpires') }}:</span>
                    <code>{{ e.expires_at }}</code>
                  </div>
                  <div v-if="!e.old_expires_at && !e.expires_at" class="text-muted">
                    {{ t('routingAudit.expand.noDiff') }}
                  </div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
        </template>

        <template #actions="{ row }">
          <router-link
            v-if="row.override_id"
            class="card-link"
            :to="`/routing/overrides#${row.override_id}`"
          >
            {{ t('routingAudit.table.details') }}
          </router-link>
          <button
            v-if="row.expires_at || row.old_expires_at"
            class="btn-expand"
            @click="expandedId = expandedId === row.id ? null : row.id"
          >
            {{ expandedId === row.id ? '−' : '+' }}
          </button>
          <div v-if="expandedId === row.id" class="diff">
            <div v-if="row.old_expires_at" class="diff-field">
              <span class="diff-label">{{ t('routingAudit.expand.oldExpires') }}:</span>
              <code>{{ row.old_expires_at }}</code>
            </div>
            <div v-if="row.expires_at" class="diff-field">
              <span class="diff-label">{{ t('routingAudit.expand.newExpires') }}:</span>
              <code>{{ row.expires_at }}</code>
            </div>
            <div v-if="!row.old_expires_at && !row.expires_at" class="text-muted">
              {{ t('routingAudit.expand.noDiff') }}
            </div>
          </div>
        </template>
      </ResponsiveDataView>
    </section>
  </div>
</template>

<style scoped>
.audit-view {
  padding: 24px;
  color: var(--text);
}
h1 { margin: 0 0 8px; font-size: 24px; }
h2 {
  margin: 0 0 12px;
  font-size: 18px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}
.subtitle {
  margin: 0 0 24px;
  color: var(--muted);
  font-size: 14px;
}
.summary-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.summary-card {
  background: var(--bg);
  border: 1px solid var(--bg);
  border-radius: 6px;
  padding: 12px 16px;
}
.summary-label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.summary-value {
  font-size: 24px;
  font-weight: 600;
  margin-top: 4px;
}
.card {
  background: var(--card-bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 16px;
}
.filter-bar {
  display: flex;
  gap: 16px;
  align-items: center;
  flex-wrap: wrap;
}
.filter-bar label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--muted);
}
.filter-bar input,
.filter-bar select {
  /* width:auto 覆盖全局 input/select width:100%，避免筛选控件占满整行 */
  width: auto;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--bg);
  color: inherit;
  border-radius: 4px;
  font-size: 13px;
  min-width: 120px;
}
.filter-bar button {
  padding: 6px 14px;
  background: var(--accent);
  color: var(--on-primary);
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
}
.filter-bar button:disabled { opacity: 0.5; cursor: not-allowed; }
.error {
  color: var(--danger);
  font-size: 13px;
  margin-top: 8px;
}
.empty {
  color: var(--muted);
  font-size: 13px;
  font-style: italic;
}
.audit-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.audit-table th {
  text-align: left;
  padding: 8px 10px;
  background: var(--bg);
  border-bottom: 1px solid var(--bg);
  color: var(--muted);
  font-weight: 500;
}
.audit-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--bg);
  vertical-align: top;
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.text-muted { color: var(--muted); }
.action-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.action-insert { background: var(--success-strong); color: var(--success); }
.action-update { background: var(--accent-dark); color: var(--accent-h); }
.action-delete { background: var(--danger-dark); color: var(--danger-bd); }
.actor {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
  color: var(--warning);
}
.tag {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 3px;
  display: inline-block;
  margin-right: 4px;
}
.tag-task { background: var(--success-strong); color: var(--success); }
.tag-profile { background: var(--kx-text); color: var(--accent-h); }
.tag-model { background: var(--kx-text); color: var(--accent-h); }
.mode-ban { background: var(--warning-dark); color: var(--warning); }
.mode-pin { background: var(--success-strong); color: var(--success); }
.reason {
  color: var(--border);
  font-size: 12px;
  max-width: 320px;
  word-break: break-word;
}
.btn-expand {
  width: 24px;
  height: 24px;
  border: 1px solid var(--bg);
  background: var(--bg);
  color: var(--muted);
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
}
.btn-expand:hover { background: var(--kx-text); }
/*
 * compact 卡片里「详情」链接的触控目标。桌面 `.btn-expand` 是 24×24 的小方钮
 * （贴着表格行），**直接搬到卡片上不够 48px** —— Android 控件基线要求 ≥48px。
 * 只在卡片这条路径上抬到 48，桌面像素不动。
 */
.card-link {
  display: inline-flex;
  align-items: center;
  min-height: 48px;
  font-size: 13px;
  color: var(--accent-h);
}
.expand-row {
  background: var(--bg-subtle);
}
.expand-row td {
  padding: 12px 16px;
}
.diff {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.diff-field {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.diff-label {
  color: var(--muted);
  min-width: 140px;
}
.diff-field code {
  background: var(--bg);
  padding: 2px 6px;
  border-radius: 3px;
  color: var(--warning);
  font-family: 'SF Mono', Menlo, monospace;
}
.audit-row a {
  color: var(--accent-h);
  text-decoration: none;
}
.audit-row a:hover {
  text-decoration: underline;
}
</style>
