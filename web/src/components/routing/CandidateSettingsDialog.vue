<script setup lang="ts">
import { reactive, ref, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { isSuperAdmin } from '../../store'
import { patchCandidateBinding, type RoutingCandidate } from '../../api/routing'
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
const form = reactive({
  manual_priority: props.candidate.manual_priority ?? 99,
  routing_tier: props.candidate.tier ?? 2,
  weight: props.candidate.weight ?? 100,
  manual_disabled: false,
  lifecycle_status: (props.candidate.lifecycle_status || 'active') as string,
})

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
        <div class="cs-body">
          <p v-if="!canEdit" class="cs-warn">
            ⚠ 仅 super_admin 可见 / 可写；当前账号无权限，字段全部只读。
          </p>
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
        </div>
        <footer class="cs-foot">
          <button type="button" class="btn btn-ghost" @click="emit('close')">取消</button>
          <button type="button" class="btn btn-primary" :disabled="!canEdit || saving" @click="save">
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
  /* 2026-07-24 修正：backdrop 由 0.45 降到 0.28，对话框本身用实色 + 1px 边框 + box-shadow，
   * 让卡片轮廓在任何主题下都清晰，不被深色蒙层"吃"掉。 */
  background: rgba(0, 0, 0, 0.28);
  z-index: 75;
  display: flex;
  align-items: center;
  justify-content: center;
}
.cs-dialog {
  width: min(520px, 92vw);
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
.cs-body {
  overflow-y: auto;
  padding: 14px 16px;
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
</style>