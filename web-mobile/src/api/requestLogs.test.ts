import { describe, it, expect } from 'vitest'
import {
  maxWindowHours,
  costNumber,
  REQUEST_LOG_MAX_PAGE_SIZE,
  TENANT_MAX_WINDOW_HOURS,
  DEFAULT_MAX_WINDOW_DAYS,
} from './requestLogs'

/**
 * 请求日志 API 层（2026-10-06）。
 *
 * ★ 本组判据针对一个**读源码才看得到**的静默行为：后端会**擅自收窄时间窗**。
 *   admin/logs.go:476 → clampQueryWindowForTenant（:1336-1341）：
 *     · 非 default 租户：跨度 > 72h ⇒ 收窄到「以 end 为锚回推 72h」
 *     · default 租户 / 空 tenant：跨度 > 366 天 ⇒ 同样收窄
 *   **响应里没有任何字段说明窗口被改过**，count 也只是「被改过之后的窗口内」的计数。
 *   ⇒ 用户选「7 天」在非 default 租户上只拿到 3 天，且看起来像「最近 3 天真的没请求」。
 *   移动端的修法是**不提供超限选项**（见 RequestsLogsView 的 availableRanges），
 *   而这要求 maxWindowHours 与后端判定**逐字对齐**。
 */
describe('maxWindowHours —— 必须与后端 clampQueryWindowForTenant 对齐', () => {
  it('后端常量原样', () => {
    expect(TENANT_MAX_WINDOW_HOURS).toBe(72)
    expect(DEFAULT_MAX_WINDOW_DAYS).toBe(366)
    expect(REQUEST_LOG_MAX_PAGE_SIZE).toBe(500)
  })

  it('非 default 租户 → 72h', () => {
    expect(maxWindowHours('tenant-a')).toBe(72)
    expect(maxWindowHours('kx_tenant_01')).toBe(72)
  })

  it('default 租户 → 366 天档', () => {
    expect(maxWindowHours('default')).toBe(DEFAULT_MAX_WINDOW_DAYS * 24)
  })

  it('空 / null / undefined → default 档（后端判定是 tenantID != "" && != "default"，logs.go:1337）', () => {
    expect(maxWindowHours('')).toBe(DEFAULT_MAX_WINDOW_DAYS * 24)
    expect(maxWindowHours(null)).toBe(DEFAULT_MAX_WINDOW_DAYS * 24)
    expect(maxWindowHours(undefined)).toBe(DEFAULT_MAX_WINDOW_DAYS * 24)
  })

  it('★ 空白串等同空串（后端 handler.go 的 GetTenantID 不会返回带空格的）', () => {
    // 这里测的是我们自己的 trim —— 若漏了 trim，'  ' 会被判成非 default
    // 而错误地收窄到 72h，让用户看不到本该可见的数据。
    expect(maxWindowHours('   ')).toBe(DEFAULT_MAX_WINDOW_DAYS * 24)
  })
})

/**
 * costNumber —— 成本字段归一。
 *
 * 后端 cost_usd / cost_display **可能是 number 也可能是 string**（列存路径按文本返回，
 * 见桌面 web/src/api/logs.ts 对同名字段的注释）。直接 `.toFixed` 会在其中一种形态上：
 *   · number 形态正常
 *   · string 形态抛 TypeError（string 没有 toFixed）
 * 或者若用 `String(v).toFixed` 则在 number 上抛。
 * ⇒ 必须先 Number()，且非数值返回 null（而不是 NaN —— NaN 会被渲染成 "NaN"）。
 */
describe('costNumber', () => {
  it('number 直接透传', () => {
    expect(costNumber(0.0123)).toBe(0.0123)
    expect(costNumber(0)).toBe(0)
  })

  it('string 形态能解析（列存路径的真实形态）', () => {
    expect(costNumber('0.0123')).toBeCloseTo(0.0123, 10)
    expect(costNumber('0')).toBe(0)
  })

  it('null / undefined → null（缺数据不是 0）', () => {
    expect(costNumber(null)).toBeNull()
    expect(costNumber(undefined)).toBeNull()
  })

  it('非数值字符串 → null，绝不返回 NaN', () => {
    expect(costNumber('abc')).toBeNull()
    expect(costNumber('')).toBeNull()
    // ★ 反向判据：若改成透传，视图会渲染出字面量 "NaN"
    expect(costNumber('abc')).not.toBeNaN()
  })
})
