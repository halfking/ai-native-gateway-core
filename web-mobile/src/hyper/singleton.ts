import type { Router } from 'vue-router'
import { HyperRuntime, type HyperRuntimeOptions } from './runtime'

// Hyper 单例（06 §8 facade）。拆出本模块避免 useHyperPage/useHyperOverlay
// ↔ index 的循环导入；runtime.ts 保持无单例依赖、可独立实例化测试。
export const Hyper = new HyperRuntime()

export function initHyper(router: Router, options: HyperRuntimeOptions): void {
  Hyper.install(router, options)
}

export function resetHyperForTests(): void {
  Hyper.resetForTests()
}
