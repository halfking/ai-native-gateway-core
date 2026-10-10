import { req } from './client'

export interface CostReconciliationRecord {
  id: number
  provider_id: number
  reconciliation_month: string
  gateway_total_tokens: number
  gateway_input_tokens: number
  gateway_output_tokens: number
  gateway_total_cost: number
  provider_total_tokens: number
  provider_input_tokens: number
  provider_output_tokens: number
  provider_total_cost: number
  token_diff_rate: number
  cost_diff_rate: number
  data_source: string
  notes: string
  updated_at: string
}

export interface CostReconciliationResponse {
  month: string
  items: CostReconciliationRecord[]
  count: number
}

export function fetchCostReconciliation(month: string): Promise<CostReconciliationResponse> {
  return req<CostReconciliationResponse>('GET', `/api/admin/provider-cost-reconciliation?month=${encodeURIComponent(month)}`)
}
