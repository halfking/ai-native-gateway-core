import { describe, expect, it } from 'vitest'
import {
  formatCompactDateTime,
  formatDateTime,
  formatDateTimeIso,
  formatRelativeTime,
  formatTimeOnly,
  parseLocalMinute,
} from './datetime'

// 固定时区无关的构造：全部用本地时间 Date 再喂给被测函数。
const d = (s: string) => new Date(s)

describe('formatDateTime', () => {
  it('空值返回默认哨兵 —', () => {
    expect(formatDateTime(null)).toBe('—')
    expect(formatDateTime(undefined)).toBe('—')
    expect(formatDateTime('')).toBe('—')
    expect(formatDateTime('   ')).toBe('—')
  })

  it('空值哨兵可覆盖', () => {
    expect(formatDateTime(null, { empty: '-' })).toBe('-')
  })

  it('不可解析字符串原样返回', () => {
    expect(formatDateTime('not-a-date')).toBe('not-a-date')
  })

  it('无效数字返回哨兵', () => {
    expect(formatDateTime(Number.NaN)).toBe('—')
  })

  it('合法时间走 toLocaleString（与仓内主格式一致）', () => {
    const date = d('2026-09-09T08:30:00')
    expect(formatDateTime(date)).toBe(date.toLocaleString())
    expect(formatDateTime(date, { locale: 'zh-CN' })).toBe(date.toLocaleString('zh-CN'))
  })

  it('options 透传 Intl', () => {
    const date = d('2026-09-09T08:30:00')
    expect(formatDateTime(date, {
      locale: 'zh-CN',
      options: { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' },
    })).toBe(
      date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }),
    )
  })
})

describe('formatTimeOnly', () => {
  it('仅时间部分', () => {
    const date = d('2026-09-09T08:30:00')
    expect(formatTimeOnly(date, { locale: 'en-US' })).toBe(
      date.toLocaleTimeString('en-US'),
    )
  })
  it('空值哨兵', () => {
    expect(formatTimeOnly('', { empty: '—' })).toBe('—')
  })
})

describe('formatCompactDateTime', () => {
  it('MM-DD HH:mm', () => {
    const out = formatCompactDateTime(d('2026-09-09T08:05:00'))
    expect(out).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}$/)
  })
  it('withSeconds 追加秒', () => {
    const out = formatCompactDateTime(d('2026-09-09T08:05:07'), { withSeconds: true })
    expect(out).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })
  it('自定义 empty（CredentialMonitorView 刷新场景）', () => {
    expect(formatCompactDateTime(null, { empty: '尚未刷新' })).toBe('尚未刷新')
  })
})

describe('formatDateTimeIso', () => {
  it('YYYY-MM-DD HH:mm 确定性输出', () => {
    const out = formatDateTimeIso(d('2026-01-05T09:07:00'))
    expect(out).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  })
})

describe('parseLocalMinute（2026-09-30 审计 P3-3：无秒日期串补秒走规范解析）', () => {
  it('空格分隔与 T 分隔的分钟串等价，均按本地时区解析', () => {
    expect(parseLocalMinute('2026-09-30 12:00').getTime()).toBe(new Date(2026, 8, 30, 12, 0, 0).getTime())
    expect(parseLocalMinute('2026-09-30T12:00').getTime()).toBe(new Date(2026, 8, 30, 12, 0, 0).getTime())
  })

  it('已带秒的串与 date-only 串原样通过', () => {
    expect(parseLocalMinute('2026-09-30T12:00:00').getTime()).toBe(new Date(2026, 8, 30, 12, 0, 0).getTime())
    // date-only 本身是规范格式：按 UTC 零点解析（与 Date.parse 直通一致）
    expect(parseLocalMinute('2026-09-30').getTime()).toBe(Date.parse('2026-09-30'))
    const iso = '2026-09-30T12:00:00.500Z'
    expect(parseLocalMinute(iso).getTime()).toBe(Date.parse(iso))
  })

  it('脏输入产生 Invalid Date（调用方按 NaN 分支处理）', () => {
    expect(Number.isNaN(parseLocalMinute('garbage').getTime())).toBe(true)
  })
})

describe('formatRelativeTime', () => {
  const labels = {
    justNow: '刚刚',
    minutesAgo: (n: number) => `${n}分钟前`,
    hoursAgo: (n: number) => `${n}小时前`,
    daysAgo: (n: number) => `${n}天前`,
  }

  it('空/脏输入返回空串', () => {
    expect(formatRelativeTime(undefined, labels)).toBe('')
    expect(formatRelativeTime('not-a-date', labels)).toBe('')
    expect(formatRelativeTime('1970-01-01T00:00:00Z', labels)).toBe('')
  })

  it('分级渲染', () => {
    const now = Date.now()
    expect(formatRelativeTime(new Date(now - 30_000).toISOString(), labels)).toBe('刚刚')
    expect(formatRelativeTime(new Date(now - 5 * 60_000).toISOString(), labels)).toBe('5分钟前')
    expect(formatRelativeTime(new Date(now - 3 * 3600_000).toISOString(), labels)).toBe('3小时前')
    expect(formatRelativeTime(new Date(now - 2 * 24 * 3600_000).toISOString(), labels)).toBe('2天前')
  })

  it('older 回调：超过 7 天回退绝对时间（ApprovalListView 形态）', () => {
    const withOlder = {
      ...labels,
      older: (x: Date) => `abs:${x.getFullYear()}`,
    }
    const iso = new Date(Date.now() - 30 * 24 * 3600_000).toISOString()
    expect(formatRelativeTime(iso, withOlder)).toMatch(/^abs:\d{4}$/)
  })

  it('无 older 时远期仍走 daysAgo 钳制内', () => {
    const iso = new Date(Date.now() - 100 * 24 * 3600_000).toISOString()
    expect(formatRelativeTime(iso, labels)).toBe('100天前')
  })
})
