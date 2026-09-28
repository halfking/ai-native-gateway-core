import { describe, expect, it } from 'vitest'
import {
  modelScopeIdentity,
  resolveModelScopeOwnership,
  type ModelScopeResolution,
} from './modelScopeOwnership'

// 载荷取自 2026-09-29 llmgateway.internal.example.com 实测（dashboard?tab=stream 复现）：
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

  const SAME_ID_DIFFERENT_NAMES: ModelScopeResolution[] = [
    { scopeKey: 'glm-5.3', canonicalId: 42, canonicalName: 'glm-5.3' },
    { scopeKey: 'glm-5-3', canonicalId: 42, canonicalName: 'glm-5-3' },
  ]

  it.each([
    ['dot alias first', SAME_ID_DIFFERENT_NAMES],
    ['dash alias first', [...SAME_ID_DIFFERENT_NAMES].reverse()],
  ] as const)('chooses the same representative for one canonical ID with different names: %s', (_order, resolutions) => {
    const { representatives } = resolveModelScopeOwnership(resolutions)
    expect(representatives.get('canonical:42')).toBe('glm-5-3')
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

  // ── 2026-09-29 二轮审计：响应级 canonical_id 不能单独当证据 ──────────────
  //
  // 线上实测 doubao-seed-2-0-code-preview 与 glm-5.3-flash 的 resolve 响应
  // canonical_id 都是 null（候选跨 canonical），但它们**自己名字**的候选带着
  // 真实 canonical（353857 / 2716170）。若只信响应级 id，这两个真模型会被判成
  // "无证据"而整个消失（实测 doubao-seed-2-0-code-preview 确实消失过一次）。

  it('validates a scope by the canonical of its own-name binding when the response canonical_id is null', () => {
    const { aliasOwner } = resolveModelScopeOwnership([
      {
        scopeKey: 'glm-5.3-flash',
        canonicalId: null, // 候选跨 canonical → 响应级为 null
        canonicalName: 'glm-5.3-flash',
        rawModels: ['glm-5.3-flash', 'glm-5-3-flash', 'glm-5.3', 'glm-5-3'],
        candidates: [
          { modelName: 'glm-5.3-flash', canonicalId: 2716170 }, // 自己名字的绑定
          { modelName: 'glm-5-3-flash-260828', canonicalId: 2716170 },
          { modelName: 'free/glm-5.3-flash', canonicalId: 2716170 },
          { modelName: 'glm-5.3', canonicalId: 2422803 }, // 词法变体拖进来的
        ],
      },
      {
        scopeKey: 'glm-5.3',
        canonicalId: 2422803,
        canonicalName: 'glm-5.3',
        rawModels: ['glm-5.3'],
        candidates: [{ modelName: 'glm-5.3', canonicalId: 2422803 }],
      },
    ])
    expect(aliasOwner.get('glm-5.3')).toBe('glm-5.3')
    expect(aliasOwner.get('glm-5-3-flash-260828')).toBe('glm-5.3-flash')
  })

  it('keeps a model whose only binding is a date-suffixed raw (no bare-name binding)', () => {
    const { aliasOwner } = resolveModelScopeOwnership([
      {
        scopeKey: 'doubao-seed-2-0-code-preview',
        canonicalId: null,
        canonicalName: 'doubao-seed-2-0-code-preview',
        rawModels: ['doubao-seed-2-0-code-preview'],
        candidates: [{ modelName: 'doubao-seed-2-0-code-preview-260215', canonicalId: 353857 }],
      },
    ])
    expect(aliasOwner.get('doubao-seed-2-0-code-preview-260215')).toBe('doubao-seed-2-0-code-preview')
  })

  it('leaves a raw unowned when no claiming scope can be validated (never files it under another model)', () => {
    // 线上实测形态：集合（featured+热门）里只有 glm-4.7-flash，glm-4.7 本身不在，
    // 但节点 raw_models 里有 glm-4.7（旧实现会把它挂到 glm-4.7-flash 名下）。
    const { aliasOwner } = resolveModelScopeOwnership([
      {
        scopeKey: 'glm-4.7-flash',
        canonicalId: 52,
        canonicalName: 'glm-4.7-flash',
        rawModels: ['glm-4.7-flash', 'glm-4.7', 'glm-4-7-251222'],
        candidates: [
          { modelName: 'glm-4.7-flash', canonicalId: 52 },
          { modelName: 'glm-4.7', canonicalId: 51 },
          { modelName: 'glm-4-7-251222', canonicalId: 51 },
        ],
      },
    ])
    expect(aliasOwner.get('glm-4.7-flash')).toBe('glm-4.7-flash')
    // 属于集合外模型 51 的 raw 不归属给 52 的作用域
    expect(aliasOwner.has('glm-4.7')).toBe(false)
    expect(aliasOwner.has('glm-4-7-251222')).toBe(false)
  })
})
