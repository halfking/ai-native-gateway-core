<template>
  <div class="plugin-settings-page">
    <div class="page-header">
      <h1>{{ t('plugin.providerConfig.title') }}</h1>
      <p class="subtitle">{{ t('plugin.providerConfig.subtitle') }}</p>
    </div>

    <div v-if="loading" class="loading-container">
      <div class="loading-spinner"></div>
      <p>{{ t('plugin.providerConfig.loading') }}</p>
    </div>

    <div v-else-if="error" class="error-container">
      <p class="error-message">{{ error }}</p>
      <button @click="loadConfig" class="retry-button">{{ t('plugin.providerConfig.retry') }}</button>
    </div>

    <div v-else class="settings-container">
      <!-- Configuration Source Info -->
      <div class="info-box">
        <div class="info-icon">ℹ️</div>
        <div>
          <strong>{{ t('plugin.providerConfig.sourceLabel') }}</strong> {{ configSourceText }}
          <br>
          <small>{{ t('plugin.providerConfig.restartHint') }}</small>
        </div>
      </div>

      <!-- Provider Selection Grid -->
      <div class="provider-grid">
        <!-- Auth Provider -->
        <div class="provider-card">
          <div class="card-header">
            <h3>{{ t('plugin.providerConfig.authProvider') }}</h3>
            <StatusBadge :status="getProviderStatus('auth_provider')" />
          </div>
          <div class="card-body">
            <select 
              v-model="configForm.auth_provider" 
              class="provider-select"
              :disabled="saving"
            >
              <option 
                v-for="provider in availableProviders.auth_providers" 
                :key="provider"
                :value="provider"
              >
                {{ getProviderDisplayName(provider) }}
              </option>
            </select>
            <p class="provider-description">
              {{ t('plugin.providerConfig.authProviderDesc') }}
            </p>
          </div>
        </div>

        <!-- User Manager -->
        <div class="provider-card">
          <div class="card-header">
            <h3>{{ t('plugin.providerConfig.userManager') }}</h3>
            <StatusBadge :status="getProviderStatus('user_manager')" />
          </div>
          <div class="card-body">
            <select 
              v-model="configForm.user_manager" 
              class="provider-select"
              :disabled="saving"
            >
              <option 
                v-for="provider in availableProviders.user_managers" 
                :key="provider"
                :value="provider"
              >
                {{ getProviderDisplayName(provider) }}
              </option>
            </select>
            <p class="provider-description">
              {{ t('plugin.providerConfig.userManagerDesc') }}
            </p>
          </div>
        </div>

        <!-- Tenant Manager -->
        <div class="provider-card">
          <div class="card-header">
            <h3>{{ t('plugin.providerConfig.tenantManager') }}</h3>
            <StatusBadge :status="getProviderStatus('tenant_manager')" />
          </div>
          <div class="card-body">
            <select 
              v-model="configForm.tenant_manager" 
              class="provider-select"
              :disabled="saving"
            >
              <option 
                v-for="provider in availableProviders.tenant_managers" 
                :key="provider"
                :value="provider"
              >
                {{ getProviderDisplayName(provider) }}
              </option>
            </select>
            <p class="provider-description">
              {{ t('plugin.providerConfig.tenantManagerDesc') }}
            </p>
          </div>
        </div>

        <!-- Approval Provider -->
        <div class="provider-card">
          <div class="card-header">
            <h3>{{ t('plugin.providerConfig.approvalProvider') }}</h3>
            <StatusBadge :status="getProviderStatus('approval_provider')" />
          </div>
          <div class="card-body">
            <select 
              v-model="configForm.approval_provider" 
              class="provider-select"
              :disabled="saving"
            >
              <option 
                v-for="provider in availableProviders.approval_providers" 
                :key="provider"
                :value="provider"
              >
                {{ getProviderDisplayName(provider) }}
              </option>
            </select>
            <p class="provider-description">
              {{ t('plugin.providerConfig.approvalProviderDesc') }}
            </p>
          </div>
        </div>
      </div>

      <!-- Action Buttons -->
      <div class="actions">
        <button 
          @click="applyConfig" 
          class="apply-button"
          :disabled="!hasChanges || saving"
        >
          {{ saving ? t('plugin.providerConfig.saving') : t('plugin.providerConfig.apply') }}
        </button>
        <button 
          @click="resetConfig" 
          class="reset-button"
          :disabled="!hasChanges || saving"
        >
          {{ t('plugin.providerConfig.reset') }}
        </button>
      </div>

      <!-- Warning Box -->
      <div v-if="hasChanges" class="warning-box">
        <div class="warning-icon">⚠️</div>
        <div>
          <strong>{{ t('plugin.providerConfig.importantNote') }}</strong>
          <ul>
            <li>{{ t('plugin.providerConfig.noteRestart') }}</li>
            <li>{{ t('plugin.providerConfig.noteEnv') }}</li>
            <li>{{ t('plugin.providerConfig.noteOffPeak') }}</li>
          </ul>
        </div>
      </div>

      <!-- Success Message -->
      <div v-if="successMessage" class="success-box">
        <div class="success-icon">✓</div>
        <p>{{ successMessage }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { defineComponent, h, ref, computed, onMounted, type PropType } from 'vue';
import { useI18n } from 'vue-i18n';
import { pluginConfigAPI, type PluginConfigResponse, type PluginConfig } from '@/api/modules/plugin-config';

const { t } = useI18n();

// Component for status badge
// 2026-11-16: 此处原先写的是 JSX（`<span class={...}>{...}</span>`）。JSX 在
// `<script setup lang="ts">` 里不被 SFC 编译链支持 ⇒ vue-tsc 报 TS1005，
// 且 `pnpm build` 在类型检查这一步整条失败。改为等价的 render function
// （同样的 class、同样的文案、同样的 title 属性，渲染结果不变）。
const StatusBadge = defineComponent({
  name: 'StatusBadge',
  props: {
    status: {
      type: Object as PropType<{ available: boolean; error: string }>,
      required: true,
    },
  },
  setup(props) {
    const { t } = useI18n();
    return () =>
      h(
        'span',
        { class: ['status-badge', props.status.available ? 'available' : 'unavailable'] },
        [
          props.status.available
            ? `● ${t('plugin.providerConfig.available')}`
            : `● ${t('plugin.providerConfig.unavailable')}`,
          props.status.error
            ? h('span', { class: 'status-error', title: props.status.error }, '!')
            : null,
        ],
      );
  },
});

// State
const loading = ref(true);
const saving = ref(false);
const error = ref('');
const successMessage = ref('');

const currentConfig = ref<PluginConfig | null>(null);
const configForm = ref<PluginConfig>({
  auth_provider: 'native',
  user_manager: 'native',
  tenant_manager: 'native',
  approval_provider: 'native',
});

const availableProviders = ref({
  auth_providers: [] as string[],
  user_managers: [] as string[],
  tenant_managers: [] as string[],
  approval_providers: [] as string[],
});

const providerStatus = ref<any>({});
const configSource = ref('');

// Computed
const hasChanges = computed(() => {
  if (!currentConfig.value) return false;
  return (
    configForm.value.auth_provider !== currentConfig.value.auth_provider ||
    configForm.value.user_manager !== currentConfig.value.user_manager ||
    configForm.value.tenant_manager !== currentConfig.value.tenant_manager ||
    configForm.value.approval_provider !== currentConfig.value.approval_provider
  );
});

const configSourceText = computed(() => {
  const source = configSource.value;
  if (source === 'environment') return t('plugin.providerConfig.sourceEnv');
  if (source === 'database') return t('plugin.providerConfig.sourceDb');
  return t('plugin.providerConfig.sourceDefault');
});

// Methods
const loadConfig = async () => {
  try {
    loading.value = true;
    error.value = '';
    
    const response = await pluginConfigAPI.getConfig();
    
    currentConfig.value = response.current;
    configForm.value = { ...response.current };
    availableProviders.value = response.available;
    providerStatus.value = response.status;
    configSource.value = response.source;
    
  } catch (err: any) {
    error.value = err.message || t('plugin.providerConfig.loadFailed');
  } finally {
    loading.value = false;
  }
};

const applyConfig = async () => {
  if (!hasChanges.value) return;
  
  try {
    saving.value = true;
    successMessage.value = '';
    error.value = '';
    
    const response = await pluginConfigAPI.updateConfig(configForm.value);
    
    if (response.success) {
      currentConfig.value = { ...configForm.value };
      successMessage.value = response.message;
      
      // Auto-hide success message after 5 seconds
      setTimeout(() => {
        successMessage.value = '';
      }, 5000);
    }
    
  } catch (err: any) {
    error.value = err.message || t('plugin.providerConfig.updateFailed');
  } finally {
    saving.value = false;
  }
};

const resetConfig = () => {
  if (currentConfig.value) {
    configForm.value = { ...currentConfig.value };
  }
  successMessage.value = '';
  error.value = '';
};

const getProviderStatus = (providerType: string) => {
  return providerStatus.value[providerType] || { available: false, error: '' };
};

const getProviderDisplayName = (provider: string) => {
  if (provider === 'native') return t('plugin.providerConfig.nativeProvider');
  if (provider === 'casdoor') return t('plugin.providerConfig.casdoorProvider');
  return provider;
};

// Lifecycle
onMounted(() => {
  loadConfig();
});
</script>

<style scoped>
.plugin-settings-page {
  max-width: 1200px;
  margin: 0 auto;
  padding: 24px;
}

.page-header {
  margin-bottom: 32px;
}

.page-header h1 {
  font-size: 28px;
  font-weight: 600;
  margin-bottom: 8px;
  color: #1a1a1a;
}

.subtitle {
  font-size: 14px;
  color: #666;
}

.loading-container, .error-container {
  text-align: center;
  padding: 48px 24px;
}

.loading-spinner {
  width: 40px;
  height: 40px;
  margin: 0 auto 16px;
  border: 4px solid #f3f3f3;
  border-top: 4px solid #3498db;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin {
  0% { transform: rotate(0deg); }
  100% { transform: rotate(360deg); }
}

.error-message {
  color: #e74c3c;
  margin-bottom: 16px;
}

.retry-button {
  padding: 8px 24px;
  background: #3498db;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
}

.info-box, .warning-box, .success-box {
  display: flex;
  gap: 12px;
  padding: 16px;
  border-radius: 8px;
  margin-bottom: 24px;
}

.info-box {
  background: #e3f2fd;
  border: 1px solid #90caf9;
}

.warning-box {
  background: #fff3cd;
  border: 1px solid #ffc107;
}

.success-box {
  background: #d4edda;
  border: 1px solid #28a745;
}

.provider-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 24px;
  margin-bottom: 32px;
}

.provider-card {
  background: white;
  border: 1px solid #e0e0e0;
  border-radius: 8px;
  padding: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.card-header h3 {
  font-size: 16px;
  font-weight: 600;
  color: #333;
  margin: 0;
}

.status-badge {
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 12px;
}

.status-badge.available {
  color: #28a745;
  background: #d4edda;
}

.status-badge.unavailable {
  color: #dc3545;
  background: #f8d7da;
}

.provider-select {
  width: 100%;
  padding: 10px;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-size: 14px;
  margin-bottom: 12px;
}

.provider-select:disabled {
  background: #f5f5f5;
  cursor: not-allowed;
}

.provider-description {
  font-size: 13px;
  color: #666;
  margin: 0;
}

.actions {
  display: flex;
  gap: 12px;
  margin-bottom: 24px;
}

.apply-button, .reset-button {
  padding: 12px 32px;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.apply-button {
  background: #3498db;
  color: white;
}

.apply-button:hover:not(:disabled) {
  background: #2980b9;
}

.apply-button:disabled {
  background: #ccc;
  cursor: not-allowed;
}

.reset-button {
  background: white;
  color: #666;
  border: 1px solid #ddd;
}

.reset-button:hover:not(:disabled) {
  background: #f5f5f5;
}

.warning-box ul {
  margin: 8px 0 0 0;
  padding-left: 20px;
}

.warning-box li {
  margin-bottom: 4px;
}
</style>
