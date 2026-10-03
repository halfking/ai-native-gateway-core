// useV1DataHorizon.ts — 全平台「v1 数据已停更」告示的状态（审计 §9.73.7）。
//
// # 三态，不是两态
//
//	unknown   还没取到（页面刚起来的几毫秒）
//	live      取到了，且未停更 ⇒ 不显示
//	frozen    取到了，已停更 ⇒ 显示横幅
//	failed    取失败 ⇒ **显示横幅，且说明是取不到**（不是当成 live）
//
// 「取不到」被归到 live 那一支是这里唯一真正危险的写法：停写期间告示端点
// 恰好挂了，页面会照常展示停写前的数字，而**没有任何东西在提示**。
// 这与 silently_frozen 那一档的定义是同一个失效形态（没有错误信号），
// 所以告示本身不能继承它。
import { ref, readonly } from 'vue'
import { getV1DataHorizon, type V1DataHorizonNotice } from '../api/v1DataHorizon'

export type V1HorizonState = 'unknown' | 'live' | 'frozen' | 'failed' | 'unconfirmed'

const state = ref<V1HorizonState>('unknown')
const notice = ref<V1DataHorizonNotice | null>(null)
let inflight: Promise<void> | null = null

async function load(): Promise<void> {
  try {
    const res = await getV1DataHorizon()
    // ★ 判据是「键**存在**且为 null」才算 live。
    //
    // 第一稿写的是 `res?.v1_data_horizon ?? null` —— 后端哪天把整个键省掉时，
    // `??` 把它变成 null，于是状态变成 live：停写期间页面照常展示过期数字，
    // 而没有任何东西在提示。**这正是这道 composable 要防的失效形态，
    // 而我自己第一稿就犯了。**（被 useV1DataHorizon.test.ts 的第 5 个用例当场抓住。）
    //
    // 后端契约里该键**永远存在**（未冻结时值为 null），所以「键不存在」
    // 只能意味着契约被破坏或中间层改写了响应 —— 两种都必须按「可能已停更」处理。
    if (res == null || !('v1_data_horizon' in res)) {
      notice.value = null
      state.value = 'frozen'
      return
    }
    const n = res.v1_data_horizon ?? null
    notice.value = n
    if (n === null) {
      state.value = 'live'
      return
    }
    // ★ unknown（§9.79.8）：后端读到的是**回落值**，不是有人在 DB 里设的值。
    // 它归到 failed 而不是 live —— 归 live 就是把「不知道」显示成「知道」，
    // 而这正是 silently_frozen 那一档的失效形态。
    // ⚠ 必须**先判 unknown 再判 frozen**：unknown 态的 `frozen` 是 false，
    // 而本函数第一版只写 `notice !== null ⇒ frozen`，会把 unknown 显示成
    // 「已停更」—— 那是反向的另一种编造。
    if (n.unknown) {
      state.value = 'unconfirmed'
      return
    }
    if (n.frozen) {
      state.value = 'frozen'
      return
    }
    // 后端给了对象、既非 unknown 也非 frozen：契约被破坏。
    // ⚠ 归 failed（=「取不到」）而不是 frozen：报 frozen 是在编造事实。
    state.value = 'failed'
  } catch {
    // 取不到 ≠ 未停更。归到 live 会让停写期间的页面照常展示过期数字。
    notice.value = null
    state.value = 'failed'
  }
}

/**
 * 拉一次告示。并发调用共享同一个 in-flight 请求
 * （App.vue 与横幅组件都会调它，重复请求既浪费也会让两个组件短暂不同步）。
 */
export function refreshV1DataHorizon(): Promise<void> {
  if (!inflight) {
    inflight = load().finally(() => {
      inflight = null
    })
  }
  return inflight
}

export function useV1DataHorizon() {
  return {
    state: readonly(state),
    notice: readonly(notice),
    /**
     * 未知态不显示横幅：页面刚起来的那几毫秒闪一条「数据已停更」比不闪更糟。
     *
     * ⚠ `unconfirmed`（§9.79.8）**必须**显示横幅：后端读到的是回落值，
     * 不是有人在 DB 里设的值。它与 `failed` 刻意分成两个状态 ——
     * 合并成一个就会让「unknown 判据」变成冗余分支（删掉它门仍然全绿，
     * 变异 F4 实录），而那个分支正是本节存在的唯一理由。
     */
    shouldShowBanner: () =>
      state.value === 'frozen' || state.value === 'failed' || state.value === 'unconfirmed',
    refresh: refreshV1DataHorizon,
  }
}

/** 仅供测试：复位模块级单例状态。 */
export function __resetV1DataHorizonForTests(): void {
  state.value = 'unknown'
  notice.value = null
  inflight = null
}
