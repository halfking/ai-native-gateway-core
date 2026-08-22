<script setup lang="ts">
// SessionMetaTitleRow — request-logs 抽屉内「标题 + 摘要入口」紧凑行。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  updateSessionTitle,
  deleteSessionTitle,
  summarizeSessionTitle,
} from '../api/memora'

const props = defineProps<{
  taskId: string | null | undefined
  sessionId: string | null | undefined
  title: string | null | undefined
}>()

const emit = defineEmits<{
  titleChanged: [title: string | null]
  openSummary: [sessionId: string]
}>()

const { t } = useI18n()

const editingTitle = ref(false)
const draftTitle = ref('')
const titleSaving = ref(false)
const titleError = ref<string | null>(null)
const regeneratingTitle = ref(false)
const titleDetailsOpen = ref(false)

watch(
  [() => props.taskId, () => props.sessionId],
  () => resetLocalState(),
)

function resetLocalState() {
  editingTitle.value = false
  draftTitle.value = ''
  titleSaving.value = false
  titleError.value = null
  regeneratingTitle.value = false
  titleDetailsOpen.value = false
}

function startEditTitle() {
  draftTitle.value = props.title ?? ''
  editingTitle.value = true
  titleError.value = null
}

function cancelEditTitle() {
  editingTitle.value = false
  draftTitle.value = ''
  titleError.value = null
}

async function saveEditTitle() {
  if (!props.taskId) return
  const title = draftTitle.value.trim()
  if (!title) {
    titleError.value = '标题不能为空'
    return
  }
  const taskId = props.taskId
  const sessionId = props.sessionId
  titleSaving.value = true
  titleError.value = null
  try {
    await updateSessionTitle(taskId, { title, scoped_session_id: sessionId ?? '' })
    if (props.taskId === taskId) {
      emit('titleChanged', title)
      editingTitle.value = false
    }
  } catch (e: unknown) {
    if (props.taskId === taskId) {
      titleError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (props.taskId === taskId) titleSaving.value = false
  }
}

async function regenerateTitle() {
  if (!props.taskId) return
  const taskId = props.taskId
  const sessionId = props.sessionId
  regeneratingTitle.value = true
  titleError.value = null
  try {
    const response = await summarizeSessionTitle(taskId, {
      session_id: sessionId ?? undefined,
      hours: 168,
    })
    if (props.taskId === taskId) emit('titleChanged', response.title)
  } catch (e: unknown) {
    if (props.taskId === taskId) {
      titleError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (props.taskId === taskId) regeneratingTitle.value = false
  }
}

async function clearTitle() {
  if (!props.taskId || !confirm('确认清空此会话的标题？')) return
  const taskId = props.taskId
  const sessionId = props.sessionId
  titleSaving.value = true
  titleError.value = null
  try {
    await deleteSessionTitle(taskId, sessionId ?? '')
    if (props.taskId === taskId) {
      emit('titleChanged', null)
      editingTitle.value = false
    }
  } catch (e: unknown) {
    if (props.taskId === taskId) {
      titleError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (props.taskId === taskId) titleSaving.value = false
  }
}

function openSummaryDrawer() {
  const sid = (props.sessionId ?? '').trim()
  if (sid) emit('openSummary', sid)
}
</script>

<template>
  <div class="session-meta-title-block">
    <div class="session-meta-title">
      <strong>会话标题:</strong>
      <template v-if="!editingTitle">
        <span class="title-value" :class="{ 'title-missing': !title }">{{ title || '无标题' }}</span>
        <button
          v-if="taskId"
          class="btn btn-sm"
          type="button"
          :aria-expanded="titleDetailsOpen"
          @click="titleDetailsOpen = !titleDetailsOpen"
        >
          {{ titleDetailsOpen ? '收起标题详情' : '标题详情' }}
        </button>
        <button
          v-if="sessionId"
          class="btn btn-sm"
          type="button"
          :aria-label="t('requests.list.trace.drawerSummaryAria')"
          :title="t('requests.list.trace.drawerSummaryTitle')"
          @click="openSummaryDrawer"
        >
          {{ t('requests.list.trace.drawerSummaryButton') }}
        </button>
      </template>
      <template v-else>
        <input
          v-model="draftTitle"
          class="title-input"
          maxlength="80"
          placeholder="2-80 字符，不含 XML 标签"
          @keydown.enter="saveEditTitle"
          @keydown.esc="cancelEditTitle"
        />
        <button class="btn btn-sm btn-primary" :disabled="titleSaving" @click="saveEditTitle">
          {{ titleSaving ? '保存中…' : '保存' }}
        </button>
        <button class="btn btn-sm" :disabled="titleSaving" @click="cancelEditTitle">取消</button>
      </template>
      <span v-if="titleError" class="meta-error">{{ titleError }}</span>
    </div>

    <div v-if="titleDetailsOpen && !editingTitle && taskId" class="session-meta-title-actions">
      <button class="btn btn-sm" :disabled="titleSaving || regeneratingTitle" @click="startEditTitle">编辑</button>
      <button class="btn btn-sm" :disabled="titleSaving || regeneratingTitle" @click="regenerateTitle">
        {{ regeneratingTitle ? '重新生成中…' : (title ? '重新生成' : '生成标题') }}
      </button>
      <button
        v-if="title"
        class="btn btn-sm btn-danger-ghost"
        :disabled="titleSaving || regeneratingTitle"
        @click="clearTitle"
      >
        清空
      </button>
    </div>
  </div>
</template>

<style scoped>
.session-meta-title-block { margin-bottom: 8px; }
.session-meta-title,
.session-meta-title-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.session-meta-title-actions { margin: -2px 0 8px; gap: 6px; }
.title-value { font-weight: 600; max-width: 42ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.title-missing { color: var(--muted); font-weight: 500; }
.title-input {
  min-width: 180px;
  flex: 1;
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 12px;
  background: var(--bg);
  color: inherit;
}
.meta-error { color: var(--danger); font-size: 12px; }
</style>
