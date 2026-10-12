import { req, type RequestOptions } from './client'

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

export function fetchCostReconciliation(month: string, options?: RequestOptions): Promise<CostReconciliationResponse> {
  return req<CostReconciliationResponse>('GET', `/api/admin/provider-cost-reconciliation?month=${encodeURIComponent(month)}`, undefined, options).then((response) => {
    if (response.month !== month || !Array.isArray(response.items)) throw new Error('Invalid reconciliation response')
    for (const row of response.items) {
      for (const field of ['gateway_total_cost', 'provider_total_cost', 'gateway_total_tokens', 'provider_total_tokens', 'cost_diff_rate', 'token_diff_rate'] as const) {
        if (typeof row[field] !== 'number' || !Number.isFinite(row[field])) throw new Error('Invalid reconciliation amount')
      }
    }
    return response
  })
}
