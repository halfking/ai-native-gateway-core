export const CREDENTIAL_DISPLAY_STATES = [
  'active', 'cooling', 'degraded', 'rate_limited', 'unreachable',
  'auth_failed', 'suspended', 'quota_exhausted', 'disabled', 'deleted', 'unknown',
] as const

export type CredentialDisplayState = typeof CREDENTIAL_DISPLAY_STATES[number]

export interface CredentialStatusLike {
  effective_state?: string | null
  effective_reason?: string | null
  status?: string | null
  lifecycle_status?: string | null
  availability_state?: string | null
  health_status?: string | null
  quota_state?: string | null
  manual_disabled?: boolean | null
}

export function credentialDisplayState(value: CredentialStatusLike): CredentialDisplayState {
  const explicit = value.effective_state as CredentialDisplayState | undefined
  if (explicit && (CREDENTIAL_DISPLAY_STATES as readonly string[]).includes(explicit)) return explicit
  if (value.status === 'deleted' || value.lifecycle_status === 'retired') return 'deleted'
  if (value.manual_disabled || value.status === 'disabled' || value.lifecycle_status === 'disabled') return 'disabled'
  if (value.availability_state === 'auth_failed') return 'auth_failed'
  if (['balance_exhausted', 'permanently_exhausted', 'periodic_exhausted'].includes(value.quota_state ?? '')) return 'quota_exhausted'
  if (value.availability_state === 'suspended') return 'suspended'
  if (value.availability_state === 'rate_limited') return 'rate_limited'
  if (value.availability_state === 'unreachable' || value.health_status === 'unreachable') return 'unreachable'
  if (value.availability_state === 'cooling' || value.status === 'cooling') return 'cooling'
  if (value.availability_state === 'degraded' || value.status === 'degraded' || value.health_status === 'warning') return 'degraded'
  if (value.status === 'active' || value.lifecycle_status === 'active' || value.availability_state === 'ready' || value.health_status === 'healthy') return 'active'
  return 'unknown'
}

export const credentialDisplayStateLabel: Record<CredentialDisplayState, string> = {
  active: 'Active', cooling: 'Cooling', degraded: 'Degraded', rate_limited: 'Rate limited',
  unreachable: 'Unreachable', auth_failed: 'Auth failed', suspended: 'Suspended',
  quota_exhausted: 'Quota exhausted', disabled: 'Disabled', deleted: 'Deleted', unknown: 'Unknown',
}
