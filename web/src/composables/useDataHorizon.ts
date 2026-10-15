// useDataHorizon.ts — 全平台「流量数据地平线」告示的状态（2026-10-15 r1033 重写）。
//
// # 三态，不是两态
//
//	unknown   还没取到（页面刚起来的几毫秒）
//	healthy   取到了，断流未发生（含网关空闲） ⇒ 不显示
//	stale     取到了，确证断流（网关有流量而 session_turns 未记录） ⇒ 显示横幅
//	failed    取失败 ⇒ **显示横幅，且说明是取不到**（不是当成 healthy）
//
// 「取不到」被归到 healthy 那一支是这里唯一危险的写法：断流期间告示端点
// 恰好挂了，页面会照常展示滞后数字，而**没有任何东西在提示**。
// 这与 stale 的定义是同一个失效形态（没有错误信号），告示不能继承它。
import { ref, readonly } from 'vue'
import { getDataHorizon, type DataHorizonNotice } from '../api/dataHorizon'

export type DataHorizonState = 'unknown' | 'healthy' | 'stale' | 'failed'

const state = ref<DataHorizonState>('unknown')
const notice = ref<DataHorizonNotice | null>(null)
let inflight: Promise<void> | null = null

async function load(): Promise<void> {
  try {
    const res = await getDataHorizon()
    // ★ 判据是「键**存在**且为 null」才算 healthy。
    //
    // 后端契约里该键**永远存在**（健康时值为 null），所以「键不存在」
    // 只能意味着契约被破坏或中间层改写了响应 —— 两种都必须按「可能断流」
    // 处理，而不是当成 healthy。
    if (res == null || !('data_horizon' in res)) {
      notice.value = null
      state.value = 'stale'
      return
    }
    const n = res.data_horizon ?? null
    notice.value = n
    if (n === null) {
      state.value = 'healthy'
      return
    }
    // ★ 必须**先判 unknown 再判 stale**：unknown 态的 `stale` 是 false，
    // 只写「对象存在 ⇒ 断流」会把「不知道」显示成「确知断流」——
    // 那是反向的编造。unknown 归 failed 分支（取不到 ≠ 健康也 ≠ 断流，
    // 横幅文案会说清是「无法确认」）。
    if (n.unknown) {
      state.value = 'failed'
      return
    }
    if (n.stale) {
      state.value = 'stale'
      return
    }
    // 后端给了对象、既非 unknown 也非 stale：契约被破坏。
    // ⚠ 归 failed（=「取不到」）而不是 stale：报 stale 是在编造事实。
    state.value = 'failed'
  } catch {
    // 取不到 ≠ 没断流。归到 healthy 会让断流期间的页面照常展示滞后数字。
    notice.value = null
    state.value = 'failed'
  }
}

/**
 * 拉一次告示。并发调用共享同一个 in-flight 请求
 * （App.vue 与横幅组件都会调它，重复请求既浪费也会让两个组件短暂不同步）。
 */
export function refreshDataHorizon(): Promise<void> {
  if (!inflight) {
    inflight = load().finally(() => {
      inflight = null
    })
  }
  return inflight
}

export function useDataHorizon() {
  return {
    state: readonly(state),
    notice: readonly(notice),
    /**
     * 未知态（页面刚起来）**不显示**：闪一条「数据断流」比不闪更糟，
     * 它会让人学会忽略这条横幅。
     *
     * ⚠ `failed`（含后端 unknown 态）**必须**显示：它与 stale 分开成两个
     * 分支——合并成一个就会让 unknown 判据变成冗余（删掉它门仍然全绿），
     * 而那个分支正是「不知道 ≠ 健康」这一纪律的全部落点。
     */
    shouldShowBanner: () => state.value === 'stale' || state.value === 'failed',
    refresh: refreshDataHorizon,
  }
}

/** 仅供测试：复位模块级单例状态。 */
export function __resetDataHorizonForTests(): void {
  state.value = 'unknown'
  notice.value = null
  inflight = null
}
