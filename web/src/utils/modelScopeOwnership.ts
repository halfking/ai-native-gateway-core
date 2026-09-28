// modelScopeOwnership.ts — 模型范围（featured + 热门）的别名归属裁决
//
// 为什么需要这个模块（2026-09-29 dashboard?tab=stream「按模型分组的可用节点」
// 实测复现：筛选 glm-5.3 却看不到 glm-5.3 分组，只看到一个冒名分组）：
//
// 1. /api/routing/resolve 的 raw_models 来自 modelname.NormalizeRouteKeyAliases
//    的**词法变体矩阵**，其中 removableWrapperTokens 会剥掉首尾包装词。
//    'glm-5.3-flash' 因此会把 'glm-5.3' 也报成自己的 raw model，candidates
//    里同样混进 glm-5.3 的凭据。这是词法层面的"逃生口"，不是模型等价关系。
// 2. 旧实现在每个 scope resolve 完成后直接 assignAlias（冲突就 delete），
//    归属取决于**并发 resolve 的完成顺序**（RESOLVE_CONCURRENCY=8）。
//    结果：真正拥有 canonical_id 的 glm-5.3 作用域被词法冒名者吞掉，
//    按模型筛选 glm-5.3 时命中的是冒名者（显示成 glm-5.3-flash / 其它冒名模型），
//    真正的 glm-5.3 分组则整个消失。
//
// 裁决规则（与到达顺序无关，纯函数、可单测）：
//   a. 同一个 canonical 身份的作用域先合并成一个代表作用域，避免
//      'glm-5.3-flash' 与 'z-ai/glm-5.3-flash' 各出一份同名分组；
//   b. 每个 raw model 名归属于「与该 raw 的候选凭据 canonical_id 一致」的作用域；
//      没有一致 canonical 时退回「有 canonical_id 的作用域」；
//      再退回「作用域名就是该 raw 本身」，最后按长度 + 字典序稳定兜底。
//      任何一步都不会再把 raw 从索引里删除 —— 删除会让节点无处归属。

export interface ModelScopeCandidateRef {
  /** Provider 侧原始模型名（v.raw_model_name），大小写敏感。 */
  modelName: string
  /** 该绑定所属 canonical（provider_models.canonical_id），可能为 NULL。 */
  canonicalId?: number | null
}

export interface ModelScopeResolution {
  /** 作用域 key（模型名小写归一后）。 */
  scopeKey: string
  /** resolve 返回的 canonical_id；混合 canonical 时为 null。 */
  canonicalId?: number | null
  /** resolve 返回的 canonical_name。 */
  canonicalName?: string | null
  rawModels?: readonly string[]
  candidates?: readonly ModelScopeCandidateRef[]
}

export interface ModelScopeOwnership {
  /** canonical 身份 → 代表作用域 key。 */
  representatives: Map<string, string>
  /** raw 模型名（归一化） → 归属作用域 key。 */
  aliasOwner: Map<string, string>
}

function norm(model: string | null | undefined): string {
  return (model ?? '').trim().toLowerCase()
}

function positiveCanonicalId(value: number | null | undefined): number | null {
  return typeof value === 'number' && value > 0 ? value : null
}

/**
 * Stable identity of a resolved scope. Scopes that share a canonical_id — or
 * that both lack one and report the same canonical_name — are the same model
 * and must not be split into several identically-titled groups.
 */
export function modelScopeIdentity(
  scopeKey: string,
  canonicalId?: number | null,
  canonicalName?: string | null,
): string {
  const id = positiveCanonicalId(canonicalId)
  if (id != null) return `canonical:${id}`
  const name = norm(canonicalName)
  if (name) return `name:${name}`
  return `key:${norm(scopeKey)}`
}

/**
 * Representative scope key per identity: the scope literally named after the
 * canonical wins (so the bare 'glm-5.3-flash' beats the provider-prefixed
 * 'z-ai/glm-5.3-flash'), then the shortest, then lexicographic — never the
 * order in which the concurrent resolves happened to finish.
 */
export function pickScopeRepresentatives(
  resolutions: readonly ModelScopeResolution[],
): Map<string, string> {
  const canonicalNameByIdentity = new Map<string, string>()
  for (const r of resolutions) {
    const identity = modelScopeIdentity(r.scopeKey, r.canonicalId, r.canonicalName)
    const name = norm(r.canonicalName)
    if (name) canonicalNameByIdentity.set(identity, name)
  }
  const representatives = new Map<string, string>()
  const rank = (scopeKey: string, identity: string): [number, number, string] => {
    const matchesName = norm(scopeKey) === canonicalNameByIdentity.get(identity) ? 0 : 1
    return [matchesName, scopeKey.length, scopeKey]
  }
  for (const r of resolutions) {
    const identity = modelScopeIdentity(r.scopeKey, r.canonicalId, r.canonicalName)
    const current = representatives.get(identity)
    if (current === undefined || compareRank(rank(r.scopeKey, identity), rank(current, identity)) < 0) {
      representatives.set(identity, r.scopeKey)
    }
  }
  return representatives
}

function compareRank(
  a: [number, number, string],
  b: [number, number, string],
): number {
  if (a[0] !== b[0]) return a[0] - b[0]
  if (a[1] !== b[1]) return a[1] - b[1]
  return a[2] < b[2] ? -1 : a[2] > b[2] ? 1 : 0
}

/**
 * Resolves which scope owns each raw model name. See the module comment for
 * why arrival order cannot decide this.
 */
export function resolveModelScopeOwnership(
  resolutions: readonly ModelScopeResolution[],
): ModelScopeOwnership {
  const representatives = pickScopeRepresentatives(resolutions)

  // Owner scope for every resolution (its identity's representative) plus the
  // canonical id that owner speaks for, used as the primary ownership signal.
  const ownerOf = new Map<string, string>()
  const canonicalIdOfOwner = new Map<string, number>()
  for (const r of resolutions) {
    const identity = modelScopeIdentity(r.scopeKey, r.canonicalId, r.canonicalName)
    const owner = representatives.get(identity) ?? r.scopeKey
    ownerOf.set(r.scopeKey, owner)
    const id = positiveCanonicalId(r.canonicalId)
    if (id != null && canonicalIdOfOwner.get(owner) == null) canonicalIdOfOwner.set(owner, id)
  }

  const claims = new Map<string, string[]>()
  const canonicalIdsByRaw = new Map<string, Set<number>>()
  const claim = (raw: string | null | undefined, owner: string) => {
    const key = norm(raw)
    if (!key) return
    const owners = claims.get(key)
    if (!owners) claims.set(key, [owner])
    else if (!owners.includes(owner)) owners.push(owner)
  }
  const recordCanonicalId = (raw: string | null | undefined, canonicalId: number | null | undefined) => {
    const key = norm(raw)
    const id = positiveCanonicalId(canonicalId)
    if (!key || id == null) return
    const ids = canonicalIdsByRaw.get(key) ?? new Set<number>()
    ids.add(id)
    canonicalIdsByRaw.set(key, ids)
  }

  for (const r of resolutions) {
    const owner = ownerOf.get(r.scopeKey) ?? r.scopeKey
    for (const raw of r.rawModels ?? []) claim(raw, owner)
    for (const candidate of r.candidates ?? []) {
      claim(candidate.modelName, owner)
      recordCanonicalId(candidate.modelName, candidate.canonicalId)
    }
  }

  const aliasOwner = new Map<string, string>()
  for (const [raw, owners] of claims) {
    const rawCanonicalIds = canonicalIdsByRaw.get(raw)
    // (b1) owner whose canonical matches what the raw's own bindings report.
    let pool = rawCanonicalIds && rawCanonicalIds.size > 0
      ? owners.filter(owner => {
        const id = canonicalIdOfOwner.get(owner)
        return id != null && rawCanonicalIds.has(id)
      })
      : []
    // (b2) any scope that actually resolved to a canonical beats pure lexical hits.
    if (pool.length === 0) pool = owners.filter(owner => canonicalIdOfOwner.has(owner))
    // (b3) deterministic fallback — a scope literally named after the raw wins.
    if (pool.length === 0) pool = [...owners]
    pool.sort((a, b) => compareRank([norm(a) === raw ? 0 : 1, a.length, a], [norm(b) === raw ? 0 : 1, b.length, b]))
    aliasOwner.set(raw, pool[0])
  }

  return { representatives, aliasOwner }
}
