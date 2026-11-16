// useModelCredibility.ts —— 模型指纹可信度的**前端关联**（2026-11-16，live2）
//
// ★ 为什么走前端 join 而不是后端在实时帧上带字段：
//   可信度是 **(凭据, 上游原始模型名)** 维度的判定（admin 的
//   /api/admin/model-credibility/list 一行 = 一个 (credential_id,
//   raw_model_name) 组合），而实时帧每来一条请求就查一次 DB ⇒ N+1。
//   改成「页面挂载时拉一次全量清单 + 客户端按关联键 join」：
//     · 请求路径零 DB 开销；
//     · 关联不上就显示「未测量」，**不猜**。
//
// ★ 关联键 = `(credential_id, raw_model_name)`：
//   · credential_id —— 实时帧的 provider 凭据 id（admin.LiveRequest.CredentialID）；
//   · raw_model_name —— `upstream_request` 动作 detail 的 `raw_model`
//     （= candidate.OfferRawModel || candidate.RawModel，**实际下发上游的
//     模型串**；可信度扫描测的就是这一个串）。
//
// ★ 诚实性纪律：
//   · 端点是 super_admin 门；租户管理员拉不到 ⇒ 显示「未测量」，不显示 0 分；
//   · `system_fingerprint` 在生产上非空率约 0（211 实测）⇒ 空值显式显示
//     「未提供」，不编一个指纹；
//   · verdict=unknown 与「没这条记录」是**两件事**：前者是「测了，判定不了」，
//     后者是「没测到」。

import { ref, type Ref } from 'vue'
import {
  getModelCredibilityList,
  type ModelCredibilityRow,
} from '../api/model-credibility'

/** 关联键。 */
export function credibilityKey(credentialId: number | null, rawModel: string): string {
  return `${credentialId ?? 0}::${rawModel}`
}

export interface ModelCredibilityState {
  /** 关联表（键 = credibilityKey）。 */
  rows: Ref<Map<string, ModelCredibilityRow>>
  /** 是否已完成首次加载（未完成时显示「未测量」，不是 0）。 */
  loaded: Ref<boolean>
  /** 端点不可用（非 super_admin / 后端错误）⇒ 关联不到任何行。 */
  unavailable: Ref<boolean>
  /** 拉一次全量清单。失败静默降级（unavailable=true）。 */
  load: () => Promise<void>
  /** 按关联键取一行；取不到返回 null（调用方显示「未测量」）。 */
  get: (credentialId: number | null, rawModel: string) => ModelCredibilityRow | null
}

export function useModelCredibility(): ModelCredibilityState {
  const rows = ref<Map<string, ModelCredibilityRow>>(new Map())
  const loaded = ref(false)
  const unavailable = ref(false)

  async function load(): Promise<void> {
    try {
      const list = await getModelCredibilityList({ limit: 500 })
      const map = new Map<string, ModelCredibilityRow>()
      for (const r of list ?? []) {
        map.set(credibilityKey(r.credential_id, r.raw_model_name), r)
      }
      rows.value = map
      unavailable.value = false
    } catch {
      // 端点是 super_admin 门；非超管必然失败 ⇒ 降级为「全部未测量」。
      rows.value = new Map()
      unavailable.value = true
    } finally {
      loaded.value = true
    }
  }

  function get(credentialId: number | null, rawModel: string): ModelCredibilityRow | null {
    if (!rawModel) return null
    return rows.value.get(credibilityKey(credentialId, rawModel)) ?? null
  }

  return { rows, loaded, unavailable, load, get }
}
