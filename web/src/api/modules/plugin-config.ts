import { req } from '../_core';

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
// 2026-11-16: 原先 import 的是不存在的 `../request`（axios 风格封装，本仓
// 从未有过该模块）⇒ vue-tsc 报 TS2307，`pnpm build` 的类型检查步骤整条失败。
// 改为本仓统一的 `req(method, path, body)`（src/api/_core.ts），端点与语义不变。
export const pluginConfigAPI = {
  /**
   * Get current plugin configuration
   */
  getConfig(): Promise<PluginConfigResponse> {
    return req<PluginConfigResponse>('GET', '/api/admin/plugin-config');
  },

  /**
   * Update plugin configuration
   */
  updateConfig(config: UpdatePluginConfigRequest): Promise<UpdatePluginConfigResponse> {
    return req<UpdatePluginConfigResponse>('PUT', '/api/admin/plugin-config', config);
  },

  /**
   * Get detailed provider status
   */
  getProviderStatus(): Promise<PluginProviderStatusResponse> {
    return req<PluginProviderStatusResponse>('GET', '/api/admin/plugin-providers');
  },
};
