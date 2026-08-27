import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { RoutingCandidate } from '../../api/routing'
import CandidateSettingsDialog from './CandidateSettingsDialog.vue'

vi.mock('../../store', () => ({ isSuperAdmin: () => true }))
vi.mock('../../api/routing', () => ({
  emergencyRepair: vi.fn(),
  patchCandidateBinding: vi.fn(),
}))
vi.mock('../../api/provider-probe', () => ({ setCredentialManualDisabled: vi.fn() }))

const candidate: RoutingCandidate = {
  rank: 1,
  provider_id: 7,
  provider_name: 'Provider',
  catalog_code: 'provider',
  protocol: 'openai',
  base_url: null,
  provider_enabled: true,
  credential_id: 11,
  credential_label: 'Credential',
  credential_status: 'active',
  lifecycle_status: 'active',
  availability_state: 'ready',
  availability_recover_at: null,
  quota_state: 'ok',
  quota_recover_at: null,
  concurrency_limit: 1,
  effective_concurrency: 1,
  effective_at: null,
  expires_at: null,
  balance_usd: null,
  circuit_state: 'closed',
  cooling_until: null,
  available: true,
  tier: 1,
  weight: 100,
  unit_price_in_per_1m: null,
  unit_price_out_per_1m: null,
  currency: null,
  success_rate: 1,
  p95_latency_ms: 1,
  quota_cap_usd: null,
  quota_used_usd: null,
  model_name: 'model',
  canonical_id: null,
  routable: true,
  runtime_routable: true,
}

describe('CandidateSettingsDialog lifecycle selector', () => {
  it('renders the credential lifecycle contract rather than another state domain', () => {
    const wrapper = mount(CandidateSettingsDialog, {
      props: { candidate },
      global: { stubs: { Teleport: true } },
    })

    expect(wrapper.findAll('select option').map((option) => option.attributes('value'))).toEqual([
      'active',
      'disabled',
      'suspended',
      'retired',
    ])
  })
})
