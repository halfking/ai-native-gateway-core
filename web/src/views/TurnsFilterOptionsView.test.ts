import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TurnsFilterOptionsView from './TurnsFilterOptionsView.vue'
import { listTurnsFilterOptions } from '../api/turns'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/turns', () => ({
  listTurnsFilterOptions: vi.fn(),
}))

const mockedGet = vi.mocked(listTurnsFilterOptions)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = {
  projects: ['proj-a', 'proj-b'],
  tasks: ['task-1', 'task-2', 'task-3'],
  owners: ['alice'],
  clients: ['web', 'mobile'],
  tags: ['vip'],
  models: ['gpt-4o', 'claude-3.5'],
  providers: ['openai', 'anthropic'],
  status_codes: ['200', '4xx', '5xx'],
  api_keys: [
    { id: 1, label: 'k-alpha' },
    { id: 2, label: 'k-beta' },
  ],
}

describe('TurnsFilterOptionsView', () => {
  it('renders 8 string sections + 1 api_keys table from sample data', async () => {
    mockedGet.mockResolvedValueOnce(SAMPLE)
    const w = mount(TurnsFilterOptionsView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // 8 string sections（projects/tasks/owners/clients/tags/models/providers/status_codes）
    const sections = w.findAll('.tfo-section')
    expect(sections).toHaveLength(9) // 8 + 1 api_keys
    // chips 总数 = 2+3+1+2+1+2+2+3 = 16
    expect(w.findAll('.tfo-chip')).toHaveLength(16)
    // api_keys table
    expect(w.findAll('.tfo-table tbody tr')).toHaveLength(2)
    expect(w.text()).toContain('k-alpha')
    expect(w.text()).toContain('k-beta')
    // 计数
    expect(w.text()).toContain('2 项')
    expect(w.text()).toContain('3 项')
  })

  it('filters out empty string sections and shows empty message when all empty', async () => {
    mockedGet.mockResolvedValueOnce({
      projects: [],
      tasks: [],
      owners: [],
      clients: [],
      tags: [],
      models: [],
      providers: [],
      status_codes: [],
      api_keys: [],
    })
    const w = mount(TurnsFilterOptionsView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.findAll('.tfo-section')).toHaveLength(0)
    expect(w.find('.tfo-empty').exists()).toBe(true)
    expect(w.text()).toContain('暂无筛选项')
  })

  it('shows error message on fetch failure and refresh works after recovery', async () => {
    mockedGet.mockRejectedValueOnce(new Error('forbidden 403'))
    const w = mount(TurnsFilterOptionsView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.tfo-error').exists()).toBe(true)
    expect(w.text()).toContain('forbidden 403')
    expect(w.findAll('.tfo-section')).toHaveLength(0)
    // 重试成功
    mockedGet.mockResolvedValueOnce(SAMPLE)
    await w.find('.btn').trigger('click')
    await flushPromises()
    expect(w.find('.tfo-error').exists()).toBe(false)
    expect(w.findAll('.tfo-section')).toHaveLength(9)
  })
})
