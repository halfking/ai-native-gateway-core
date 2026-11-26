import { describe, expect, it } from 'vitest'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'

describe('session analytics request scope', () => {
  it('discards a response from the previous tenant after resetting a tab', async () => {
    const loader = useSessionAnalyticsLoader()
    let resolvePrevious: (value: string) => void = () => {}
    const previous = loader.run(
      () => new Promise<string>((resolve) => { resolvePrevious = resolve }),
      'sessionAnalytics.error.userDetailFailed',
    )

    loader.reset()
    const current = loader.run(async () => 'tenant-b', 'sessionAnalytics.error.userDetailFailed')
    resolvePrevious('tenant-a')

    await expect(previous).resolves.toBeNull()
    await expect(current).resolves.toBe('tenant-b')
    expect(loader.error.value).toBe('')
    expect(loader.loading.value).toBe(false)
  })
})
