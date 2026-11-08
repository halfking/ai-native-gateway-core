// publicQuote.ts — 客户版报价页的免登录 API 客户端（2026-11-08 W2 客户版轮）。
// 后端面：admin/sales_public_handler.go（GET /api/public/quote/{token}）。
//
// 不走 api/_core.ts 的 req()：那条通道挂鉴权头 + 401 会话裁决重定向，
// 客户版无登录、404 即 404（后端对过期/撤销/不存在一律 404），不能把
// 客户弹去控制台登录页。auth.ts / customer.ts 的裸 fetch 是同款先例。
//
// 安全口径（与后端结构体编译期锁同源）：本类型只含报价要素（模型/量/
// 零售价/折扣/合计）—— 成本/毛利/供应商身份/凭据在响应体里物理不存在。

export interface PublicQuoteItem {
  canonical_name: string
  display_name: string
  est_tokens_in: number
  est_tokens_out: number
  volume_note: string
  rate_in: number | null
  rate_out: number | null
  discount_pct: number
  subtotal_usd: number
}

export interface PublicQuote {
  customer_name: string
  title: string
  valid_until: string | null
  expired: boolean
  tier: string
  sale_total_usd: number
  items: PublicQuoteItem[]
}

export class PublicQuoteUnavailableError extends Error {
  constructor() {
    super('public quote unavailable')
    this.name = 'PublicQuoteUnavailableError'
  }
}

export async function fetchPublicQuote(token: string): Promise<PublicQuote> {
  const r = await fetch(`/api/public/quote/${encodeURIComponent(token)}`, {
    headers: { Accept: 'application/json' },
  })
  if (!r.ok) {
    // 404（含过期/撤销）/429/5xx 一律「不可用」——不向客户区分后端语义。
    throw new PublicQuoteUnavailableError()
  }
  return (await r.json()) as PublicQuote
}
