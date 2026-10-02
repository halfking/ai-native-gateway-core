// usage.test.ts — usageTrendQs 序列化单测（2026-10-02 模型多选轮）。
// 锁两条契约：① 多选序列化为重复 model 参数（与后端 ANY 多选解析对称）；
// ② 单值/空值形态不回归（深链与请求都不携带空 model）。
import { describe, expect, it } from 'vitest'
import { usageTrendQs } from './usage'

const time = { start: '2026-09-26', end: '2026-10-02' }

describe('usageTrendQs model serialization', () => {
  it('multi-select serializes to repeated model params', () => {
    const qs = usageTrendQs({ time, model: ['model-a', 'model-b'] })
    expect(qs.getAll('model')).toEqual(['model-a', 'model-b'])
  })

  it('single string stays a single param', () => {
    const qs = usageTrendQs({ time, model: 'model-a' })
    expect(qs.getAll('model')).toEqual(['model-a'])
  })

  it('empty array / empty string omit model entirely', () => {
    expect(usageTrendQs({ time, model: [] }).get('model')).toBeNull()
    expect(usageTrendQs({ time, model: '' }).get('model')).toBeNull()
    expect(usageTrendQs({ time }).get('model')).toBeNull()
  })

  it('multi-select with empty members drops them', () => {
    const qs = usageTrendQs({ time, model: ['model-a', '', 'model-b'] })
    expect(qs.getAll('model')).toEqual(['model-a', 'model-b'])
  })
})
