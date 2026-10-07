<script setup lang="ts">
// TenantsView — 租户名录（**superAdmin 档**）。
//
// GET /api/admin/tenants?status=
//
// ⚠️⚠️⚠️ 这条端点返回的是**裸数组**，不是 `{items: […]}` —— 与 MaaS 族和本仓
// 多数 admin 端点都不同（详见 api/tenants.ts 头）。
//
// ★★ 本页最要紧的一件事：**7 天用量这一列可能不可信**。
// 后端 `attachTenantUsage7d` 有**独立的 1.5s 预算**（主查询 5s 给名单），
// 查询失败/超时时 `slog.Warn` 然后**直接 return**，四个字段留在 0 ——
// 客户端分辨不出「这个租户真没用量」与「富化没在 1.5s 内跑完」。
// 2026-10-03 的注释记录了根因：这条聚合在真实数据量下要 20s+。
//
// ★ 另有 7 个聚合键都带 `omitempty` ⇒ 真的为 0 时**键整个不存在**。
//
// ★ 写操作本页一律不碰（创建租户 PATCH、model-policies 增删改）。

import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchTenants,
  tenantUsageMayBeDegraded,
  type TenantInfo,
} from '@/api/tenants'

useHyperPage({ title: () => t('tn.title') })

const router = useRouter()

const tenants = ref<TenantInfo[] | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)
const keyword = ref('')
const statusFilter = ref('')

/** ★★ 本次列表里有几行的 7 天用量**读数不可信**。 */
const degradedCount = computed(() => (tenants.value ?? []).filter(tenantUsageMayBeDegraded).length)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    tenants.value = await fetchTenants(statusFilter.value ? { status: statusFilter.value } : {})
  } catch (e) {
    // ★★ 抛错不许退化成空名单
    tenants.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

const rows = computed<TenantInfo[]>(() => {
  const all = tenants.value ?? []
  const q = keyword.value.trim().toLowerCase()
  if (!q) return all
  return all.filter(
    (x) => x.code.toLowerCase().includes(q) || x.name.toLowerCase().includes(q) || x.status.toLowerCase().includes(q),
  )
})

function statusTone(s: string): 'success' | 'warning' | 'danger' | 'muted' {
  switch (s) {
    case 'active':
      return 'success'
    case 'suspended':
      return 'warning'
    case 'disabled':
      return 'muted'
    default:
      // ★ 枚举外的值不猜
      return 'danger'
  }
}

function openDetail(x: TenantInfo): void {
  void router.push({ path: `/tenant-detail/${encodeURIComponent(x.code)}` })
}

void load()

// ★ 状态筛选是**服务端筛选**（`?status=`），不是本地过滤 ⇒ 必须重新取数。
//   漏了这个 watch，那个分段控件就是死的：点了不发请求、列表也不变。
watch(statusFilter, () => {
  void load()
})

onBeforeUnmount(() => {
  tenants.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="tn">
    <section v-if="error" class="tn__panel">
      <p class="tn__msg tn__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="tn__note tn__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="tn__msg">{{ t('common.loading') }}</p>

    <template v-if="tenants">
      <section class="tn__panel">
        <span class="tn__panel-title">{{ t('tn.tenants') }}</span>

        <!-- ★★★ 头号问题：这一列可能不可信，必须说破 -->
        <p v-if="degradedCount > 0" class="tn__note tn__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('tn.degradedNote', { n: degradedCount }) }}</span>
        </p>
        <p v-else class="tn__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('tn.usageOkNote') }}</span>
        </p>

        <p class="tn__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('tn.bareArrayNote') }}</span>
        </p>

        <label class="tn__label" for="tn-status">{{ t('tn.statusFilter') }}</label>
        <div id="tn-status" class="tn__seg" role="group" :aria-label="t('tn.statusFilter')">
          <button
            v-for="s in ['', 'active', 'suspended']"
            :key="s || 'all'"
            type="button"
            class="tn__seg-btn"
            :class="{ 'tn__seg-btn--on': statusFilter === s }"
            :aria-pressed="statusFilter === s"
            @click="statusFilter = s"
          >
            {{ s || t('tn.all') }}
          </button>
        </div>

        <label class="tn__label" for="tn-kw">{{ t('tn.search') }}</label>
        <input
          id="tn-kw"
          v-model="keyword"
          class="tn__input"
          type="search"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          :placeholder="t('tn.searchPlaceholder')"
        />

        <p v-if="!rows.length" class="tn__msg">{{ t('tn.empty') }}</p>

        <ul v-if="rows.length" class="tn__list">
          <li v-for="x in rows" :key="x.code" class="tn__item">
            <button type="button" class="tn__open" @click="openDetail(x)">
              <div class="tn__item-head">
                <span class="tn__title">{{ x.name }}</span>
                <span class="tn__badge">
                  <StatusDot :tone="statusTone(x.status)" />
                  <span class="tn__badge-t">{{ x.status }}</span>
                </span>
              </div>
              <p class="tn__meta">{{ x.code }}</p>
              <p class="tn__meta">{{ t('tn.updated') }}: {{ relativeTime(x.updated_at) }}</p>

              <div class="tn__grid">
                <span class="tn__cell">
                  <span class="tn__cell-l">{{ t('tn.users') }}</span>
                  <span class="tn__cell-v">{{ x.user_count ?? 0 }}</span>
                </span>
                <span class="tn__cell">
                  <span class="tn__cell-l">{{ t('tn.keys') }}</span>
                  <span class="tn__cell-v">{{ x.api_key_count ?? 0 }}</span>
                </span>
                <span class="tn__cell">
                  <span class="tn__cell-l">{{ t('tn.requests7d') }}</span>
                  <span class="tn__cell-v">{{ x.requests_7d ?? 0 }}</span>
                </span>
                <span class="tn__cell">
                  <span class="tn__cell-l">{{ t('tn.credits7d') }}</span>
                  <span class="tn__cell-v">{{ x.credits_7d ?? 0 }}</span>
                </span>
              </div>

              <!-- ★★ 这一格的 0 与「富化超时」不可区分，必须打标 -->
              <p v-if="tenantUsageMayBeDegraded(x)" class="tn__note tn__note--warn">
                <AppIcon name="alert" :size="13" />
                <span>{{ t('tn.rowDegradedNote') }}</span>
              </p>
            </button>
          </li>
        </ul>
      </section>

      <p class="tn__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('tn.readOnlyNote') }}</span>
      </p>
    </template>
  </div>
</template>

<style scoped>
.tn__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.tn__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.tn__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.tn__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--app-border); border-radius: 8px; background: transparent; color: inherit; }
.tn__seg { display: flex; gap: 6px; margin-bottom: 8px; }
.tn__seg-btn { flex: 1; min-height: 48px; font-size: 13px; border-radius: 8px;
  border: 1px solid var(--app-border); background: transparent; color: inherit; }
.tn__seg-btn--on { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }
.tn__open { display: block; width: 100%; text-align: left; background: transparent; color: inherit;
  border: 0; border-top: 1px solid var(--app-border); padding: 10px 0; font: inherit; min-height: 48px; }

.tn__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.tn__msg--err { color: var(--app-danger); }
.tn__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.tn__note--warn { color: var(--app-warning); }
.tn__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.tn__cell { display: flex; flex-direction: column; }
.tn__cell-l { font-size: 11px; color: var(--app-text-muted); }
.tn__cell-v { font-size: 14px; font-weight: 600; }

.tn__list { list-style: none; margin: 0; padding: 0; }
.tn__item { padding: 0; }
.tn__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.tn__title { font-size: 14px; font-weight: 600; }
.tn__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.tn__badge-t { color: var(--app-text-secondary); }
.tn__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
</style>