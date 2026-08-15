<script setup lang="ts">
/**
 * NodeOpsRow — 节点操作行（V3.3-OBS OBS-FE2，26号 §2/§6、25号 §4）
 *
 * 一个节点的内联操作：[测试]（同步 probe）+ [强制启用]（emergency-repair
 * force_enable，≤30s 候选缓存失效后回到可用集）+ [手工禁用▼] 下拉
 * （手工禁用 / 清除熔断 / 重置错误，均为 emergency-repair 动作）。
 *
 * 端点：
 *   - POST /api/admin/providers/{provider_id}/test-now（限频 1/s/凭据）
 *   - PATCH /api/routing/emergency-repair（super_admin，审计）
 *     raw_model 传空 = 整凭据修复（2026-08-15 后端已支持）。
 *
 * 状态展示只读 SSE node_update 投影（BE4 的 disable_kind /
 * system_recover_at），操作成功后靠 ≤2s 的 node_update tick 对账回真，
 * 不在前端造第二状态机。
 *
 * 设计约束：颜色只用 var(--kx-*)；操作有 loading/成功/失败反馈；
 * 破坏性动作（手工禁用）需 confirm。
 */
import { computed, ref } from 'vue'
import { type LiveNodeStatus } from '../composables/liveStreamStore'
import { authBearer } from '../store'
import { emergencyRepair, type EmergencyRepairAction } from '../api/routing'

const props = defineProps<{
  node: LiveNodeStatus
}>()

// ── 操作状态 ────────────────────────────────────────────────────────────────
const testing = ref(false)
const repairing = ref(false)
const menuOpen = ref(false)
const testLatencyMs = ref<number | null>(null)
const opMessage = ref('')
const opError = ref('')

const busy = computed(() => testing.value || repairing.value)

// 手工禁用下拉项：28号 OBS-FE2 提示词规定的三动作。
const menuActions: Array<{ action: EmergencyRepairAction; label: string; confirm?: string }> = [
  {
    action: 'force_disable',
    label: '手工禁用',
    confirm: `确认手工禁用节点 ${props.node.credential_id}？禁用后立即从路由候选摘除。`,
  },
  { action: 'clear_circuit', label: '清除熔断' },
  { action: 'reset_errors', label: '重置错误' },
]

function resetFeedback() {
  opMessage.value = ''
  opError.value = ''
  testLatencyMs.value = null
}

// ── 测试（同步 probe） ──────────────────────────────────────────────────────
async function testNow() {
  if (busy.value || !props.node.provider_id) return
  testing.value = true
  resetFeedback()
  try {
    const resp = await fetch(`/api/admin/providers/${props.node.provider_id}/test-now`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${authBearer()}` },
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const body = await resp.json()
    if (typeof body?.latency_ms === 'number') {
      testLatencyMs.value = body.latency_ms
    } else {
      opMessage.value = body?.status ? `测试完成：${body.status}` : '测试完成'
    }
  } catch (e) {
    opError.value = (e as Error)?.message || '测试失败'
  } finally {
    testing.value = false
  }
}

// ── emergency-repair（强制启用 / 下拉三动作） ────────────────────────────────
async function repair(action: EmergencyRepairAction, reason: string) {
  if (busy.value) return
  repairing.value = true
  resetFeedback()
  try {
    await emergencyRepair({
      credential_id: props.node.credential_id,
      raw_model: '',
      action,
      reason,
    })
    opMessage.value = action === 'force_enable'
      ? '已强制启用（≤30s 候选缓存失效后生效）'
      : '操作已提交（等待 node_update 对账）'
  } catch (e) {
    opError.value = (e as Error)?.message || '操作失败'
  } finally {
    repairing.value = false
    menuOpen.value = false
  }
}

function forceEnable() {
  void repair('force_enable', 'force_enable from queue perspective node ops row')
}

function runMenuAction(item: (typeof menuActions)[number]) {
  if (item.confirm && !confirm(item.confirm)) {
    menuOpen.value = false
    return
  }
  void repair(item.action, `${item.action} from queue perspective node ops row`)
}

function toggleMenu() {
  menuOpen.value = !menuOpen.value
}

// ── 只读状态投影（BE4 字段，缺省不渲染） ────────────────────────────────────
const disableKindLabel = computed<string | null>(() => {
  if (props.node.disable_kind === 'manual') return '手工禁用'
  if (props.node.disable_kind === 'system') return '系统降级'
  return null
})

function recoverAtLabel(iso?: string): string {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`
}

const providerLabel = computed(() =>
  props.node.provider_code || (props.node.provider_id ? `P${props.node.provider_id}` : '—'),
)

// ── 状态徽标组（26号 §2：circuit/availability/quota/manual 四态点） ──────────
// 只读 SSE 投影字段，缺省按"未知"灰点展示（不冒充健康）。
function dotClass(ok: boolean | null): string {
  if (ok === null) return 'nor-dot--unknown'
  return ok ? 'nor-dot--ok' : 'nor-dot--bad'
}

const circuitOk = computed<boolean | null>(() =>
  props.node.circuit_state ? props.node.circuit_state === 'closed' : null,
)
const availabilityOk = computed<boolean | null>(() =>
  props.node.availability_state
    ? props.node.availability_state === 'ready' || props.node.availability_state === 'active'
    : null,
)
const quotaOk = computed<boolean | null>(() =>
  props.node.quota_state ? props.node.quota_state === 'ok' : null,
)

const lastErrorShort = computed<string | null>(() => {
  const e = props.node.last_error
  if (!e) return null
  return e.length > 24 ? `${e.slice(0, 24)}…` : e
})
</script>

<template>
  <div class="node-ops-row" :class="{ 'node-ops-row--disabled': node.manual_disabled }">
    <span class="nor-id" :title="`credential ${node.credential_id}`">节点 {{ node.credential_id }}</span>
    <span class="nor-provider">{{ providerLabel }}</span>
    <!-- 状态徽标组：circuit/availability/quota/manual（26号 §2），缺省=未知灰点 -->
    <span class="nor-dots" :title="`熔断 ${node.circuit_state || '未知'} · 可用性 ${node.availability_state || '未知'} · 配额 ${node.quota_state || '未知'}${node.manual_disabled ? ' · 手工禁用' : ''}`">
      <i class="nor-dot" :class="dotClass(circuitOk)" aria-hidden="true" />
      <i class="nor-dot" :class="dotClass(availabilityOk)" aria-hidden="true" />
      <i class="nor-dot" :class="dotClass(quotaOk)" aria-hidden="true" />
      <i class="nor-dot" :class="node.manual_disabled ? 'nor-dot--warn' : dotClass(null)" aria-hidden="true" />
    </span>
    <span v-if="typeof node.last_latency_ms === 'number'" class="nor-latency" :title="`最近延迟 ${node.last_latency_ms}ms`">{{ node.last_latency_ms }}ms</span>
    <span v-if="lastErrorShort" class="nor-last-error" :title="node.last_error">{{ lastErrorShort }}</span>
    <span v-if="disableKindLabel" class="nor-disable-kind" :class="`nor-disable-kind--${node.disable_kind}`">
      {{ disableKindLabel }}
    </span>
    <span
      v-if="node.fp_disabled_until && recoverAtLabel(node.fp_disabled_until)"
      class="nor-recover"
      :title="`fpslot 禁用截止 ${node.fp_disabled_until}`"
    >禁用至 {{ recoverAtLabel(node.fp_disabled_until) }}</span>
    <span
      v-if="node.system_recover_at && recoverAtLabel(node.system_recover_at)"
      class="nor-recover"
      :title="`系统恢复预计 ${node.system_recover_at}`"
    >恢复 {{ recoverAtLabel(node.system_recover_at) }}</span>
    <span v-if="node.in_flight" class="nor-inflight">在途 {{ node.in_flight }}</span>

    <span class="nor-ops">
      <button
        type="button"
        class="nor-btn"
        :disabled="busy"
        @click.stop="testNow"
      >{{ testing ? '测试中…' : '测试' }}</button>
      <button
        type="button"
        class="nor-btn nor-btn--primary"
        :disabled="busy"
        title="清除禁用/降级/熔断状态，≤30s 候选缓存失效后回到可用集"
        @click.stop="forceEnable"
      >{{ repairing ? '执行中…' : '强制启用' }}</button>
      <span class="nor-menu">
        <button
          type="button"
          class="nor-btn"
          :disabled="busy"
          :aria-expanded="menuOpen"
          aria-haspopup="menu"
          @click.stop="toggleMenu"
        >手工禁用 ▼</button>
        <span v-if="menuOpen" class="nor-menu-items" role="menu">
          <!-- 26号 §2：下拉里区分 手工禁用（需人工恢复）与 系统降级说明（预计自动恢复时间） -->
          <span
            v-if="node.disable_kind === 'system' && node.system_recover_at && recoverAtLabel(node.system_recover_at)"
            class="nor-menu-note"
            role="menuitem"
            aria-disabled="true"
          >系统降级 · 预计恢复 {{ recoverAtLabel(node.system_recover_at) }}</span>
          <button
            v-for="item in menuActions"
            :key="item.action"
            type="button"
            class="nor-menu-item"
            role="menuitem"
            :disabled="busy"
            @click.stop="runMenuAction(item)"
          >{{ item.label }}</button>
        </span>
      </span>
    </span>

    <span v-if="testLatencyMs !== null" class="nor-feedback nor-feedback--ok">{{ testLatencyMs }}ms</span>
    <span v-else-if="opMessage" class="nor-feedback nor-feedback--ok">{{ opMessage }}</span>
    <span v-else-if="opError" class="nor-feedback nor-feedback--err">{{ opError }}</span>
  </div>
</template>

<style scoped>
.node-ops-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 10px;
  padding: 4px 0;
  font-size: 12px;
  border-bottom: 1px solid color-mix(in srgb, var(--kx-border) 50%, transparent);
}
.node-ops-row:last-child {
  border-bottom: none;
}
.node-ops-row--disabled .nor-id {
  color: var(--kx-muted, var(--kx-text));
}
.nor-id {
  font-weight: 600;
  color: var(--kx-text);
  min-width: 72px;
}
/* 状态徽标组：四点 = 熔断/可用性/配额/手工，缺省未知灰点 */
.nor-dots {
  display: inline-flex;
  gap: 3px;
  align-items: center;
}
.nor-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--kx-muted, var(--kx-text));
}
.nor-dot--ok {
  background: var(--kx-success);
}
.nor-dot--bad {
  background: var(--kx-danger);
}
.nor-dot--warn {
  background: var(--kx-warning);
}
.nor-latency {
  color: var(--kx-muted, var(--kx-text));
  font-variant-numeric: tabular-nums;
}
.nor-last-error {
  color: var(--kx-danger);
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nor-provider {
  color: var(--kx-muted, var(--kx-text));
  min-width: 64px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nor-disable-kind {
  padding: 1px 6px;
  border-radius: 8px;
  font-size: 11px;
}
.nor-disable-kind--manual {
  background: color-mix(in srgb, var(--kx-warning) 14%, var(--kx-surface));
  color: var(--kx-warning);
}
.nor-disable-kind--system {
  background: color-mix(in srgb, var(--kx-danger) 12%, var(--kx-surface));
  color: var(--kx-danger);
}
.nor-recover {
  color: var(--kx-muted, var(--kx-text));
  font-variant-numeric: tabular-nums;
}
.nor-inflight {
  color: var(--kx-muted, var(--kx-text));
}
.nor-ops {
  display: inline-flex;
  gap: 6px;
  margin-left: auto;
}
.nor-btn {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: var(--kx-radius-sm, 6px);
  border: 1px solid var(--kx-border);
  background: var(--kx-surface);
  color: var(--kx-text);
  cursor: pointer;
}
.nor-btn:hover:not(:disabled) {
  border-color: var(--kx-primary);
}
.nor-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.nor-btn--primary {
  border-color: var(--kx-primary);
  color: var(--kx-primary);
}
.nor-menu {
  position: relative;
  display: inline-flex;
}
.nor-menu-items {
  position: absolute;
  top: 100%;
  right: 0;
  z-index: 100;
  display: flex;
  flex-direction: column;
  min-width: 96px;
  padding: 4px;
  border-radius: var(--kx-radius-sm, 6px);
  border: 1px solid var(--kx-border);
  background: var(--kx-surface);
  box-shadow: var(--kx-shadow-md);
}
.nor-menu-note {
  font-size: 11px;
  padding: 4px 8px;
  color: var(--kx-warning);
  cursor: default;
}
.nor-menu-item {
  font-size: 12px;
  padding: 4px 8px;
  border: none;
  border-radius: var(--kx-radius-sm, 6px);
  background: transparent;
  color: var(--kx-text);
  text-align: left;
  cursor: pointer;
}
.nor-menu-item:hover:not(:disabled) {
  background: color-mix(in srgb, var(--kx-primary) 10%, var(--kx-surface));
}
.nor-menu-item:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.nor-feedback {
  font-size: 11px;
  width: 100%;
}
.nor-feedback--ok {
  color: var(--kx-success);
}
.nor-feedback--err {
  color: var(--kx-danger);
}
</style>
