import { req, type RequestOptions } from './client'

export interface MobileNodeDescriptorResponse {
  signing_key_fingerprint?: string
  payload: string
  expires_at: string
}

export function fetchMobileNodeDescriptor(options?: RequestOptions): Promise<MobileNodeDescriptorResponse> {
  return req<MobileNodeDescriptorResponse>('GET', '/api/admin/mobile-node/descriptor', undefined, options)
}
