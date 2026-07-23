<script setup lang="ts">
import { reactive, ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { isSuperAdmin } from '../../store'
import { patchCandidateBinding, emergencyRepair, type RoutingCandidate, type EmergencyRepairAction } from '../../api/routing'
import { updateCredential } from '../../api/providers'
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

const form = reactive({
  manual_priority: props.candidate.manual_priority ?? 99,
  routing_tier: props.candidate.tier ?? 2,
  weight: props.candidate.weight ?? 100,
  manual_disabled: false,
  lifecycle_status: (props.candidate.lifecycle_status || 'active') as string,
})

// Emergency repair state
const emergencyRepairing = ref<EmergencyRepairAction | null>(null)
const emergencyErr = ref('')

// 乐观更新快照；保存失败时回滚到这里的值。
const prev = {
  manual_priority: form.manual_priority,
  routing_tier: form.routing_tier,
  weight: form.weight,
}

const canEdit = computed(() => isSuperAdmin())

const lifecycleOptions = [
  { value: 'active', label: 'active（在用）' },
  { value: 'deprecated', label: 'deprecated（弃用）' },
  { value: 'test', label: 'test（测试）' },
]

// Emergency repair actions availability based on current state
const canForceEnable = computed(() =>
  props.candidate.credential_status !== 'active' ||
  props.candidate.lifecycle_status !== 'active'
)

const canForceDisable = computed(() =>
  props.candidate.credential_status === 'active' &&
  props.candidate.lifecycle_status === 'active'
)

const canClearCircuit = computed(() =>
  props.candidate.circuit_state === 'open' || props.candidate.circuit_state === 'half_open'
)

const canResetErrors = computed(() =>
  (props.candidate.consecutive_failures ?? 0) > 0
)

async function doEmergencyRepair(action: EmergencyRepairAction, label: string) {
  emergencyErr.value = ''
  try {
    await ElMessageBox.confirm(
      `确定要执行「${label}」操作吗？\n\n凭据 ID: ${props.candidate.credential_id}\n模型: ${props.candidate.model_name}\n\n此操作会绕过正常的状态检测逻辑，请确认您知道在做什么。`,
      `紧急修复 — ${label}`,
      {
        confirmButtonText: '确认执行',
        cancelButtonText: '取消',
        type: 'warning',
        confirmButtonClass: 'el-button--danger',
      },
    )
  } catch {
    // User cancelled
    return
  }

  emergencyRepairing.value = action
  try {
    await emergencyRepair({
      credential_id: props.candidate.credential_id,
      raw_model: props.candidate.model_name,
      action,
      reason: `admin via routing-v2 resolve dialog: ${label}`,
    })
    ElMessage.success(`「${label}」执行成功`)
    emit('applied', { credential_id: props.candidate.credential_id, raw_model: props.candidate.model_name })
    emit('close')
  } catch (e: unknown) {
    emergencyErr.value = e instanceof Error ? e.message : '操作失败'
    ElMessage.error(`「${label}」失败: ${emergencyErr.value}`)
  } finally {
    emergencyRepairing.value = null
  }
}

async function save() {
  if (!canEdit.value) {
    ElMessage.warning('仅系统管理员可以修改设置')
    return
  }
  saving.value = true
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
        await updateCredential(
          props.candidate.provider_id,
          props.candidate.credential_id,
          // lifecycle_status 不属于 updateCredential 字段白名单，置空让后端忽略即可。
          // 注意：实际改 lifecycle_status 走的是 /lifecycle 端点，这里仅做"轻量提示"。
          // 留空避免误改其他字段；UI 上仍然展示便于操作员查看当前状态。
          {},
        )
        // 走专门的 lifecycle 端点（admin/routing.go 已存在）
        await fetch(
          `/api/providers/${props.candidate.provider_id}/credentials/${props.candidate.credential_id}/lifecycle`,
          {
            method: 'PATCH',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ lifecycle_status: form.lifecycle_status }),
          },
        ).then(async (r) => {
          if (!r.ok) throw new Error(`HTTP ${r.status}`)
          return r.json()
        })
        touched.push({ ok: true, label: '生命周期' })
      } catch (e) {
        touched.push({ ok: false, label: `生命周期（${(e as Error).message}）` })
      }
    }
    const fails = touched.filter((t) => !t.ok)
    if (fails.length === 0) {
      ElMessage.success('设置已保存（部分字段刷新后生效）')
      emit('applied', { credential_id: props.candidate.credential_id, raw_model: props.candidate.model_name })
      emit('close')
    } else if (fails.length === touched.length) {
      ElMessage.error(`保存失败：${fails.map((f) => f.label).join('；')}`)
    } else {
      ElMessage.warning(`部分保存失败：${fails.map((f) => f.label).join('；')}`)
      emit('applied', { credential_id: props.candidate.credential_id, raw_model: props.candidate.model_name })
      emit('close')
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
                <li>连续失败: <strong :class="{ 'text-danger': (candidate.consecutive_failures ?? 0) > 0 }">{{ candidate.consecutive_failures ?? 0 }} 次</strong></li>
              </ul>
            </div>

            <div class="cs-emergency-actions">
              <!-- Force Disable -->
              <div class="cs-emergency-card cs-emergency-card--danger" :class="{ disabled: !canForceDisable || emergencyRepairing !== null }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🔴</span>
                  <span class="cs-emergency-title">强制禁用</span>
                </div>
                <p class="cs-emergency-desc">立即将此节点标记为不可用，绕过熔断器。将 <code>manual_disabled=true</code> 写入数据库。</p>
                <div class="cs-emergency-meta">当前凭据状态: {{ candidate.credential_status }}</div>
                <button
                  class="btn btn-danger btn-sm"
                  :disabled="!canForceDisable || emergencyRepairing !== null"
                  @click="doEmergencyRepair('force_disable', '强制禁用')"
                >
                  {{ emergencyRepairing === 'force_disable' ? '处理中…' : '强制禁用' }}
                </button>
              </div>

              <!-- Force Enable -->
              <div class="cs-emergency-card cs-emergency-card--success" :class="{ disabled: !canForceEnable || emergencyRepairing !== null }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🟢</span>
                  <span class="cs-emergency-title">强制启用</span>
                </div>
                <p class="cs-emergency-desc">强制启用被禁用的节点，清除 <code>manual_disabled=false</code> 标记。</p>
                <div class="cs-emergency-meta">当前凭据状态: {{ candidate.credential_status }}</div>
                <button
                  class="btn btn-success btn-sm"
                  :disabled="!canForceEnable || emergencyRepairing !== null"
                  @click="doEmergencyRepair('force_enable', '强制启用')"
                >
                  {{ emergencyRepairing === 'force_enable' ? '处理中…' : '强制启用' }}
                </button>
              </div>

              <!-- Clear Circuit -->
              <div class="cs-emergency-card cs-emergency-card--warning" :class="{ disabled: !canClearCircuit || emergencyRepairing !== null }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🟡</span>
                  <span class="cs-emergency-title">清除熔断状态</span>
                </div>
                <p class="cs-emergency-desc">将 <code>OPEN/HALF_OPEN</code> 熔断状态重置为 <code>CLOSED</code>，清除冷却计时。</p>
                <div class="cs-emergency-meta">当前熔断状态: <strong>{{ candidate.circuit_state || 'closed' }}</strong></div>
                <button
                  class="btn btn-warning btn-sm"
                  :disabled="!canClearCircuit || emergencyRepairing !== null"
                  @click="doEmergencyRepair('clear_circuit', '清除熔断状态')"
                >
                  {{ emergencyRepairing === 'clear_circuit' ? '处理中…' : '清除熔断' }}
                </button>
              </div>

              <!-- Reset Errors -->
              <div class="cs-emergency-card cs-emergency-card--info" :class="{ disabled: !canResetErrors || emergencyRepairing !== null }">
                <div class="cs-emergency-card-header">
                  <span class="cs-emergency-icon">🔵</span>
                  <span class="cs-emergency-title">重置错误计数</span>
                </div>
                <p class="cs-emergency-desc">清除连续失败计数，让节点脱离 unhealthy 状态。将 <code>consecutive_failures=0</code>。</p>
                <div class="cs-emergency-meta">当前连续失败: <strong>{{ candidate.consecutive_failures ?? 0 }} 次</strong></div>
                <button
                  class="btn btn-info btn-sm"
                  :disabled="!canResetErrors || emergencyRepairing !== null"
                  @click="doEmergencyRepair('reset_errors', '重置错误计数')"
                >
                  {{ emergencyRepairing === 'reset_errors' ? '处理中…' : '重置计数' }}
                </button>
              </div>
            </div>

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
  z-index: 75;
  display: flex;
  align-items: center;
  justify-content: center;
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
  font-size: 16px;
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
