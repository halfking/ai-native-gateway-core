// requestLogsScanOutcome.spec.ts —— 「页码越界」与「服务端扫描故障」必须**分得开**（P1-1）
//
// ★ 这道门钉的是两个真实情形的**判定结果**：
//
//	① 正常分页溢出（页码越过最后一页）：后端回 **200 + items=[]**（count 仍是命中
//	   总数，admin/logs.go:1133 的第三项 scanErrCount=0 ⇒ 后端自己没把它当故障）
//	   ⇒ 前端**不得**报「服务端扫描故障」（旧判据 items 空 && count>0 ⇒ 误报）。
//	② 真扫描故障：后端回 **500 + "data scanning failed: found N records …"**
//	   （admin/logs.go:1133-1140）⇒ 前端**必须**当故障报出来（不许放过）。
//
// ★ 为什么两个方向都要钉：只钉正向断言（"页码越界不报错"）的话，
//   把判据改成「一律不报」也能过 ⇒ 漏报就复活了；只钉负向断言同理。
//
// ★ 判据与后端逐项对齐的依据见同目录 requestLogsScanOutcome.ts 的头注释
//   （后端三项条件：items 空 / count>0 / scanErrCount>0，第三项只在服务端可见）。

import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/_core'
import {
  isBackendScanFailure,
  resolveLogsPageOverflow,
  scanFailureCount,
} from './requestLogsScanOutcome'

describe('请求日志列表：页码越界 ≠ 服务端扫描故障', () => {
  it('① 正常分页溢出（200 + items 空 + count>0）⇒ 不判成扫描故障，并给出应跳回的页码', () => {
    // 后端在第 7 页、筛选后只剩 3 页（每页 50 条 ⇒ count=120 ⇒ 最后一页=3）
    const target = resolveLogsPageOverflow({
      itemsLength: 0,
      count: 120,
      page: 7,
      pageSize: 50,
    })
    expect(target).toBe(3)
    // 200 响应里**没有**任何服务端故障信号 ⇒ 不该被判成扫描故障
    expect(isBackendScanFailure(new Error('ok'))).toBe(false)
  })

  it('② 真扫描故障（500 + "data scanning failed"）⇒ 判成扫描故障，并取得到命中记录数', () => {
    const err = new ApiError(
      500,
      'data scanning failed: found 66235 records but failed to parse any rows (scan errors: 3)',
    )
    expect(isBackendScanFailure(err)).toBe(true)
    expect(scanFailureCount(err)).toBe(66235)
  })

  it('③ 不是扫描故障的 500（"query failed"）⇒ 不判成扫描故障（不越权顶替服务端结论）', () => {
    const err = new ApiError(500, 'query failed')
    expect(isBackendScanFailure(err)).toBe(false)
    expect(scanFailureCount(err)).toBeNull()
  })

  it('④ 游标中途死亡（503，admin/logs.go:1122）⇒ 不判成扫描故障（那是另一类存储侧故障）', () => {
    const err = new ApiError(
      503,
      'request log list query interrupted before completion: received 0 of 66235 counted rows; storage-side error, retrying may help',
    )
    expect(isBackendScanFailure(err)).toBe(false)
  })

  it('⑤ 本页有行 / count=0 ⇒ 不是越界（后端正常返回）', () => {
    expect(resolveLogsPageOverflow({ itemsLength: 50, count: 120, page: 7, pageSize: 50 })).toBeNull()
    expect(resolveLogsPageOverflow({ itemsLength: 0, count: 0, page: 7, pageSize: 50 })).toBeNull()
    // 页码仍在范围内（count=120、pageSize=50 ⇒ 最后一页=3）⇒ 不用拉回
    expect(resolveLogsPageOverflow({ itemsLength: 0, count: 120, page: 3, pageSize: 50 })).toBeNull()
  })

  it('⑥ 越界判定与后端一致：后端不钳制页码 ⇒ page 越大越界、跳回的是最后一页', () => {
    // count=66235、pageSize=50 ⇒ 最后一页 = ceil(66235/50) = 1325（末页 35 行）
    expect(resolveLogsPageOverflow({ itemsLength: 0, count: 66235, page: 5000, pageSize: 50 })).toBe(1325)
  })
})
