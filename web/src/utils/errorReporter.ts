/**
 * 前端错误上报模块
 * 
 * 捕获未处理的异常和 Promise rejections，上报到后端日志系统。
 * 轻量级实现，无外部依赖，适合私有化部署。
 */

interface ErrorReport {
  type: 'error' | 'unhandledrejection' | 'vue-error'
  message: string
  stack?: string
  url: string
  timestamp: string
  userAgent: string
  componentName?: string
  propsData?: any
  extra?: Record<string, any>
}

let reportEndpoint = '/api/system/client-error'
let isEnabled = true
let maxReportsPerMinute = 10
let reportCount = 0
let lastResetTime = Date.now()

/**
 * 配置错误上报
 */
export function configureErrorReporter(options: {
  endpoint?: string
  enabled?: boolean
  maxReportsPerMinute?: number
}) {
  if (options.endpoint !== undefined) reportEndpoint = options.endpoint
  if (options.enabled !== undefined) isEnabled = options.enabled
  if (options.maxReportsPerMinute !== undefined) maxReportsPerMinute = options.maxReportsPerMinute
}

/**
 * 速率限制检查
 */
function shouldReport(): boolean {
  if (!isEnabled) return false
  
  const now = Date.now()
  if (now - lastResetTime > 60000) {
    reportCount = 0
    lastResetTime = now
  }
  
  if (reportCount >= maxReportsPerMinute) {
    console.warn('[ErrorReporter] Rate limit exceeded, dropping error report')
    return false
  }
  
  reportCount++
  return true
}

/**
 * 发送错误报告到后端
 */
async function sendReport(report: ErrorReport): Promise<void> {
  if (!shouldReport()) return
  
  try {
    // 使用 sendBeacon（如果可用）确保页面卸载时也能发送
    const payload = JSON.stringify(report)
    
    if (navigator.sendBeacon) {
      const blob = new Blob([payload], { type: 'application/json' })
      navigator.sendBeacon(reportEndpoint, blob)
    } else {
      // 降级到 fetch，设置较短超时
      await fetch(reportEndpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: payload,
        keepalive: true,
      }).catch(() => {
        // 静默失败，避免错误上报本身引发错误
      })
    }
  } catch (err) {
    // 静默失败
    console.warn('[ErrorReporter] Failed to send report:', err)
  }
}

/**
 * 构建错误报告
 */
function buildReport(
  type: ErrorReport['type'],
  error: Error | any,
  extra?: Record<string, any>
): ErrorReport {
  return {
    type,
    message: error?.message || String(error),
    stack: error?.stack,
    url: window.location.href,
    timestamp: new Date().toISOString(),
    userAgent: navigator.userAgent,
    extra,
  }
}

/**
 * 全局错误处理器
 */
function handleGlobalError(event: ErrorEvent): void {
  const report = buildReport('error', event.error || new Error(event.message), {
    filename: event.filename,
    lineno: event.lineno,
    colno: event.colno,
  })
  sendReport(report)
}

/**
 * Promise rejection 处理器
 */
function handleUnhandledRejection(event: PromiseRejectionEvent): void {
  const report = buildReport('unhandledrejection', event.reason)
  sendReport(report)
}

/**
 * Vue 错误处理器
 */
export function createVueErrorHandler() {
  return (err: unknown, instance: any, info: string) => {
    const error = err instanceof Error ? err : new Error(String(err))
    const report = buildReport('vue-error', error, {
      componentName: instance?.$options?.name || instance?.$options?.__name,
      propsData: instance?.$props,
      lifecycleHook: info,
    })
    sendReport(report)
    
    // 继续抛出错误到控制台
    console.error('[Vue Error]', err, instance, info)
  }
}

/**
 * 手动上报错误
 */
export function reportError(error: Error | string, extra?: Record<string, any>): void {
  const err = typeof error === 'string' ? new Error(error) : error
  const report = buildReport('error', err, extra)
  sendReport(report)
}

/**
 * 初始化错误上报
 */
export function initErrorReporter(): void {
  if (typeof window === 'undefined') return
  
  window.addEventListener('error', handleGlobalError)
  window.addEventListener('unhandledrejection', handleUnhandledRejection)
  
  console.info('[ErrorReporter] Initialized')
}

/**
 * 清理错误上报
 */
export function cleanupErrorReporter(): void {
  if (typeof window === 'undefined') return
  
  window.removeEventListener('error', handleGlobalError)
  window.removeEventListener('unhandledrejection', handleUnhandledRejection)
  
  console.info('[ErrorReporter] Cleaned up')
}
