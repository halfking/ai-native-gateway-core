/**
 * 分析入口注册表 - 中央化管理所有数据分析路由和入口
 * 
 * 设计原则:
 * 1. 单一真源: 所有路由、API端点、标题都从此注册表生成
 * 2. 可追溯性: 记录每个分析的所有入口点,便于审计和维护
 * 3. 类型安全: TypeScript严格类型约束,避免硬编码字符串
 * 4. 复用优先: 相同维度的分析在不同入口处共享同一套组件
 */

export type AnalyticsDimension = 'model' | 'provider' | 'client' | 'tenant'

/**
 * 分析路由配置
 */
export interface AnalyticsRoute {
  /** 维度标识符 */
  dimension: AnalyticsDimension
  
  /** 基础路径前缀 */
  basePath: string
  
  /** 完整列表页路径 */
  listPath: string
  
  /** 详情页路径生成函数 */
  detailPath: (id: string) => string
  
  /** API端点生成函数 */
  apiEndpoint: (id: string, params: AnalyticsQueryParams) => string
  
  /** 标题国际化键 */
  titleKey: string
  
  /** 列表页标题国际化键 */
  listTitleKey: string
  
  /** 所有可进入此分析的页面路径(用于审计和导航追踪) */
  entryPoints: string[]
}

/**
 * 分析查询参数
 */
export interface AnalyticsQueryParams {
  days?: number
  start?: string
  end?: string
  tenant_id?: string
}

/**
 * 中央注册表 - 所有分析维度的配置
 * 
 * 使用示例:
 * ```typescript
 * import { ANALYTICS_REGISTRY } from '@/config/analyticsRegistry'
 * 
 * // 获取模型详情页路径
 * const path = ANALYTICS_REGISTRY.model.detailPath('gpt-4')
 * // => '/admin/analytics/model/gpt-4'
 * 
 * // 获取API端点
 * const endpoint = ANALYTICS_REGISTRY.provider.apiEndpoint('openai', { days: 7 })
 * // => '/api/admin/analytics/provider/openai?days=7'
 * ```
 */
export const ANALYTICS_REGISTRY: Record<AnalyticsDimension, AnalyticsRoute> = {
  model: {
    dimension: 'model',
    basePath: '/admin/analytics/model',
    listPath: '/admin/board-operational/models',
    detailPath: (id: string) => `/admin/analytics/model/${encodeURIComponent(id)}`,
    apiEndpoint: (id: string, params: AnalyticsQueryParams) => {
      const qs = buildQueryString({ ...params })
      return `/api/admin/analytics/model/${encodeURIComponent(id)}${qs ? '?' + qs : ''}`
    },
    titleKey: 'analytics.dimension.model',
    listTitleKey: 'analytics.list.models',
    entryPoints: [
      '/admin/board-operational',
      '/admin/dashboard',
    ],
  },
  
  provider: {
    dimension: 'provider',
    basePath: '/admin/analytics/provider',
    listPath: '/admin/board-operational/providers',
    detailPath: (id: string) => `/admin/analytics/provider/${encodeURIComponent(id)}`,
    apiEndpoint: (id: string, params: AnalyticsQueryParams) => {
      const qs = buildQueryString({ ...params })
      return `/api/admin/analytics/provider/${encodeURIComponent(id)}${qs ? '?' + qs : ''}`
    },
    titleKey: 'analytics.dimension.provider',
    listTitleKey: 'analytics.list.providers',
    entryPoints: [
      '/admin/board-operational',
      '/admin/dashboard',
    ],
  },
  
  client: {
    dimension: 'client',
    basePath: '/admin/analytics/client',
    listPath: '/admin/board-operational/clients',
    detailPath: (id: string) => `/admin/analytics/client/${encodeURIComponent(id)}`,
    apiEndpoint: (id: string, params: AnalyticsQueryParams) => {
      const qs = buildQueryString({ ...params })
      return `/api/admin/analytics/client/${encodeURIComponent(id)}${qs ? '?' + qs : ''}`
    },
    titleKey: 'analytics.dimension.client',
    listTitleKey: 'analytics.list.clients',
    entryPoints: [
      '/admin/board-operational',
      '/admin/dashboard',
    ],
  },
  
  tenant: {
    dimension: 'tenant',
    basePath: '/admin/analytics/tenant',
    listPath: '/admin/board-operational/tenants',
    detailPath: (id: string) => `/admin/analytics/tenant/${encodeURIComponent(id)}`,
    apiEndpoint: (id: string, params: AnalyticsQueryParams) => {
      const qs = buildQueryString({ ...params })
      return `/api/admin/analytics/tenant/${encodeURIComponent(id)}${qs ? '?' + qs : ''}`
    },
    titleKey: 'analytics.dimension.tenant',
    listTitleKey: 'analytics.list.tenants',
    entryPoints: [
      '/admin/board-operational',
      '/admin/dashboard',
    ],
  },
}

/**
 * 构建查询字符串
 */
function buildQueryString(params: Record<string, any>): string {
  const qs = new URLSearchParams()
  
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') {
      qs.set(key, String(value))
    }
  }
  
  return qs.toString()
}

/**
 * 根据维度获取路由配置
 */
export function getAnalyticsRoute(dimension: AnalyticsDimension): AnalyticsRoute {
  return ANALYTICS_REGISTRY[dimension]
}

/**
 * 检查路径是否是分析入口点
 */
export function isAnalyticsEntryPoint(path: string, dimension: AnalyticsDimension): boolean {
  return ANALYTICS_REGISTRY[dimension].entryPoints.includes(path)
}

/**
 * 获取所有注册的维度
 */
export function getAllDimensions(): AnalyticsDimension[] {
  return Object.keys(ANALYTICS_REGISTRY) as AnalyticsDimension[]
}
