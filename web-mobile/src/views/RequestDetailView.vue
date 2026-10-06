<script setup lang="ts">
// RequestDetailView — 统一请求详情（admin 档）。
//
// GET /api/admin/request-detail/{request_id}?omit_body=1
//
// 一个 request_id 背后有**五层取数**（memory / file / live_stream /
// request_logs / session_turns），所以本页要回答的第一个问题是
// 「**这份数据是从哪来的、有多新**」，不是先渲染正文。
//
// ⚠️⚠️⚠️ 四个容易渲染错的语义（详见 api/requestDetail.ts 文件头）：
//
// (1) ★★★★★★ **404 身兼两职**：不存在 **与** 跨租户被拒都返
//     404 `request detail not found`（空 tenant_id 也按「来源不明」fail-closed 拒绝）
//     ⇒ 404 **不许**只说「这个请求不存在」。
// (2) ★★★★★ **413 不是 500**（body > 10MB）。
// (3) ★★★★★ **503 = 部署未接线**（request detail store not configured），
//     不是「查不到」。
// (4) ★★★★ `body_status` 是**三态**：`available` / `unavailable` / **键缺失=未知**。
//     ★ 且**不能**用「available ⇒ 一定有内容」来渲染：JSON null 判无载荷、
//       而空容器 `[]`/`{}` 判有载荷。
//
// ★ 本页只读，不碰任何写操作。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import { useAuthStore } from '@/stores/auth'
import {
  fetchRequestDetail,
  isValidRequestId,
  bodyStatusOf,
  bodiesPresent,
  isInFlightSource,
  notFoundIsAlsoTenantDenied,
  REQUEST_DETAIL_BODY_STATUS_UNKNOWN,
  type RequestDetail,
} from '@/api/requestDetail'

useHyperPage({ title: () => t('rd.title') })

const route = useRoute()
const auth = useAuthStore()
/** ★ 决策回放是 superAdmin 档 ⇒ 入口按角色隐藏（口径同 /integrity）。 */
const isSuperAdmin = computed(() => auth.role === 'super_admin')
const idInput = ref(typeof route.params.id === 'string' ? route.params.id : '')
const omitBody = ref(false)
const detail = ref<RequestDetail | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'notfound' | 'unconfigured' | 'toolarge' | 'other'>('none')
const loading = ref(false)

const localIdValid = computed(() => isValidRequestId(idInput.value.trim()))
const bodyStatus = computed(() => bodyStatusOf(detail.value?.meta))
const hasBodies = computed(() => bodiesPresent(detail.value))
const inFlight = computed(() => isInFlightSource(detail.value))

/** ★ 同一块 body 可能是对象/数组/字符串/number（json.RawMessage）⇒ 原样字符串化。 */
function bodyText(v: unknown): string {
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

function load(id: string): void {
  const trimmed = id.trim()
  if (!isValidRequestId(trimmed)) {
    detail.value = null
    error.value = t('rd.badId')
    errorKind.value = 'other'
    return
  }
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  void fetchRequestDetail(trimmed, { omitBody: omitBody.value })
    .then((d) => {
      detail.value = d
    })
    .catch((e) => {
      // ★★ 抛错**不许**清成空态：那几个状态码各有含义。
      detail.value = null
      const msg = (e as Error)?.message || t('common.error')
      error.value = msg
      if (notFoundIsAlsoTenantDenied(e)) errorKind.value = 'notfound'
      else if (/store not configured/i.test(msg)) errorKind.value = 'unconfigured'
      else if (/exceeds 10MB/i.test(msg)) errorKind.value = 'toolarge'
      else errorKind.value = 'other'
    })
    .finally(() => {
      loading.value = false
    })
}

function submit(): void {
  load(idInput.value)
}

function toggleOmit(): void {
  omitBody.value = !omitBody.value
  if (detail.value || error.value) load(idInput.value)
}

// 路由带 id 进来时直接加载
watch(
  () => route.params.id,
  (v) => {
    if (typeof v === 'string' && v) {
      idInput.value = v
      load(v)
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  detail.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="view-root rd">
    <!-- ══════ 查询 ══════ -->
    <section class="rd__panel">
      <span class="rd__panel-title">{{ t('rd.query') }}</span>

      <label class="rd__label" for="rd-id">{{ t('rd.idLabel') }}</label>
      <input
        id="rd-id"
        v-model="idInput"
        class="rd__input"
        type="text"
        inputmode="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('rd.idPlaceholder')"
        @keyup.enter="submit"
      />

      <!-- ★ 本地先按后端同一规则校验，别拿非法 id 去换一个 400 -->
      <p v-if="idInput && !localIdValid" class="rd__note rd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('rd.idInvalid') }}</span>
      </p>
      <p v-else class="rd__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('rd.idHint') }}</span>
      </p>

      <button type="button" class="rd__btn" :disabled="!localIdValid" @click="submit">
        {{ t('rd.lookup') }}
      </button>

      <button type="button" class="rd__btn rd__btn--ghost" @click="toggleOmit">
        {{ omitBody ? t('rd.omitOn') : t('rd.omitOff') }}
      </button>
    </section>

    <!-- ══════ 错误：四种状态码各有含义，不许一律显示「查不到」 ══════ -->
    <section v-if="error" class="rd__panel">
      <p class="rd__msg rd__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <!-- ★★ 404 身兼两职 -->
      <p v-if="errorKind === 'notfound'" class="rd__note rd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('rd.notFoundDual') }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="rd__note rd__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('rd.unconfigured') }}</span>
      </p>
      <p v-if="errorKind === 'toolarge'" class="rd__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('rd.tooLarge') }}</span>
      </p>
    </section>

    <p v-if="loading" class="rd__msg">{{ t('common.loading') }}</p>

    <template v-if="detail">
      <!-- ══════ 来源：先回答「这份数据从哪来」 ══════ -->
      <section class="rd__panel">
        <span class="rd__panel-title">{{ t('rd.provenance') }}</span>

        <div class="rd__row2">
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.source') }}</span>
            <span class="rd__cell-v">{{ detail.source }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.persistence') }}</span>
            <span class="rd__cell-v">{{ detail.persistence }}</span>
          </span>
        </div>

        <!-- ★ 在途源只在当前进程/实时流里，进程一重启就没了 -->
        <p v-if="inFlight" class="rd__note rd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('rd.inFlightNote') }}</span>
        </p>
        <p v-else class="rd__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('rd.persistedNote') }}</span>
        </p>

        <p v-if="detail.warning" class="rd__note rd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ detail.warning }}</span>
        </p>
      </section>

      <!-- ══════ 元信息：全部 omitempty ⇒ 缺键显示「—」 ══════ -->
      <section class="rd__panel">
        <span class="rd__panel-title">{{ t('rd.meta') }}</span>

        <!-- ★ 决策回放入口。整段 auto-route 线是 superAdmin 档
             （handler.go:1381 / :1430 的 h.superAdmin；形参名 adminWrap 是假名），
             而本页是 admin 档 ⇒ 必须**按角色隐藏**，否则 tenant_admin
             点进去只会撞一个 403。缺 request_id 时也不给入口。 -->
        <p v-if="isSuperAdmin" class="rd__note">
          <AppIcon name="search" :size="13" />
          <router-link class="rd__decision-link" :to="`/auto-route-decision/${detail.meta.request_id}`">
            {{ t('rd.decisionReplay') }}
          </router-link>
        </p>

        <div class="rd__grid">
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.requestId') }}</span>
            <span class="rd__cell-v rd__mono">{{ detail.meta.request_id }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.tenantId') }}</span>
            <span class="rd__cell-v">{{ detail.meta.tenant_id || t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.sessionId') }}</span>
            <span class="rd__cell-v rd__mono">{{ detail.meta.gw_session_id || t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.taskId') }}</span>
            <span class="rd__cell-v rd__mono">{{ detail.meta.gw_task_id || t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.clientModel') }}</span>
            <span class="rd__cell-v">{{ detail.meta.client_model || t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.status') }}</span>
            <span class="rd__cell-v">{{ detail.meta.request_status || t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.success') }}</span>
            <span class="rd__cell-v">{{ typeof detail.meta.success === 'boolean' ? (detail.meta.success ? t('rd.yes') : t('rd.no')) : t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.latency') }}</span>
            <span class="rd__cell-v">{{ typeof detail.meta.latency_ms === 'number' ? fmtInt(detail.meta.latency_ms) + ' ms' : t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.turnNumber') }}</span>
            <span class="rd__cell-v">{{ typeof detail.meta.turn_number === 'number' ? fmtInt(detail.meta.turn_number) : t('rd.noValue') }}</span>
          </span>
          <span class="rd__cell">
            <span class="rd__cell-l">{{ t('rd.firstSeen') }}</span>
            <span class="rd__cell-v">{{ detail.meta.request_id ? relativeTime(detail.meta.request_id) : t('rd.noValue') }}</span>
          </span>
        </div>
      </section>

      <!-- ══════ body_status：三态 ══════ -->
      <section class="rd__panel">
        <span class="rd__panel-title">{{ t('rd.bodyStatus') }}</span>

        <div class="rd__status">
          <StatusDot
            :tone="bodyStatus === 'available' ? 'success' : bodyStatus === 'unavailable' ? 'warning' : 'muted'"
          />
          <span class="rd__cell-v">{{ t('rd.bstatus_' + bodyStatus) }}</span>
        </div>

        <p class="rd__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('rd.bodyStatusNote') }}</span>
        </p>
        <!-- ★★ 键缺失是第三态；`dropped` 有意不是契约值 -->
        <p v-if="bodyStatus === REQUEST_DETAIL_BODY_STATUS_UNKNOWN" class="rd__note rd__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('rd.bodyStatusUnknown') }}</span>
        </p>
      </section>

      <!-- ══════ 正文 ══════ -->
      <section class="rd__panel">
        <span class="rd__panel-title">{{ t('rd.bodies') }}</span>

        <p v-if="omitBody" class="rd__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('rd.omittedNote') }}</span>
        </p>

        <p v-if="!omitBody && !hasBodies" class="rd__msg">{{ t('rd.noBodies') }}</p>

        <template v-if="!omitBody && hasBodies && detail.bodies">
          <div v-for="k in (['request_body', 'response_body', 'outbound_body'] as const)" :key="k" class="rd__body">
            <span class="rd__body-l">{{ t('rd.b_' + k) }}</span>
            <pre v-if="detail.bodies[k] !== undefined" class="rd__pre">{{ bodyText(detail.bodies[k]) }}</pre>
            <p v-else class="rd__note">{{ t('rd.b_absent') }}</p>
          </div>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.rd__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.rd__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.rd__label { display: block; font-size: 12px; color: var(--text-3, #999); margin-bottom: 4px; }
.rd__mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; word-break: break-all; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.rd__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }
.rd__btn { min-height: 48px; min-width: 96px; margin-top: 8px; margin-right: 8px; padding: 0 16px;
  border-radius: 8px; border: 1px solid var(--border, #ddd); background: var(--accent, #2f6feb); color: #fff; font-size: 13px; }
.rd__btn--ghost { background: transparent; color: inherit; }
.rd__btn:disabled { opacity: 0.4; }

.rd__msg { font-size: 13px; color: var(--text-2, #666); padding: 8px 0; }
.rd__msg--err { color: var(--danger, #c0392b); }
.rd__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--text-2, #666); margin: 6px 0; }
.rd__note--warn { color: var(--warn, #b26a00); }

.rd__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
.rd__row2 { display: flex; gap: 16px; }
.rd__cell { display: flex; flex-direction: column; min-width: 0; }
.rd__cell-l { font-size: 11px; color: var(--text-3, #999); }
.rd__cell-v { font-size: 14px; font-weight: 600; word-break: break-all; }
.rd__status { display: flex; align-items: center; gap: 6px; }

.rd__body { margin-top: 10px; }
.rd__body-l { font-size: 12px; color: var(--text-3, #999); }
.rd__pre { margin: 4px 0 0; padding: 8px; font-size: 11px; line-height: 1.5; overflow-x: auto;
  background: var(--bg-2, #f6f7f9); border-radius: 8px; }
.rd__decision-link {
  color: var(--app-primary);
  text-decoration: underline;
  min-height: 48px; /* R1：新增触控控件 ≥48 CSS px */
  display: inline-flex;
  align-items: center;
}
</style>
