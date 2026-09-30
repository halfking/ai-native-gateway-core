// providerCards.ts — 供应商成本卡排序。
// 效果图把成本写成 pies.providers.cost_usd，但饼图 key 经常是展示名或 __other__，
// 且 cost_usd 常为 0。窗口成本只认用量行 total_cost_usd；积分仍从饼图按代码或名称回查。

export const PROVIDER_CARD_LIMIT = 5

export interface ProviderPieRef {
  key: string
  requests?: number
  tokens?: number
  credits?: number
  cost_usd?: number
}

export interface ProviderUsageRef {
  provider_id?: number
  provider_code: string
  provider_name: string
  request_count?: number
  prompt_tokens?: number
  completion_tokens?: number
  total_cost_usd?: number
}

export interface ProviderCardModel {
  code: string
  name: string
  id?: number
  requests: number
  tokens: number
  credits: number
  costUsd: number
}

export function matchProviderPie(pies: ProviderPieRef[], code: string, name: string) {
  return pies.find((pie) => pie.key === code || pie.key === name)
}

/** 按窗口成本降序取前 N。没有用量行时返回空，不用 $0 饼图桶充数。 */
export function buildProviderCards(
  rows: ProviderUsageRef[],
  pies: ProviderPieRef[],
  limit = PROVIDER_CARD_LIMIT,
): ProviderCardModel[] {
  return [...rows]
    .sort((a, b) => (b.total_cost_usd ?? 0) - (a.total_cost_usd ?? 0))
    .slice(0, limit)
    .map((row) => {
      const pie = matchProviderPie(pies, row.provider_code, row.provider_name)
      return {
        code: row.provider_code,
        name: row.provider_name || row.provider_code,
        id: row.provider_id,
        requests: row.request_count ?? pie?.requests ?? 0,
        tokens: pie?.tokens ?? (row.prompt_tokens ?? 0) + (row.completion_tokens ?? 0),
        credits: pie?.credits ?? 0,
        costUsd: row.total_cost_usd ?? 0,
      }
    })
}

/** 不足 1 美分保留四位；四位仍是 0 的非零值用科学计数，避免印出 $0.0000。 */
export function fmtUsd(value: number | undefined | null): string {
  if (value == null) return '—'
  const amount = Number(value)
  if (!Number.isFinite(amount)) return '—'
  if (amount !== 0 && Math.abs(amount) < 0.01) {
    const four = amount.toFixed(4)
    if (Number(four) === 0) return '$' + amount.toExponential(1)
    return '$' + four
  }
  return '$' + amount.toFixed(2)
}
