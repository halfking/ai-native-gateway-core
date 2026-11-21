<template>
  <div class="ep-plan-editor">
    <div class="ep-plan-editor__kinds" role="radiogroup" :aria-label="t('errorPlaybook.form.planKind')">
      <label v-for="k in PLAN_KINDS" :key="k" class="ep-radio">
        <input
          type="radio"
          name="ep-plan-kind"
          :value="k"
          :checked="kind === k"
          @change="switchKind(k)"
        />
        <span>{{ t(`errorPlaybook.kind.${k}`) }}</span>
      </label>
    </div>

    <!-- ── script：白名单动作步骤表格 ── -->
    <div v-if="kind === 'script'" class="ep-plan-editor__body">
      <p class="ep-plan-editor__hint">{{ t('errorPlaybook.form.stepsHint') }}</p>
      <table class="ep-steps">
        <thead>
          <tr>
            <th class="ep-steps__idx">#</th>
            <th>{{ t('errorPlaybook.form.stepAction') }}</th>
            <th>{{ t('errorPlaybook.form.stepParams') }}</th>
            <th>{{ t('errorPlaybook.form.stepNote') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(s, i) in steps" :key="i">
            <td class="ep-steps__idx">{{ i + 1 }}</td>
            <td>
              <select v-model="s.action" class="ep-input">
                <option v-for="a in ACTIONS" :key="a" :value="a">{{ a }}</option>
              </select>
            </td>
            <td>
              <textarea
                v-model="s.paramsText"
                class="ep-input ep-input--mono"
                rows="2"
                :placeholder="t('errorPlaybook.form.stepParams')"
              />
              <p v-if="s.paramsError" class="ep-field-error">{{ s.paramsError }}</p>
            </td>
            <td>
              <input v-model="s.note" class="ep-input" type="text" />
            </td>
            <td>
              <button class="ep-btn ep-btn--ghost" type="button" @click="removeStep(i)">
                {{ t('errorPlaybook.form.removeStep') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <button
        class="ep-btn"
        type="button"
        :disabled="steps.length >= MAX_STEPS"
        @click="addStep"
      >
        {{ t('errorPlaybook.form.addStep') }}
      </button>
    </div>

    <!-- ── api：管理面调用表单 ── -->
    <div v-else-if="kind === 'api'" class="ep-plan-editor__body">
      <div class="ep-form-grid">
        <label class="ep-field">
          <span class="ep-field__label">{{ t('errorPlaybook.form.method') }}</span>
          <select v-model="apiMethod" class="ep-input">
            <option v-for="m in API_METHODS" :key="m" :value="m">{{ m }}</option>
          </select>
        </label>
        <label class="ep-field">
          <span class="ep-field__label">{{ t('errorPlaybook.form.timeout') }}</span>
          <input v-model.number="apiTimeout" class="ep-input" type="number" min="0" max="60000" />
        </label>
      </div>
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.form.path') }}</span>
        <input v-model="apiPath" class="ep-input" type="text" placeholder="/api/admin/..." />
        <span class="ep-field__hint">{{ t('errorPlaybook.form.pathHint') }}</span>
      </label>
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.form.body') }}</span>
        <textarea
          v-model="apiBodyText"
          class="ep-input ep-input--mono"
          rows="3"
          :placeholder="t('errorPlaybook.form.body')"
        />
        <p v-if="apiBodyError" class="ep-field-error">{{ apiBodyError }}</p>
      </label>
    </div>

    <!-- ── prompt：AI 提示词 ── -->
    <div v-else class="ep-plan-editor__body">
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.form.expect') }}</span>
        <select v-model="promptExpect" class="ep-input">
          <option value="analysis">{{ t('errorPlaybook.form.expectAnalysis') }}</option>
          <option value="proposal">{{ t('errorPlaybook.form.expectProposal') }}</option>
        </select>
      </label>
      <label class="ep-field">
        <span class="ep-field__label">{{ t('errorPlaybook.form.prompt') }}</span>
        <textarea
          v-model="promptText"
          class="ep-input ep-input--area"
          rows="8"
          :placeholder="t('errorPlaybook.form.promptHint')"
        />
        <span class="ep-field__hint">{{ t('errorPlaybook.form.promptHint') }}</span>
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
// PlanEditor.vue — 三形态方案编辑器（script 步骤表 / api 表单 / prompt 文本域）。
//
// ★ 归一化职责在后端（NormalizePlan fail-closed）；本组件只负责把「人能填的
//   表单」拼成后端认得的 payload 形状，并在客户端先挡一层明显错误
//   （JSON 不合法）——门控原文仍以服务端 422 为准，前端不复制白名单语义。
//
// 用法：父组件拿 ref 调 `getPayload()`，成功得 {plan_kind, plan}，失败得
// {error}（已翻译文案）。props 只在挂载时读一次（编辑场景整组件重建）。

import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PlanKind, PlaybookPlan } from '../../../../api/errorPlaybook'

const props = defineProps<{
  initialKind?: PlanKind
  initialPlan?: PlaybookPlan | null
}>()

const { t } = useI18n()

const PLAN_KINDS: PlanKind[] = ['script', 'api', 'prompt']
const ACTIONS = ['record_note', 'set_settings_kv', 'disable_pi_rule', 'call_admin_api'] as const
const API_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'] as const
const MAX_STEPS = 8

const kind = ref<PlanKind>(props.initialKind ?? props.initialPlan?.kind ?? 'prompt')

interface StepDraft {
  action: string
  paramsText: string
  paramsError: string
  note: string
}

function stepsFrom(plan: PlaybookPlan | null | undefined): StepDraft[] {
  const raw = plan?.steps ?? []
  if (!raw.length) return [newStep()]
  return raw.map((s) => ({
    action: s.action,
    paramsText: s.params && Object.keys(s.params).length ? JSON.stringify(s.params, null, 0) : '',
    paramsError: '',
    note: s.note ?? '',
  }))
}

function newStep(): StepDraft {
  return { action: 'record_note', paramsText: '', paramsError: '', note: '' }
}

const steps = ref<StepDraft[]>(stepsFrom(props.initialPlan))
const apiMethod = ref<string>(props.initialPlan?.method ?? 'GET')
const apiPath = ref<string>(props.initialPlan?.path ?? '')
const apiBodyText = ref<string>(
  props.initialPlan?.body != null ? JSON.stringify(props.initialPlan.body, null, 0) : '',
)
const apiBodyError = ref('')
const apiTimeout = ref<number | undefined>(props.initialPlan?.timeout_ms || undefined)
const promptText = ref<string>(props.initialPlan?.prompt ?? '')
const promptExpect = ref<'analysis' | 'proposal'>(
  props.initialPlan?.expect === 'proposal' ? 'proposal' : 'analysis',
)

function switchKind(next: PlanKind): void {
  kind.value = next
}

function addStep(): void {
  if (steps.value.length >= MAX_STEPS) return
  steps.value.push(newStep())
}

function removeStep(i: number): void {
  steps.value.splice(i, 1)
  if (!steps.value.length) steps.value.push(newStep())
}

function parseJsonText(text: string): { ok: true; value: unknown } | { ok: false } {
  const trimmed = text.trim()
  if (!trimmed) return { ok: true, value: {} }
  try {
    return { ok: true, value: JSON.parse(trimmed) }
  } catch {
    return { ok: false }
  }
}

interface PlanPayload {
  plan_kind: PlanKind
  plan: unknown
}

/** 产出后端 NormalizePlan 认得的 payload；失败返回已翻译错误文案。 */
function getPayload(): { ok: true; payload: PlanPayload } | { ok: false; error: string } {
  if (kind.value === 'script') {
    const out: Array<{ action: string; params: Record<string, unknown>; note?: string }> = []
    for (let i = 0; i < steps.value.length; i++) {
      const s = steps.value[i]
      const parsed = parseJsonText(s.paramsText)
      if (!parsed.ok) {
        s.paramsError = t('errorPlaybook.form.invalidJsonStep', { n: i + 1 })
        return { ok: false, error: s.paramsError }
      }
      s.paramsError = ''
      const step: { action: string; params: Record<string, unknown>; note?: string } = {
        action: s.action,
        params: (parsed.value as Record<string, unknown>) ?? {},
      }
      if (s.note.trim()) step.note = s.note.trim()
      out.push(step)
    }
    if (!out.length) {
      return { ok: false, error: t('errorPlaybook.form.stepsRequired') }
    }
    return { ok: true, payload: { plan_kind: 'script', plan: { kind: 'script', steps: out } } }
  }

  if (kind.value === 'api') {
    let body: unknown = undefined
    if (apiBodyText.value.trim()) {
      const parsed = parseJsonText(apiBodyText.value)
      if (!parsed.ok) {
        apiBodyError.value = t('errorPlaybook.form.invalidJsonBody')
        return { ok: false, error: apiBodyError.value }
      }
      apiBodyError.value = ''
      body = parsed.value
    }
    const plan: Record<string, unknown> = {
      method: apiMethod.value,
      path: apiPath.value.trim(),
    }
    if (body !== undefined) plan.body = body
    if (apiTimeout.value && apiTimeout.value > 0) plan.timeout_ms = apiTimeout.value
    return { ok: true, payload: { plan_kind: 'api', plan } }
  }

  return {
    ok: true,
    payload: {
      plan_kind: 'prompt',
      plan: { kind: 'prompt', prompt: promptText.value, expect: promptExpect.value },
    },
  }
}

defineExpose({ getPayload })
</script>

<style scoped>
.ep-plan-editor {
  border: 1px solid var(--border);
  border-radius: var(--radius-card, 8px);
  padding: 12px;
  background: var(--surface);
}

.ep-plan-editor__kinds {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
}

.ep-radio {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}

.ep-plan-editor__hint {
  margin: 0 0 8px;
  color: var(--muted);
  font-size: 12px;
}

.ep-plan-editor__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ep-steps {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.ep-steps th {
  text-align: left;
  color: var(--muted);
  font-weight: 500;
  padding: 4px 6px;
}

.ep-steps td {
  padding: 4px 6px;
  vertical-align: top;
}

.ep-steps__idx {
  width: 28px;
  color: var(--muted);
}

.ep-form-grid {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.ep-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.ep-field__label {
  color: var(--muted);
  font-size: 12px;
}

.ep-field__hint {
  color: var(--muted);
  font-size: 11px;
}

.ep-field-error {
  margin: 0;
  color: var(--danger, #c0392b);
  font-size: 12px;
}

.ep-input {
  padding: 6px 10px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  width: 100%;
  box-sizing: border-box;
}

.ep-input--mono {
  font-family: ui-monospace, monospace;
}

.ep-input--area {
  resize: vertical;
}

.ep-btn {
  padding: 6px 14px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
  align-self: flex-start;
}

.ep-btn--ghost {
  border-color: transparent;
  background: none;
  color: var(--danger, #c0392b);
}

.ep-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
