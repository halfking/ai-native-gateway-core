import { describe, expect, it } from 'vitest'
import {
  modelScopeIdentity,
  resolveModelScopeOwnership,
  type ModelScopeResolution,
} from './modelScopeOwnership'

// 载荷取自 2026-09-29 llm.kxpms.cn 实测（dashboard?tab=stream 复现）：
//   GET /api/routing/resolve?model=glm-5.3
//     → canonical_id=2422803, raw_models=[glm-5.3, glm-5-3, z-ai/glm-5.3]
//   GET /api/routing/resolve?model=glm-5.3-flash
//     → canonical_id=null（候选跨了两个 canonical），raw_models 里混进了
//       'glm-5.3'/'glm-5-3' —— 这是 modelname.NormalizeRouteKeyAliases 剥掉
//       包装词 flash 的词法结果，不是模型等价关系。
const GLM_53: ModelScopeResolution = {
  scopeKey: 'glm-5.3',
  canonicalId: 2422803,
  canonicalName: 'glm-5.3',
  rawModels: ['glm-5.3', 'glm-5-3', 'z-ai/glm-5.3'],
  candidates: [
    { modelName: 'glm-5.3', canonicalId: 2422803 },
    { modelName: 'glm/5.3', canonicalId: 2422803 },
    { modelName: 'z-ai/glm-5.3', canonicalId: 2422803 },
  ],
}

const GLM_53_FLASH: ModelScopeResolution = {
  scopeKey: 'glm-5.3-flash',
  canonicalId: null,
  canonicalName: 'glm-5.3-flash',
  rawModels: ['glm-5.3-flash', 'glm-5-3-flash', 'glm-5.3', 'glm-5-3'],
  candidates: [
    { modelName: 'glm-5.3-flash', canonicalId: null },
    { modelName: 'glm-5-3-flash-260828', canonicalId: null },
    // 冒名者：词法变体把 glm-5.3 的凭据也拖了进来。
    { modelName: 'glm-5.3', canonicalId: 2422803 },
    { modelName: 'z-ai/glm-5.3-flash', canonicalId: null },
    { modelName: 'free/glm-5.3-flash', canonicalId: null },
  ],
}

const ZAI_GLM_53_FLASH: ModelScopeResolution = {
  scopeKey: 'z-ai/glm-5.3-flash',
  canonicalId: null,
  canonicalName: 'glm-5.3-flash',
  rawModels: ['glm-5.3-flash', 'glm-5-3-flash', 'glm-5.3', 'glm-5-3', 'z-ai/glm-5.3-flash'],
  candidates: [{ modelName: 'z-ai/glm-5.3-flash', canonicalId: null }],
}

describe('modelScopeOwnership', () => {
  it('gives a wrapper-suffixed scope its own raw models instead of swallowing the base model', () => {
    const { aliasOwner } = resolveModelScopeOwnership([GLM_53, GLM_53_FLASH])
    // 真模型归真作用域
    expect(aliasOwner.get('glm-5.3')).toBe('glm-5.3')
    expect(aliasOwner.get('glm-5-3')).toBe('glm-5.3')
    expect(aliasOwner.get('z-ai/glm-5.3')).toBe('glm-5.3')
    expect(aliasOwner.get('glm/5.3')).toBe('glm-5.3')
    // 包装词作用域只保留自己的 raw
    expect(aliasOwner.get('glm-5.3-flash')).toBe('glm-5.3-flash')
    expect(aliasOwner.get('glm-5-3-flash')).toBe('glm-5.3-flash')
    expect(aliasOwner.get('free/glm-5.3-flash')).toBe('glm-5.3-flash')
    expect(aliasOwner.get('z-ai/glm-5.3-flash')).toBe('glm-5.3-flash')
  })

  it('never drops a raw model from the index (no delete-on-conflict)', () => {
    const resolutions = [GLM_53, GLM_53_FLASH, ZAI_GLM_53_FLASH]
    const expected = new Set(
      resolutions.flatMap(r => [
        ...(r.rawModels ?? []),
        ...(r.candidates ?? []).map(c => c.modelName),
      ]).map(m => m.toLowerCase()),
    )
    const { aliasOwner } = resolveModelScopeOwnership(resolutions)
    for (const raw of expected) {
      expect(aliasOwner.has(raw), `raw ${raw} must have an owner`).toBe(true)
    }
  })

  it('is independent of the order concurrent resolves happen to finish in', () => {
    const resolutions = [GLM_53, GLM_53_FLASH, ZAI_GLM_53_FLASH]
    const permutations = [
      resolutions,
      [ZAI_GLM_53_FLASH, GLM_53_FLASH, GLM_53],
      [GLM_53_FLASH, ZAI_GLM_53_FLASH, GLM_53],
      [GLM_53_FLASH, GLM_53, ZAI_GLM_53_FLASH],
    ]
    const baseline = resolveModelScopeOwnership(permutations[0]).aliasOwner
    for (const permutation of permutations.slice(1)) {
      const actual = resolveModelScopeOwnership(permutation).aliasOwner
      expect([...actual.entries()].sort()).toEqual([...baseline.entries()].sort())
    }
  })

  it('merges scopes that resolve to the same canonical into one representative', () => {
    const { representatives } = resolveModelScopeOwnership([GLM_53, GLM_53_FLASH, ZAI_GLM_53_FLASH])
    const identity = modelScopeIdentity('glm-5.3-flash', null, 'glm-5.3-flash')
    expect(representatives.get(identity)).toBe('glm-5.3-flash')
    // 真实 canonical 作用域保持独立
    const canonicalIdentity = modelScopeIdentity('glm-5.3', 2422803, 'glm-5.3')
    expect(representatives.get(canonicalIdentity)).toBe('glm-5.3')
  })

  it('falls back to the scope literally named after the raw when no canonical is known', () => {
    const { aliasOwner } = resolveModelScopeOwnership([
      { scopeKey: 'mystery-model', rawModels: [], candidates: [{ modelName: 'mystery-model' }] },
      { scopeKey: 'vendor/mystery-model', rawModels: [], candidates: [{ modelName: 'mystery-model' }] },
    ])
    expect(aliasOwner.get('mystery-model')).toBe('mystery-model')
  })

  it('keeps a canonical-scoped owner when a lexical scope also claims the raw', () => {
    const { aliasOwner } = resolveModelScopeOwnership([
      { scopeKey: 'deepseek-v4', canonicalId: 42, canonicalName: 'deepseek-v4', rawModels: ['deepseek-v4'], candidates: [] },
      { scopeKey: 'deepseek-v4-flash', canonicalName: 'deepseek-v4-flash', rawModels: ['deepseek-v4-flash', 'deepseek-v4'], candidates: [{ modelName: 'deepseek-v4', canonicalId: 42 }] },
    ])
    expect(aliasOwner.get('deepseek-v4')).toBe('deepseek-v4')
    expect(aliasOwner.get('deepseek-v4-flash')).toBe('deepseek-v4-flash')
  })

  it('ignores non-positive canonical ids', () => {
    expect(modelScopeIdentity('m', 0, 'm')).toBe('name:m')
    expect(modelScopeIdentity('m', null, '')).toBe('key:m')
    expect(modelScopeIdentity('m', 7, 'other')).toBe('canonical:7')
  })
})
