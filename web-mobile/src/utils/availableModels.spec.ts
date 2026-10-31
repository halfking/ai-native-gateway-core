import { describe, expect, it } from 'vitest'
import {
  modelDisplayName,
  popularKeys,
  projectFamilies,
  projectPopularModels,
  versionDisplayName,
} from './availableModels'
import type { AvailableModelsResponse } from '@/api/models'

/**
 * 2026-10-28：模型选择的投影口径。
 *
 * 后端契约：`popular` 是 Go popularModelEntry **对象数组**
 * （{canonical_name, display_name, source}）。移动端此前按 string[] 消费 ⇒
 * 界面整列渲染成 JSON 字样（用户报的「路由检查选模型是一列 JSON」）。
 * 本文件是桌面 `web/src/utils/availableModels.ts` 的移动端同形件。
 */
const response = (overrides: Partial<AvailableModelsResponse> = {}): AvailableModelsResponse => ({
  families: [],
  popular: [],
  ...overrides,
})

describe('projectPopularModels', () => {
  it('对象数组 ⇒ 保留 display_name，按 canonical_name 去重', () => {
    const result = projectPopularModels(response({
      popular: [
        { canonical_name: 'gpt-5-1', display_name: 'GPT-5.1', source: 'policy' },
        { canonical_name: 'gpt-5-1', display_name: 'GPT-5.1', source: 'usage' },
        { canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1', source: 'usage' },
      ],
    }))
    expect(result.map((m) => m.canonical_name)).toEqual(['gpt-5-1', 'claude-fable-5-1'])
    expect(result[1]?.display_name).toBe('Claude Fable 5.1')
  })

  it('空 canonical_name 的条目被丢弃（后端保证不会出现，但客户端也要挡）', () => {
    const result = projectPopularModels(response({
      popular: [
        { canonical_name: '  ', display_name: 'x', source: 'usage' },
        { canonical_name: 'ok', display_name: 'OK', source: 'usage' },
      ],
    }))
    expect(result.map((m) => m.canonical_name)).toEqual(['ok'])
  })

  it('旧形状（字符串数组）也能归一，不崩', () => {
    const result = projectPopularModels(response({ popular: ['legacy-model'] as never }))
    expect(result.map((m) => m.canonical_name)).toEqual(['legacy-model'])
  })
})

describe('projectFamilies', () => {
  it('排除已在常用模型里出现过的模型 ⇒ 一个模型只出现一次', () => {
    const families = projectFamilies(response({
      families: [{
        id: 'claude', display_name: 'Claude', vendor: 'Anthropic',
        versions: [
          { canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1', modality: 'text', context_window: null, parameters_b: null, provider_count: 1, aliases: [] },
          { canonical_name: 'claude-opus-4-1', display_name: 'Claude Opus 4.1', modality: 'text', context_window: null, parameters_b: null, provider_count: 1, aliases: [] },
        ],
      }],
    }), popularKeys([
      { canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1', source: 'policy' },
    ]))
    expect(families[0]?.versions.map((v) => v.canonical_name)).toEqual(['claude-opus-4-1'])
  })

  it('排除后整个家族为空 ⇒ 该家族不再出现', () => {
    const families = projectFamilies(response({
      families: [{
        id: 'claude', display_name: 'Claude', vendor: 'Anthropic',
        versions: [
          { canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1', modality: 'text', context_window: null, parameters_b: null, provider_count: 1, aliases: [] },
        ],
      }],
    }), popularKeys([
      { canonical_name: 'claude-fable-5-1', display_name: 'Claude Fable 5.1', source: 'policy' },
    ]))
    expect(families).toHaveLength(0)
  })
})

describe('展示名回落', () => {
  it('display_name 为空 ⇒ 回落 canonical_name', () => {
    expect(modelDisplayName({ canonical_name: 'x-1', display_name: '', source: 'usage' })).toBe('x-1')
    expect(modelDisplayName({ canonical_name: 'x-1', display_name: '   ', source: 'usage' })).toBe('x-1')
    expect(versionDisplayName({
      canonical_name: 'x-1', display_name: '', modality: 'text',
      context_window: null, parameters_b: null, provider_count: 1,
    })).toBe('x-1')
  })
})
