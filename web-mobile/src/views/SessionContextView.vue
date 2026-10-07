<script setup lang="ts">
// SessionContextView — 会话上下文读面（**admin 档**，两条只读端点）。
//
//   GET  /api/system/session-context/{taskId}/extraction-status
//   POST /api/system/session-context/titles/batch        ← ★ 只读语义但**只能 POST**
//
// ⚠️ 前缀是 `/api/system/…`，不是 `/api/admin/…`（`admin/handler.go:1296`）
// 档位 `h.admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//
// ★★★★★★★★ `extraction-status` 是**异形端点**：A 形只有 2 个键、B 形有 8 个键。
// ★★★★★★★★ `extracted:false` 有**三种**成因且**完全分不开**：
//   不属于你的租户 / 没抽过 / **数据库查询出错** —— 三者都是 200 + 同样的两键。
//   ⇒ 页面只能说「未能确定」，不许说「没抽取过」。
// ★★★★★★★ 批量标题的 map 键里**含一个字面 NUL**（`taskId + "\0" + scoped.trim()`）
//   ⇒ 只能靠 splitSessionTitleMapKey 拆开显示，不能按 taskId 直查。
// ★★★★ 批量结果的「空」有**四种**成因（空请求 / 全被跳过 / DB 出错 / 真没存过），
//   全部是 `{titles:{}}`，**分不开**。
//
// ★★ 不碰的四个：`extract-to-memora`（抽取写入 + 调 memora）、
//   `summarize-title`（调模型生成标题）、`PUT title`（人工改标题）、`DELETE title`。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchExtractionStatus,
  fetchTitlesBatch,
  normalizeTitlesBatchKeys,
  titlesBatchExceedsLimit,
  TITLES_BATCH_MAX_KEYS,
  titlesBatchPartiallyAnswered,
  titlesBatchMissingKeys,
  extractionStatusIsIndeterminate,
  extractionStatusLacksDetailFields,
  extractionDetailIsNull,
  sessionContextSettingsMissing,
  taskIdIsShadowedByBatch,
  splitSessionTitleMapKey,
  type ExtractionStatus,
  type TitlesBatchResult,
} from '@/api/sessionContext'

useHyperPage({ title: () => t('sctx.title') })

const taskId = ref('')
const status = ref<ExtractionStatus | null>(null)
const statusError = ref<string | null>(null)
const statusLoading = ref(false)

const rawIds = ref('')
const titles = ref<TitlesBatchResult | null>(null)
const titlesError = ref<string | null>(null)
const titlesLoading = ref(false)
/** ★★ 请求侧的键（已去空/去重/trim），用于算「哪些没答」。 */
const askedKeys = ref<Array<{ task_id: string; scoped_session_id?: string }>>([])

/** ★ 用户可以一行一个，也可以逗号/空格分隔。 */
const requestedKeys = computed(() =>
  normalizeTitlesBatchKeys(
    rawIds.value
      .split(/[\n,;\s]+/)
      .filter(Boolean)
      .map((s) => ({ task_id: s })),
  ),
)
const overLimit = computed(() => titlesBatchExceedsLimit(requestedKeys.value.length))
/** ★ task_id 恰好叫 `titles` 会被后端特判截胡。 */
const shadowedId = computed(() => taskIdIsShadowedByBatch(taskId.value.trim()))

const missingKeys = computed(() =>
  titles.value ? titlesBatchMissingKeys(titles.value, askedKeys.value) : [],
)
/** ★★ 键拆开后按 taskId 分组显示（键含 NUL，不能直接显示）。 */
const titleRows = computed(() =>
  Object.entries(titles.value?.titles ?? {}).map(([k, v]) => {
    const parts = splitSessionTitleMapKey(k)
    return { key: k, taskId: parts.taskId, scopedSessionId: parts.scopedSessionId, title: v }
  }),
)

async function loadStatus(): Promise<void> {
  statusLoading.value = true
  statusError.value = null
  status.value = null
  try {
    status.value = await fetchExtractionStatus(taskId.value.trim())
  } catch (e) {
    // ★★ 抛错不许退化成「未抽取」—— 那和真的没抽过长得一样
    status.value = null
    statusError.value = (e as Error)?.message || t('common.error')
  } finally {
    statusLoading.value = false
  }
}

async function loadTitles(): Promise<void> {
  titlesLoading.value = true
  titlesError.value = null
  titles.value = null
  askedKeys.value = []
  try {
    askedKeys.value = requestedKeys.value
    titles.value = await fetchTitlesBatch(askedKeys.value)
  } catch (e) {
    titles.value = null
    titlesError.value = (e as Error)?.message || t('common.error')
  } finally {
    titlesLoading.value = false
  }
}

onBeforeUnmount(() => {
  status.value = null
  statusError.value = null
  titles.value = null
  titlesError.value = null
  askedKeys.value = []
})
</script>

<template>
  <div class="sc">
    <!-- ══ 面板 1：抽取状态 ══ -->
    <section class="sc__panel">
      <span class="sc__panel-title">{{ t('sctx.statusTitle') }}</span>

      <!-- ★★★★★★ `extracted:false` 三种成因分不开 -->
      <p class="sc__note sc__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sctx.statusIndeterminateNote') }}</span>
      </p>
      <p class="sc__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('sctx.twoShapesNote') }}</span>
      </p>

      <label class="sc__label" for="sc-task">{{ t('sctx.taskId') }}</label>
      <input
        id="sc-task"
        v-model="taskId"
        class="sc__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('sctx.taskIdPlaceholder')"
      />
      <!-- ★★ task_id 叫 `titles` 会被后端特判截胡 -->
      <p v-if="shadowedId" class="sc__note sc__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sctx.shadowedIdNote') }}</span>
      </p>

      <button type="button" class="sc__btn" @click="loadStatus">
        {{ t('sctx.statusRun') }}
      </button>
      <p v-if="statusLoading" class="sc__msg">{{ t('common.loading') }}</p>

      <p v-if="statusError" class="sc__msg sc__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ statusError }}</span>
      </p>
      <p v-if="statusError && sessionContextSettingsMissing(statusError)" class="sc__note sc__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sctx.dbMissingNote') }}</span>
      </p>

      <template v-if="status">
        <!-- ★★ A 形：只有两个键，其余**不存在**（不是空值） -->
        <p v-if="extractionStatusIsIndeterminate(status)" class="sc__note sc__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('sctx.indeterminateResult') }}</span>
        </p>
        <!-- ★ 这句只在 A 形下成立：已抽取时那些字段**是在的**，不能无条件说「不存在」 -->

        <div class="sc__grid">
          <span class="sc__cell">
            <span class="sc__cell-l">{{ t('sctx.taskIdLabel') }}</span>
            <span class="sc__cell-v">{{ status.task_id }}</span>
          </span>
          <span class="sc__cell">
            <span class="sc__cell-l">{{ t('sctx.extracted') }}</span>
            <span class="sc__cell-v">
              <template v-if="status.extracted">{{ t('common.yes') }}</template>
              <template v-else>{{ t('sctx.undetermined') }}</template>
            </span>
          </span>
        </div>

        <!-- ★★ 只有 B 形才有这些键 -->
        <template v-if="status.extracted">
          <div class="sc__grid">
            <span class="sc__cell">
              <span class="sc__cell-l">{{ t('sctx.statusField') }}</span>
              <span class="sc__cell-v">{{ status.status }}</span>
            </span>
            <span class="sc__cell">
              <span class="sc__cell-l">{{ t('sctx.written') }}</span>
              <span class="sc__cell-v">{{ status.written }}</span>
            </span>
            <span class="sc__cell">
              <span class="sc__cell-l">{{ t('sctx.skippedNoise') }}</span>
              <span class="sc__cell-v">{{ status.skipped_noise }}</span>
            </span>
            <span class="sc__cell">
              <span class="sc__cell-l">{{ t('sctx.skippedDuplicate') }}</span>
              <span class="sc__cell-v">{{ status.skipped_duplicate }}</span>
            </span>
            <span class="sc__cell">
              <span class="sc__cell-l">{{ t('sctx.extractedAt') }}</span>
              <span class="sc__cell-v">{{ status.extracted_at }}</span>
            </span>
            <span class="sc__cell">
              <span class="sc__cell-l">{{ t('sctx.detail') }}</span>
              <span class="sc__cell-v">
                <template v-if="extractionDetailIsNull(status)">{{ t('sctx.detailNull') }}</template>
                <template v-else>{{ JSON.stringify(status.detail) }}</template>
              </span>
            </span>
          </div>
        </template>
        <p v-else-if="extractionStatusLacksDetailFields(status)" class="sc__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('sctx.lacksFieldsNote') }}</span>
        </p>
      </template>
    </section>

    <!-- ══ 面板 2：批量标题 ══ -->
    <section class="sc__panel">
      <span class="sc__panel-title">{{ t('sctx.titlesTitle') }}</span>

      <p class="sc__note sc__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sctx.postOnlyNote') }}</span>
      </p>
      <p class="sc__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('sctx.nulKeyNote') }}</span>
      </p>
      <p class="sc__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('sctx.missingKeyNote') }}</span>
      </p>

      <label class="sc__label" for="sc-ids">{{ t('sctx.idsLabel') }}</label>
      <textarea
        id="sc-ids"
        v-model="rawIds"
        class="sc__input sc__input--area"
        rows="4"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('sctx.idsPlaceholder')"
      />
      <p class="sc__meta">{{ t('sctx.normalizedCount', { n: requestedKeys.length }) }}</p>
      <!-- ★★ 限幅是 `> 500`，正好 500 合法 -->
      <p v-if="overLimit" class="sc__note sc__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sctx.overLimitNote', { max: TITLES_BATCH_MAX_KEYS, n: requestedKeys.length }) }}</span>
      </p>

      <button type="button" class="sc__btn" :disabled="overLimit || !requestedKeys.length" @click="loadTitles">
        {{ t('sctx.titlesRun') }}
      </button>
      <p v-if="titlesLoading" class="sc__msg">{{ t('common.loading') }}</p>

      <p v-if="titlesError" class="sc__msg sc__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ titlesError }}</span>
      </p>

      <template v-if="titles">
        <div class="sc__head">
          <span class="sc__sub">{{ t('sctx.resultTitle') }}</span>
          <span class="sc__badge">
            <span class="sc__badge-t">{{ t('sctx.resultCount', { n: titleRows.length }) }}</span>
          </span>
        </div>

        <!-- ★★★★ 「空」有四种成因，分不开 -->
        <p v-if="titleRows.length === 0" class="sc__note sc__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('sctx.emptyIndeterminateNote') }}</span>
        </p>
        <p v-if="titlesBatchPartiallyAnswered(askedKeys.length, titleRows.length)" class="sc__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('sctx.missingCountNote', { n: missingKeys.length, asked: askedKeys.length }) }}</span>
        </p>

        <ul v-if="titleRows.length" class="sc__list">
          <li v-for="r in titleRows" :key="r.key" class="sc__item">
            <div class="sc__head">
              <span class="sc__title">{{ r.title }}</span>
              <span class="sc__badge">
                <StatusDot tone="success" />
                <span class="sc__badge-t">{{ r.taskId }}</span>
              </span>
            </div>
            <!-- ★ 键含 NUL ⇒ 这里显示的是**拆开后**的两段 -->
            <p class="sc__meta">
              {{ t('sctx.scopedLine', { id: r.scopedSessionId || '—' }) }}
            </p>
          </li>
        </ul>
      </template>
    </section>

    <p class="sc__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('sctx.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.sc__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.sc__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.sc__sub { display: block; font-size: 13px; font-weight: 600; margin: 10px 0 4px; }
.sc__head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.sc__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }
.sc__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.sc__msg--err { color: var(--app-danger); }
.sc__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.sc__note--warn { color: var(--app-warning); }
.sc__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
.sc__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.sc__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; flex-shrink: 0; }
.sc__badge-t { color: var(--app-text-secondary); word-break: break-all; }
.sc__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.sc__cell { display: flex; flex-direction: column; }
.sc__cell-l { font-size: 11px; color: var(--app-text-muted); }
.sc__cell-v { font-size: 14px; font-weight: 600; word-break: break-all; }
.sc__list { list-style: none; margin: 0; padding: 0; }
.sc__item { padding: 10px 0; border-top: 1px solid var(--app-border); }

/* ★ R1：新增交互控件 ≥48 CSS px */
.sc__input { width: 100%; min-height: 48px; padding: 8px 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }
.sc__input--area { min-height: 96px; resize: vertical; }
.sc__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-primary); background: transparent; color: var(--app-primary); }
.sc__btn[disabled] { opacity: .5; }
</style>