// useWorkTypes.test.ts — guards DB-backed work type list + label fallbacks.

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { useWorkTypes } from './useWorkTypes'
import * as api from '../api-work-types'

vi.mock('../api-work-types', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api-work-types')>()
  return {
    ...actual,
    listWorkTypes: vi.fn(),
  }
})

const sample = [
  {
    key: 'code_gen',
    label: '代码生成',
    category: '研发',
    l1_task_type: 'code',
    default_profile: 'smart' as const,
    tags: [],
    prompt_keywords: [],
    enabled: true,
    sort_order: 1,
    updated_at: '2026-07-20T00:00:00Z',
  },
  {
    key: 'disabled_wt',
    label: '已禁用',
    category: '通用',
    l1_task_type: 'chat',
    default_profile: 'smart' as const,
    tags: [],
    prompt_keywords: [],
    enabled: false,
    sort_order: 99,
    updated_at: '2026-07-20T00:00:00Z',
  },
]

describe('useWorkTypes', () => {
  beforeEach(() => {
    const { workTypes } = useWorkTypes()
    workTypes.value = []
    vi.mocked(api.listWorkTypes).mockReset()
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('refreshWorkTypes loads enabled rows by default', async () => {
    vi.mocked(api.listWorkTypes).mockResolvedValue(sample)
    const { refreshWorkTypes, workTypes } = useWorkTypes()
    await refreshWorkTypes()
    expect(api.listWorkTypes).toHaveBeenCalledWith(false)
    expect(workTypes.value.map((w) => w.key)).toEqual(['code_gen'])
  })

  it('workTypeLabel returns label then raw key', async () => {
    vi.mocked(api.listWorkTypes).mockResolvedValue(sample)
    const { refreshWorkTypes, workTypeLabel } = useWorkTypes()
    await refreshWorkTypes()
    expect(workTypeLabel('code_gen')).toBe('代码生成')
    expect(workTypeLabel('unknown_key')).toBe('unknown_key')
    expect(workTypeLabel('')).toBe('')
  })

  it('keeps previous list when fetch fails', async () => {
    vi.mocked(api.listWorkTypes)
      .mockResolvedValueOnce(sample)
      .mockRejectedValueOnce(new Error('network'))
    const { refreshWorkTypes, workTypes, error } = useWorkTypes()
    await refreshWorkTypes()
    expect(workTypes.value).toHaveLength(1)
    await refreshWorkTypes()
    expect(error.value).toContain('network')
    expect(workTypes.value.map((w) => w.key)).toEqual(['code_gen'])
  })
})
