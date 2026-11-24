// logsLoadingCopy.spec.ts —— 请求日志页加载态文案的分档边界。
import { describe, expect, it } from 'vitest'
import {
  LOGS_LOADING_HINT_AFTER_SEC,
  LOGS_LOADING_SLOW_AFTER_SEC,
  logsLoadingCopy,
  type LogsTranslate,
} from './logsLoadingCopy'

/**
 * 真实的 zh-CN 词条（与 locales/zh-CN/requests.ts 的 `list.loading.*` 逐字相同）——
 * 断言落在中国用户实际看到的文案上；同时另有一道门保证 8 个语种都定义了这些键。
 */
import zhCN from '../locales/zh-CN/requests'
import enUS from '../locales/en-US/requests'

/** 最小翻译函数：取词条并代入 `{name}` 参数。 */
function makeT(messages: Record<string, unknown>): LogsTranslate {
  return (key, params) => {
    const raw = key.split('.').reduce<any>((o, k) => (o == null ? undefined : o[k]), messages)
    if (typeof raw !== 'string') return `<missing:${key}>`
    return raw.replace(/\{(\w+)\}/g, (_m, name) => String((params ?? {})[name] ?? ''))
  }
}
const tZh = makeT({ requests: zhCN })
const tEn = makeT({ requests: enUS })

describe('logsLoadingCopy（按已等待秒数分档）', () => {
  it('刚发起（0~2 秒）：只说在加载，不给建议', () => {
    for (const s of [0, 1, LOGS_LOADING_SLOW_AFTER_SEC - 1]) {
      const copy = logsLoadingCopy(s, tZh)
      expect(copy.title, `${s}s`).toBe('正在加载请求日志…')
      expect(copy.hint, `${s}s`).toBeNull()
    }
  })

  it('3~9 秒：提示数据量较大，仍不给建议', () => {
    for (const s of [LOGS_LOADING_SLOW_AFTER_SEC, 5, LOGS_LOADING_HINT_AFTER_SEC - 1]) {
      const copy = logsLoadingCopy(s, tZh)
      expect(copy.title, `${s}s`).toContain('数据量较大')
      expect(copy.hint, `${s}s`).toBeNull()
    }
  })

  it('10 秒起：给出处置建议（缩小时间范围 / 减少过滤条件）', () => {
    const copy = logsLoadingCopy(LOGS_LOADING_HINT_AFTER_SEC, tZh)
    expect(copy.title).toContain('仍在查询')
    expect(copy.hint).toContain('缩小时间范围')
    expect(copy.hint).toContain('减少过滤条件')
    // 业务口径：不建议关闭一个没开着的开关。
    expect(copy.hint).not.toContain('包含探测流量')
  })

  it('含探测时，建议里多一条「关闭包含探测流量」', () => {
    const copy = logsLoadingCopy(12, tZh, true)
    expect(copy.hint).toContain('关闭「包含探测流量」')
    // 参数确实被代入（而不是把 {probe} 原样留在文案里）。
    expect(copy.hint).not.toContain('{probe}')
  })

  it('边界两侧的档位不同（阈值本身有判别力，不是写死的常数文案）', () => {
    expect(logsLoadingCopy(LOGS_LOADING_SLOW_AFTER_SEC - 1, tZh).title).not.toBe(
      logsLoadingCopy(LOGS_LOADING_SLOW_AFTER_SEC, tZh).title,
    )
    expect(logsLoadingCopy(LOGS_LOADING_HINT_AFTER_SEC - 1, tZh).hint).toBeNull()
    expect(logsLoadingCopy(LOGS_LOADING_HINT_AFTER_SEC, tZh).hint).not.toBeNull()
  })

  it('分档只取决于已等待秒数，不按查询参数预判「可能需要 1-2 分钟」', () => {
    for (const s of [0, 5, 30, 120]) {
      for (const probe of [false, true]) {
        const copy = logsLoadingCopy(s, tZh, probe)
        expect(`${copy.title}${copy.hint ?? ''}`).not.toMatch(/1-2\s*分钟|可能需要/)
      }
    }
  })

  it('非法输入（负数 / NaN / Infinity）按 0 秒处理，不产出奇怪文案', () => {
    for (const bad of [-1, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(logsLoadingCopy(bad, tZh).title).toBe('正在加载请求日志…')
      expect(logsLoadingCopy(bad, tZh).hint).toBeNull()
    }
    // 小数向下取整：2.9 秒仍是第一档。
    expect(logsLoadingCopy(2.9, tZh).title).toBe('正在加载请求日志…')
  })

  it('文案来自词条而不是函数体：换语种后分档不变、语言随词条走', () => {
    expect(logsLoadingCopy(0, tEn).title).toBe('Loading request logs…')
    expect(logsLoadingCopy(4, tEn).title).toContain('large')
    expect(logsLoadingCopy(11, tEn, true).hint).toContain('Include probe traffic')
  })

  it('用到的词条键在 zh-CN / en-US 都真实存在（防键名漂移成 <missing:…>）', () => {
    const keys = [
      'requests.list.loading.title',
      'requests.list.loading.slow',
      'requests.list.loading.still',
      'requests.list.loading.hint',
      'requests.list.loading.probeSuffix',
      'requests.list.loading.elapsed',
      'requests.list.queryFailedRow',
    ]
    for (const key of keys) {
      for (const t of [tZh, tEn]) {
        expect(t(key, { n: 1, probe: '' }), key).not.toContain('<missing:')
      }
    }
    // 「已等待 N 秒」的参数名是 n（视图里传的就是 { n }）。
    expect(tZh('requests.list.loading.elapsed', { n: 7 })).toBe('已等待 7 秒')
  })
})
