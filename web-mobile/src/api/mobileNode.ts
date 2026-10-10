import { req } from './client'

export interface MobileNodeDescriptorResponse {
  payload: string
  expires_at: string
}

export function fetchMobileNodeDescriptor(): Promise<MobileNodeDescriptorResponse> {
  return req<MobileNodeDescriptorResponse>('GET', '/api/admin/mobile-node/descriptor')
}
