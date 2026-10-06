<script setup lang="ts">
// ApprovalConfigView — 租户审批配置（**admin 档**，只读）。
//
// GET /api/admin/tenant-approval-config/{code}/approval-config
// GET /api/admin/tenant-approval-config/{code}/approval-config/stats
//
// ⚠️ 路径是 **`tenant-approval-config`（单数 tenant）**，打复数会落进
//   `handleTenants` 的 `unknown sub-resource` 404。
//
// ★ 档位是 **admin**（`wrapAdmin` = `admin.AdminMiddleware`）⇒ tenant_admin 可用
//   ⇒ 抽屉席**不设** `requiresRole`（与 model-policies 那族正好相反）。
//
// ★★★★★★ 头号陷阱：**「没有配置」不是错误**。
//   后端 `GetConfig` 对 `approval_configs` 里没有行的租户返回**合成默认配置**：
//   `enabled:false / mode:"disabled" / timeout_seconds:3600 /
//    auto_reject_on_timeout:true`，`created_at`/`updated_at` 是**零值时间**。
//   ⇒ 没有 404、没有「该租户不存在」。
//   ⇒ 而且那 3600 与 auto_reject 是**凭空造的**，库里没有这行。
//
// ★★★★★ stats 是 config 的**纯函数**（11 个字段全部可由 config 复算）
//   ⇒ 页面并排显示两者，并**独立复算**核对；不一致要说破。
//
// ★★★ 另两个端点读的是**另一张表**且**只回 enabled 行**：
//   `/approvers`、`/rules` 读 `approval_approvers` / `approval_rules`，
//   而 `stats.*_count` 读 JSONB 列（含停用的）⇒ 条数可以不一致，不是数据错。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchApprovalConfig,
  fetchApprovalConfigStats,
  approvalConfigIsSynthesized,
  approvalTimeoutMayBeSynthetic,
  approvalAutoRejectMayBeSynthetic,
  approvalModeTone,
  approvalStatsMatchesConfig,
  channelHasNoConfig,
  type ApprovalConfig,
  type ApprovalConfigStats,
} from '@/api/approvalConfig'

useHyperPage({ title: () => t('acfg.title') })

const route = useRoute()

const tenantCode = ref(typeof route.query.tenant === 'string' ? route.query.tenant : '')

const config = ref<ApprovalConfig | null>(null)
const stats = ref<ApprovalConfigStats | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'notregistered' | 'wrongpath' | 'other'>('none')
const loading = ref(false)

const code = computed(() => tenantCode.value.trim())
const synthesized = computed(() => (config.value ? approvalConfigIsSynthesized(config.value) : false))
const consistent = computed(() =>
  config.value && stats.value ? approvalStatsMatchesConfig(config.value, stats.value) : false,
)

/** ★ `channels` 可能是 `null`，归一化成数组。 */
const channelRows = computed(() => config.value?.channels ?? [])

/** ★ 客户端**独立复算**出来的计数，不信任后端给的数。 */
const recomputed = computed(() => {
  const c = config.value
  if (!c) return null
  const app = c.approvers ?? []
  const rules = c.rules ?? []
  const chans = c.channels ?? []
  return {
    approvers: app.length,
    enabledApprovers: app.filter((a) => a.enabled).length,
    rules: rules.length,
    enabledRules: rules.filter((r) => r.enabled).length,
    channels: chans.length,
    enabledChannels: chans.filter((x) => x.enabled).length,
  }
})

function classifyError(msg: string): 'none' | 'notregistered' | 'wrongpath' | 'other' {
  // ★★ default 分支是 `http.NotFound` ⇒ **裸文本**，不是 JSON 信封
  if (/404 page not found|page not found/i.test(msg)) return 'notregistered'
  // ★ 打复数路径会收到的那个 404
  if (/unknown sub-resource/i.test(msg)) return 'wrongpath'
  return 'other'
}

async function load(): Promise<void> {
  if (!code.value) {
    config.value = null
    stats.value = null
    return
  }
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    // ★ 两个端点都要；任一失败都**不许**退化成半个页面
    const [c, s] = await Promise.all([
      fetchApprovalConfig(code.value),
      fetchApprovalConfigStats(code.value),
    ])
    config.value = c
    stats.value = s
  } catch (e) {
    config.value = null
    stats.value = null
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
  config.value = null
  stats.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="acfg">
    <section class="acfg__panel">
      <span class="acfg__panel-title">{{ t('acfg.tenantCode') }}</span>
      <input
        id="acfg-code"
        v-model="tenantCode"
        class="acfg__input"
        type="search"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('acfg.tenantCodePlaceholder')"
        @keyup.enter="load"
      />
      <p class="acfg__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('acfg.pathNote') }}</span>
      </p>
    </section>

    <section v-if="error" class="acfg__panel">
      <p class="acfg__msg acfg__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <!-- ★★ 整族没注册（Redis 没起）时是**路由层**的裸文本 404 -->
      <p v-if="errorKind === 'notregistered'" class="acfg__note acfg__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('acfg.notRegisteredNote') }}</span>
      </p>
      <p v-else-if="errorKind === 'wrongpath'" class="acfg__note acfg__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('acfg.wrongPathNote') }}</span>
      </p>
    </section>

    <p v-if="loading" class="acfg__msg">{{ t('common.loading') }}</p>

    <template v-if="config">
      <!-- ★★★★★★ 从未配置过 ⇒ 这些数是后端凭空造的 -->
      <section v-if="synthesized" class="acfg__panel">
        <span class="acfg__panel-title">{{ t('acfg.synthTitle') }}</span>
        <p class="acfg__note acfg__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('acfg.synthNote') }}</span>
        </p>
      </section>

      <section class="acfg__panel">
        <div class="acfg__item-head">
          <span class="acfg__panel-title">{{ t('acfg.configTitle') }}</span>
          <span class="acfg__badge">
            <StatusDot :tone="config.enabled ? 'success' : 'muted'" />
            <span class="acfg__badge-t">{{ config.enabled ? t('acfg.on') : t('acfg.off') }}</span>
          </span>
        </div>

        <div class="acfg__grid">
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.mode') }}</span>
            <span class="acfg__cell-v">
              <StatusDot :tone="approvalModeTone(config.mode)" />
              {{ config.mode }}
            </span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.timeout') }}</span>
            <!-- ★ 3600 可能是合成的，必须打标 -->
            <span class="acfg__cell-v">
              {{ config.timeout_seconds }}
              <span v-if="approvalTimeoutMayBeSynthetic(config)" class="acfg__tag">
                {{ t('acfg.mayBeSynthetic') }}
              </span>
            </span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.autoReject') }}</span>
            <span class="acfg__cell-v">
              {{ config.auto_reject_on_timeout ? t('acfg.yes') : t('acfg.no') }}
              <span v-if="approvalAutoRejectMayBeSynthetic(config)" class="acfg__tag">
                {{ t('acfg.mayBeSynthetic') }}
              </span>
            </span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.updatedAt') }}</span>
            <span class="acfg__cell-v">{{ config.updated_at }}</span>
          </span>
        </div>
      </section>

      <section class="acfg__panel">
        <span class="acfg__panel-title">{{ t('acfg.statsTitle') }}</span>

        <p class="acfg__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('acfg.statsSourceNote') }}</span>
        </p>

        <div v-if="stats" class="acfg__grid">
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.approverCount') }}</span>
            <span class="acfg__cell-v">{{ stats.approver_count }}</span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.enabledApprovers') }}</span>
            <span class="acfg__cell-v">{{ stats.enabled_approvers }}</span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.ruleCount') }}</span>
            <span class="acfg__cell-v">{{ stats.rule_count }}</span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.enabledRules') }}</span>
            <span class="acfg__cell-v">{{ stats.enabled_rules }}</span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.channelCount') }}</span>
            <span class="acfg__cell-v">{{ stats.channel_count }}</span>
          </span>
          <span class="acfg__cell">
            <span class="acfg__cell-l">{{ t('acfg.enabledChannels') }}</span>
            <span class="acfg__cell-v">{{ stats.enabled_channels }}</span>
          </span>
        </div>

        <!-- ★★★★★ stats 是 config 的纯函数 ⇒ 客户端独立复算并核对 -->
        <p v-if="stats" class="acfg__note" :class="{ 'acfg__note--warn': !consistent }">
          <AppIcon :name="consistent ? 'check' : 'alert'" :size="13" />
          <span v-if="consistent">{{ t('acfg.consistentNote') }}</span>
          <span v-else>{{ t('acfg.inconsistentNote') }}</span>
        </p>

        <p v-if="recomputed" class="acfg__note">
          <AppIcon name="key" :size="13" />
          <span>
            {{
              t('acfg.recomputedNote', {
                a: recomputed.approvers,
                ar: recomputed.rules,
                c: recomputed.channels,
              })
            }}
          </span>
        </p>
      </section>

      <section class="acfg__panel">
        <div class="acfg__item-head">
          <span class="acfg__panel-title">{{ t('acfg.channelsTitle') }}</span>
          <span class="acfg__badge">
            <span class="acfg__badge-t">{{ t('acfg.shownCount', { n: channelRows.length }) }}</span>
          </span>
        </div>
        <p class="acfg__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('acfg.channelsNote') }}</span>
        </p>
        <p v-if="!channelRows.length" class="acfg__msg">{{ t('acfg.noChannels') }}</p>
        <ul v-if="channelRows.length" class="acfg__list">
          <li v-for="(c, i) in channelRows" :key="i" class="acfg__row">
            <span class="acfg__cell-v">{{ c.type }}</span>
            <span class="acfg__badge-t">{{ c.enabled ? t('acfg.on') : t('acfg.off') }}</span>
            <!-- ★ map 无 omitempty ⇒ nil map 序列化成 null，不是 {} -->
            <span class="acfg__note">
              <AppIcon name="key" :size="13" />
              <span>
                {{
                  channelHasNoConfig(c)
                    ? t('acfg.channelNoConfig')
                    : Object.entries(c.config ?? {}).map(([k, v]) => `${k}=${v}`).join(' · ')
                }}
              </span>
            </span>
          </li>
        </ul>
      </section>
    </template>

    <p class="acfg__note">
      <AppIcon name="key" :size="13" />
      <span>{{ t('acfg.readOnlyNote') }}</span>
    </p>
  </div>
</template>

<style scoped>
.acfg__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.acfg__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.acfg__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.acfg__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }

.acfg__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.acfg__msg--err { color: var(--app-danger); }
.acfg__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.acfg__note--warn { color: var(--app-warning); }
.acfg__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.acfg__cell { display: flex; flex-direction: column; }
.acfg__cell-l { font-size: 11px; color: var(--app-text-muted); }
.acfg__cell-v { font-size: 14px; font-weight: 600; }
.acfg__tag { font-size: 11px; color: var(--app-warning); font-weight: 500; }
.acfg__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.acfg__badge-t { color: var(--app-text-secondary); }
.acfg__list { list-style: none; margin: 0; padding: 0; }
.acfg__row { display: flex; flex-direction: column; gap: 2px; padding: 8px 0;
  border-top: 1px solid var(--border, #eee); }
</style>