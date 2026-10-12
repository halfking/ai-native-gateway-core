<script setup lang="ts">
// ModulesView — 功能模块清单（**admin 档**，只读）。
//
// GET /api/admin/modules
//
// ⚠️★ 注册**不在** `admin/handler.go`，而在 `admin/modules.go:1129-1134` 的
//   `registerModuleRoutes`：
//       mux.HandleFunc("/api/admin/modules",   h.admin(h.handleModulesList))
//       mux.HandleFunc("/api/admin/modules/",  h.admin(h.handleModulesRouter))
//   ★ 我第一轮只 grep 了 `handler.go`，差点误判成「这族根本没注册」。
//     **grep 路由注册必须限定到全仓。**
//
// 档位：两条都是 `h.admin(...)` ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//
// ★★★★★★ 头号陷阱：`enabled: true` **可能是「没读到配置」的兜底**。
//   `resolveModuleEnabled`（`admin/modules.go:628-648`）**五条**失败路径全部
//   返回 `(true, "default")`；而 `EffectiveValue`（`settings/spec.go:284-317`）
//   优先级链 DB > env > default 的**第 3 步**回落 spec 自身默认值时
//   **也**返回 `("default")`。
//   ⇒ `source === "default"` 有**六种**成因，客户端**一种都分辨不出**。
//   ⇒ 只有 `source ∈ {"db","env"}` 才代表「真读到了某处显式配置」。
//   ⇒ 所以本页**不说**「已启用」，只说「**没能读到配置**」或「读自 db/env」。
//
// ★★★ `source` 的取值集合由 `EffectiveValue` 的 doc 注释写死
//   `source ∈ {"db","env","default"}` —— **没有第四个值**。
//
// ★★★ `blocked_reason` 带 `omitempty` ⇒「没有阻塞」= **键整个不存在**。
//   `can_toggle_enabled = (blocked_reason == "")` 与它**联动**。
//
// ★★ 不碰的两个端点（**都不是只读**）：
//   · `PUT /{key}/toggle`  —— 写开关
//   · `POST /{key}/test`   —— ★ 它名字叫 test，但后端对 feishu_bot
//     **真发一条消息到 webhook_url**（`admin/modules.go:1169-1180`）
//     ⇒ 有**外部副作用**，本页绝不调用。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchModules,
  moduleEnabledMayBeFallback,
  moduleEnabledWasReallyRead,
  moduleIsBlocked,
  moduleIsAlwaysOn,
  moduleHasNoCapabilities,
  moduleRequiredDepNames,
  moduleDangerTone,
  modulesSourceIsKnown,
  modulesSettingsNotInitialised,
  type ModuleList,
  type ModuleWithStatus,
} from '@/api/modules'

useHyperPage({ title: () => t('mods.title') })

const router = useRouter()

const list = ref<ModuleList | null>(null)
const error = ref<string | null>(null)
/** ★ 503 的两种文案都是「settings 注册表没起」，**不是**角色问题。 */
const settingsMissing = ref(false)
const loading = ref(false)

const items = computed<ModuleWithStatus[]>(() => list.value?.items ?? [])
/** ★★ 有多少条的 `enabled:true` 其实**没读到配置**。 */
const fallbackCount = computed(() => items.value.filter(moduleEnabledMayBeFallback).length)
/** ★★ 有多少条被必需依赖挡住。 */
const blockedCount = computed(() => items.value.filter(moduleIsBlocked).length)
/** ★ `source` 出现集合外的值（后端实现上不可能）⇒ 值得上报。 */
const unknownSources = computed(() => items.value.filter((m) => !modulesSourceIsKnown(m.source)))

function classifyError(msg: string): void {
  settingsMissing.value = modulesSettingsNotInitialised(msg)
}

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  settingsMissing.value = false
  try {
    list.value = await fetchModules()
  } catch (e) {
    // ★★ 抛错不许退化成空清单 —— 空清单和「真没有模块」在页面上长得一样
    list.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    classifyError(msg)
  } finally {
    loading.value = false
  }
}

function openDetail(m: ModuleWithStatus): void {
  void router.push(`/modules/${encodeURIComponent(m.key)}`)
}

void load()

onBeforeUnmount(() => {
  list.value = null
  error.value = null
  settingsMissing.value = false
})
</script>

<template>
  <div class="mods">
    <section v-if="error" class="mods__panel">
      <p class="mods__msg mods__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <!-- ★ 503 的两种文案都不是权限问题 ⇒ 提示要说「注册表没起」而不是「无权限」 -->
      <p v-if="settingsMissing" class="mods__note mods__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mods.settingsMissingNote') }}</span>
      </p>
      <button type="button" class="mods__btn" @click="load">
        {{ t('common.retry') }}
      </button>
    </section>

    <p v-if="loading" class="mods__msg">{{ t('common.loading') }}</p>

    <template v-if="list">
      <section class="mods__panel">
        <div class="mods__head">
          <span class="mods__panel-title">{{ t('mods.listTitle') }}</span>
          <!-- ★★ 计数徽标**只**活在 v-if="list" 里 ⇒ 判「面板没渲染」时用它当锚 -->
          <span class="mods__badge">
            <span class="mods__badge-t">{{ t('mods.shownCount', { n: items.length }) }}</span>
          </span>
        </div>

        <!-- ★★★★★★ 头号免责：enabled:true 可能是读不出来的兜底 -->
        <p class="mods__note mods__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mods.fallbackNote') }}</span>
        </p>
        <p v-if="fallbackCount > 0" class="mods__note mods__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mods.fallbackCountNote', { n: fallbackCount }) }}</span>
        </p>
        <p class="mods__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mods.sourceNote') }}</span>
        </p>
        <p v-if="unknownSources.length" class="mods__note mods__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mods.unknownSourceNote', { n: unknownSources.length }) }}</span>
        </p>
        <p class="mods__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mods.readOnlyNote') }}</span>
        </p>
        <!-- ★ 只读页也得有重取手段：模块开关在别处被改后，这一页没有任何刷新入口 -->
        <button type="button" class="mods__btn mods__btn--refresh" @click="load">
          {{ t('common.refresh') }}
        </button>
      </section>

      <section class="mods__panel">
        <div class="mods__head">
          <span class="mods__panel-title">{{ t('mods.statusTitle') }}</span>
          <span class="mods__badge">
            <span class="mods__badge-t">{{ t('mods.blockedCount', { n: blockedCount }) }}</span>
          </span>
        </div>

        <p v-if="!items.length" class="mods__msg">{{ t('mods.empty') }}</p>

        <ul v-if="items.length" class="mods__list">
          <li v-for="m in items" :key="m.key" class="mods__item">
            <button type="button" class="mods__row" @click="openDetail(m)">
              <div class="mods__head">
                <span class="mods__title">{{ m.name }}</span>
                <span class="mods__badge">
                  <!-- ★ 兜底态不给「成功」绿点：读没读到是未知的 -->
                  <StatusDot :tone="moduleEnabledMayBeFallback(m) ? 'warning' : m.enabled ? 'success' : 'danger'" />
                  <span class="mods__badge-t">
                    <template v-if="moduleEnabledMayBeFallback(m)">{{ t('mods.stateUnread') }}</template>
                    <template v-else-if="m.enabled">{{ t('mods.stateOn') }}</template>
                    <template v-else>{{ t('mods.stateOff') }}</template>
                  </span>
                </span>
              </div>

              <p class="mods__meta">{{ m.key }} · {{ t('mods.categoryLabel') }} {{ m.category || '—' }}</p>

              <!-- ★★★ source 只有 {db, env, default} 三个值 -->
              <p class="mods__meta">
                {{ t('mods.sourceLabel') }}
                <template v-if="moduleEnabledWasReallyRead(m)">{{ m.source }}</template>
                <template v-else>{{ t('mods.sourceUnknown') }}</template>
              </p>

              <!-- ★★ setting_key 为空 ⇒ 后端压根没查任何设置 -->
              <p v-if="moduleIsAlwaysOn(m)" class="mods__note">
                <AppIcon name="key" :size="13" />
                <span>{{ t('mods.alwaysOnNote') }}</span>
              </p>

              <!-- ★★★ blocked_reason 键存在（omitempty）⇒ 必是被必需依赖挡住 -->
              <p v-if="moduleIsBlocked(m)" class="mods__note mods__note--warn">
                <AppIcon name="alert" :size="13" />
                <span>{{ m.blocked_reason }}</span>
              </p>
              <p v-else-if="moduleRequiredDepNames(m).length" class="mods__note">
                <AppIcon name="key" :size="13" />
                <span>{{ t('mods.depsOkNote', { names: moduleRequiredDepNames(m).join('、') }) }}</span>
              </p>

              <p v-if="moduleHasNoCapabilities(m)" class="mods__note">
                <AppIcon name="key" :size="13" />
                <span>{{ t('mods.noCapabilitiesNote') }}</span>
              </p>

              <p class="mods__tail">
                <span class="mods__tail-l">{{ t('mods.dangerLabel') }}</span>
                <span class="mods__chip" :class="`mods__chip--${moduleDangerTone(m.danger_level)}`">
                  {{ m.danger_level || t('mods.dangerUnknown') }}
                </span>
                <span class="mods__tail-l">{{ t('mods.toggleLabel') }}</span>
                <span class="mods__chip" :class="m.can_toggle_enabled ? 'mods__chip--muted' : 'mods__chip--warning'">
                  {{ m.can_toggle_enabled ? t('mods.canToggleYes') : t('mods.canToggleNo') }}
                </span>
              </p>
            </button>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.mods__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mods__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mods__head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }

.mods__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mods__msg--err { color: var(--app-danger); }
.mods__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mods__note--warn { color: var(--app-warning); }
.mods__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; word-break: break-all; }
.mods__title { font-size: 14px; font-weight: 600; word-break: break-all; }
.mods__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; flex-shrink: 0; }
.mods__badge-t { color: var(--app-text-secondary); }
.mods__tail { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin: 8px 0 0; }
.mods__tail-l { font-size: 11px; color: var(--app-text-muted); }
.mods__chip { font-size: 11px; padding: 2px 6px; border-radius: 6px; border: 1px solid var(--app-border); }
.mods__chip--danger { color: var(--app-danger); border-color: var(--app-danger); }
.mods__chip--warning { color: var(--app-warning); border-color: var(--app-warning); }
.mods__chip--muted { color: var(--app-text-muted); }
.mods__list { list-style: none; margin: 0; padding: 0; }
.mods__item { border-top: 1px solid var(--app-border); }

/* ★ R1：新增交互控件 ≥48 CSS px。整行可点，min-height 48 */
.mods__row { display: block; width: 100%; min-height: 48px; padding: 10px 0; text-align: left;
  background: transparent; border: none; color: inherit; font: inherit; }
.mods__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--app-primary); background: transparent; color: var(--app-primary); }
</style>
