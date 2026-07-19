/**
 * LLM Gateway - 质量画像 API 客户端实现
 * 
 * 基于 axios 的 HTTP 客户端
 * 支持请求/响应拦截、错误处理、类型安全
 */

import axios, { AxiosInstance, AxiosRequestConfig, AxiosError } from 'axios';
import { ref } from 'vue';
import type {
  QualityApiClient,
  ProviderQualityResponse,
  RankingResponse,
  RecalculateResponse,
  GetProviderQualityParams,
  GetRankingParams,
  RecalculateRequest,
  ErrorResponse,
} from '../types/quality-api';

/**
 * API 客户端配置
 */
export interface QualityApiConfig {
  /** 基础 URL */
  baseURL: string;
  /** 超时时间（毫秒） */
  timeout?: number;
  /** 请求头 */
  headers?: Record<string, string>;
}

/**
 * 默认配置
 */
const DEFAULT_CONFIG: Partial<QualityApiConfig> = {
  timeout: 10000,
  headers: {
    'Content-Type': 'application/json',
  },
};

/**
 * 质量画像 API 客户端实现
 */
export class QualityApiClientImpl implements QualityApiClient {
  private client: AxiosInstance;

  constructor(config: QualityApiConfig) {
    this.client = axios.create({
      ...DEFAULT_CONFIG,
      ...config,
    });

    // 请求拦截器
    this.client.interceptors.request.use(
      (config: any) => {
        // 可以在这里添加认证 token
        // config.headers.Authorization = `Bearer ${token}`;
        return config;
      },
      (error: any) => {
        return Promise.reject(error);
      }
    );

    // 响应拦截器
    this.client.interceptors.response.use(
      (response: any) => {
        return response;
      },
      (error: any) => {
        // 统一错误处理
        if (error.response) {
          // 服务器返回错误响应
          const errorResponse: ErrorResponse = error.response.data;
          return Promise.reject(errorResponse);
        } else if (error.request) {
          // 请求已发送但无响应
          return Promise.reject({
            code: 50002,
            message: '网络错误，请检查连接',
          } as ErrorResponse);
        } else {
          // 请求配置错误
          return Promise.reject({
            code: 50003,
            message: error.message || '请求失败',
          } as ErrorResponse);
        }
      }
    );
  }

  /**
   * 查询供应商质量画像
   */
  async getProviderQuality(
    params: GetProviderQualityParams
  ): Promise<ProviderQualityResponse> {
    const { provider_id, model_name } = params;
    const queryParams: Record<string, string> = {};
    
    if (model_name) {
      queryParams.model_name = model_name;
    }

    const response = await this.client.get<ProviderQualityResponse>(
      `/api/quality/providers/${provider_id}`,
      { params: queryParams }
    );

    return response.data;
  }

  /**
   * 查询质量排行榜
   */
  async getRanking(params?: GetRankingParams): Promise<RankingResponse> {
    const response = await this.client.get<RankingResponse>(
      '/api/quality/ranking',
      { params }
    );

    return response.data;
  }

  /**
   * 手动触发质量重算
   */
  async recalculate(request: RecalculateRequest): Promise<RecalculateResponse> {
    const response = await this.client.post<RecalculateResponse>(
      '/api/quality/recalculate',
      request
    );

    return response.data;
  }
}

/**
 * 创建 API 客户端实例
 */
export function createQualityApiClient(config: QualityApiConfig): QualityApiClient {
  return new QualityApiClientImpl(config);
}

/**
 * 默认客户端实例（根据环境自动选择 baseURL）
 */
export const qualityApi = createQualityApiClient({
  baseURL: getBaseURL(),
});

/**
 * 根据环境获取基础 URL
 */
function getBaseURL(): string {
  // 优先使用环境变量
  if (import.meta.env.VITE_API_BASE_URL) {
    return import.meta.env.VITE_API_BASE_URL;
  }

  // 根据部署环境判断
  const hostname = window.location.hostname;

  if (hostname === 'localhost' || hostname === '127.0.0.1') {
    // 本地开发
    return 'http://localhost:8781';
  } else if (hostname === '192.168.31.28') {
    // kaixuan-1 开发服务器
    return 'http://192.168.31.28:8781';
  } else if (hostname === '8.136.114.245') {
    // 245 测试环境
    return 'http://8.136.114.245:8781';
  } else {
    // 154 生产环境
    return 'https://api.kxpms.cn';
  }
}

// ============================================================================
// React Hooks（可选，用于 Vue 项目可以忽略）
// ============================================================================

/**
 * Vue Composition API 示例（如果项目使用 Vue）
 */
export function useQualityApi() {
  const loading = ref(false);
  const error = ref<ErrorResponse | null>(null);

  const getProviderQuality = async (params: GetProviderQualityParams) => {
    loading.value = true;
    error.value = null;

    try {
      const response = await qualityApi.getProviderQuality(params);
      return response;
    } catch (e) {
      error.value = e as ErrorResponse;
      throw e;
    } finally {
      loading.value = false;
    }
  };

  const getRanking = async (params?: GetRankingParams) => {
    loading.value = true;
    error.value = null;

    try {
      const response = await qualityApi.getRanking(params);
      return response;
    } catch (e) {
      error.value = e as ErrorResponse;
      throw e;
    } finally {
      loading.value = false;
    }
  };

  return {
    loading,
    error,
    getProviderQuality,
    getRanking,
  };
}
