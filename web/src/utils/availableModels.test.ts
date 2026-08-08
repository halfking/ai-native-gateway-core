import { describe, expect, it } from 'vitest'
import { projectAvailableModels } from './availableModels'
import type { AvailableModelsResponse } from '../api'

const catalog = (overrides: Partial<AvailableModelsResponse> = {}): AvailableModelsResponse => ({
  families: [],
  unmapped: [],
  total_raw: 0,
  ...overrides,
})

describe('projectAvailableModels', () => {
  it('keeps popular order and appends catalog-only available models', () => {
    const result = projectAvailableModels(catalog({
      popular: [
        { canonical_name: 'gpt-5.5', display_name: 'GPT-5.5', source: 'usage' },
      ],
      families: [{
        id: 'openai',
        display_name: 'OpenAI',
        vendor: 'OpenAI',
        versions: [
          {
            canonical_name: 'gpt-5.5',
            display_name: 'GPT-5.5',
            modality: 'text',
            context_window: null,
            parameters_b: null,
            aliases: [],
            raw_names: ['gpt-5.5'],
            provider_count: 1,
            featured: false,
            tags: [],
          },
          {
            canonical_name: 'gpt-5.6-luna',
            display_name: 'GPT-5.6 Luna',
            modality: 'text',
            context_window: null,
            parameters_b: null,
            aliases: [],
            raw_names: ['gpt-5.6-luna'],
            provider_count: 2,
            featured: false,
            tags: [],
          },
        ],
      }],
    }))

    expect(result.map((model) => model.canonical_name)).toEqual(['gpt-5.5', 'gpt-5.6-luna'])
    expect(result[1].display_name).toBe('GPT-5.6 Luna')
    expect(result[1].source).toBe('catalog')
  })

  it('deduplicates case-insensitively and tolerates missing popular data', () => {
    const result = projectAvailableModels(catalog({
      families: [{
        id: 'other', display_name: 'Other', vendor: 'Other', versions: [
          {
            canonical_name: 'GPT-5.6-LUNA', display_name: 'Luna', modality: 'text',
            context_window: null, parameters_b: null, aliases: [], raw_names: [],
            provider_count: 1, featured: false, tags: [],
          },
          {
            canonical_name: 'gpt-5.6-luna', display_name: 'Luna duplicate', modality: 'text',
            context_window: null, parameters_b: null, aliases: [], raw_names: [],
            provider_count: 1, featured: false, tags: [],
          },
        ],
      }],
    }))

    expect(result).toHaveLength(1)
    expect(result[0].canonical_name).toBe('GPT-5.6-LUNA')
  })
})
