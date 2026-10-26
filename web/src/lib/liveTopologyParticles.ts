// liveTopologyParticles.ts — 请求粒子池（纯 TS，2026-10-16）
//
// 一次请求在图上是一颗有身份的粒子：沿链路飞行 → 到达 → 消亡。
// 三条硬约束：
//  1. **时间基准用 dt（ms）而不是帧数** —— 掉帧时速度不变，否则慢机器上
//     粒子会「变慢」并给人「网关变慢了」的错误直觉；
//  2. **上限硬顶**：池满时丢最旧（同时计数，供 UI 说明「已聚合 N/s」）；
//  3. **不产生随机数**：横向偏移由 request_id 的稳定哈希派生，
//     便于单测断言，也避免刷新后粒子抖动。

import { TOPO_PARTICLE_CAP } from '../types/liveTopology'

export interface ParticleSpawn {
  id: string
  edgeKey: string
  status?: string
  tokens?: number | null
  isProbe?: boolean
  /** 实测分段耗时（ms）。缺省 → 用 durationMsMs 基线，并在 UI 标「示意」。 */
  measuredMs?: number
}

export interface Particle {
  id: string
  edgeKey: string
  /** 0..1 沿路径进度 */
  t: number
  durationMs: number
  measured: boolean
  status: string
  tokens: number
  isProbe: boolean
  /** 路径内横向偏移（-1..1），让并行请求不重叠成一条线 */
  lateral: number
  ageMs: number
}

export const BASELINE_SEGMENT_MS = 420

function hashUnit(input: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return (hash % 1000) / 1000
}

export class ParticlePool {
  private items: Particle[] = []
  private readonly cap: number
  private droppedCount = 0

  constructor(cap: number = TOPO_PARTICLE_CAP) {
    this.cap = Math.max(1, cap)
  }

  get size(): number {
    return this.items.length
  }

  /** 因超上限被丢弃的累计数（UI 用它说明「已聚合 N/s」）。 */
  get dropped(): number {
    return this.droppedCount
  }

  list(): readonly Particle[] {
    return this.items
  }

  spawn(spawn: ParticleSpawn): Particle | null {
    const durationMs = spawn.measuredMs && spawn.measuredMs > 0 ? spawn.measuredMs : BASELINE_SEGMENT_MS
    const particle: Particle = {
      id: spawn.id,
      edgeKey: spawn.edgeKey,
      t: 0,
      durationMs,
      measured: !!(spawn.measuredMs && spawn.measuredMs > 0),
      status: spawn.status ?? 'in_progress',
      tokens: spawn.tokens ?? 0,
      isProbe: !!spawn.isProbe,
      lateral: hashUnit(spawn.id) * 2 - 1,
      ageMs: 0,
    }
    this.items.push(particle)
    while (this.items.length > this.cap) {
      this.items.shift()
      this.droppedCount += 1
    }
    return particle
  }

  /** 推进 dt 毫秒；返回本次到达终点的粒子（用于触发终点闪光）。 */
  step(dtMs: number): Particle[] {
    const arrived: Particle[] = []
    if (dtMs <= 0) return arrived
    const keep: Particle[] = []
    for (const p of this.items) {
      p.t += dtMs / p.durationMs
      p.ageMs += dtMs
      if (p.t >= 1) {
        arrived.push(p)
      } else {
        keep.push(p)
      }
    }
    this.items = keep
    return arrived
  }

  removeByRequest(id: string): void {
    this.items = this.items.filter((p) => p.id !== id)
  }

  clear(): void {
    this.items = []
  }
}
