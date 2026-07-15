// public.ts — Public portal API (download, donation, offline activation)

import { req } from './_core'

export interface CatalogItem {
  platform: string
  arch: string
  label: string
  artifact_name: string
  sha256?: string
  size_bytes?: number
  size_label?: string
}

export interface DownloadCatalog {
  version: string
  build_seq: number
  channel: string
  release_date?: string
  items: CatalogItem[]
  supporters: number
  git_repo_url?: string
  git_branch?: string
  docs_url?: string
  contact_email?: string
}

export interface DownloadTicket {
  request_id: string
  url: string
  expires_at: string
  sha256?: string
  file_name: string
}

export interface DonationOrder {
  id: number
  order_no: string
  email: string
  amount_cents: number
  channel: string
  status: string
  tier_label: string
}

export interface PaymentQR {
  payment_channel: string
  qr_url: string
  hint: string
  stub_mode: boolean
}

export interface DownloadStats {
  today_downloads: number
  week_downloads: number
  total_downloads: number
  today_donations: number
  total_donations: number
  donation_amount_cents: number
  activation_rate_pct: number
  supporter_count: number
}

export interface LicenseHolder {
  id: number
  email: string
  display_name: string
  holder_type: string
  license_count?: number
  device_count?: number
  donation_total_cents?: number
  created_at: string
}

export async function getDownloadCatalog(): Promise<DownloadCatalog> {
  return req<DownloadCatalog>('GET', '/api/downloads/catalog')
}

export async function createDownloadTicket(data: {
  version?: string
  platform: string
  arch: string
  email?: string
  donation_id?: number
}): Promise<DownloadTicket> {
  return req<DownloadTicket>('POST', '/api/downloads/ticket', data)
}

export async function recordDownloadEvent(data: {
  request_id: string
  result?: string
  duration_ms?: number
}): Promise<void> {
  await req('POST', '/api/downloads/events', data)
}

export async function createDonation(data: {
  email?: string
  amount_cents: number
  channel?: string
  tier_label?: string
}): Promise<{ donation: DonationOrder; payment: PaymentQR }> {
  return req('POST', '/api/donations', data)
}

export async function confirmStubDonation(orderNo: string): Promise<DonationOrder> {
  return req<DonationOrder>('POST', `/api/donations/${orderNo}/confirm-stub`)
}

export async function submitOfflineRequest(signedRequest: string): Promise<{
  request_id: string
  status: string
  message?: string
}> {
  return req('POST', '/api/public/offline-activation/submit', { signed_request: signedRequest })
}

export async function getOfflineStatus(requestId: string): Promise<{
  request_id: string
  status: string
  license_key?: string
  device_name?: string
  reject_reason?: string
}> {
  return req('GET', `/api/public/offline-activation/status/${encodeURIComponent(requestId)}`)
}

export async function getOfflineResponse(requestId: string): Promise<{
  request_id: string
  activation_code?: string
  signed_license?: string
  status?: string
  message?: string
}> {
  return req('GET', `/api/public/offline-activation/response/${encodeURIComponent(requestId)}`)
}

export async function getDownloadStats(): Promise<DownloadStats> {
  return req<DownloadStats>('GET', '/api/admin/downloads/stats')
}

export async function getLicenseHolders(params?: {
  offset?: number
  limit?: number
  query?: string
}): Promise<{ holders: LicenseHolder[]; total: number }> {
  const q = new URLSearchParams()
  if (params?.offset !== undefined) q.set('offset', String(params.offset))
  if (params?.limit !== undefined) q.set('limit', String(params.limit))
  if (params?.query) q.set('query', params.query)
  const qs = q.toString()
  return req('GET', '/api/admin/downloads/license-holders' + (qs ? '?' + qs : ''))
}
