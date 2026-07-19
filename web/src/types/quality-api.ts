/**
 * LLM Gateway - 质量画像 API TypeScript 类型定义
 * 
 * 从 OpenAPI 3.0 规范自动生成
 * 版本: 1.0.0
 * 生成时间: 2026-07-19
 */

// ============================================================================
// 基础响应类型
// ============================================================================

/**
 * 统一响应包装
 */
export interface Response<T = any> {
  /** 响应码（0=成功） */
  code: number;
  /** 响应消息 */
  message: string;
  /** 响应数据 */
  data?: T;
}

/**
 * 错误响应
 */
export interface ErrorResponse {
  /** 错误码 */
  code: number;
  /** 错误消息 */
  message: string;
}

// ============================================================================
// 质量画像相关类型
// ============================================================================

/**
 * 五维质量评分
 */
export interface QualityScores {
  /** 可用性评分（0-100） */
  availability: number;
  /** 性能评分（0-100） */
  performance: number;
  /** 稳定性评分（0-100） */
  stability: number;
  /** 成本效益评分（0-100） */
  cost_efficiency: number;
}

/**
 * 质量等级
 */
export type QualityGrade = 'S' | 'A' | 'B' | 'C' | 'D';

/**
 * 模型质量画像
 */
export interface ModelQualityProfile {
  /** 模型名称（空字符串表示供应商级聚合） */
  model_name: string;
  /** 综合质量分数（0-100） */
  quality_score: number;
  /** 质量等级 */
  quality_grade: QualityGrade;
  /** 五维评分 */
  scores: QualityScores;
  /** 计算时间（ISO 8601） */
  calculated_at: string;
}

/**
 * 供应商质量画像数据
 */
export interface ProviderQualityData {
  /** 供应商 ID */
  provider_id: number;
  /** 供应商名称 */
  provider_name: string;
  /** 模型质量画像列表 */
  models: ModelQualityProfile[];
}

/**
 * 供应商质量画像响应
 */
export type ProviderQualityResponse = Response<ProviderQualityData>;

// ============================================================================
// 排行榜相关类型
// ============================================================================

/**
 * 排行榜项
 */
export interface RankingItem {
  /** 排名（从1开始） */
  rank: number;
  /** 供应商 ID */
  provider_id: number;
  /** 供应商名称 */
  provider_name: string;
  /** 模型名称 */
  model_name: string;
  /** 综合质量分数 */
  quality_score: number;
  /** 质量等级 */
  quality_grade: QualityGrade;
  /** 可用性评分 */
  availability_score: number;
  /** 性能评分 */
  performance_score: number;
  /** 计算时间 */
  calculated_at: string;
}

/**
 * 排行榜数据
 */
export interface RankingData {
  /** 排行榜列表 */
  ranking: RankingItem[];
  /** 总条数 */
  total: number;
}

/**
 * 排行榜响应
 */
export type RankingResponse = Response<RankingData>;

// ============================================================================
// 重算相关类型
// ============================================================================

/**
 * 重算结果数据
 */
export interface RecalculateData {
  /** 供应商 ID */
  provider_id: number;
  /** 更新的模型数量 */
  models_updated: number;
  /** 计算耗时（毫秒） */
  duration_ms: number;
}

/**
 * 重算响应
 */
export type RecalculateResponse = Response<RecalculateData>;

/**
 * 重算请求体
 */
export interface RecalculateRequest {
  /** 供应商 ID */
  provider_id: number;
}

// ============================================================================
// API 请求参数类型
// ============================================================================

/**
 * 获取供应商质量画像请求参数
 */
export interface GetProviderQualityParams {
  /** 供应商 ID */
  provider_id: number;
  /** 可选的模型名称过滤 */
  model_name?: string;
}

/**
 * 排序字段
 */
export type OrderBy = 'quality_score' | 'availability_score' | 'performance_score';

/**
 * 获取排行榜请求参数
 */
export interface GetRankingParams {
  /** 可选的模型名称过滤 */
  model_name?: string;
  /** 返回条数限制（1-100，默认20） */
  limit?: number;
  /** 排序字段（默认 quality_score） */
  order_by?: OrderBy;
  /** 最低质量分数过滤（0-100，默认0） */
  min_score?: number;
}

// ============================================================================
// 错误码常量
// ============================================================================

/**
 * API 错误码
 */
export enum QualityApiErrorCode {
  /** 成功 */
  SUCCESS = 0,
  /** 参数错误 */
  BAD_REQUEST = 40001,
  /** 供应商不存在 */
  PROVIDER_NOT_FOUND = 40401,
  /** 暂无质量数据 */
  NO_QUALITY_DATA = 40402,
  /** 方法不允许 */
  METHOD_NOT_ALLOWED = 40501,
  /** 服务器内部错误 */
  INTERNAL_ERROR = 50001,
}

/**
 * 错误消息映射
 */
export const ERROR_MESSAGES: Record<QualityApiErrorCode, string> = {
  [QualityApiErrorCode.SUCCESS]: '成功',
  [QualityApiErrorCode.BAD_REQUEST]: '参数错误',
  [QualityApiErrorCode.PROVIDER_NOT_FOUND]: '供应商不存在',
  [QualityApiErrorCode.NO_QUALITY_DATA]: '暂无质量数据',
  [QualityApiErrorCode.METHOD_NOT_ALLOWED]: '方法不允许',
  [QualityApiErrorCode.INTERNAL_ERROR]: '服务器内部错误',
};

// ============================================================================
// API 客户端接口
// ============================================================================

/**
 * 质量画像 API 客户端接口
 */
export interface QualityApiClient {
  /**
   * 查询供应商质量画像
   * @param params 请求参数
   * @returns 供应商质量画像数据
   */
  getProviderQuality(params: GetProviderQualityParams): Promise<ProviderQualityResponse>;

  /**
   * 查询质量排行榜
   * @param params 请求参数
   * @returns 排行榜数据
   */
  getRanking(params?: GetRankingParams): Promise<RankingResponse>;

  /**
   * 手动触发质量重算
   * @param request 请求体
   * @returns 重算结果
   */
  recalculate(request: RecalculateRequest): Promise<RecalculateResponse>;
}

// ============================================================================
// 工具函数类型
// ============================================================================

/**
 * 判断是否为成功响应
 */
export function isSuccessResponse<T>(response: Response<T>): response is Response<T> & { data: T } {
  return response.code === QualityApiErrorCode.SUCCESS && response.data !== undefined;
}

/**
 * 判断是否为错误响应
 */
export function isErrorResponse(response: Response): response is ErrorResponse {
  return response.code !== QualityApiErrorCode.SUCCESS;
}

/**
 * 获取错误消息
 */
export function getErrorMessage(code: number): string {
  return ERROR_MESSAGES[code as QualityApiErrorCode] || '未知错误';
}

/**
 * 质量等级颜色映射
 */
export const QUALITY_GRADE_COLORS: Record<QualityGrade, string> = {
  S: 'var(--danger)',
  A: 'var(--warning)',
  B: 'var(--warning)',
  C: 'var(--accent)',
  D: 'var(--muted)',
};

/**
 * 质量等级文本
 */
export const QUALITY_GRADE_LABELS: Record<QualityGrade, string> = {
  S: '卓越',
  A: '优秀',
  B: '良好',
  C: '一般',
  D: '较差',
};

/**
 * 格式化质量分数
 */
export function formatQualityScore(score: number): string {
  return score.toFixed(1);
}

/**
 * 格式化时间
 */
export function formatCalculatedAt(isoString: string): string {
  return new Date(isoString).toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}
