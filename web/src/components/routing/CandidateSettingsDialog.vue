<script setup lang="ts">
import { reactive, ref, computed } from 'vue'
import { isSuperAdmin } from '../../store'
import { patchCandidateBinding, emergencyRepair, type RoutingCandidate, type EmergencyRepairAction } from '../../api/routing'
import {
  CREDENTIAL_LIFECYCLE_STATUSES,
  updateCredentialLifecycle,
  type CredentialLifecycleStatus,
} from '../../api/providers'
import { setCredentialManualDisabled } from '../../api/provider-probe'

const props = defineProps<{
  candidate: RoutingCandidate
}>()

const emit = defineEmits<{
  close: []
  /** 父组件收到 applied 后会立即用新值刷新候选列表；本组件保留乐观更新 + 失败回滚语义。 */
  applied: [payload: { credential_id: number; raw_model: string }]
}>()

const saving = ref(false)
const activeTab = ref<'settings' | 'emergency'>('settings')
const settingsMsg = ref('')
const settingsMsgKind = ref<'ok' | 'err' | 'warn'>('ok')

const form = reactive({
  manual_priority: props.candidate.manual_priority ?? 99,
  routing_tier: props.candidate.tier ?? 2,
  weight: props.candidate.weight ?? 100,
  manual_disabled: false,
  lifecycle_status: (props.candidate.lifecycle_status || 'active') as CredentialLifecycleStatus,
})

// Emergency repair state — confirm stays inside this dialog (no ElMessageBox:
// project has no Element Plus CSS, so MessageBox rendered unstyled behind cs-overlay).
const emergencyRepairing = ref<EmergencyRepairAction | null>(null)
const emergencyErr = ref('')
const emergencyOk = ref('')
const pendingRepair = ref<{ action: EmergencyRepairAction; label: string } | null>(null)

// 乐观更新快照；保存失败时回滚到这里的值。
const prev = {
  manual_priority: form.manual_priority,
  routing_tier: form.routing_tier,
  weight: form.weight,
}

const canEdit = computed(() => isSuperAdmin())

const lifecycleLabels: Record<CredentialLifecycleStatus, string> = {
  active: 'active（在用）',
  disabled: 'disabled（停用）',
  suspended: 'suspended（暂停）',
  retired: 'retired（退役）',
}

const lifecycleOptions = CREDENTIAL_LIFECYCLE_STATUSES.map((value) => ({
  value,
  label: lifecycleLabels[value],
}))

// Emergency repair actions availability based on current state.
//
// canForceEnable is gated on the credential being STRUCTURALLY active
// (status/lifecycle active, in-effect, not expired) — force_enable resets
// RUNTIME blocks (manual_disabled / availability_state / circuit / quota /
// broken_confirmed) without changing structure, so it is only meaningful on a
// structurally-active node. It does NOT require a runtime block to be present:
// an admin may still click it to clear a stale broken_confirmed that resolve
// surfaces (2026-08-13: force_enable now also resets model_probe_state). When
// the node is already fully healthy, confirmEmergencyRepair short-circuits to
// a no-op message instead of silently resetting counters.
const canForceEnable = computed(() => {
  const c = props.candidate
  const inEffect = (c.effective_at == null) ||
    new Date(c.effective_at).getTime() <= Date.now()
  const notExpired = (c.expires_at == null) ||
    new Date(c.expires_at).getTime() > Date.now()
  return c.credential_status === 'active' &&
    c.lifecycle_status === 'active' &&
    inEffect &&
    notExpired
})

// hasRuntimeBlock reports whether any runtime gate currently makes the node
// non-routable or degraded (so force_enable would actually do something).
const hasRuntimeBlock = computed(() => {
  const c = props.candidate
  if (!c.routable) return true
  if (c.circuit_state && c.circuit_state !== 'closed') return true
  if (c.availability_state && c.availability_state !== 'ready') return true
  if (c.quota_state && c.quota_state !== 'ok') return true
  return false
})

const canForceDisable = computed(() => {
  const c = props.candidate
  return c.credential_status === 'active' &&
    c.lifecycle_status === 'active'
})

const canClearCircuit = computed(() =>
  props.candidate.circuit_state === 'open' || props.candidate.circuit_state === 'half_open'
)

// R7 fix: source authoritative value from credential-level field.
// resolve returns both cmb.consecutive_failures and c.consecutive_failures;
// the reset_errors operation only touches the credential-level column.
const canResetErrors = computed(() =>
  (props.candidate.credential_consecutive_failures ?? 0) > 0
)

function requestEmergencyRepair(action: EmergencyRepairAction, label: string) {
  emergencyErr.value = ''
  emergencyOk.value = ''
  pendingRepair.value = { action, label }
  activeTab.value = 'emergency'
}

function cancelPendingRepair() {
  if (emergencyRepairing.value) return
  pendingRepair.value = null
}

async function confirmEmergencyRepair() {
  const pending = pendingRepair.value
  if (!pending || emergencyRepairing.value) return

  emergencyErr.value = ''
  emergencyOk.value = ''
  // 2026-08-13: force_enable on an already-healthy node is a no-op — surface
  // that to the operator instead of silently resetting counters/timestamps.
  if (pending.action === 'force_enable' && !hasRuntimeBlock.value) {
    emergencyOk.value = '节点当前已健康（无运行态阻塞），无需强制启用'
    pendingRepair.value = null
    return
  }
  emergencyRepairing.value = pending.action
  try {
    const result = await emergencyRepair({
      credential_id: props.candidate.credential_id,
      raw_model: props.candidate.model_name,
      action: pending.action,
      reason: `admin via routing-v2 resolve dialog: ${pending.label}`,
    })
    const warnings: string[] = []
    if (pending.action === 'force_enable' && result.cmb_rows_updated === 0) {
      warnings.push('模型绑定未更新，请检查 raw model 映射')
    }
    if (pending.action === 'force_enable' && result.ursm_v2_cleared === false) {
      warnings.push('URSM 状态未清理，运行态可能仍需重试')
    }
    emergencyOk.value = warnings.length
      ? `「${pending.label}」已执行；${warnings.join('；')}`
      : `「${pending.label}」执行成功`
    pendingRepair.value = null
    emit('applied', { credential_id: props.candidate.credential_id, raw_model: props.candidate.model_name })
    window.setTimeout(() => emit('close'), 600)
  } catch (e: unknown) {
    emergencyErr.value = e instanceof Error ? e.message : '操作失败'
  } finally {
    emergencyRepairing.value = null
  }
}

async function save() {
  if (!canEdit.value) {
    settingsMsgKind.value = 'warn'
    settingsMsg.value = '仅系统管理员可以修改设置'
    return
  }
  saving.value = true
  settingsMsg.value = ''
  const touched: Array<{ ok: boolean; label: string }> = []
  try {
    const rawModel = props.candidate.model_name
    const bindingPatch: { manual_priority?: number; routing_tier?: number; weight?: number } = {}
    if (form.manual_priority !== prev.manual_priority) bindingPatch.manual_priority = form.manual_priority
    if (form.routing_tier !== prev.routing_tier) bindingPatch.routing_tier = form.routing_tier
    if (form.weight !== prev.weight) bindingPatch.weight = form.weight
    if (Object.keys(bindingPatch).length > 0) {
      // 乐观更新：先把 UI 改成期望值
      Object.assign(prev, bindingPatch)
      try {
        await patchCandidateBinding(props.candidate.credential_id, rawModel, bindingPatch)
        touched.push({ ok: true, label: '路由排序字段' })
      } catch (e) {
        // 回滚
        Object.assign(form, {
          manual_priority: prev.manual_priority,
          routing_tier: prev.routing_tier,
          weight: prev.weight,
        })
        // 把 prev 也复位，避免污染下次保存
        Object.assign(prev, {
          manual_priority: props.candidate.manual_priority ?? 99,
          routing_tier: props.candidate.tier ?? 2,
          weight: props.candidate.weight ?? 100,
        })
        Object.assign(form, {
          manual_priority: prev.manual_priority,
          routing_tier: prev.routing_tier,
          weight: prev.weight,
        })
        touched.push({ ok: false, label: `路由排序字段（${(e as Error).message}）` })
      }
    }
    if (form.manual_disabled) {
      try {
        await setCredentialManualDisabled(
          props.candidate.provider_id,
          props.candidate.credential_id,
          true,
          'admin via routing-v2 resolve',
        )
        touched.push({ ok: true, label: '人工停用' })
      } catch (e) {
        touched.push({ ok: false, label: `人工停用（${(e as Error).message}）` })
      }
    }
    if (form.lifecycle_status !== (props.candidate.lifecycle_status || 'active')) {
      try {
        await updateCredentialLifecycle(
          props.candidate.provider_id,
          props.candidate.credential_id,
          form.lifecycle_status,
        )
        touched.push({ ok: true, label: '生命周期' })
      } catch (e) {
        touched.push({ ok: false, label: `生命周期（${(e as Error).message}）` })
      }
    }
    const fails = touched.filter((t) => !t.ok)
    if (fails.length === 0) {
      settingsMsgKind.value = 'ok'
      settingsMsg.value = '设置已保存（部分字段刷新后生效）'
      emit('applied', { credential_id: props.candidate.credential_id, raw_model: props.candidate.model_name })
      window.setTimeout(() => emit('close'), 400)
    } else if (fails.length === touched.length) {
      settingsMsgKind.value = 'err'
      settingsMsg.value = `保存失败：${fails.map((f) => f.label).join('；')}`
    } else {
      settingsMsgKind.value = 'warn'
      settingsMsg.value = `部分保存失败：${fails.map((f) => f.label).join('；')}`
      emit('applied', { credential_id: props.candidate.credential_id, raw_model: props.candidate.model_name })
      window.setTimeout(() => emit('close'), 800)
    }
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div class="cs-overlay" @click.self="emit('close')">
      <aside class="cs-dialog" role="dialog" :aria-label="`设置凭据 #${candidate.credential_id}`">
        <header class="cs-head">
          <div>
            <h3>设置 — 凭据 #{{ candidate.credential_id }}</h3>
            <p class="cs-sub">{{ candidate.provider_name }} · {{ candidate.model_name }} · {{ candidate.credential_label }}</p>
          </div>
          <button class="cs-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
        </header>

        <!-- Tab navigation -->
        <nav class="cs-tabs">
          <button
            class="cs-tab"
            :class="{ active: activeTab === 'settings' }"
            @click="activeTab = 'settings'"
          >设置</button>
          <button
            class="cs-tab"
            :class="{ active: activeTab === 'emergency' }"
            @click="activeTab = 'emergency'"
          >⚠️ 紧急修复</button>
        </nav>

        <div class="cs-body">
          <p v-if="!canEdit" class="cs-warn">
            ⚠ 仅 super_admin 可见 / 可写；当前账号无权限，字段全部只读。
          </p>
          <p
            v-if="settingsMsg && activeTab === 'settings'"
            class="cs-settings-msg"
            :class="`cs-settings-msg--${settingsMsgKind}`"
          >{{ settingsMsg }}</p>

          <!-- Settings Tab -->
          <template v-if="activeTab === 'settings'">
            <fieldset class="cs-group" :disabled="!canEdit">
              <legend>路由排序（cmb · 影响排序，不绕过熔断 / 可用性）</legend>
              <label class="cs-row">
                <span>manual_priority <small>0–99，越小越优先</small></span>
                <input v-model.number="form.manual_priority" type="number" min="0" max="99" />
              </label>
              <label class="cs-row">
                <span>routing_tier <small>0–9，free tier 固定 9</small></span>
                <input v-model.number="form.routing_tier" type="number" min="0" max="9" />
              </label>
              <label class="cs-row">
                <span>weight <small>0–10000，同 tier 内权重</small></span>
                <input v-model.number="form.weight" type="number" min="0" max="10000" />
              </label>
            </fieldset>
            <fieldset class="cs-group" :disabled="!canEdit">
              <legend>凭据硬规则（已有专用端点）</legend>
              <label class="cs-row cs-row--inline">
                <input v-model="form.manual_disabled" type="checkbox" />
                <span>人工停用（manual_disabled=true 会立即将该凭据从可路由中剔除）</span>
              </label>
              <label class="cs-row">
                <span>lifecycle_status</span>
                <select v-model="form.lifecycle_status">
                  <option v-for="opt in lifecycleOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                </select>
              </label>
              <p class="cs-hint">说明：lifecycle_status 会通过 <code>PATCH /api/providers/:id/credentials/:cid/lifecycle</code> 写入；manual_disabled 会通过 <code>PATCH /api/providers/:id/credentials/:cid/manual-disabled</code> 写入。两者均产生 routing_audit_log 审计行。</p>
            </fieldset>
          </template>

          <!-- Emergency Repair Tab -->
          <template v-if="activeTab === 'emergency'">
            <div class="cs-emergency-intro">
              <p class="cs-emergency-warning">⚠️ 紧急修复操作会绕过正常的状态检测逻辑，请确认您知道在做什么。</p>
              <p class="cs-emergency-info">当前状态：</p>
              <ul class="cs-emergency-state">
                <li>凭据状态: <strong>{{ candidate.credential_status }}</strong></li>
                <li>生命周期: <strong>{{ candidate.lifecycle_status || '—' }}</strong></li>
                <li>熔断状态: <strong :class="{ 'text-danger': candidate.circuit_state === 'open' || candidate.circuit_state === 'half_open' }">{{ candidate.circuit_state || 'closed' }}</strong></li>
                <li>连续失败: <strong :class="{ 'text-danger': (candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0) > 0 }">{{ candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0 }} 次</strong></li>
              </ul>
            </div>

            <div v-if="pendingRepair" class="cs-confirm-panel" role="alertdialog" aria-labelledby="cs-confirm-title">
              <p id="cs-confirm-title" class="cs-confirm-title">确认执行「{{ pendingRepair.label }}」？</p>
              <p class="cs-confirm-body">
                凭据 ID: {{ candidate.credential_id }} · 模型: {{ candidate.model_name }}
                <br />此操作会绕过正常状态检测逻辑，请确认您知道在做什么。
              </p>
              <div class="cs-confirm-actions">
                <button type="button" class="btn btn-ghost" :disabled="emergencyRepairing !== null" @click="cancelPendingRepair">取消</button>
                <button type="button" class="btn btn-danger" :disabled="emergencyRepairing !== null" @click="confirmEmergencyRepair">
                  {{ emergencyRepairing ? '处理中…' : '确认执行' }}
                </button>
              </div>
            </div>

            <div class="cs-emergency-actions" :class="{ 'cs-emergency-actions--dimmed': !!pendingRepair }">
              <!-- Force Disable -->
              <div class="cs-emergency-card cs-emergency-card--danger" :class="{ disabled: !canForceDisable || emergencyRepairing !== null || !!pendingRepair }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🔴</span>
                  <span class="cs-emergency-title">强制禁用</span>
                </div>
                <p class="cs-emergency-desc">立即将此节点标记为不可用，绕过熔断器。将 <code>manual_disabled=true</code> 写入数据库。</p>
                <div class="cs-emergency-meta">当前凭据状态: {{ candidate.credential_status }}</div>
                <button
                  class="btn btn-danger btn-sm"
                  :disabled="!canForceDisable || emergencyRepairing !== null || !!pendingRepair"
                  @click="requestEmergencyRepair('force_disable', '强制禁用')"
                >
                  强制禁用
                </button>
              </div>

              <!-- Force Enable -->
              <div class="cs-emergency-card cs-emergency-card--success" :class="{ disabled: !canForceEnable || emergencyRepairing !== null || !!pendingRepair }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🟢</span>
                  <span class="cs-emergency-title">强制启用</span>
                </div>
                <p class="cs-emergency-desc">强制启用：清除 <code>manual_disabled</code>、重置 availability/circuit、清空 <code>node_probe</code> 退避，并恢复该模型 binding 可用，使节点重新进入可路由。</p>
                <div class="cs-emergency-meta">当前凭据状态: {{ candidate.credential_status }}</div>
                <button
                  class="btn btn-success btn-sm"
                  :disabled="!canForceEnable || emergencyRepairing !== null || !!pendingRepair"
                  @click="requestEmergencyRepair('force_enable', '强制启用')"
                >
                  强制启用
                </button>
              </div>

              <!-- Clear Circuit -->
              <div class="cs-emergency-card cs-emergency-card--warning" :class="{ disabled: !canClearCircuit || emergencyRepairing !== null || !!pendingRepair }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🟡</span>
                  <span class="cs-emergency-title">清除熔断状态</span>
                </div>
                <p class="cs-emergency-desc">将 <code>OPEN/HALF_OPEN</code> 熔断状态重置为 <code>CLOSED</code>，清除冷却计时。</p>
                <div class="cs-emergency-meta">当前熔断状态: <strong>{{ candidate.circuit_state || 'closed' }}</strong></div>
                <button
                  class="btn btn-warning btn-sm"
                  :disabled="!canClearCircuit || emergencyRepairing !== null || !!pendingRepair"
                  @click="requestEmergencyRepair('clear_circuit', '清除熔断状态')"
                >
                  清除熔断
                </button>
              </div>

              <!-- Reset Errors -->
              <div class="cs-emergency-card cs-emergency-card--info" :class="{ disabled: !canResetErrors || emergencyRepairing !== null || !!pendingRepair }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🔵</span>
                  <span class="cs-emergency-title">重置错误计数</span>
                </div>
                <p class="cs-emergency-desc">清除连续失败计数，让节点脱离 unhealthy 状态。将 <code>consecutive_failures=0</code>。</p>
                <div class="cs-emergency-meta">当前连续失败: <strong>{{ candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0 }} 次</strong></div>
                <button
                  class="btn btn-info btn-sm"
                  :disabled="!canResetErrors || emergencyRepairing !== null || !!pendingRepair"
                  @click="requestEmergencyRepair('reset_errors', '重置错误计数')"
                >
                  重置计数
                </button>
              </div>
            </div>

            <p v-if="emergencyOk" class="cs-emergency-ok">{{ emergencyOk }}</p>
            <p v-if="emergencyErr" class="cs-emergency-err">{{ emergencyErr }}</p>
          </template>
        </div>

        <footer class="cs-foot">
          <button type="button" class="btn btn-ghost" @click="emit('close')">取消</button>
          <button v-if="activeTab === 'settings'" type="button" class="btn btn-primary" :disabled="!canEdit || saving" @click="save">
            {{ saving ? '保存中…' : '保存' }}
          </button>
        </footer>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.cs-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.28);
  z-index: 9999;
  display: flex;
  align-items: center;
  justify-content: center;
}
.cs-settings-msg {
  margin: 0 0 10px;
  padding: 6px 10px;
  border-radius: 4px;
  font-size: 12px;
  border-left: 3px solid transparent;
}
.cs-settings-msg--ok {
  background: rgba(22, 163, 74, 0.1);
  border-left-color: var(--kx-success);
  color: var(--kx-text);
}
.cs-settings-msg--err {
  background: rgba(220, 38, 38, 0.1);
  border-left-color: var(--kx-danger);
  color: var(--kx-danger);
}
.cs-settings-msg--warn {
  background: var(--kx-warning-soft, rgba(217, 119, 6, 0.12));
  border-left-color: var(--kx-warning);
  color: var(--kx-text);
}
.cs-confirm-panel {
  position: fixed;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  z-index: 10000;
  width: min(420px, 88vw);
  padding: 16px;
  border-radius: 8px;
  border: 1px solid var(--kx-danger);
  background: var(--kx-surface);
  box-shadow: 0 12px 36px rgba(0, 0, 0, 0.35);
}
.cs-confirm-title {
  margin: 0 0 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--kx-text);
}
.cs-confirm-body {
  margin: 0 0 10px;
  font-size: 11px;
  line-height: 1.55;
  color: var(--kx-muted);
}
.cs-confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.cs-emergency-actions--dimmed {
  opacity: 0.45;
  pointer-events: none;
}
.cs-emergency-ok {
  margin: 12px 0 0;
  padding: 6px 10px;
  background: rgba(22, 163, 74, 0.1);
  border-left: 3px solid var(--kx-success);
  border-radius: 4px;
  font-size: 11px;
  color: var(--kx-text);
}
.cs-dialog {
  width: min(560px, 92vw);
  max-height: 86vh;
  background: var(--kx-surface);
  color: var(--kx-text);
  border-radius: 8px;
  border: 1px solid var(--kx-border);
  box-shadow: 0 20px 48px rgba(0, 0, 0, 0.22);
  display: flex;
  flex-direction: column;
}
.cs-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--kx-border);
}
.cs-head h3 {
  margin: 0;
  font-size: 15px;
}
.cs-sub {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--kx-muted);
}
.cs-close {
  margin-left: auto;
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  color: var(--kx-muted);
}
.cs-tabs {
  display: flex;
  gap: 0;
  padding: 0 16px;
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface-soft);
}
.cs-tab {
  padding: 8px 16px;
  border: none;
  background: transparent;
  font-size: 12px;
  color: var(--kx-muted);
  cursor: pointer;
  border-bottom: 2px solid transparent;
  transition: all .15s;
}
.cs-tab:hover {
  color: var(--kx-text);
}
.cs-tab.active {
  color: var(--kx-primary);
  border-bottom-color: var(--kx-primary);
  font-weight: 600;
}
.cs-body {
  overflow-y: auto;
  padding: 14px 16px;
  flex: 1;
}
.cs-warn {
  margin: 0 0 10px;
  padding: 6px 10px;
  background: var(--kx-warning-soft, rgba(217, 119, 6, 0.12));
  border-left: 3px solid var(--kx-warning);
  color: var(--kx-text);
  font-size: 12px;
  border-radius: 4px;
}
.cs-group {
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  padding: 8px 12px;
  margin-bottom: 12px;
  background: var(--kx-surface-soft);
}
.cs-group legend {
  font-size: 11px;
  color: var(--kx-muted);
  padding: 0 6px;
}
.cs-row {
  display: grid;
  grid-template-columns: 1fr 110px;
  gap: 8px;
  padding: 6px 0;
  align-items: center;
}
.cs-row small {
  display: block;
  font-size: 10.5px;
  color: var(--kx-muted);
  font-weight: 400;
}
.cs-row span {
  font-size: 12px;
}
.cs-row--inline {
  grid-template-columns: auto 1fr;
  gap: 8px;
}
.cs-row input,
.cs-row select {
  padding: 4px 6px;
  font-size: 12px;
  border: 1px solid var(--kx-border);
  border-radius: 4px;
  background: var(--kx-surface);
  color: var(--kx-text);
}
.cs-hint {
  margin: 6px 0 0;
  font-size: 11px;
  color: var(--kx-muted);
  line-height: 1.55;
}
.cs-hint code {
  font-size: 10.5px;
  background: var(--kx-bg);
  padding: 1px 4px;
  border-radius: 3px;
}
.cs-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 16px;
  border-top: 1px solid var(--kx-border);
}
.btn {
  padding: 6px 12px;
  font-size: 12px;
  border-radius: 4px;
  cursor: pointer;
  border: 1px solid transparent;
}
.btn-primary {
  background: var(--kx-primary);
  color: #fff;
  border-color: var(--kx-primary);
}
.btn-primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.btn-ghost {
  background: transparent;
  color: var(--kx-text);
  border-color: var(--kx-border);
}
.btn-danger {
  background: var(--kx-danger);
  color: #fff;
  border-color: var(--kx-danger);
}
.btn-danger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-success {
  background: var(--kx-success);
  color: #fff;
  border-color: var(--kx-success);
}
.btn-success:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-warning {
  background: #d97706;
  color: #fff;
  border-color: #d97706;
}
.btn-warning:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-info {
  background: #2563eb;
  color: #fff;
  border-color: #2563eb;
}
.btn-info:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Emergency repair styles */
.cs-emergency-intro {
  margin-bottom: 16px;
}
.cs-emergency-warning {
  margin: 0 0 12px;
  padding: 8px 12px;
  background: rgba(220, 38, 38, 0.1);
  border-left: 3px solid var(--kx-danger);
  border-radius: 4px;
  font-size: 12px;
  color: var(--kx-text);
}
.cs-emergency-info {
  margin: 0 0 6px;
  font-size: 11px;
  color: var(--kx-muted);
}
.cs-emergency-state {
  margin: 0;
  padding-left: 20px;
  font-size: 11px;
  color: var(--kx-text);
}
.cs-emergency-state li {
  margin-bottom: 4px;
}
.cs-emergency-state strong {
  font-weight: 600;
}
.cs-emergency-actions {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.cs-emergency-card {
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 12px;
  background: var(--kx-surface-soft);
}
.cs-emergency-card.disabled {
  opacity: 0.5;
}
.cs-emergency-card--danger {
  border-left: 3px solid var(--kx-danger);
}
.cs-emergency-card--success {
  border-left: 3px solid var(--kx-success);
}
.cs-emergency-card--warning {
  border-left: 3px solid #d97706;
}
.cs-emergency-card--info {
  border-left: 3px solid #2563eb;
}
.cs-emergency-card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.cs-emergency-icon {
  /* emoji 在 Safari/macOS 上默认按 native glyph (≈64px) 渲染，
     仅 font-size 不够；必须显式限制宽度 + 行高 + 文本呈现方式 */
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 14px;
  line-height: 1;
  flex-shrink: 0;
  font-variant-emoji: text;
  text-align: center;
}
.cs-emergency-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--kx-text);
}
.cs-emergency-desc {
  margin: 0 0 8px;
  font-size: 11px;
  color: var(--kx-muted);
  line-height: 1.5;
}
.cs-emergency-desc code {
  font-size: 10.5px;
  background: var(--kx-bg);
  padding: 1px 4px;
  border-radius: 3px;
}
.cs-emergency-meta {
  font-size: 10px;
  color: var(--kx-muted);
  margin-bottom: 8px;
}
.cs-emergency-err {
  margin: 12px 0 0;
  padding: 6px 10px;
  background: rgba(220, 38, 38, 0.1);
  border-left: 3px solid var(--kx-danger);
  border-radius: 4px;
  font-size: 11px;
  color: var(--kx-danger);
}
.text-danger {
  color: var(--kx-danger);
}
@media (max-width: 480px) {
  .cs-emergency-actions {
    grid-template-columns: 1fr;
  }
}
</style>
