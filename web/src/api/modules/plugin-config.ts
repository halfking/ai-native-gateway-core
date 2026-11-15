import request from '../request';

// Plugin configuration types
export interface PluginConfig {
  auth_provider: string;
  user_manager: string;
  tenant_manager: string;
  approval_provider: string;
}

export interface ProviderStatus {
  name: string;
  available: boolean;
  error: string;
}

export interface PluginConfigResponse {
  current: PluginConfig;
  available: {
    auth_providers: string[];
    user_managers: string[];
    tenant_managers: string[];
    approval_providers: string[];
  };
  status: {
    auth_provider: ProviderStatus;
    user_manager: ProviderStatus;
    tenant_manager: ProviderStatus;
    approval_provider: ProviderStatus;
  };
  source: string;
}

export interface PluginProviderStatusResponse {
  providers: {
    auth_provider: ProviderStatus;
    user_manager: ProviderStatus;
    tenant_manager: ProviderStatus;
    approval_provider: ProviderStatus;
  };
  summary: string;
}

export interface UpdatePluginConfigRequest {
  auth_provider?: string;
  user_manager?: string;
  tenant_manager?: string;
  approval_provider?: string;
}

export interface UpdatePluginConfigResponse {
  success: boolean;
  message: string;
  config: PluginConfig;
}

// Plugin configuration API
export const pluginConfigAPI = {
  /**
   * Get current plugin configuration
   */
  getConfig(): Promise<PluginConfigResponse> {
    return request.get('/api/admin/plugin-config');
  },

  /**
   * Update plugin configuration
   */
  updateConfig(config: UpdatePluginConfigRequest): Promise<UpdatePluginConfigResponse> {
    return request.put('/api/admin/plugin-config', config);
  },

  /**
   * Get detailed provider status
   */
  getProviderStatus(): Promise<PluginProviderStatusResponse> {
    return request.get('/api/admin/plugin-providers');
  },
};
