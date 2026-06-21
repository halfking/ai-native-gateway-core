import { req } from './_core'

// catalog.ts — v6.0 audit T12 (2026-06-22)
// Provider catalog (read-only). The catalog is the seed data admins
// use to register providers; entries come from a YAML manifest
// (configs/catalog.yaml in the Go backend) and are immutable from
// the frontend.

export interface CatalogEntry {
  code: string
  tier: string
  display_name: string
  display_name_en: string
  category: string
  kind: string
  protocol: string
  base_url_template: string
  docs_url: string
  default_egress_profile: string
  domestic: boolean
  models_manifest_json: Array<{ id: string; display_name: string; ctx_k?: number }>
  discovery_strategy: string
  hidden: boolean
  notes: string
}

export function getCatalog(tier?: string) {
  const qs = tier ? `?tier=${encodeURIComponent(tier)}` : ''
  return req<CatalogEntry[]>('GET', `/api/catalog${qs}`)
}

export function getCatalogEntry(code: string) {
  return req<CatalogEntry>('GET', `/api/catalog/${encodeURIComponent(code)}`)
}