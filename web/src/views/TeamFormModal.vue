<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { createTeam, updateTeam, type Team, type CreateTeamRequest, type UpdateTeamRequest } from '../api-teams'
import { surfaceError } from '../i18n/errorSurface'

const { t } = useI18n()

interface Props {
  modelValue: boolean
  team?: Team | null
  mode?: 'create' | 'edit'
}

const props = withDefaults(defineProps<Props>(), {
  team: null,
  mode: 'create',
})

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'success': [team: Team]
}>()

const formData = ref({
  name: '',
  display_name: '',
  description: '',
})

const loading = ref(false)

const dialogVisible = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

const title = computed(() => 
  props.mode === 'create' ? t('teams.createTeam') : t('teams.editTeam')
)

// Initialize form with team data if editing
if (props.team) {
  formData.value = {
    name: props.team.name,
    display_name: props.team.display_name || '',
    description: props.team.description || '',
  }
}

async function handleSubmit() {
  // Validation
  if (!formData.value.name.trim()) {
    ElMessage.error(t('teams.validation.nameRequired'))
    return
  }

  // URL-safe validation for name
  if (!/^[a-z0-9-_]+$/.test(formData.value.name)) {
    ElMessage.error(t('teams.validation.nameFormat'))
    return
  }

  loading.value = true
  try {
    let team: Team

    if (props.mode === 'create') {
      const request: CreateTeamRequest = {
        name: formData.value.name,
        display_name: formData.value.display_name || null,
        description: formData.value.description || null,
      }
      team = await createTeam(request)
      ElMessage.success(t('teams.createSuccess'))
    } else {
      if (!props.team) throw new Error('Team is required for edit mode')
      
      const request: UpdateTeamRequest = {
        display_name: formData.value.display_name || null,
        description: formData.value.description || null,
      }
      team = await updateTeam(props.team.id, request)
      ElMessage.success(t('teams.updateSuccess'))
    }

    emit('success', team)
    dialogVisible.value = false
    resetForm()
  } catch (err) {
    surfaceError(err, { key: 'teams.errors.saveFailed' })
  } finally {
    loading.value = false
  }
}

function resetForm() {
  formData.value = {
    name: '',
    display_name: '',
    description: '',
  }
}

function handleClose() {
  dialogVisible.value = false
  resetForm()
}
</script>

<template>
  <el-dialog
    v-model="dialogVisible"
    :title="title"
    width="500px"
    :close-on-click-modal="false"
    @close="handleClose"
  >
    <el-form :model="formData" label-width="120px">
      <el-form-item :label="t('teams.name')" required>
        <el-input
          v-model="formData.name"
          :placeholder="t('teams.namePlaceholder')"
          :disabled="mode === 'edit'"
        />
        <div class="form-tip">{{ t('teams.nameHint') }}</div>
      </el-form-item>

      <el-form-item :label="t('teams.displayName')">
        <el-input
          v-model="formData.display_name"
          :placeholder="t('teams.displayNamePlaceholder')"
        />
      </el-form-item>

      <el-form-item :label="t('teams.description')">
        <el-input
          v-model="formData.description"
          type="textarea"
          :rows="3"
          :placeholder="t('teams.descriptionPlaceholder')"
        />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="handleClose" :disabled="loading">
        {{ t('common.cancel') }}
      </el-button>
      <el-button type="primary" :loading="loading" @click="handleSubmit">
        {{ mode === 'create' ? t('common.create') : t('common.save') }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.form-tip {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
}
</style>
