<script setup lang="ts">
// ModuleDetailView — 单个功能模块的详情（**admin 档**，只读）。
//
// GET /api/admin/modules/{key}            ⇒ {module, config}
// GET /api/admin/modules/{key}/config     ⇒ **只有 feishu_bot 实现**，其余 501
//
// ★★ `/config` 是本仓**第一次**出现 501 的地方（`admin/modules.go:1277-1281`）：
//     switch key { case "feishu_bot": …; default: writeError(w, 501, …) }
//   ⇒ 页面**先按 key 判断**再决定要不要给按钮，不去打注定 501 的请求。
//
// ★★★★★★ `config` 里的键是**三**态，不是两态。
//   `handleModulesGet`（`:816-833`）的循环**没有** `raw == nil` 判断
//   （对比 `resolveModuleEnabled` 是有的）⇒ 值为 NULL 的键**不会**被 continue
//   跳过，而是 `jsoncol.Decode` 失败、`v` 保持 nil、**键照样进 config**：
//       1. 键**整个不在** config 里     ⇒ spec 不存在 **或** EffectiveValue 报错
//       2. 键**在但 value 是 null**     ⇒ 值确实是 NULL（读到了，只是没值）
//       3. 键在且有值
//   ⇒ 只报「缺了哪些键」会把第 2 态**误并进第 1 态** ⇒ 本页三态分开列。
//
// ★★★★★★ 跨端点**自相矛盾**：模块面与配置摘要读的是**同一个**
//   `feishu_bot.enabled`，但判定函数不同：
//   · `resolveModuleEnabled` 五条失败路径 ⇒ fallback `(true,"default")`
//   · `readBool` 的失败路径           ⇒ fallback `false`（Go 零值）
//   ⇒ 读失败时两处会给出**相反**答案。页面**原样并列呈现**，不 reconcile。
//
// ★★★★★★ 配置摘要两个**零值产物**（页面上都不许当真值渲染）：
//   · `allowed_user_count = len(strings.Split(readString(…), ","))`
//     而 readString 的失败路径全返回 `""` ⇒ `Split("", ",")` 长度是 **1**
//     ⇒ `1` 不可分辨「一个没配」与「恰好允许 1 人」。
//   · `quiet_hours_window = start + "–" + end` ⇒ 两端空时是字面量 `"–"`
//     ⇒ 非空**不等于**配了。
//
// ★★ 不碰 `PUT /{key}/toggle`（写）与 `POST /{key}/test`（**真给飞书发消息**）。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchModule,
  fetchModuleConfig,
  moduleHasConfigEndpoint,
  moduleEnabledMayBeFallback,
  moduleEnabledWasReallyRead,
  moduleEnabledDisagrees,
  moduleIsBlocked,
  moduleIsAlwaysOn,
  moduleHasNoCapabilities,
  moduleHasNoSettingKey,
  moduleRequiredDepNames,
  moduleDangerTone,
  moduleConfigStates,
  moduleConfigStateCounts,
  modulesSettingsNotInitialised,
  allowedUserCountMayBeEmptySplit,
  quietHoursWindowIsEmptyArtifact,
  type ModuleDetail,
  type FeishuBotConfigSummary,
} from '@/api/modules'

const route = useRoute()
useHyperPage({ title: () => t('modsDetail.title') })

const key = computed(() => String(route.params.key ?? ''))

const detail = ref<ModuleDetail | null>(null)
const summary = ref<FeishuBotConfigSummary | null>(null)
const error = ref<string | null>(null)
const settingsMissing = ref(false)
const summaryError = ref<string | null>(null)
/** ★ 本仓首次出现的状态码，必须**单列**渲染，不许混进「其他错误」。 */
const notImplemented = ref(false)
const loading = ref(false)

const moduleRow = computed(() => detail.value?.module ?? null)
/** ★★★ 三态分开，不合并。 */
const states = computed(() => (detail.value ? moduleConfigStates(detail.value) : null))
const counts = computed(() => (detail.value ? moduleConfigStateCounts(detail.value) : null))
/** ★★ 只有 feishu_bot 有 `/config`；其余一律 501 ⇒ 不去打注定失败的请求。 */
const hasConfigEndpoint = computed(() => moduleHasConfigEndpoint(key.value))

function classifyError(msg: string): void {
  settingsMissing.value = modulesSettingsNotInitialised(msg)
  notImplemented.value = /not implemented/i.test(msg)
}

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  settingsMissing.value = false
  notImplemented.value = false
  summary.value = null
  summaryError.value = null
  try {
    detail.value = await fetchModule(key.value)
  } catch (e) {
    // ★★ 抛错不许退化成「空详情」—— 那和「这个模块没有配置键」长得一样
    detail.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    classifyError(msg)
  } finally {
    loading.value = false
  }
}

async function runSummary(): Promise<void> {
  summaryError.value = null
  notImplemented.value = false
  try {
    summary.value = await fetchModuleConfig(key.value)
  } catch (e) {
    summary.value = null
    const msg = (e as Error)?.message || t('common.error')
    summaryError.value = msg
    classifyError(msg)
  }
}

void load()
watch(key, () => {
  void load()
})

onBeforeUnmount(() => {
  detail.value = null
  summary.value = null
  error.value = null
  summaryError.value = null
  settingsMissing.value = false
  notImplemented.value = false
})
</script>

<template>
  <div class="mdt">
    <section v-if="error" class="mdt__panel">
      <p class="mdt__msg mdt__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="settingsMissing" class="mdt__note mdt__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mods.settingsMissingNote') }}</span>
      </p>
      <p v-else-if="notImplemented" class="mdt__note mdt__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('modsDetail.notImplementedNote') }}</span>
      </p>
      <button type="button" class="mdt__btn" @click="load">
        {{ t('common.retry') }}
      </button>
    </section>

    <p v-if="loading" class="mdt__msg">{{ t('common.loading') }}</p>

    <template v-if="detail && moduleRow">
      <section class="mdt__panel">
        <div class="mdt__head">
          <span class="mdt__panel-title">{{ moduleRow.name }}</span>
          <span class="mdt__badge">
            <!-- ★ 兜底态不给绿点：读没读到是未知的 -->
            <StatusDot :tone="moduleEnabledMayBeFallback(moduleRow) ? 'warning' : moduleRow.enabled ? 'success' : 'danger'" />
            <span class="mdt__badge-t">
              <template v-if="moduleEnabledMayBeFallback(moduleRow)">{{ t('mods.stateUnread') }}</template>
              <template v-else-if="moduleRow.enabled">{{ t('mods.stateOn') }}</template>
              <template v-else>{{ t('mods.stateOff') }}</template>
            </span>
          </span>
        </div>

        <p class="mdt__meta">{{ moduleRow.key }}</p>
        <p class="mdt__desc">{{ moduleRow.description || '—' }}</p>

        <div class="mdt__grid">
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('mods.sourceLabel') }}</span>
            <span class="mdt__cell-v">
              <template v-if="moduleEnabledWasReallyRead(moduleRow)">{{ moduleRow.source }}</template>
              <template v-else>{{ t('mods.sourceUnknown') }}</template>
            </span>
          </span>
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('mods.dangerLabel') }}</span>
            <span class="mdt__cell-v">
              <span class="mdt__chip" :class="`mdt__chip--${moduleDangerTone(moduleRow.danger_level)}`">
                {{ moduleRow.danger_level || t('mods.dangerUnknown') }}
              </span>
            </span>
          </span>
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('modsDetail.settingKey') }}</span>
            <span class="mdt__cell-v">{{ moduleRow.setting_key || t('modsDetail.settingKeyEmpty') }}</span>
          </span>
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('mods.categoryLabel') }}</span>
            <span class="mdt__cell-v">{{ moduleRow.category || '—' }}</span>
          </span>
        </div>

        <!-- ★★★★★★ 头号免责：enabled:true 可能是读不出来的兜底 -->
        <p class="mdt__note mdt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mods.fallbackNote') }}</span>
        </p>
        <p v-if="moduleIsAlwaysOn(moduleRow)" class="mdt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mods.alwaysOnNote') }}</span>
        </p>
        <p v-else-if="moduleHasNoSettingKey(moduleRow)" class="mdt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('modsDetail.noSettingKeyNote') }}</span>
        </p>

        <!-- ★★★ blocked_reason 带 omitempty ⇒ 键存在就是被挡住了 -->
        <p v-if="moduleIsBlocked(moduleRow)" class="mdt__note mdt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ moduleRow.blocked_reason }}</span>
        </p>

        <p class="mdt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mods.readOnlyNote') }}</span>
        </p>
        <p class="mdt__note mdt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('modsDetail.testSideEffectNote') }}</span>
        </p>
        <!-- ★ 详情页也要有重取手段：开关在别处被改后，这一页没有任何刷新入口 -->
        <button type="button" class="mdt__btn mdt__btn--refresh" @click="load">
          {{ t('common.refresh') }}
        </button>
      </section>

      <section class="mdt__panel">
        <span class="mdt__panel-title">{{ t('modsDetail.capTitle') }}</span>
        <p v-if="moduleHasNoCapabilities(moduleRow)" class="mdt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mods.noCapabilitiesNote') }}</span>
        </p>
        <ul v-else class="mdt__tags">
          <li v-for="(c, i) in moduleRow.capabilities" :key="i" class="mdt__chip mdt__chip--muted">{{ c }}</li>
        </ul>
      </section>

      <section v-if="moduleRow.dependencies && moduleRow.dependencies.length" class="mdt__panel">
        <span class="mdt__panel-title">{{ t('modsDetail.depTitle') }}</span>
        <ul class="mdt__list">
          <li v-for="d in moduleRow.dependencies" :key="d.key" class="mdt__row">
            <span class="mdt__title">{{ d.name }}</span>
            <span class="mdt__meta">
              {{ t('modsDetail.depRequired') }} {{ d.required ? t('common.yes') : t('common.no') }}
            </span>
          </li>
        </ul>
        <p v-if="!moduleRequiredDepNames(moduleRow).length" class="mdt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('modsDetail.depNoRequiredNote') }}</span>
        </p>
      </section>

      <!-- ★★★★★★ 配置三态 -->
      <section v-if="counts && states" class="mdt__panel">
        <div class="mdt__head">
          <span class="mdt__panel-title">{{ t('modsDetail.configTitle') }}</span>
          <span class="mdt__badge">
            <span class="mdt__badge-t">{{ t('modsDetail.configCount', { n: counts.declared }) }}</span>
          </span>
        </div>

        <p class="mdt__note mdt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('modsDetail.configThreeStateNote') }}</span>
        </p>

        <div class="mdt__grid">
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('modsDetail.stateResolved') }}</span>
            <span class="mdt__cell-v">{{ counts.resolved }}</span>
          </span>
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('modsDetail.stateNullValue') }}</span>
            <span class="mdt__cell-v">{{ counts.nullValue }}</span>
          </span>
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('modsDetail.stateAbsent') }}</span>
            <span class="mdt__cell-v">{{ counts.absent }}</span>
          </span>
          <span class="mdt__cell">
            <span class="mdt__cell-l">{{ t('modsDetail.configTotal') }}</span>
            <span class="mdt__cell-v">{{ counts.declared }}</span>
          </span>
        </div>

        <template v-if="states.resolved.length">
          <span class="mdt__sub">{{ t('modsDetail.resolvedList') }}</span>
          <ul class="mdt__list">
            <li v-for="k in states.resolved" :key="k" class="mdt__row">
              <span class="mdt__title">{{ k }}</span>
              <span class="mdt__meta">{{ String(detail.config[k]?.value) }} · {{ detail.config[k]?.source }}</span>
            </li>
          </ul>
        </template>

        <template v-if="states.nullValue.length">
          <span class="mdt__sub">{{ t('modsDetail.nullList') }}</span>
          <ul class="mdt__list">
            <li v-for="k in states.nullValue" :key="k" class="mdt__row">
              <span class="mdt__title">{{ k }}</span>
              <span class="mdt__meta">{{ t('modsDetail.nullValueRow') }}</span>
            </li>
          </ul>
        </template>

        <template v-if="states.absent.length">
          <span class="mdt__sub">{{ t('modsDetail.absentList') }}</span>
          <ul class="mdt__list">
            <li v-for="k in states.absent" :key="k" class="mdt__row">
              <span class="mdt__title">{{ k }}</span>
              <span class="mdt__meta">{{ t('modsDetail.absentRow') }}</span>
            </li>
          </ul>
        </template>

        <p v-if="!counts.declared" class="mdt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('modsDetail.noConfigKeysNote') }}</span>
        </p>
      </section>

      <!-- ★★ 只有 feishu_bot 实现了 /config -->
      <section class="mdt__panel">
        <span class="mdt__panel-title">{{ t('modsDetail.summaryTitle') }}</span>
        <p v-if="!hasConfigEndpoint" class="mdt__note mdt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('modsDetail.noConfigEndpointNote') }}</span>
        </p>
        <template v-else>
          <p class="mdt__note">
            <AppIcon name="key" :size="13" />
            <span>{{ t('modsDetail.summaryNote') }}</span>
          </p>
          <p class="mdt__note mdt__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('modsDetail.summaryZeroValueNote') }}</span>
          </p>
          <button type="button" class="mdt__btn mdt__btn--summary" @click="runSummary">
            {{ t('modsDetail.summaryRun') }}
          </button>
        </template>

        <p v-if="summaryError" class="mdt__msg mdt__msg--err">
          <AppIcon name="alert" :size="14" />
          <span>{{ summaryError }}</span>
        </p>
        <!-- ★ 501 是本仓首次出现的状态码，单列 -->
        <p v-if="notImplemented" class="mdt__note mdt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('modsDetail.notImplementedNote') }}</span>
        </p>

        <template v-if="summary">
          <!-- ★★★★★★ 跨端点自相矛盾：并列呈现，不 reconcile -->
          <p v-if="moduleEnabledDisagrees(moduleRow.enabled, summary.enabled === true)" class="mdt__note mdt__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('modsDetail.disagreeNote') }}</span>
          </p>
          <p v-if="allowedUserCountMayBeEmptySplit(Number(summary.allowed_user_count))" class="mdt__note mdt__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('modsDetail.allowedUsersArtifactNote') }}</span>
          </p>
          <div class="mdt__grid">
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumEnabled') }}</span>
              <span class="mdt__cell-v">{{ summary.enabled ? t('common.yes') : t('common.no') }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumWebhook') }}</span>
              <span class="mdt__cell-v">{{ summary.webhook_url_set ? t('common.yes') : t('common.no') }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumVerifyToken') }}</span>
              <span class="mdt__cell-v">{{ summary.verify_token_set ? t('common.yes') : t('common.no') }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumEncryptKey') }}</span>
              <span class="mdt__cell-v">{{ summary.encrypt_key_set ? t('common.yes') : t('common.no') }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumConnectionMode') }}</span>
              <span class="mdt__cell-v">{{ summary.connection_mode || '—' }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumAllowedUsers') }}</span>
              <span class="mdt__cell-v">{{ summary.allowed_user_count }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumQuietWindow') }}</span>
              <span class="mdt__cell-v">
                <template v-if="quietHoursWindowIsEmptyArtifact(String(summary.quiet_hours_window))">
                  {{ t('modsDetail.quietUnset') }}
                </template>
                <template v-else>{{ summary.quiet_hours_window }}</template>
              </span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumSeverityMin') }}</span>
              <span class="mdt__cell-v">{{ summary.alert_severity_min || '—' }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumRateLimit') }}</span>
              <span class="mdt__cell-v">{{ summary.alert_rate_limit_min }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumSignature') }}</span>
              <span class="mdt__cell-v">{{ summary.signature_required ? t('common.yes') : t('common.no') }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumTimestampWindow') }}</span>
              <span class="mdt__cell-v">{{ summary.timestamp_window_sec }}</span>
            </span>
            <span class="mdt__cell">
              <span class="mdt__cell-l">{{ t('modsDetail.sumNotifyAlert') }}</span>
              <span class="mdt__cell-v">{{ summary.notify_on_alert ? t('common.yes') : t('common.no') }}</span>
            </span>
          </div>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.mdt__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mdt__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mdt__sub { display: block; font-size: 12px; font-weight: 600; color: var(--app-text-secondary); margin: 10px 0 4px; }
.mdt__head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mdt__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mdt__msg--err { color: var(--app-danger); }
.mdt__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mdt__note--warn { color: var(--app-warning); }
.mdt__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
.mdt__desc { font-size: 13px; color: var(--app-text-secondary); margin: 6px 0; }
.mdt__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.mdt__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; flex-shrink: 0; }
.mdt__badge-t { color: var(--app-text-secondary); }
.mdt__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.mdt__cell { display: flex; flex-direction: column; }
.mdt__cell-l { font-size: 11px; color: var(--app-text-muted); }
.mdt__cell-v { font-size: 14px; font-weight: 600; word-break: break-all; }
.mdt__list { list-style: none; margin: 0; padding: 0; }
.mdt__row { display: flex; flex-direction: column; gap: 2px; padding: 8px 0; border-top: 1px solid var(--app-border); }
.mdt__tags { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 6px; }
.mdt__chip { font-size: 11px; padding: 2px 6px; border-radius: 6px; border: 1px solid var(--app-border);
  display: inline-block; }
.mdt__chip--danger { color: var(--app-danger); border-color: var(--app-danger); }
.mdt__chip--warning { color: var(--app-warning); border-color: var(--app-warning); }
.mdt__chip--muted { color: var(--app-text-muted); }

/* ★ R1：新增交互控件 ≥48 CSS px */
.mdt__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-primary); background: transparent; color: var(--app-primary); }
</style>
