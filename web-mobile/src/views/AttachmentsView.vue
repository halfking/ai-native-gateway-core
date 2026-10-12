<script setup lang="ts">
// AttachmentsView — 附件留存清单（**admin 档**，只读）。
//
// GET /api/admin/attachments?limit=&offset=&since=&until=&tenant_id=
// GET /api/admin/attachments/stats
// GET /api/admin/attachments/policy
// GET  /api/admin/attachments/cleanup/preview
// GET  /api/admin/attachments/{request_id}
//
// ★★ 档位：六条**全是 admin 档**（`admin/handler.go:998-1008` 的 `admin(...)`），
//   ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//   但**同一前缀下混着两条 superAdmin**（`cleanup/execute`、`filesystem/cleanup`），
//   本页两个都不碰。
//
// ★★★★★★ 头号陷阱：**同一个 `attachments` 字段，list 与 item 的 nullability 不同**。
//   · list 是 `attachments::text` **原样透传** ⇒ **可以是 JSON 标量 `null`**
//     （后端注释：18k+ 行存的就是 `null`）⇒ 一行出现在列表里但没有任何附件。
//   · item 是 `COALESCE(attachments::text,'[]')` ⇒ **保证是数组**。
//   ⇒ 列表里那条**不能**当成「无附件」的数组，也不许因此报错。
//
// ★★★★ `stats` 的行集合是 `list` 的**真子集**（多一个 `jsonb_typeof='array'`）
//   ⇒ 「列表 N 条」与「统计 M」**对不上是预期的**，页面必须标口径。
//
// ★★★★ 时间参数**解析失败被静默丢弃**（后端 `if err == nil` 才赋值）
//   ⇒ `?since=garbage` 返回**全时间范围**且**不报错**。
//   ⇒ 页面填了非法时间必须提示「窗口没生效」，不能默认它生效了。
//
// ★★ `limit`/`offset` 是**两端 clamp**：`99999 ⇒ 200`、`-5 ⇒ 0`（不是回落默认）。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchAttachments,
  fetchAttachmentStats,
  fetchAttachmentPolicy,
  previewAttachmentCleanup,
  attachmentLimitClamped,
  attachmentOffsetClamped,
  attachmentLimitWasClamped,
  attachmentOffsetWasClamped,
  attachmentTimeWindowWasDropped,
  attachmentRawIsNull,
  attachmentRowSetDiffers,
  attachmentStatsTotalsMatch,
  attachmentPreviewIsAlwaysDryRun,
  attachmentAutoCleanupEnabled,
  attachmentFsWarningLevel,
  fetchAttachmentFilesystemStats,
  type AttachmentRow,
  type AttachmentList,
  type AttachmentStats,
  type AttachmentPolicy,
  type AttachmentCleanupPreview,
  type AttachmentFilesystemStats,
} from '@/api/attachments'

useHyperPage({ title: () => t('att.title') })

const limit = ref(50)
const offset = ref(0)
const since = ref('')
const until = ref('')

const list = ref<AttachmentList | null>(null)
const stats = ref<AttachmentStats | null>(null)
const policy = ref<AttachmentPolicy | null>(null)
const preview = ref<AttachmentCleanupPreview | null>(null)
const fs = ref<AttachmentFilesystemStats | null>(null)

const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'viewmissing' | 'other'>('none')
const loading = ref(false)

const effectiveLimit = computed(() => attachmentLimitClamped(limit.value))
const effectiveOffset = computed(() => attachmentOffsetClamped(offset.value))
const limitClamped = computed(() => attachmentLimitWasClamped(limit.value))
const offsetClamped = computed(() => attachmentOffsetWasClamped(offset.value))
/** ★★ 用户填了非法时间 ⇒ 后端会静默丢弃 ⇒ 必须提示。 */
const windowDropped = computed(() => attachmentTimeWindowWasDropped(since.value, until.value))
const rows = computed<AttachmentRow[]>(() => list.value?.items ?? [])
/** ★★ 列表条数 vs 统计条数**口径不同**，对不上是预期的。 */
const rowSetDiffers = computed(() =>
  list.value && stats.value ? attachmentRowSetDiffers(list.value.items.length, stats.value.total_count) : false,
)
const statsConsistent = computed(() => (stats.value ? attachmentStatsTotalsMatch(stats.value) : false))

function classifyError(msg: string): 'none' | 'unconfigured' | 'viewmissing' | 'other' {
  if (/database unavailable/i.test(msg)) return 'unconfigured'
  if (/analytics_view_missing/i.test(msg)) return 'viewmissing'
  return 'other'
}

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    const [l, s, p, f] = await Promise.all([
      fetchAttachments({
        limit: limit.value,
        offset: offset.value,
        ...(since.value ? { since: since.value } : {}),
        ...(until.value ? { until: until.value } : {}),
      }),
      fetchAttachmentStats({
        ...(since.value ? { since: since.value } : {}),
        ...(until.value ? { until: until.value } : {}),
      }),
      fetchAttachmentPolicy(),
      fetchAttachmentFilesystemStats(),
    ])
    list.value = l
    stats.value = s
    policy.value = p
    fs.value = f
    preview.value = null
  } catch (e) {
    // ★★ 抛错不许退化成空清单 —— 空清单和「真的没有附件」长得一模一样
    list.value = null
    stats.value = null
    policy.value = null
    fs.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = classifyError(msg)
  } finally {
    loading.value = false
  }
}

async function runPreview(): Promise<void> {
  try {
    preview.value = await previewAttachmentCleanup({ olderThanDays: 30 })
  } catch (e) {
    preview.value = null
    error.value = (e as Error)?.message || t('common.error')
    errorKind.value = classifyError(error.value)
  }
}

void load()

// ★ limit / offset / 时间都是**服务端参数** ⇒ 必须重新取数。
watch([limit, offset, since, until], () => {
  void load()
})

onBeforeUnmount(() => {
  list.value = null
  stats.value = null
  policy.value = null
  preview.value = null
  fs.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="att">
    <section v-if="error" class="att__panel">
      <p class="att__msg att__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="att__note att__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('att.unavailableNote') }}</span>
      </p>
      <p v-else-if="errorKind === 'viewmissing'" class="att__note att__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('att.viewMissingNote') }}</span>
      </p>
    </section>

    <p v-if="loading" class="att__msg">{{ t('common.loading') }}</p>

    <section class="att__panel">
      <span class="att__panel-title">{{ t('att.filters') }}</span>

      <label class="att__label" for="att-limit">{{ t('att.limit') }}</label>
      <input id="att-limit" v-model.number="limit" class="att__input" type="number" inputmode="numeric" />
      <!-- ★★ 后端两端 clamp：99999 ⇒ 200、-5 ⇒ 0，不是回落默认 -->
      <p v-if="limitClamped || offsetClamped" class="att__note att__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('att.clampedNote', { limit: effectiveLimit, offset: effectiveOffset }) }}</span>
      </p>

      <label class="att__label" for="att-offset">{{ t('att.offset') }}</label>
      <input id="att-offset" v-model.number="offset" class="att__input" type="number" inputmode="numeric" />

      <label class="att__label" for="att-since">{{ t('att.since') }}</label>
      <input
        id="att-since"
        v-model="since"
        class="att__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('att.sincePlaceholder')"
      />

      <label class="att__label" for="att-until">{{ t('att.until') }}</label>
      <input
        id="att-until"
        v-model="until"
        class="att__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('att.untilPlaceholder')"
      />

      <!-- ★★★★ 非法时间被后端**静默丢弃** ⇒ 返回全时间范围且不报错 -->
      <p v-if="windowDropped" class="att__note att__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('att.windowDroppedNote') }}</span>
      </p>
      <p class="att__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('att.rangeNote') }}</span>
      </p>
    </section>

    <template v-if="list">
      <section class="att__panel">
        <div class="att__item-head">
          <span class="att__panel-title">{{ t('att.rowsTitle') }}</span>
          <span class="att__badge">
            <span class="att__badge-t">{{ t('att.shownCount', { n: rows.length }) }}</span>
          </span>
        </div>

        <!-- ★★★ 同一字段，list 侧可能是 JSON 标量 null（18k+ 行） -->
        <p class="att__note att__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('att.nullAttachmentsNote') }}</span>
        </p>
        <p class="att__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('att.countNote') }}</span>
        </p>

        <p v-if="!rows.length" class="att__msg">{{ t('att.empty') }}</p>

        <ul v-if="rows.length" class="att__list">
          <li v-for="x in rows" :key="x.request_id + x.ts" class="att__item">
            <div class="att__item-head">
              <span class="att__title">{{ x.request_id }}</span>
              <span class="att__badge">
                <StatusDot :tone="x.success ? 'success' : 'danger'" />
                <span class="att__badge-t">{{ x.success ? t('att.success') : t('att.failed') }}</span>
              </span>
            </div>
            <p class="att__meta">{{ x.tenant_id }} · {{ x.client_model || '—' }}</p>
            <p class="att__meta">{{ relativeTime(x.ts) }}</p>
            <!-- ★★ null 与「空数组」在列表侧是**两回事**（这里只标 null） -->
            <p v-if="attachmentRawIsNull(x)" class="att__note">
              <AppIcon name="key" :size="13" />
              <span>{{ t('att.rawNull') }}</span>
            </p>
          </li>
        </ul>
      </section>

      <section v-if="stats" class="att__panel">
        <span class="att__panel-title">{{ t('att.statsTitle') }}</span>

        <div class="att__grid">
          <span class="att__cell">
            <span class="att__cell-l">{{ t('att.totalCount') }}</span>
            <span class="att__cell-v">{{ stats.total_count }}</span>
          </span>
          <span class="att__cell">
            <span class="att__cell-l">{{ t('att.totalBytes') }}</span>
            <span class="att__cell-v">{{ stats.total_bytes }}</span>
          </span>
        </div>

        <!-- ★★★★ stats 的行集合 ⊂ list 的行集合 ⇒ 条数对不上是预期的 -->
        <p v-if="rowSetDiffers" class="att__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('att.rowSetDiffersNote') }}</span>
        </p>
        <p v-if="!statsConsistent" class="att__note att__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('att.statsMismatchNote') }}</span>
        </p>

        <ul v-if="stats.breakdown.length" class="att__list">
          <li v-for="(b, i) in stats.breakdown" :key="i" class="att__row">
            <span class="att__title">{{ b.type }} · {{ b.content_type }}</span>
            <span class="att__meta">
              {{ t('att.breakdownLine', { n: b.count, bytes: b.total_bytes }) }}
            </span>
          </li>
        </ul>
      </section>
    </template>

    <section v-if="policy" class="att__panel">
      <span class="att__panel-title">{{ t('att.policyTitle') }}</span>
      <!-- ★★★ policy 是**硬编码常量**，不从任何配置读 -->
      <p class="att__note att__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('att.policyHardcodedNote') }}</span>
      </p>
      <div class="att__grid">
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.retentionDays') }}</span>
          <span class="att__cell-v">{{ policy.policy.retention_days }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.maxSize') }}</span>
          <span class="att__cell-v">{{ policy.policy.max_size_bytes }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.autoCleanup') }}</span>
          <span class="att__cell-v">
            {{ attachmentAutoCleanupEnabled(policy) ? t('att.yes') : t('att.no') }}
          </span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.deleteFilesystem') }}</span>
          <span class="att__cell-v">
            {{ policy.policy.delete_filesystem ? t('att.yes') : t('att.no') }}
          </span>
        </span>
      </div>
      <p class="att__meta">{{ policy.policy.description }}</p>
      <p class="att__note">
        <AppIcon name="key" :size="13" />
        <span>{{ policy.note }}</span>
      </p>
    </section>

    <section v-if="fs" class="att__panel">
      <div class="att__item-head">
        <span class="att__panel-title">{{ t('att.fsTitle') }}</span>
        <span class="att__badge">
          <StatusDot :tone="attachmentFsWarningLevel(fs.disk_usage_percent) === 'danger' ? 'danger' : attachmentFsWarningLevel(fs.disk_usage_percent) === 'warning' ? 'warning' : 'success'" />
          <span class="att__badge-t">{{ fs.disk_warning_level }}</span>
        </span>
      </div>
      <div class="att__grid">
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.fsFiles') }}</span>
          <span class="att__cell-v">{{ fs.total_files }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.fsSize') }}</span>
          <span class="att__cell-v">{{ fs.total_size_human }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.fsDiskUsed') }}</span>
          <span class="att__cell-v">{{ fs.disk_usage_percent }}%</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.fsOldest') }}</span>
          <!-- ★ oldest_file_time 是无 omitempty 的指针：null = 一个文件都没有 -->
          <span class="att__cell-v">{{ fs.oldest_file_time ?? t('att.fsNoFiles') }}</span>
        </span>
      </div>
      <p class="att__meta">{{ fs.attachment_dir }}</p>
    </section>

    <section class="att__panel">
      <span class="att__panel-title">{{ t('att.previewTitle') }}</span>
      <p class="att__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('att.previewDryRunNote') }}</span>
      </p>
      <button type="button" class="att__btn" @click="runPreview">
        {{ t('att.previewRun') }}
      </button>
      <div v-if="preview" class="att__grid">
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.previewDays') }}</span>
          <span class="att__cell-v">{{ preview.older_than_days }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.previewAffected') }}</span>
          <span class="att__cell-v">{{ preview.affected_records }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.previewBytes') }}</span>
          <span class="att__cell-v">{{ preview.total_bytes }}</span>
        </span>
        <span class="att__cell">
          <span class="att__cell-l">{{ t('att.previewDryRun') }}</span>
          <span class="att__cell-v">
            {{ preview.dry_run || attachmentPreviewIsAlwaysDryRun() ? t('att.yes') : t('att.no') }}
          </span>
        </span>
      </div>
      <p v-if="preview" class="att__meta">{{ preview.action }}</p>
    </section>

    <p class="att__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('att.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.att__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.att__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.att__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.att__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.att__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }
.att__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-primary); background: transparent; color: var(--app-primary); }

.att__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.att__msg--err { color: var(--app-danger); }
.att__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.att__note--warn { color: var(--app-warning); }
.att__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.att__cell { display: flex; flex-direction: column; }
.att__cell-l { font-size: 11px; color: var(--app-text-muted); }
.att__cell-v { font-size: 14px; font-weight: 600; }
.att__list { list-style: none; margin: 0; padding: 0; }
.att__item { padding: 10px 0; border-top: 1px solid var(--app-border); }
.att__row { display: flex; flex-direction: column; gap: 2px; padding: 8px 0;
  border-top: 1px solid var(--app-border); }
.att__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.att__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.att__badge-t { color: var(--app-text-secondary); }
.att__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
</style>