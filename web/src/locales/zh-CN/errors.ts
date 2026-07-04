// errors.ts — API 错误文案。优先按后端稳定的 ApiError.code 翻译（errors.code.<code>），
// 无 code 时回退到 ApiError.message（后端中文兜底）。code 列表随接口演进逐步补充。
export default {
  // 按 ApiError.code 翻译（机器可读的稳定字符串）。新增 code 时在此追加。
  code: {
    unauthorized: '未授权，请重新登录',
    forbidden: '无权访问',
    not_found: '资源不存在',
    rate_limited: '请求过于频繁，请稍后重试',
    internal: '服务器内部错误',
  },
  // 按 HTTP 状态翻译的兜底文案。
  byStatus: {
    400: '请求参数有误',
    401: '未授权，请重新登录',
    403: '无权访问',
    404: '资源不存在',
    409: '操作冲突',
    429: '请求过于频繁，请稍后重试',
    500: '服务器内部错误',
    502: '网关错误',
    503: '服务暂不可用',
    504: '网关超时',
  },
  network: '网络错误，请检查连接',
  unknown: '发生未知错误',
  /**
   * 渲染 API 错误的推荐方式：
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // 返回当前语种的本地化消息
   * 这里仅提供数据；解析逻辑见 useApiError.ts。
   */
}
