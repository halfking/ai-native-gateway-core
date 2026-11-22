// saQueryParams.spec.ts —— 会话分析族的 URL 深链参数读取口径（2026-11-23 审计轮 P1-3 / P2-5）
//
// ★ 这道门钉的是「带参数的链接进来必须被认出来」：
//   · ?start_date=…&end_date=… ⇒ 取该区间（否则三个排行页一律回退 7 天窗口，
//     用户点的区间与看到的不是同一区间 ⇒ P2-5）；
//   · ?task=… ⇒ 取该任务过滤值（否则点 Top5 任务行跳过去什么也没发生 ⇒ P1-3）。
// ★ 同时钉住**非法输入不外泄**：半个区间 / 非法格式 / 顺序颠倒 ⇒ 当作没带
//   （回退默认窗口），不把非法值发给后端换 400。
import { describe, expect, it } from 'vitest'
import { rangeFromQuery, readQueryString, taskFilterFromQuery } from './saQueryParams'

describe('会话分析深链参数读取', () => {
  it('① 合法 start_date/end_date 成对出现 ⇒ 取该区间', () => {
    expect(rangeFromQuery({ start_date: '2026-09-01', end_date: '2026-09-10' })).toEqual({
      start: '2026-09-01',
      end: '2026-09-10',
    })
  })

  it('② 缺一半 / 非法格式 / 顺序颠倒 ⇒ null（调用方回退默认窗口）', () => {
    expect(rangeFromQuery({ start_date: '2026-09-01' })).toBeNull()
    expect(rangeFromQuery({ end_date: '2026-09-10' })).toBeNull()
    expect(rangeFromQuery({ start_date: '2026/09/01', end_date: '2026-09-10' })).toBeNull()
    expect(rangeFromQuery({ start_date: '2026-09-10', end_date: '2026-09-01' })).toBeNull()
    expect(rangeFromQuery({})).toBeNull()
    expect(rangeFromQuery(null)).toBeNull()
  })

  it('③ ?task= 取任务过滤值；没有就是空串（无过滤）', () => {
    expect(taskFilterFromQuery({ task: 'build-report' })).toBe('build-report')
    expect(taskFilterFromQuery({})).toBe('')
    // 数组形态（?task=a&task=b）不认 ⇒ 后端没有「多任务」语义
    expect(taskFilterFromQuery({ task: ['a', 'b'] })).toBe('')
  })

  it('④ query 值只认单个字符串（trim 后）', () => {
    expect(readQueryString('  x ')).toBe('x')
    expect(readQueryString(['x'])).toBe('')
    expect(readQueryString(undefined)).toBe('')
  })
})
