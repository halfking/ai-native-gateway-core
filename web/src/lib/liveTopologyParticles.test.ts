// liveTopologyParticles.test.ts — 粒子池判据（2026-10-16）
//
// 三条真问题：dt 推进与帧率无关、池上限硬顶、到达即消亡。
// 另钉一条「不产生随机数」——横向偏移必须来自 id 哈希。

import { describe, expect, it } from 'vitest'
import { BASELINE_SEGMENT_MS, ParticlePool } from '../lib/liveTopologyParticles'

describe('ParticlePool', () => {
  it('advances by elapsed time, not by frame count', () => {
    const pool = new ParticlePool()
    pool.spawn({ id: 'a', edgeKey: 'client:a->gateway:core', measuredMs: 1000 })
    pool.step(500)
    expect(pool.list()[0].t).toBeCloseTo(0.5, 5)
    // 一次走完 500ms 与分 50 次走 10ms 必须等价（掉帧不改变速度）
    const slow = new ParticlePool()
    slow.spawn({ id: 'b', edgeKey: 'client:a->gateway:core', measuredMs: 1000 })
    for (let i = 0; i < 50; i += 1) slow.step(10)
    expect(slow.list()[0].t).toBeCloseTo(0.5, 5)
  })

  it('removes particles on arrival and reports them', () => {
    const pool = new ParticlePool()
    pool.spawn({ id: 'a', edgeKey: 'client:a->gateway:core', measuredMs: 100 })
    const arrived = pool.step(100)
    expect(arrived).toHaveLength(1)
    expect(arrived[0].id).toBe('a')
    expect(pool.size).toBe(0)
  })

  it('enforces the cap and counts what it dropped', () => {
    const pool = new ParticlePool(3)
    for (let i = 0; i < 6; i += 1) pool.spawn({ id: `r${i}`, edgeKey: 'client:a->gateway:core', measuredMs: 10_000 })
    expect(pool.size).toBe(3)
    expect(pool.dropped).toBe(3)
    // 超上限丢的是**最旧**的（最新请求必须还在图上）
    expect(pool.list().map((p) => p.id)).toEqual(['r3', 'r4', 'r5'])
  })

  it('marks measured vs schematic segments distinctly', () => {
    const pool = new ParticlePool()
    const measured = pool.spawn({ id: 'a', edgeKey: 'client:a->gateway:core', measuredMs: 250 })!
    const schematic = pool.spawn({ id: 'b', edgeKey: 'client:a->gateway:core' })!
    expect(measured.measured).toBe(true)
    expect(schematic.measured).toBe(false)
    expect(schematic.durationMs).toBe(BASELINE_SEGMENT_MS)
  })

  it('derives lateral offset deterministically from the request id', () => {
    const a = new ParticlePool().spawn({ id: 'same-id', edgeKey: 'client:a->gateway:core' })!
    const b = new ParticlePool().spawn({ id: 'same-id', edgeKey: 'client:a->gateway:core' })!
    expect(a.lateral).toBe(b.lateral)
    expect(a.lateral).toBeGreaterThanOrEqual(-1)
    expect(a.lateral).toBeLessThanOrEqual(1)
  })

  it('ignores non-positive dt (a paused tab must not teleport particles)', () => {
    const pool = new ParticlePool()
    pool.spawn({ id: 'a', edgeKey: 'client:a->gateway:core', measuredMs: 1000 })
    pool.step(0)
    pool.step(-50)
    expect(pool.list()[0].t).toBe(0)
  })
})
