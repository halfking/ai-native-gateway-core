import { mount, flushPromises } from '@vue/test-utils'
import { computed } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import OperationsMarketingView from './OperationsMarketingView.vue'
import { testI18n } from '../../test-utils/i18n'
import { patchAdminMaasPlan, patchAdminMaasCoupon, patchAdminMaasCampaignStatus } from '../../api'

vi.mock('../../composables/useDataViewMode', () => ({
  useDataViewMode: () => ({ effective: computed(() => 'cards'), canSwitch: computed(() => false), setMode: vi.fn() }),
}))
vi.mock('../../api', () => ({
  getAdminMaasPlans: vi.fn(async () => ({ items: [{ id: 1, code: 'plan-a', name: 'Plan A', price_cents: 1234, monthly_credits: 1000, enabled: true }] })),
  getAdminMaasTopupPackages: vi.fn(async () => ({ items: [{ id: 2, code: 'topup-a', name: 'Topup A', price_cents: 500, credits_amount: 400, bonus_credits: 50, first_time_only: true, enabled: true }] })),
  getAdminMaasCoupons: vi.fn(async () => ({ items: [{ id: 3, code: 'coupon-a', credits_amount: 500, redemption_count: 2, max_redemptions: 10, status: 'active' }] })),
  getAdminMaasCampaigns: vi.fn(async () => ({ items: [{ id: 4, code: 'campaign-a', name: 'Campaign A', benefit_type: 'discount', benefit_value: { percent: 20 }, granted_credits: 100, total_budget_credits: 500, status: 'active' }] })),
  putAdminMaasPlan: vi.fn(), patchAdminMaasPlan: vi.fn(async () => ({})),
  putAdminMaasTopup: vi.fn(), patchAdminMaasTopup: vi.fn(async () => ({})),
  patchAdminMaasCoupon: vi.fn(async () => ({})), postAdminMaasCampaign: vi.fn(),
  patchAdminMaasCampaignStatus: vi.fn(async () => ({})),
}))

describe('marketing compact cards', () => {
  it('keeps all four catalogs and their management actions usable', async () => {
    const w = mount(OperationsMarketingView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').text()).toContain('¥12.34')
    const card = () => w.find('[data-testid="card-list"]')
    await card().get('button').trigger('click')
    expect(patchAdminMaasPlan).toHaveBeenCalledWith(1, false)

    await w.findAll('.tab-row button')[1].trigger('click')
    for (const value of ['Topup A', '400', '50']) expect(card().text()).toContain(value)
    expect(card().find('button').exists()).toBe(true)

    await w.findAll('.tab-row button')[2].trigger('click')
    expect(card().text()).toContain('coupon-a')
    expect(card().text()).toContain('2 / 10')
    await card().get('button').trigger('click')
    expect(patchAdminMaasCoupon).toHaveBeenCalledWith(3, 'disabled')

    await w.findAll('.tab-row button')[3].trigger('click')
    for (const value of ['Campaign A', '-20%', '100 / 500']) expect(card().text()).toContain(value)
    await card().get('select').setValue('paused')
    expect(patchAdminMaasCampaignStatus).toHaveBeenCalledWith(4, 'paused')
  })
})
