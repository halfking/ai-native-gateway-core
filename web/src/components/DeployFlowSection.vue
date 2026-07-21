<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

const { t, tm } = useI18n()
const router = useRouter()

const emit = defineEmits<{ login: [] }>()

const steps = computed(() => [
  {
    num: 1,
    title: t('landing.deployFlow.steps.download.title'),
    description: t('landing.deployFlow.steps.download.description'),
    action: t('landing.deployFlow.steps.download.action'),
    path: '/download',
  },
  {
    num: 2,
    title: t('landing.deployFlow.steps.install.title'),
    description: t('landing.deployFlow.steps.install.description'),
    hint: t('landing.deployFlow.steps.install.hint'),
  },
  {
    num: 3,
    title: t('landing.deployFlow.steps.activate.title'),
    description: t('landing.deployFlow.steps.activate.description'),
    action: t('landing.deployFlow.steps.activate.action'),
    path: '/activate',
    secondaryAction: t('landing.deployFlow.steps.activate.offlineAction'),
    secondaryPath: '/offline-activation',
  },
  {
    num: 4,
    title: t('landing.deployFlow.steps.login.title'),
    description: t('landing.deployFlow.steps.login.description'),
    action: t('landing.deployFlow.steps.login.action'),
    login: true,
  },
])

const notes = computed(() => tm('landing.deployFlow.notes') as string[])

function handleStep(step: (typeof steps.value)[number]) {
  if (step.login) {
    emit('login')
    return
  }
  if (step.path) {
    router.push(step.path)
  }
}

function handleSecondary(path?: string) {
  if (path) {
    router.push(path)
  }
}
</script>

<template>
  <section class="deploy-flow" aria-labelledby="deploy-flow-title">
    <div class="deploy-flow__inner">
      <header class="deploy-flow__head">
        <h2 id="deploy-flow-title" class="deploy-flow__title">{{ t('landing.deployFlow.title') }}</h2>
        <p class="deploy-flow__sub">{{ t('landing.deployFlow.subtitle') }}</p>
      </header>

      <ol class="deploy-flow__steps">
        <li v-for="step in steps" :key="step.num" class="deploy-flow__step">
          <span class="deploy-flow__num" aria-hidden="true">{{ step.num }}</span>
          <div class="deploy-flow__body">
            <h3>{{ step.title }}</h3>
            <p>{{ step.description }}</p>
            <code v-if="step.hint" class="deploy-flow__hint">{{ step.hint }}</code>
            <div v-if="step.action || step.secondaryAction" class="deploy-flow__actions">
              <el-button
                v-if="step.action"
                type="primary"
                size="small"
                @click="handleStep(step)"
              >
                {{ step.action }}
              </el-button>
              <el-button
                v-if="step.secondaryAction"
                size="small"
                link
                type="primary"
                @click="handleSecondary(step.secondaryPath)"
              >
                {{ step.secondaryAction }}
              </el-button>
            </div>
          </div>
        </li>
      </ol>

      <ul class="deploy-flow__notes">
        <li v-for="note in notes" :key="note">{{ note }}</li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.deploy-flow {
  padding: 8px 16px 24px;
  max-width: 960px;
  margin: 0 auto;
  width: 100%;
}

.deploy-flow__inner {
  padding: 28px 24px;
  border: 1px solid rgba(99, 102, 241, 0.35);
  border-radius: 14px;
  background: linear-gradient(135deg, rgba(99, 102, 241, 0.1), rgba(15, 17, 23, 0.55));
}

.deploy-flow__head {
  margin-bottom: 20px;
  text-align: center;
}

.deploy-flow__title {
  margin: 0 0 8px;
  font-size: 22px;
  font-weight: 600;
  color: #e8eaed;
}

.deploy-flow__sub {
  margin: 0;
  font-size: 14px;
  color: #94a3b8;
}

.deploy-flow__steps {
  display: grid;
  gap: 14px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.deploy-flow__step {
  display: grid;
  grid-template-columns: 36px 1fr;
  gap: 14px;
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--panel);
}

.deploy-flow__num {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: 10px;
  font-size: 15px;
  font-weight: 700;
  color: var(--accent-h);
  background: rgba(99, 102, 241, 0.16);
}

.deploy-flow__body h3 {
  margin: 0 0 6px;
  font-size: 15px;
  font-weight: 600;
  color: #e8eaed;
}

.deploy-flow__body p {
  margin: 0;
  font-size: 13px;
  line-height: 1.55;
  color: #9aa0a6;
}

.deploy-flow__hint {
  display: block;
  margin-top: 8px;
  padding: 8px 10px;
  border-radius: 8px;
  font-size: 12px;
  color: #cbd5e1;
  background: rgba(15, 23, 42, 0.65);
  word-break: break-all;
}

.deploy-flow__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  margin-top: 12px;
}

.deploy-flow__notes {
  margin: 20px 0 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 6px;
}

.deploy-flow__notes li {
  position: relative;
  padding-left: 16px;
  font-size: 12px;
  color: #64748b;
}

.deploy-flow__notes li::before {
  content: '·';
  position: absolute;
  left: 4px;
  color: var(--accent);
}

@media (max-width: 600px) {
  .deploy-flow__inner {
    padding: 20px 16px;
  }

  .deploy-flow__step {
    grid-template-columns: 1fr;
    gap: 10px;
  }

  .deploy-flow__num {
    width: 32px;
    height: 32px;
  }
}
</style>
